package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/rauli/internal/chrome"
	"github.com/fclaude/rauli/internal/tools"
)

// client talks to a server over pipes, the way a program would over its
// standard streams.
type client struct {
	t      *testing.T
	in     io.WriteCloser
	out    *bufio.Reader
	done   chan error
	lines  []string // every line the server wrote, to prove they are all JSON
	logbuf *bytes.Buffer
}

func start(t *testing.T, b Backend) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, in: inW, out: bufio.NewReader(outR), done: make(chan error, 1), logbuf: &bytes.Buffer{}}
	srv := &Server{Backend: b, Name: "rauli-test", Version: "0.0.1", Instructions: "Read pages as text.", Log: c.logbuf}
	go func() {
		err := srv.Serve(context.Background(), inR, outW)
		outW.Close()
		c.done <- err
	}()
	t.Cleanup(func() { inW.Close() })
	return c
}

func (c *client) send(v any) {
	c.t.Helper()
	var data []byte
	switch x := v.(type) {
	case string:
		data = []byte(x)
	default:
		var err error
		if data, err = json.Marshal(v); err != nil {
			c.t.Fatal(err)
		}
	}
	if _, err := c.in.Write(append(data, '\n')); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) read() map[string]any {
	c.t.Helper()
	type res struct {
		line string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		l, err := c.out.ReadString('\n')
		ch <- res{l, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			c.t.Fatalf("reading from the server: %v", r.err)
		}
		c.lines = append(c.lines, r.line)
		var m map[string]any
		if err := json.Unmarshal([]byte(r.line), &m); err != nil {
			c.t.Fatalf("the server wrote a line that is not JSON: %q", r.line)
		}
		return m
	case <-time.After(30 * time.Second):
		c.t.Fatal("no reply from the server")
		return nil
	}
}

func (c *client) rpc(id int, method string, params any) map[string]any {
	c.t.Helper()
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return c.read()
}

// fake is a backend that needs no browser.
type fake struct {
	mu      sync.Mutex
	started chan string
	release chan struct{}
	called  []string
}

func newFake() *fake { return &fake{started: make(chan string, 8), release: make(chan struct{})} }

func (f *fake) Tools() []tools.Tool {
	return []tools.Tool{
		{Name: "echo", Description: "Echo the text.", Schema: map[string]any{"type": "object"}, ReadOnly: true},
		{Name: "slow", Description: "Wait until told, or cancelled.", Schema: map[string]any{"type": "object"}},
		{Name: "fail", Description: "Always fails.", Schema: map[string]any{"type": "object"}},
		{Name: "pictures", Description: "Return an image.", Schema: map[string]any{"type": "object"}},
	}
}

func (f *fake) Call(ctx context.Context, name string, args json.RawMessage) (tools.Result, error) {
	f.mu.Lock()
	f.called = append(f.called, name+" "+string(args))
	f.mu.Unlock()
	switch name {
	case "echo":
		var a struct{ Text string }
		json.Unmarshal(args, &a)
		return tools.Result{Text: a.Text}, nil
	case "slow":
		f.started <- "slow"
		select {
		case <-ctx.Done():
			return tools.Result{}, ctx.Err()
		case <-f.release:
			return tools.Result{Text: "finished"}, nil
		}
	case "fail":
		return tools.Result{}, errors.New("not allowed: payments need approval")
	case "pictures":
		return tools.Result{Text: "a picture", Images: []tools.Image{{MIME: "image/png", Data: []byte{1, 2, 3}}}}, nil
	}
	return tools.Result{}, tools.ErrUnknownTool
}

func initialize(c *client, version string) map[string]any {
	c.t.Helper()
	r := c.rpc(1, "initialize", map[string]any{"protocolVersion": version, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}})
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return r
}

func TestInitializeNegotiatesTheVersion(t *testing.T) {
	for asked, want := range map[string]string{
		"2024-11-05": "2024-11-05",
		"2025-03-26": "2025-03-26",
		"2025-06-18": "2025-06-18",
		"2099-01-01": "2025-06-18",
		"":           "2025-06-18",
	} {
		c := start(t, newFake())
		r := initialize(c, asked)
		res := r["result"].(map[string]any)
		if res["protocolVersion"] != want {
			t.Errorf("asked for %q, got %v, want %s", asked, res["protocolVersion"], want)
		}
		info := res["serverInfo"].(map[string]any)
		if info["name"] != "rauli-test" || info["version"] != "0.0.1" {
			t.Errorf("serverInfo %v", info)
		}
		if res["instructions"] != "Read pages as text." {
			t.Errorf("instructions %v", res["instructions"])
		}
		caps := res["capabilities"].(map[string]any)
		if _, ok := caps["tools"]; !ok {
			t.Errorf("the server must announce its tools: %v", caps)
		}
	}
}

func TestToolsListHasSchemasAndReadOnlyHints(t *testing.T) {
	c := start(t, newFake())
	initialize(c, "2025-06-18")
	r := c.rpc(2, "tools/list", nil)
	list := r["result"].(map[string]any)["tools"].([]any)
	if len(list) != 4 {
		t.Fatalf("got %d tools", len(list))
	}
	first := list[0].(map[string]any)
	if first["name"] != "echo" || first["description"] != "Echo the text." || first["inputSchema"] == nil {
		t.Fatalf("first tool: %v", first)
	}
	if first["annotations"].(map[string]any)["readOnlyHint"] != true {
		t.Errorf("echo should be marked read-only: %v", first["annotations"])
	}
	if list[1].(map[string]any)["annotations"].(map[string]any)["readOnlyHint"] != false {
		t.Errorf("slow is not read-only")
	}
}

func TestACallReturnsTextAndImages(t *testing.T) {
	c := start(t, newFake())
	initialize(c, "2025-06-18")
	r := c.rpc(3, "tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "hello"}})
	res := r["result"].(map[string]any)
	content := res["content"].([]any)
	if res["isError"] != false || content[0].(map[string]any)["text"] != "hello" {
		t.Fatalf("echo: %v", res)
	}
	r = c.rpc(4, "tools/call", map[string]any{"name": "pictures"})
	content = r["result"].(map[string]any)["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("pictures: %v", content)
	}
	img := content[1].(map[string]any)
	if img["type"] != "image" || img["mimeType"] != "image/png" {
		t.Fatalf("image block: %v", img)
	}
	if data, _ := base64.StdEncoding.DecodeString(img["data"].(string)); string(data) != "\x01\x02\x03" {
		t.Fatalf("image data: %q", img["data"])
	}
}

func TestAToolThatFailsIsAResultAndNotAProtocolError(t *testing.T) {
	c := start(t, newFake())
	initialize(c, "2025-06-18")
	r := c.rpc(5, "tools/call", map[string]any{"name": "fail"})
	if r["error"] != nil {
		t.Fatalf("a tool error must not be a JSON-RPC error: %v", r)
	}
	res := r["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if res["isError"] != true || !strings.Contains(text, "payments need approval") {
		t.Fatalf("got %v", res)
	}
}

func TestProtocolErrors(t *testing.T) {
	c := start(t, newFake())
	initialize(c, "2025-06-18")

	code := func(m map[string]any) float64 { return m["error"].(map[string]any)["code"].(float64) }

	if got := code(c.rpc(6, "tools/call", map[string]any{"name": "nonsense"})); got != -32602 {
		t.Errorf("unknown tool: code %v", got)
	}
	if got := code(c.rpc(7, "tools/call", map[string]any{"arguments": map[string]any{}})); got != -32602 {
		t.Errorf("no name: code %v", got)
	}
	if got := code(c.rpc(8, "tools/call", map[string]any{"name": "echo", "arguments": []int{1}})); got != -32602 {
		t.Errorf("arguments that are not an object: code %v", got)
	}
	if got := code(c.rpc(9, "resources/list", nil)); got != -32601 {
		t.Errorf("unknown method: code %v", got)
	}

	// Malformed JSON gets a parse error with a null id, and the server carries on.
	c.send(`{"jsonrpc": "2.0", "id": 10, "method": `)
	r := c.read()
	if code(r) != -32700 || r["id"] != nil {
		t.Errorf("parse error reply: %v", r)
	}
	if r := c.rpc(11, "ping", nil); r["result"] == nil {
		t.Errorf("the server did not survive a malformed message: %v", r)
	}
	// A message with an id and no method is an invalid request.
	c.send(`{"jsonrpc":"2.0","id":12}`)
	if got := code(c.read()); got != -32600 {
		t.Errorf("no method: code %v", got)
	}
	// String ids come back as strings.
	c.send(`{"jsonrpc":"2.0","id":"abc","method":"ping"}`)
	if r := c.read(); r["id"] != "abc" {
		t.Errorf("string id: %v", r)
	}
	// Notifications, known or not, get no reply: the next reply is the ping's.
	c.send(`{"jsonrpc":"2.0","method":"notifications/whatever"}`)
	c.send(`{"jsonrpc":"2.0","id":13,"method":"ping"}`)
	if r := c.read(); r["id"] != float64(13) {
		t.Errorf("a notification was answered: %v", r)
	}
}

func TestCancellingEndsTheCallAndSilencesItsReply(t *testing.T) {
	f := newFake()
	c := start(t, f)
	initialize(c, "2025-06-18")

	c.send(map[string]any{"jsonrpc": "2.0", "id": 20, "method": "tools/call", "params": map[string]any{"name": "slow"}})
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow tool never started")
	}
	// While it runs the server still answers other requests.
	if r := c.rpc(21, "ping", nil); r["id"] != float64(21) {
		t.Fatalf("ping during a call: %v", r)
	}
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 20}})
	// If the cancelled call replied, its reply would arrive before this ping's.
	time.Sleep(200 * time.Millisecond)
	if r := c.rpc(22, "ping", nil); r["id"] != float64(22) {
		t.Fatalf("the cancelled call still answered: %v", r)
	}
}

func TestClosingInputLetsACallFinishAndAnswer(t *testing.T) {
	f := newFake()
	c := start(t, f)
	initialize(c, "2025-06-18")
	c.send(map[string]any{"jsonrpc": "2.0", "id": 30, "method": "tools/call", "params": map[string]any{"name": "slow"}})
	<-f.started
	c.in.Close()

	select {
	case err := <-c.done:
		t.Fatalf("Serve returned (%v) while a call was still running", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(f.release)
	r := c.read()
	if r["id"] != float64(30) || r["result"].(map[string]any)["isError"] != false {
		t.Fatalf("the call did not answer after the input closed: %v", r)
	}
	select {
	case err := <-c.done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return once the last call had answered")
	}
}

func TestShuttingDownCancelsWhatIsRunning(t *testing.T) {
	f := newFake()
	inR, inW := io.Pipe()
	defer inW.Close()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() { done <- (&Server{Backend: f, Name: "t", Version: "1"}).Serve(ctx, inR, &out) }()

	fmt.Fprintln(inW, `{"jsonrpc":"2.0","id":40,"method":"tools/call","params":{"name":"slow"}}`)
	<-f.started
	stop()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled Serve should say why it stopped")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its context was cancelled")
	}
	if out.Len() != 0 {
		t.Fatalf("a call cancelled by shutdown wrote %q", out.String())
	}
}

// With a real browser: the whole path from a protocol message to a page.
func TestARealBrowserThroughTheProtocol(t *testing.T) {
	if _, err := chrome.Find(); err != nil {
		t.Skip(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Fixture</title><h1>Hello from the fixture</h1><button>Press me</button>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s, err := tools.New(context.Background(), tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	c := start(t, s)
	initialize(c, "2025-06-18")

	list := c.rpc(2, "tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, x := range list {
		names[x.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"navigate", "snapshot", "click", "type", "screenshot", "tabs"} {
		if !names[want] {
			t.Errorf("tool %s is not listed", want)
		}
	}

	r := c.rpc(3, "tools/call", map[string]any{"name": "navigate", "arguments": map[string]any{"url": srv.URL}})
	text := r["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	for _, want := range []string{"Page: Fixture", `heading "Hello from the fixture"`, `button "Press me"`} {
		if !strings.Contains(text, want) {
			t.Errorf("navigate result lacks %q:\n%s", want, text)
		}
	}

	r = c.rpc(4, "tools/call", map[string]any{"name": "screenshot", "arguments": map[string]any{}})
	content := r["result"].(map[string]any)["content"].([]any)
	var img map[string]any
	for _, b := range content {
		if b.(map[string]any)["type"] == "image" {
			img = b.(map[string]any)
		}
	}
	if img == nil {
		t.Fatalf("the screenshot has no image block: %v", content)
	}
	data, err := base64.StdEncoding.DecodeString(img["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil || img["mimeType"] != "image/png" {
		t.Fatalf("the image block is not a PNG: %v %v", err, img["mimeType"])
	}

	// A tool the page cannot satisfy fails as a result.
	r = c.rpc(5, "tools/call", map[string]any{"name": "click", "arguments": map[string]any{"ref": "e99"}})
	if r["result"].(map[string]any)["isError"] != true {
		t.Fatalf("a stale reference should be an error result: %v", r)
	}

	// Everything the server wrote was a JSON line: read() already enforced it.
	if len(c.lines) < 5 {
		t.Fatalf("only %d lines were read", len(c.lines))
	}
	if c.logbuf.Len() > 0 {
		t.Logf("diagnostics: %s", c.logbuf.String())
	}
}

// A client may send several requests without waiting for each answer. They run
// in the order they were sent, one at a time: a click that loads a page has to
// finish before the call after it looks at that page. Before calls were queued
// each ran on its own goroutine and the order was whatever the scheduler chose.
func TestPipelinedCallsRunOneAtATimeInTheOrderTheyArrive(t *testing.T) {
	f := newFake()
	c := start(t, f)
	initialize(c, "2025-06-18")
	call := func(id int, name string, args map[string]any) {
		c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	}
	call(40, "slow", nil)
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow tool never started")
	}
	for i := 41; i <= 45; i++ {
		call(i, "echo", map[string]any{"Text": fmt.Sprint("call ", i)})
	}
	// None of the later calls may start while the first is still running.
	time.Sleep(300 * time.Millisecond)
	f.mu.Lock()
	running := len(f.called)
	f.mu.Unlock()
	if running != 1 {
		t.Fatalf("%d calls had started while the first was still running", running)
	}
	close(f.release)
	for want := 40; want <= 45; want++ {
		r := c.read()
		if r["id"] != float64(want) {
			t.Fatalf("replies came out of order: wanted %d, got %v", want, r["id"])
		}
	}
}

func TestACallWithdrawnWhileItWaitsItsTurnNeverRuns(t *testing.T) {
	f := newFake()
	c := start(t, f)
	initialize(c, "2025-06-18")
	call := func(id int, name string, args map[string]any) {
		c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	}
	call(50, "slow", nil)
	<-f.started
	call(51, "echo", map[string]any{"Text": "withdrawn"})
	call(52, "echo", map[string]any{"Text": "kept"})
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 51}})
	time.Sleep(200 * time.Millisecond)
	close(f.release)
	if r := c.read(); r["id"] != float64(50) {
		t.Fatalf("first reply %v", r)
	}
	if r := c.read(); r["id"] != float64(52) {
		t.Fatalf("the withdrawn call must not answer; the next reply was %v", r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, line := range f.called {
		if strings.Contains(line, "withdrawn") {
			t.Fatalf("a call withdrawn before its turn ran anyway: %v", f.called)
		}
	}
}
