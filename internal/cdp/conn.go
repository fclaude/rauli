// Package cdp is a small client for the Chrome DevTools Protocol: JSON
// messages over a WebSocket. It knows nothing about pages or the DOM. It sends
// a command, hands back the reply, and delivers the events the browser sends.
//
// One connection serves the whole browser. Commands for a page carry that
// page's session id (the "flat" mode of Target.attachToTarget), so a single
// WebSocket is enough however many tabs are open.
package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

// maxMessage bounds one protocol message. A full-page screenshot arrives as a
// single base64 string, so the library's default of 32 KiB is far too small.
const maxMessage = 128 << 20

// Error is the error object a command returns.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("devtools: %s (%d): %s", e.Message, e.Code, e.Data)
	}
	return fmt.Sprintf("devtools: %s (%d)", e.Message, e.Code)
}

// ErrClosed is returned by calls made after the connection has ended.
var ErrClosed = errors.New("devtools connection closed")

type reply struct {
	Result json.RawMessage
	Err    *Error
}

type subKey struct{ session, method string }

type subscription struct {
	id int
	ch chan json.RawMessage
}

// Conn is one DevTools connection. It is safe for concurrent use.
type Conn struct {
	ws     *websocket.Conn
	cancel context.CancelFunc

	nextID atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan reply
	subs    map[subKey][]subscription
	subSeq  int
	err     error

	done chan struct{}
}

// Dial connects to a DevTools WebSocket URL and starts reading from it.
func Dial(ctx context.Context, url string) (*Conn, error) {
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", url, err)
	}
	ws.SetReadLimit(maxMessage)
	// The read loop outlives the context that dialled, so it gets its own.
	loopCtx, cancel := context.WithCancel(context.Background())
	c := &Conn{
		ws:      ws,
		cancel:  cancel,
		pending: map[int64]chan reply{},
		subs:    map[subKey][]subscription{},
		done:    make(chan struct{}),
	}
	go c.readLoop(loopCtx)
	return c, nil
}

// Done is closed when the connection has ended, because the browser exited or
// Close was called.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err says why the connection ended, or nil while it is open.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the connection and fails every call still waiting.
func (c *Conn) Close() error {
	c.cancel()
	err := c.ws.Close(websocket.StatusNormalClosure, "")
	<-c.done
	return err
}

type outgoing struct {
	ID        int64  `json:"id"`
	SessionID string `json:"sessionId,omitempty"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
}

type incoming struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"sessionId"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
	Result    json.RawMessage `json:"result"`
	Error     *Error          `json:"error"`
}

// Call sends a command and waits for its reply. session is empty for a
// command to the browser itself. out may be nil when the result is unused.
func (c *Conn) Call(ctx context.Context, session, method string, params, out any) error {
	id := c.nextID.Add(1)
	ch := make(chan reply, 1)

	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.pending[id] = ch
	c.mu.Unlock()

	data, err := json.Marshal(outgoing{ID: id, SessionID: session, Method: method, Params: params})
	if err != nil {
		c.forget(id)
		return err
	}
	if err := c.ws.Write(ctx, websocket.MessageText, data); err != nil {
		c.forget(id)
		return fmt.Errorf("%s: %w", method, err)
	}

	select {
	case r := <-ch:
		if r.Err != nil {
			return fmt.Errorf("%s: %w", method, r.Err)
		}
		if out != nil && len(r.Result) > 0 {
			if err := json.Unmarshal(r.Result, out); err != nil {
				return fmt.Errorf("%s: decode result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.forget(id)
		return fmt.Errorf("%s: %w", method, ctx.Err())
	case <-c.done:
		return fmt.Errorf("%s: %w", method, c.Err())
	}
}

func (c *Conn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// eventBuffer is how many events a subscriber may fall behind by. Beyond it
// the oldest are dropped, since blocking the read loop would stall every
// command on the connection.
const eventBuffer = 1024

// Subscribe delivers the params of every event with the given method for the
// session ("" for browser-level events). Call the returned function to stop.
func (c *Conn) Subscribe(session, method string) (<-chan json.RawMessage, func()) {
	key := subKey{session, method}
	ch := make(chan json.RawMessage, eventBuffer)

	c.mu.Lock()
	c.subSeq++
	sub := subscription{id: c.subSeq, ch: ch}
	c.subs[key] = append(c.subs[key], sub)
	c.mu.Unlock()

	cancel := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		list := c.subs[key]
		for i, s := range list {
			if s.id == sub.id {
				c.subs[key] = append(list[:i:i], list[i+1:]...)
				return
			}
		}
	}
	return ch, cancel
}

func (c *Conn) readLoop(ctx context.Context) {
	defer close(c.done)
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			c.fail(err)
			return
		}
		var m incoming
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if m.ID != 0 {
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- reply{Result: m.Result, Err: m.Error}
			}
			continue
		}
		if m.Method == "" {
			continue
		}
		c.mu.Lock()
		list := append([]subscription(nil), c.subs[subKey{m.SessionID, m.Method}]...)
		c.mu.Unlock()
		for _, s := range list {
			select {
			case s.ch <- m.Params:
			default:
				// A subscriber that has stopped reading loses its oldest event.
				select {
				case <-s.ch:
				default:
				}
				select {
				case s.ch <- m.Params:
				default:
				}
			}
		}
	}
}

// fail records why the connection ended and wakes everything waiting on it.
func (c *Conn) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		if errors.Is(err, context.Canceled) || websocket.CloseStatus(err) == websocket.StatusNormalClosure {
			err = ErrClosed
		}
		c.err = err
	}
	c.mu.Unlock()
}
