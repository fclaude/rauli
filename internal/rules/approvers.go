package rules

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/rauli/internal/tools"
)

// ErrNoAnswer is returned when nobody answered an approval request in time.
var ErrNoAnswer = errors.New("nobody answered the approval request in time")

// ErrUnknownRequest is returned when Resolve names a request that is not
// waiting, because it was answered already or it ran out of time.
var ErrUnknownRequest = errors.New("there is no such approval request waiting")

// Request is an action waiting for a person to approve it.
type Request struct {
	ID      string    `json:"id"`
	Tool    string    `json:"tool"`
	Kind    string    `json:"kind"`
	URL     string    `json:"url"`
	Element string    `json:"element"`
	Detail  string    `json:"detail"`
	Reason  string    `json:"reason"`
	Time    time.Time `json:"time"`
}

// Approvals is what a live view needs to show waiting requests to a person
// and take their answers. Queue implements it.
type Approvals interface {
	// Pending lists the requests waiting, oldest first.
	Pending() []Request
	// Resolve answers a request. It fails if the request is no longer waiting.
	Resolve(id string, approve bool) error
	// Changes receives a value whenever a request arrives or is settled. A
	// reader that is slow misses nothing it needs: it reads Pending again.
	Changes() <-chan struct{}
}

// Deny is the approver used when nothing else is configured. It never approves,
// so a rule that asks for approval refuses until someone sets up a way to answer.
type Deny struct{}

func (Deny) Approve(context.Context, tools.Action, string) (bool, error) { return false, nil }

// Queue holds approval requests until a person answers them, through whatever
// shows Pending to them and calls Resolve. A request nobody answers within the
// timeout is refused.
type Queue struct {
	timeout time.Duration

	mu      sync.Mutex
	seq     int
	waiting map[string]*waiter
	changes chan struct{}
}

type waiter struct {
	req    Request
	answer chan bool
}

// NewQueue makes a Queue. A zero timeout means five minutes.
func NewQueue(timeout time.Duration) *Queue {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &Queue{timeout: timeout, waiting: map[string]*waiter{}, changes: make(chan struct{}, 1)}
}

var (
	_ Approver  = (*Queue)(nil)
	_ Approvals = (*Queue)(nil)
)

func (q *Queue) notify() {
	select {
	case q.changes <- struct{}{}:
	default:
	}
}

// Approve adds the action to the queue and waits for the answer.
func (q *Queue) Approve(ctx context.Context, a tools.Action, reason string) (bool, error) {
	q.mu.Lock()
	q.seq++
	id := fmt.Sprintf("r%d", q.seq)
	w := &waiter{
		req: Request{ID: id, Tool: a.Tool, Kind: a.Kind, URL: a.URL, Element: a.Element,
			Detail: a.Detail, Reason: reason, Time: time.Now()},
		answer: make(chan bool, 1),
	}
	q.waiting[id] = w
	q.mu.Unlock()
	q.notify()

	timer := time.NewTimer(q.timeout)
	defer timer.Stop()
	defer func() {
		q.mu.Lock()
		delete(q.waiting, id)
		q.mu.Unlock()
		q.notify()
	}()

	select {
	case ok := <-w.answer:
		return ok, nil
	case <-timer.C:
		return false, ErrNoAnswer
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// Pending lists the requests waiting, oldest first.
func (q *Queue) Pending() []Request {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Request, 0, len(q.waiting))
	for _, w := range q.waiting {
		out = append(out, w.req)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.Before(out[j].Time)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Resolve answers a waiting request.
func (q *Queue) Resolve(id string, approve bool) error {
	q.mu.Lock()
	w, ok := q.waiting[id]
	if ok {
		// Taking it out here, under the lock, makes a second answer fail.
		delete(q.waiting, id)
	}
	q.mu.Unlock()
	if !ok {
		return ErrUnknownRequest
	}
	w.answer <- approve
	q.notify()
	return nil
}

// Changes receives a value whenever the set of waiting requests changes.
func (q *Queue) Changes() <-chan struct{} { return q.changes }

// Command approves by running a program. The program gets the request as JSON
// on its standard input and approves by exiting with status 0; any other exit
// status refuses. It is the way to route approvals to a phone or a chat.
type Command struct {
	// Argv is the program and its arguments; no shell is involved.
	Argv []string
	// Timeout bounds the run. Zero means two minutes.
	Timeout time.Duration
}

var _ Approver = Command{}

func (c Command) Approve(ctx context.Context, a tools.Action, reason string) (bool, error) {
	if len(c.Argv) == 0 {
		return false, errors.New("the approval command is empty")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload, err := json.Marshal(Request{Tool: a.Tool, Kind: a.Kind, URL: a.URL, Element: a.Element,
		Detail: a.Detail, Reason: reason, Time: time.Now()})
	if err != nil {
		return false, err
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Stdin = bytes.NewReader(payload)
	// What the program prints is its own business; keeping it would only
	// risk putting a page's address into a log by accident.
	err = cmd.Run()
	switch {
	case err == nil:
		return true, nil
	case ctx.Err() != nil:
		return false, ErrNoAnswer
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return false, nil
	}
	return false, fmt.Errorf("could not run the approval command %q: %w", c.Argv[0], err)
}

// Terminal approves by asking on the person's terminal.
type Terminal struct {
	// Timeout bounds the wait for an answer. Zero means two minutes.
	Timeout time.Duration
	// Open gives the terminal to ask on. The default is /dev/tty, which works
	// even when the program's own input and output carry something else, such
	// as a protocol over a pipe.
	Open func() (io.ReadWriteCloser, error)

	mu sync.Mutex // one question at a time
}

var _ Approver = (*Terminal)(nil)

func (t *Terminal) Approve(ctx context.Context, a tools.Action, reason string) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	open := t.Open
	if open == nil {
		open = func() (io.ReadWriteCloser, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) }
	}
	tty, err := open()
	if err != nil {
		return false, fmt.Errorf("there is no terminal to ask on: %w", err)
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\nThe agent wants to %s", a.Kind)
	if a.Element != "" {
		fmt.Fprintf(&b, " %s", a.Element)
	}
	if a.Detail != "" {
		fmt.Fprintf(&b, " (%s)", a.Detail)
	}
	if a.URL != "" {
		fmt.Fprintf(&b, " on %s", a.URL)
	}
	if reason != "" {
		fmt.Fprintf(&b, "\n  %s", reason)
	}
	b.WriteString("\nAllow? [y/N] ")
	if _, err := io.WriteString(tty, b.String()); err != nil {
		tty.Close()
		return false, err
	}

	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(tty).ReadString('\n')
		line <- s
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case s := <-line:
		tty.Close()
		s = strings.ToLower(strings.TrimSpace(s))
		return s == "y" || s == "yes", nil
	case <-timer.C:
		// Closing the terminal ends a read that is still waiting on it.
		tty.Close()
		return false, ErrNoAnswer
	case <-ctx.Done():
		tty.Close()
		return false, ctx.Err()
	}
}
