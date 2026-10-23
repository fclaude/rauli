// Package mcp serves Raulí's tools over the Model Context Protocol: JSON-RPC 2.0
// messages, one per line, on a pair of streams (standard input and output when
// run as a program).
//
// The server writes protocol messages and nothing else to its output stream.
// A stray line there would be read as a malformed message by the client, so
// every diagnostic goes to the log writer instead.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/fclaude/rauli/internal/tools"
)

// Backend is what the server needs from a browser session. *tools.Session is
// one; a test can supply a smaller one.
type Backend interface {
	Tools() []tools.Tool
	Call(ctx context.Context, name string, args json.RawMessage) (tools.Result, error)
}

// Supported protocol versions, oldest first. A client that asks for one of
// these gets it; any other request gets the newest.
var versions = []string{"2024-11-05", "2025-03-26", "2025-06-18"}

// JSON-RPC error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// Server answers requests for a Backend.
type Server struct {
	Backend Backend
	// Name and Version identify the server to the client.
	Name, Version string
	// Instructions is shown to the client's model as guidance on using the tools.
	Instructions string
	// Log receives diagnostics. It must not be the output stream. Nil discards them.
	Log io.Writer
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// run is one tool call in flight, which a cancellation notice can end.
type run struct {
	cancel    context.CancelFunc
	cancelled bool
}

type session struct {
	srv *Server
	out io.Writer

	wmu sync.Mutex // serialises writes, so two replies never interleave

	mu       sync.Mutex
	inflight map[string]*run
	wg       sync.WaitGroup

	// queue holds tool calls in the order they arrived. One worker runs them one
	// at a time, because a browser tab is a single thing to act on: a click that
	// loads a page must finish before the call after it looks at that page. The
	// server still reads while a call runs, which is how a cancellation is heard.
	queue chan func()
}

// Serve reads requests from in until it ends or ctx is cancelled, and writes
// the replies to out. When the input ends, calls already running finish and
// reply first. When ctx is cancelled they are cancelled. Serve returns once
// every call in flight is over.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ss := &session{srv: s, out: out, inflight: map[string]*run{}, queue: make(chan func(), 1024)}
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for run := range ss.queue {
			run()
		}
	}()
	lines := make(chan []byte)
	errc := make(chan error, 1)
	go func() {
		r := bufio.NewReader(in)
		for {
			// ReadBytes has no line limit; a request can be large.
			line, err := r.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				errc <- err
				return
			}
		}
	}()

	var result error
loop:
	for {
		select {
		case line := <-lines:
			ss.handle(ctx, line)
		case err := <-errc:
			if !errors.Is(err, io.EOF) {
				result = err
			}
			break loop
		case <-ctx.Done():
			result = ctx.Err()
			break loop
		}
	}
	if result == nil && ctx.Err() == nil {
		// The input ended. That is how a client says it has nothing more to ask,
		// and it may still be reading: a script pipes its requests in and waits
		// for the answers. So the calls in flight are allowed to finish and
		// reply, each within its own time limit.
		ss.wg.Wait()
	}
	// A shutdown, or a failed stream, leaves no one to answer: cancel what is
	// left and wait for it.
	cancel()
	ss.wg.Wait()
	close(ss.queue)
	<-workerDone
	return result
}

func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		fmt.Fprintf(s.Log, format+"\n", args...)
	}
}

func (ss *session) write(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		ss.srv.logf("mcp: could not encode a reply: %v", err)
		return
	}
	ss.wmu.Lock()
	defer ss.wmu.Unlock()
	if _, err := ss.out.Write(append(data, '\n')); err != nil {
		ss.srv.logf("mcp: could not write a reply: %v", err)
	}
}

func (ss *session) fail(id json.RawMessage, code int, format string, args ...any) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	ss.write(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: fmt.Sprintf(format, args...)}})
}

func (ss *session) reply(id json.RawMessage, result any) {
	ss.write(response{JSONRPC: "2.0", ID: id, Result: result})
}

func (ss *session) handle(ctx context.Context, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		ss.fail(nil, codeParse, "the message is not valid JSON: %v", err)
		return
	}
	notification := len(req.ID) == 0
	if req.Method == "" {
		if !notification {
			ss.fail(req.ID, codeInvalidRequest, "the message has no method")
		}
		return
	}

	switch req.Method {
	case "initialize":
		ss.initialize(req)
	case "ping":
		if !notification {
			ss.reply(req.ID, map[string]any{})
		}
	case "tools/list":
		if !notification {
			ss.reply(req.ID, map[string]any{"tools": ss.listTools()})
		}
	case "tools/call":
		if notification {
			return
		}
		ss.call(ctx, req)
	case "notifications/cancelled":
		ss.cancel(req.Params)
	default:
		// A notification the server does not know is none of its business.
		if !notification {
			ss.fail(req.ID, codeMethodNotFound, "this server has no method %q", req.Method)
		}
	}
}

func (ss *session) initialize(req request) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			ss.fail(req.ID, codeInvalidParams, "bad initialize parameters: %v", err)
			return
		}
	}
	version := versions[len(versions)-1]
	for _, v := range versions {
		if v == p.ProtocolVersion {
			version = v
		}
	}
	info := map[string]any{"name": ss.srv.Name, "version": ss.srv.Version}
	result := map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      info,
	}
	if ss.srv.Instructions != "" {
		result["instructions"] = ss.srv.Instructions
	}
	ss.reply(req.ID, result)
}

func (ss *session) listTools() []map[string]any {
	list := ss.srv.Backend.Tools()
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.Schema,
			"annotations": map[string]any{"readOnlyHint": t.ReadOnly},
		})
	}
	return out
}

func (ss *session) known(name string) bool {
	for _, t := range ss.srv.Backend.Tools() {
		if t.Name == name {
			return true
		}
	}
	return false
}

// call queues a tool call. It returns at once, so a slow page never stops the
// server from reading the next message, in particular a cancellation.
func (ss *session) call(ctx context.Context, req request) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		ss.fail(req.ID, codeInvalidParams, "tools/call needs a name and arguments")
		return
	}
	if !ss.known(p.Name) {
		ss.fail(req.ID, codeInvalidParams, "there is no tool called %q", p.Name)
		return
	}
	if len(p.Arguments) > 0 && !bytes.HasPrefix(bytes.TrimSpace(p.Arguments), []byte("{")) && !bytes.Equal(bytes.TrimSpace(p.Arguments), []byte("null")) {
		ss.fail(req.ID, codeInvalidParams, "the arguments of %s must be an object", p.Name)
		return
	}

	key := string(req.ID)
	cctx, cancel := context.WithCancel(ctx)
	r := &run{cancel: cancel}
	ss.mu.Lock()
	ss.inflight[key] = r
	ss.mu.Unlock()

	ss.wg.Add(1)
	ss.queue <- func() {
		defer ss.wg.Done()
		defer cancel()
		if cctx.Err() != nil {
			// Withdrawn while it waited its turn, or the server is stopping.
			ss.mu.Lock()
			delete(ss.inflight, key)
			ss.mu.Unlock()
			return
		}
		res, err := ss.srv.Backend.Call(cctx, p.Name, p.Arguments)

		ss.mu.Lock()
		delete(ss.inflight, key)
		cancelled := r.cancelled
		ss.mu.Unlock()
		if cancelled || ctx.Err() != nil {
			// The client withdrew the request, or the server is stopping because
			// the client has gone. Either way nobody is waiting for an answer, and
			// writing one could block on a stream no one reads.
			return
		}
		ss.reply(req.ID, toResult(res, err))
	}
}

// toResult shapes a tool's outcome for the client. A tool that fails is a
// normal reply with isError set, so the model can read the reason and adjust.
func toResult(res tools.Result, err error) map[string]any {
	if err != nil {
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		}
	}
	content := make([]map[string]any, 0, 1+len(res.Images))
	if res.Text != "" || len(res.Images) == 0 {
		content = append(content, map[string]any{"type": "text", "text": res.Text})
	}
	for _, img := range res.Images {
		content = append(content, map[string]any{
			"type": "image", "data": base64.StdEncoding.EncodeToString(img.Data), "mimeType": img.MIME,
		})
	}
	return map[string]any{"content": content, "isError": false}
}

func (ss *session) cancel(params json.RawMessage) {
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(params, &p) != nil || len(p.RequestID) == 0 {
		return
	}
	ss.mu.Lock()
	r := ss.inflight[string(p.RequestID)]
	if r != nil {
		r.cancelled = true
	}
	ss.mu.Unlock()
	if r != nil {
		r.cancel()
	}
}
