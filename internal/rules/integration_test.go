package rules

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fclaude/rauli/internal/chrome"
	"github.com/fclaude/rauli/internal/tools"
)

// These tests drive a real headless browser through a real tools.Session with
// the rules engine as its guard. They share one browser: starting one for each
// test would triple the run time and the load on the machine.

const canary = "typed-by-the-agent"
const password = "hunter2-correct-horse"

type fixtureSites struct {
	a, b       *httptest.Server
	bHits      atomic.Int64
	pixelsSeen atomic.Int64
}

func sites(t *testing.T) *fixtureSites {
	t.Helper()
	f := &fixtureSites{}
	html := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		}
	}
	bm := http.NewServeMux()
	bm.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.bHits.Add(1)
		html(`<!doctype html><title>Site B</title><h1>Site B</h1>`)(w, r)
	})
	bm.HandleFunc("/pixel.png", func(w http.ResponseWriter, r *http.Request) {
		f.bHits.Add(1)
		f.pixelsSeen.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG"))
	})
	f.b = httptest.NewServer(bm)
	t.Cleanup(f.b.Close)

	am := http.NewServeMux()
	am.HandleFunc("/", html(fmt.Sprintf(`<!doctype html><title>Site A</title><h1>Site A</h1>
<img src="%s/pixel.png" alt="tracker">
<a href="%s/">to site B</a>
<button onclick="document.title='paid'">Pay now</button>
<button onclick="document.title='saved'">Save</button>
<label for="u">User</label><input id="u" name="u">
<label for="p">Password</label><input id="p" name="p" type="password">`, f.b.URL, f.b.URL)))
	f.a = httptest.NewServer(am)
	t.Cleanup(f.a.Close)
	return f
}

type buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func session(t *testing.T, e *Engine, v tools.Vault) *tools.Session {
	t.Helper()
	if _, err := chrome.Find(); err != nil {
		t.Skip(err)
	}
	s, err := tools.New(context.Background(), tools.Options{Guard: e, Vault: v, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func call(t *testing.T, s *tools.Session, name string, args any) (tools.Result, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	return s.Call(ctx, name, raw)
}

func must(t *testing.T, s *tools.Session, name string, args any) tools.Result {
	t.Helper()
	r, err := call(t, s, name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return r
}

func refOf(t *testing.T, text, line string) string {
	t.Helper()
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, line) {
			if i := strings.Index(l, "[ref="); i >= 0 {
				return l[i+5 : i+strings.Index(l[i:], "]")]
			}
		}
	}
	t.Fatalf("no reference for %q in:\n%s", line, text)
	return ""
}

func TestAnAllowListBlocksSubresourcesAndNavigations(t *testing.T) {
	f := sites(t)
	e := engine(t, Config{Network: NetworkConfig{Allow: []string{f.a.URL}}}, nil)
	s := session(t, e, nil)

	r := must(t, s, "navigate", map[string]any{"url": f.a.URL})
	if !strings.Contains(r.Text, "Site A") {
		t.Fatalf("the allowed site did not load:\n%s", r.Text)
	}
	// The page asked for an image from the other site. The rules refused it, the
	// agent was told why, and the request never reached that server.
	if want := "Blocked by the rules: " + f.b.URL + "/pixel.png"; !strings.Contains(r.Text, want) {
		t.Fatalf("the blocked subresource is not reported:\n%s", r.Text)
	}
	if !strings.Contains(r.Text, "not on the list of sites the rules allow") {
		t.Fatalf("the reason is missing:\n%s", r.Text)
	}
	if f.pixelsSeen.Load() != 0 {
		t.Fatal("the blocked image was fetched anyway")
	}

	// Going to the other site is refused with the reason.
	_, err := call(t, s, "navigate", map[string]any{"url": f.b.URL})
	if err == nil || !strings.Contains(err.Error(), "not on the list of sites the rules allow") {
		t.Fatalf("navigating off the list: %v", err)
	}
	if f.bHits.Load() != 0 {
		t.Fatalf("the other site was contacted %d times", f.bHits.Load())
	}
	// A click that leads off the list is told the same.
	snap := must(t, s, "navigate", map[string]any{"url": f.a.URL}).Text
	r = must(t, s, "click", map[string]any{"ref": refOf(t, snap, `link "to site B"`)})
	if !strings.Contains(r.Text, "Blocked by the rules: "+f.b.URL+"/") || f.bHits.Load() != 0 {
		t.Fatalf("a click off the list:\n%s", r.Text)
	}
}

func TestAskGoesThroughTheQueueToAPerson(t *testing.T) {
	f := sites(t)
	q := NewQueue(10 * time.Second)
	e := engine(t, Config{Actions: ActionsConfig{Rule: []ActionRule{
		{Kind: "click", Element: "*pay*", Decision: "ask", Reason: "payments need a person"},
	}}}, q)
	s := session(t, e, nil)
	snap := must(t, s, "navigate", map[string]any{"url": f.a.URL}).Text
	pay := refOf(t, snap, `button "Pay now"`)

	// A person, played here by a goroutine, answers whatever waits.
	answers := make(chan bool, 2)
	answered := make(chan Request, 2)
	go func() {
		for yes := range answers {
			for {
				<-q.Changes()
				if p := q.Pending(); len(p) > 0 {
					answered <- p[0]
					q.Resolve(p[0].ID, yes)
					break
				}
			}
		}
	}()

	answers <- false
	_, err := call(t, s, "click", map[string]any{"ref": pay})
	if err == nil || !strings.Contains(err.Error(), "did not approve") {
		t.Fatalf("a refused request should refuse the click, got %v", err)
	}
	req := <-answered
	if req.Tool != "click" || req.Element != `button "Pay now"` || req.Reason != "payments need a person" || !strings.HasPrefix(req.URL, f.a.URL) {
		t.Fatalf("the person was shown %+v", req)
	}
	if text := must(t, s, "snapshot", nil).Text; !strings.Contains(text, "Page: Site A") {
		t.Fatalf("the refused click still ran:\n%s", text)
	}

	answers <- true
	must(t, s, "click", map[string]any{"ref": pay})
	if text := must(t, s, "snapshot", nil).Text; !strings.Contains(text, "Page: paid") {
		t.Fatalf("the approved click did not run:\n%s", text)
	}
	// Other buttons were never in question.
	must(t, s, "click", map[string]any{"ref": refOf(t, must(t, s, "snapshot", nil).Text, `button "Save"`)})
	close(answers)
}

func TestAskGoesToACommand(t *testing.T) {
	f := sites(t)
	dir := t.TempDir()
	flag := filepath.Join(dir, "answer")
	os.WriteFile(flag, []byte("yes"), 0o600)
	sh := filepath.Join(dir, "approve.sh")
	os.WriteFile(sh, []byte("#!/bin/sh\ncat >"+filepath.Join(dir, "seen.json")+"\n[ \"$(cat "+flag+")\" = yes ]\n"), 0o700)

	e := engine(t, Config{
		Actions:  ActionsConfig{Rule: []ActionRule{{Kind: "click", Element: "*pay*", Decision: "ask"}}},
		Approval: ApprovalConfig{Mode: "command", Command: []string{sh}, Timeout: "20s"},
	}, nil)
	s := session(t, e, nil)
	snap := must(t, s, "navigate", map[string]any{"url": f.a.URL}).Text
	pay := refOf(t, snap, `button "Pay now"`)

	os.WriteFile(flag, []byte("no"), 0o600)
	if _, err := call(t, s, "click", map[string]any{"ref": pay}); err == nil || !strings.Contains(err.Error(), "did not approve") {
		t.Fatalf("the command said no: %v", err)
	}
	os.WriteFile(flag, []byte("yes"), 0o600)
	must(t, s, "click", map[string]any{"ref": pay})
	if text := must(t, s, "snapshot", nil).Text; !strings.Contains(text, "Page: paid") {
		t.Fatalf("the command said yes and the click did not run:\n%s", text)
	}
	seen, _ := os.ReadFile(filepath.Join(dir, "seen.json"))
	var r Request
	if err := json.Unmarshal(seen, &r); err != nil || r.Kind != "click" || r.Element != `button "Pay now"` {
		t.Fatalf("the command was given %q (%v)", seen, err)
	}
}

func TestEvalAsksByDefaultAndRefusesWithoutAnApprover(t *testing.T) {
	f := sites(t)
	s := session(t, engine(t, Config{}, nil), nil)
	must(t, s, "navigate", map[string]any{"url": f.a.URL})
	_, err := call(t, s, "eval", map[string]any{"expression": "1+1"})
	if err == nil || !strings.Contains(err.Error(), "needs a person's approval") {
		t.Fatalf("eval with nobody to approve: %v", err)
	}

	q := NewQueue(10 * time.Second)
	s2 := session(t, engine(t, Config{}, q), nil)
	must(t, s2, "navigate", map[string]any{"url": f.a.URL})
	go func() {
		for i := 0; i < 50; i++ {
			if p := q.Pending(); len(p) > 0 {
				q.Resolve(p[0].ID, true)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	if r := must(t, s2, "eval", map[string]any{"expression": "1+1"}); r.Text != "2" {
		t.Fatalf("eval returned %q", r.Text)
	}
}

type stubVault struct{}

func (stubVault) Handles(context.Context) ([]tools.Handle, error) {
	return []tools.Handle{{Name: "site-a", Fields: []string{"user", "password"}}}, nil
}

func (stubVault) Secret(_ context.Context, handle, field, _ string) (string, error) {
	if handle == "site-a" && field == "password" {
		return password, nil
	}
	if handle == "site-a" && field == "user" {
		return "alice", nil
	}
	return "", fmt.Errorf("no such field")
}

func TestTheAuditLogHoldsNoSecretsAndNoQueryStrings(t *testing.T) {
	f := sites(t)
	e := engine(t, Config{
		Network: NetworkConfig{Allow: []string{f.a.URL}},
		Actions: ActionsConfig{Rule: []ActionRule{
			{Kind: "click", Element: "*pay*", Decision: "deny", Reason: "no payments"},
			{Kind: "eval", Decision: "allow"},
		}},
	}, nil)
	s := session(t, e, stubVault{})

	var log buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Audit(ctx, s, &log) }()
	// Give the subscriptions a moment to start before the first call.
	time.Sleep(200 * time.Millisecond)

	snap := must(t, s, "navigate", map[string]any{"url": f.a.URL + "/?session=TOKENVALUE#private"}).Text
	must(t, s, "type", map[string]any{"ref": refOf(t, snap, `textbox "User"`), "text": canary})
	must(t, s, "fill_credential", map[string]any{"ref": refOf(t, snap, `textbox "Password"`), "handle": "site-a", "field": "password"})
	call(t, s, "click", map[string]any{"ref": refOf(t, snap, `button "Pay now"`)})
	must(t, s, "eval", nil2("document.title"))
	time.Sleep(300 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	out := log.String()
	for _, banned := range []string{password, canary, "TOKENVALUE", "private"} {
		if strings.Contains(out, banned) {
			t.Fatalf("the audit log holds %q:\n%s", banned, out)
		}
	}
	var tool, deny, refusedPayment, evalLine int
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("a log line is not JSON: %q", line)
		}
		switch m["type"] {
		case "tool":
			tool++
			if m["tool"] == "eval" {
				evalLine++
				if _, has := m["summary"]; has {
					t.Fatalf("the audit kept what eval returned: %s", line)
				}
			}
		case "decision":
			if m["outcome"] == "deny" && m["subject"] == "request" {
				deny++
			}
			if m["outcome"] == "deny" && m["subject"] == "action" && m["reason"] == "no payments" && m["element"] == `button "Pay now"` {
				refusedPayment++
			}
		}
	}
	if tool < 5 || evalLine != 1 {
		t.Errorf("expected a line per tool call, got %d tool lines and %d for eval:\n%s", tool, evalLine, out)
	}
	if deny == 0 {
		t.Errorf("the blocked image was not logged as a denied request:\n%s", out)
	}
	if refusedPayment != 1 {
		t.Errorf("the refused payment was not logged once with its rule and reason:\n%s", out)
	}
}

func nil2(expr string) map[string]any { return map[string]any{"expression": expr} }
