package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// syncBuffer is a buffer a test can read while a command writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var liveView = regexp.MustCompile(`Live view: (http://\S+)`)

func waitForView(t *testing.T, bufs ...*syncBuffer) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, b := range bufs {
			if m := liveView.FindStringSubmatch(b.String()); m != nil {
				return m[1]
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the command never printed the address of the live view: %q", bufs[0].String())
	return ""
}

func emptyConfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWatchServesAViewOnlyTheHolderOfTheAddressCanOpen(t *testing.T) {
	needBrowser(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, errw := &syncBuffer{}, &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"watch", "--config", emptyConfig(t)}, strings.NewReader(""), out, errw)
	}()
	addr := waitForView(t, out)

	u, err := url.Parse(addr)
	if err != nil || u.Query().Get("token") == "" {
		t.Fatalf("the address carries no secret: %q (%v)", addr, err)
	}
	jar, _ := cookiejar.New(nil)
	holder := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	resp, err := holder.Get(addr)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "Take control") {
		t.Fatalf("the holder of the address got %d and %q", resp.StatusCode, string(body)[:min(len(body), 200)])
	}

	// Someone who only knows where the view is, not its secret, gets nothing.
	bare := *u
	bare.RawQuery = ""
	stranger, err := (&http.Client{Timeout: 10 * time.Second}).Get(bare.String())
	if err != nil {
		t.Fatal(err)
	}
	stranger.Body.Close()
	if stranger.StatusCode == 200 {
		t.Fatalf("the view opened without its secret")
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("stopping the view should end cleanly, got %d: %s", code, errw.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the command did not stop")
	}
}

// A rule can ask for a person's approval. When the live view is on, that
// person is the one looking at it: the request appears there, and the answer
// they give decides whether the action runs.
func TestAnActionThatNeedsApprovalWaitsForThePersonAtTheView(t *testing.T) {
	needBrowser(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errw := &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		code := run(ctx, []string{"serve", "--watch", "--config", emptyConfig(t)}, inR, outW, errw)
		outW.Close()
		done <- code
	}()
	replies := bufio.NewReader(outR)
	send := func(v any) {
		b, _ := json.Marshal(v)
		if _, err := inW.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	type reply struct {
		ID     int `json:"id"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	read := func() reply {
		line, err := replies.ReadBytes('\n')
		if err != nil {
			t.Fatalf("reading a reply: %v (stderr: %s)", err, errw.String())
		}
		var r reply
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("not a protocol message: %q", line)
		}
		return r
	}
	call := func(id int, expr string) {
		send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "eval", "arguments": map[string]any{"expression": expr}}})
	}

	send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "0"}}})
	read()
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})

	// The person opens the view the command printed, and keeps the connection.
	addr := waitForView(t, errw)
	u, _ := url.Parse(addr)
	jar, _ := cookiejar.New(nil)
	resp, err := (&http.Client{Jar: jar, Timeout: 10 * time.Second}).Get(addr)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var cookies []string
	for _, c := range jar.Cookies(u) {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	ws, _, err := websocket.Dial(ctx, "ws://"+u.Host+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {strings.Join(cookies, "; ")}, "Origin": {"http://" + u.Host}},
	})
	if err != nil {
		t.Fatalf("the person could not open the view's connection: %v", err)
	}
	defer ws.CloseNow()

	waitingRequest := func() string {
		deadline := time.After(20 * time.Second)
		for {
			rctx, rcancel := context.WithTimeout(ctx, 20*time.Second)
			_, data, err := ws.Read(rctx)
			rcancel()
			if err != nil {
				t.Fatalf("the view said nothing about a request waiting: %v", err)
			}
			var m struct {
				T    string `json:"t"`
				List []struct {
					ID      string `json:"id"`
					Tool    string `json:"tool"`
					Element string `json:"element"`
					Detail  string `json:"detail"`
				} `json:"list"`
			}
			if json.Unmarshal(data, &m) == nil && m.T == "approvals" && len(m.List) > 0 {
				if m.List[0].Tool != "eval" {
					t.Fatalf("the waiting request is for %q, not eval", m.List[0].Tool)
				}
				return m.List[0].ID
			}
			select {
			case <-deadline:
				t.Fatal("no request ever appeared in the view")
			default:
			}
		}
	}
	answer := func(id string, approve bool) {
		b, _ := json.Marshal(map[string]any{"t": "resolve", "id": id, "approve": approve})
		if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}

	call(2, "6*7")
	answer(waitingRequest(), true)
	if r := read(); r.ID != 2 || r.Result.IsError || r.Result.Content[0].Text != "42" {
		t.Fatalf("an approved script should run and return its value, got %+v", r)
	}

	call(3, "'never runs'")
	answer(waitingRequest(), false)
	r := read()
	if r.ID != 3 || !r.Result.IsError || strings.Contains(r.Result.Content[0].Text, "never runs") && !strings.Contains(r.Result.Content[0].Text, "not allowed") {
		t.Fatalf("a refused script must not run and the reason must say so, got %+v", r)
	}

	inW.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve ended with %d: %s", code, errw.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not stop")
	}
}
