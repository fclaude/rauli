package main

import (
	"context"
	"net"
	"strconv"
	"sync"

	"github.com/fclaude/rauli/internal/config"
	"github.com/fclaude/rauli/internal/rules"
	"github.com/fclaude/rauli/internal/tools"
	"github.com/fclaude/rauli/internal/watch"
)

// approvals shows the rules' waiting requests in the live view. The queue has
// one channel that says "something changed"; the view wants one per viewer, so
// this reads the queue's and tells each viewer.
type approvals struct {
	q *rules.Queue

	mu   sync.Mutex
	seq  int
	subs map[int]chan struct{}
}

func newApprovals(ctx context.Context, q *rules.Queue) *approvals {
	a := &approvals{q: q, subs: map[int]chan struct{}{}}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-q.Changes():
				a.mu.Lock()
				for _, ch := range a.subs {
					select {
					case ch <- struct{}{}:
					default:
					}
				}
				a.mu.Unlock()
			}
		}
	}()
	return a
}

func (a *approvals) Pending() []watch.Pending {
	var out []watch.Pending
	for _, r := range a.q.Pending() {
		out = append(out, watch.Pending{ID: r.ID, Tool: r.Tool, Element: r.Element, Detail: r.Detail, Reason: r.Reason, Since: r.Time})
	}
	return out
}

func (a *approvals) Resolve(id string, approve bool) error { return a.q.Resolve(id, approve) }

func (a *approvals) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	a.mu.Lock()
	a.seq++
	id := a.seq
	a.subs[id] = ch
	a.mu.Unlock()
	return ch, func() {
		a.mu.Lock()
		delete(a.subs, id)
		a.mu.Unlock()
	}
}

// approverFor picks who answers a request that needs a person. A way the person
// chose in the rules wins; with none chosen, the live view is the way, because
// it is the thing they are looking at. Without a view there is no one to ask,
// and the rules refuse with a message that says how to set one up.
func approverFor(cfg config.Config, q *rules.Queue) rules.Approver {
	if q != nil && cfg.Rules.Approval.Mode == "" {
		return q
	}
	return nil
}

// startView serves the live view of a session and returns the address to open.
func startView(ctx context.Context, cfg config.Config, session *tools.Session, q *rules.Queue, listen string) (string, error) {
	host, port := "127.0.0.1", cfg.Watch.Port
	if cfg.Watch.Listen != "" {
		host = cfg.Watch.Listen
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	if listen != "" {
		addr = listen
	}
	o := watch.Options{Addr: addr}
	if q != nil {
		o.Approvals = newApprovals(ctx, q)
	}
	return watch.Serve(ctx, session, o)
}
