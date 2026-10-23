package rules

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/rauli/internal/tools"
)

var sample = tools.Action{Tool: "click", Kind: "click", URL: "https://shop.example/cart", Element: `button "Pay now"`}

func TestQueueHoldsARequestUntilItIsAnswered(t *testing.T) {
	q := NewQueue(5 * time.Second)
	done := make(chan bool, 1)
	go func() {
		ok, err := q.Approve(context.Background(), sample, "payments need a person")
		if err != nil {
			t.Error(err)
		}
		done <- ok
	}()

	select {
	case <-q.Changes():
	case <-time.After(2 * time.Second):
		t.Fatal("no change was announced for the new request")
	}
	pending := q.Pending()
	if len(pending) != 1 {
		t.Fatalf("pending: %+v", pending)
	}
	got := pending[0]
	if got.Tool != "click" || got.Element != `button "Pay now"` || got.Reason != "payments need a person" || got.ID == "" || got.Time.IsZero() {
		t.Fatalf("the request is incomplete: %+v", got)
	}
	if err := q.Resolve(got.ID, true); err != nil {
		t.Fatal(err)
	}
	if ok := <-done; !ok {
		t.Fatal("the answer was yes, but the action was refused")
	}
	if err := q.Resolve(got.ID, false); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("answering twice should fail, got %v", err)
	}
	if len(q.Pending()) != 0 {
		t.Fatalf("an answered request is still pending: %+v", q.Pending())
	}
}

func TestQueueRefusesWhenNobodyAnswersOrTheCallerGivesUp(t *testing.T) {
	q := NewQueue(50 * time.Millisecond)
	if ok, err := q.Approve(context.Background(), sample, ""); ok || !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("got %v, %v", ok, err)
	}
	if len(q.Pending()) != 0 {
		t.Fatal("a request that timed out stays in the queue")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ok, err := NewQueue(time.Minute).Approve(ctx, sample, ""); ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, %v", ok, err)
	}
}

func TestQueueListsOldestFirstAndTakesRefusals(t *testing.T) {
	q := NewQueue(5 * time.Second)
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		go func() {
			ok, _ := q.Approve(context.Background(), sample, "")
			results <- ok
		}()
		deadline := time.Now().Add(2 * time.Second)
		for len(q.Pending()) < i+1 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	}
	p := q.Pending()
	if len(p) != 2 || p[0].ID >= p[1].ID {
		t.Fatalf("pending is not oldest first: %+v", p)
	}
	q.Resolve(p[0].ID, false)
	q.Resolve(p[1].ID, false)
	for i := 0; i < 2; i++ {
		if <-results {
			t.Fatal("a refusal was taken as approval")
		}
	}
}

func TestDenyNeverApproves(t *testing.T) {
	if ok, err := (Deny{}).Approve(context.Background(), sample, ""); ok || err != nil {
		t.Fatalf("got %v, %v", ok, err)
	}
}

func script(t *testing.T, body string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "approve.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{path}
}

func TestCommandApprovesByExitStatusAndReadsTheRequestOnStdin(t *testing.T) {
	out := filepath.Join(t.TempDir(), "seen.json")
	yes := Command{Argv: script(t, `cat > `+out+`; exit 0`)}
	if ok, err := yes.Approve(context.Background(), sample, "because"); !ok || err != nil {
		t.Fatalf("got %v, %v", ok, err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var r Request
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("the command did not get JSON: %v: %s", err, data)
	}
	if r.Kind != "click" || r.Element != `button "Pay now"` || r.Reason != "because" || r.URL != sample.URL {
		t.Fatalf("the command saw %+v", r)
	}

	no := Command{Argv: script(t, `cat >/dev/null; exit 3`)}
	if ok, err := no.Approve(context.Background(), sample, ""); ok || err != nil {
		t.Fatalf("a non-zero exit should refuse quietly, got %v, %v", ok, err)
	}
	if ok, err := (Command{Argv: []string{"/nonexistent/approver"}}).Approve(context.Background(), sample, ""); ok || err == nil {
		t.Fatalf("a command that cannot run is an error, got %v, %v", ok, err)
	}
	if ok, err := (Command{}).Approve(context.Background(), sample, ""); ok || err == nil {
		t.Fatalf("an empty command is an error, got %v, %v", ok, err)
	}
	slow := Command{Argv: script(t, `sleep 5`), Timeout: 100 * time.Millisecond}
	if ok, err := slow.Approve(context.Background(), sample, ""); ok || !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("a command that takes too long should refuse with ErrNoAnswer, got %v, %v", ok, err)
	}
}

type pipeTTY struct {
	io.Reader
	out    *strings.Builder
	closed bool
}

func (p *pipeTTY) Write(b []byte) (int, error) { return p.out.Write(b) }
func (p *pipeTTY) Close() error                { p.closed = true; return nil }

func TestTerminalAsksAndTakesYesOrNo(t *testing.T) {
	for answer, want := range map[string]bool{"y\n": true, "YES\n": true, "n\n": false, "\n": false, "maybe\n": false} {
		var asked strings.Builder
		term := &Terminal{Open: func() (io.ReadWriteCloser, error) {
			return &pipeTTY{Reader: strings.NewReader(answer), out: &asked}, nil
		}}
		ok, err := term.Approve(context.Background(), sample, "payments need a person")
		if err != nil || ok != want {
			t.Errorf("answer %q: got %v, %v; want %v", answer, ok, err, want)
		}
		for _, part := range []string{"click", `button "Pay now"`, "https://shop.example/cart", "payments need a person", "[y/N]"} {
			if !strings.Contains(asked.String(), part) {
				t.Errorf("the question lacks %q: %q", part, asked.String())
			}
		}
	}
}

func TestTerminalGivesUpOnTimeoutAndWithoutATerminal(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	var asked strings.Builder
	term := &Terminal{Timeout: 50 * time.Millisecond, Open: func() (io.ReadWriteCloser, error) {
		return &pipeTTY{Reader: pr, out: &asked}, nil
	}}
	if ok, err := term.Approve(context.Background(), sample, ""); ok || !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("got %v, %v", ok, err)
	}
	none := &Terminal{Open: func() (io.ReadWriteCloser, error) { return nil, errors.New("no tty") }}
	if ok, err := none.Approve(context.Background(), sample, ""); ok || err == nil || !strings.Contains(err.Error(), "no terminal") {
		t.Fatalf("got %v, %v", ok, err)
	}
}
