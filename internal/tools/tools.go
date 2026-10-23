// Package tools is the interface agents use. It owns one browser session and
// offers every operation as a named tool with a JSON schema, so the same set
// serves a protocol server, a command line and a script alike.
//
// It is also where the rules and the credentials plug in. Two interfaces, Guard
// and Vault, are the only way a decision or a secret reaches a tool; the
// packages that implement them know nothing about browsers, and the tools know
// nothing about how a rule is written.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/fclaude/rauli/internal/chrome"
)

// Image is a picture returned by a tool.
type Image struct {
	MIME string
	Data []byte
}

// Result is what a tool gives back: text for a reader, and pictures when the
// tool takes screenshots.
type Result struct {
	Text   string
	Images []Image
}

// Tool is one operation an agent can ask for.
type Tool struct {
	Name        string
	Description string
	// Schema is the JSON Schema of the arguments, an object schema.
	Schema map[string]any
	// ReadOnly marks a tool that only looks. It changes nothing in the page,
	// so a policy may let it through where it would stop an action.
	ReadOnly bool

	run func(ctx context.Context, s *Session, args json.RawMessage) (Result, error)
}

// Action is what a Guard is asked to approve before the tool carries it out.
type Action struct {
	// Tool is the name of the tool.
	Tool string
	// Kind groups tools by what they can do to the world: "navigate", "click",
	// "type", "key", "select", "upload", "eval", "credential".
	Kind string
	// URL is the address of the page the action happens on.
	URL string
	// Element describes the target, such as `button "Pay now"`, when there is one.
	Element string
	// Detail carries extra facts that rules may match on: the address to go to,
	// a file's path, the name of a credential. It never holds typed text.
	Detail string
}

// Guard decides what the agent may do. It is asked about every request the
// browser makes, and about every action before the tool runs. A nil error
// allows; any error refuses, and its text is shown to the agent so it can
// explain itself or ask for something else. A Guard may block while it waits
// for a person to approve, and must give up when ctx ends.
type Guard interface {
	CheckURL(ctx context.Context, r chrome.Request) error
	CheckAction(ctx context.Context, a Action) error
}

// AllowAll is the Guard used when no rules are given.
type AllowAll struct{}

func (AllowAll) CheckURL(context.Context, chrome.Request) error { return nil }
func (AllowAll) CheckAction(context.Context, Action) error      { return nil }

// Handle names a credential in the vault. It never carries a secret.
type Handle struct {
	// Name is what an agent asks for, such as "work-email".
	Name string `json:"name"`
	// Fields are the parts the credential has, such as "user", "password", "totp".
	Fields []string `json:"fields"`
	// Origins limit where it may be used, such as "https://mail.example.com".
	// Empty means anywhere, which a vault should treat as a decision to be made on purpose.
	Origins []string `json:"origins,omitempty"`
}

// Vault holds the credentials. The agent never sees a value: a tool asks for
// one field, types it into the page itself, and reports only that it did.
type Vault interface {
	// Handles lists what is available, without secrets.
	Handles(ctx context.Context) ([]Handle, error)
	// Secret returns one field of one credential for use on the page at pageURL.
	// A vault refuses a page outside the credential's origins.
	Secret(ctx context.Context, handle, field, pageURL string) (string, error)
}

// Event is one thing the agent did, for a live view or an audit log to read.
type Event struct {
	Time    time.Time       `json:"time"`
	Tool    string          `json:"tool"`
	Args    json.RawMessage `json:"args,omitempty"`
	URL     string          `json:"url,omitempty"`
	Title   string          `json:"title,omitempty"`
	Summary string          `json:"summary,omitempty"`
	Error   string          `json:"error,omitempty"`
	Elapsed time.Duration   `json:"elapsed"`
}

// Options say how to build a Session.
type Options struct {
	Browser chrome.Options
	Guard   Guard
	Vault   Vault
	// Timeout bounds each tool call. Zero means 45 seconds.
	Timeout time.Duration
}

// Session is a browser and the tools to drive it. It is safe for concurrent
// use, but tool calls run one at a time: a browser tab is a single thing to
// act on.
type Session struct {
	b     *chrome.Browser
	guard Guard
	vault Vault
	opts  Options

	call sync.Mutex // one tool at a time

	mu    sync.Mutex
	cur   *chrome.Page
	subs  map[int]chan Event
	seq   int
	known map[string]bool // tabs already reported to the agent
	held  chan struct{}   // non-nil while a person has the browser; closed to give it back

	tools map[string]Tool
}

// New starts the browser. The browser's request filter is the Guard, so rules
// apply to scripts, images and frames as well as to typed addresses.
func New(ctx context.Context, opts Options) (*Session, error) {
	if opts.Guard == nil {
		opts.Guard = AllowAll{}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 45 * time.Second
	}
	bo := opts.Browser
	guard := opts.Guard
	bo.Filter = guard.CheckURL
	b, err := chrome.Launch(ctx, bo)
	if err != nil {
		return nil, err
	}
	s := &Session{b: b, guard: guard, vault: opts.Vault, opts: opts, subs: map[int]chan Event{}, tools: map[string]Tool{}}
	for _, t := range builtin() {
		s.tools[t.Name] = t
	}
	return s, nil
}

// Close ends the browser.
func (s *Session) Close() error { return s.b.Close() }

// Guard is the Guard the session was built with. An audit log uses it to read
// the decisions a rules engine made.
func (s *Session) Guard() Guard { return s.guard }

// Browser gives access to the browser, for interfaces that need more than tools.
func (s *Session) Browser() *chrome.Browser { return s.b }

// Tools lists the tools, in name order.
func (s *Session) Tools() []Tool {
	out := make([]Tool, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Page is the tab the tools act on, opening one if there is none yet.
func (s *Session) Page(ctx context.Context) (*chrome.Page, error) {
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	if cur != nil {
		return cur, nil
	}
	pages, err := s.b.Pages(ctx)
	if err != nil {
		return nil, err
	}
	var p *chrome.Page
	if len(pages) > 0 {
		p = pages[0]
	} else if p, err = s.b.NewPage(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cur = p
	s.mu.Unlock()
	return p, nil
}

func (s *Session) setPage(p *chrome.Page) {
	s.mu.Lock()
	s.cur = p
	s.mu.Unlock()
}

// Subscribe delivers an Event for every tool call, as it finishes. A reader
// that falls behind loses events, so a slow log can never slow the browser.
func (s *Session) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	s.mu.Lock()
	s.seq++
	id := s.seq
	s.subs[id] = ch
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

func (s *Session) emit(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// ErrHeld is returned when a person has taken over the browser and has not
// given it back in time. The agent can try again later.
var ErrHeld = errors.New("a person is using the browser right now: try again in a little while")

// Hold takes the browser for a person. Tool calls wait until Release.
func (s *Session) Hold() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held == nil {
		s.held = make(chan struct{})
	}
}

// Release gives the browser back to the agent.
func (s *Session) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held != nil {
		close(s.held)
		s.held = nil
	}
}

// Held says whether a person has the browser.
func (s *Session) Held() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held != nil
}

// ErrUnknownTool is returned for a name that is not a tool.
var ErrUnknownTool = errors.New("unknown tool")

// Call runs a tool by name with JSON arguments.
func (s *Session) Call(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	t, ok := s.tools[name]
	if !ok {
		return Result{}, fmt.Errorf("%w %q", ErrUnknownTool, name)
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}

	// A person driving the tab goes first. A call waits for the browser to be
	// handed back, within its own time limit, instead of fighting for it.
	for {
		s.mu.Lock()
		held := s.held
		s.mu.Unlock()
		if held == nil {
			break
		}
		select {
		case <-held:
		case <-ctx.Done():
			return Result{}, ErrHeld
		case <-time.After(s.opts.Timeout):
			return Result{}, ErrHeld
		}
	}

	s.call.Lock()
	defer s.call.Unlock()
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()

	start := time.Now()
	res, err := t.run(ctx, s, args)

	e := Event{Time: start, Tool: name, Args: redact(name, args), Elapsed: time.Since(start)}
	if p, perr := s.Page(ctx); perr == nil {
		if info, ierr := p.Info(context.Background()); ierr == nil {
			e.URL, e.Title = info.URL, info.Title
		}
	}
	if err != nil {
		e.Error = err.Error()
	} else {
		e.Summary = firstLine(res.Text)
	}
	s.emit(e)
	return res, err
}

// redact removes from the logged arguments what must not be kept: typed text.
// Only the length is recorded, because a field may hold something private and
// a log is read by more people than the page is.
func redact(tool string, args json.RawMessage) json.RawMessage {
	if tool != "type" {
		return args
	}
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return nil
	}
	if text, ok := m["text"].(string); ok {
		m["text"] = fmt.Sprintf("<%d characters>", len([]rune(text)))
	}
	out, _ := json.Marshal(m)
	return out
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
