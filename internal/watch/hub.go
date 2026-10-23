package watch

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/fclaude/rauli/internal/chrome"
	"github.com/fclaude/rauli/internal/tools"
)

const (
	// logSize is how many past actions a new viewer is shown.
	logSize = 200
	// tickEvery is how often the hub checks which tab is current and what it is called.
	tickEvery = 300 * time.Millisecond
	// stillEvery is how long a silent screencast is trusted before a fresh
	// picture is taken. A page that does not change sends no frames, and a
	// viewer that connects to it should not stare at an empty box.
	stillEvery = 1500 * time.Millisecond
)

// What the server sends. Pictures go as binary messages; everything else is
// one JSON object with a "t" field that says what it is.
type (
	helloMsg struct {
		T        string `json:"t"`
		DevTools string `json:"devtools"`
	}
	metaMsg struct {
		T      string  `json:"t"`
		URL    string  `json:"url"`
		Title  string  `json:"title"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
		Open   bool    `json:"open"`
	}
	controlMsg struct {
		T   string `json:"t"`
		Who string `json:"who"` // "agent" or "person"
		You bool   `json:"you"`
	}
	eventMsg struct {
		T string `json:"t"`
		tools.Event
		ElapsedMS int64 `json:"elapsedMs"`
	}
	eventsMsg struct {
		T    string     `json:"t"`
		List []eventMsg `json:"list"`
	}
	approvalsMsg struct {
		T    string    `json:"t"`
		List []Pending `json:"list"`
	}
	errorMsg struct {
		T       string `json:"t"`
		Message string `json:"message"`
	}
)

// What a viewer sends.
type clientMsg struct {
	T       string  `json:"t"`
	Type    string  `json:"type"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	DX      float64 `json:"dx"`
	DY      float64 `json:"dy"`
	Button  string  `json:"button"`
	Clicks  int     `json:"clicks"`
	Mods    int     `json:"mods"`
	Key     string  `json:"key"`
	Code    string  `json:"code"`
	VK      int     `json:"vk"`
	Text    string  `json:"text"`
	ID      string  `json:"id"`
	Approve bool    `json:"approve"`
}

type outMsg struct {
	binary bool
	data   []byte
}

type viewer struct {
	h      *hub
	conn   *websocket.Conn
	out    chan outMsg
	ctx    context.Context
	cancel context.CancelFunc
}

// send queues a message. A slow viewer skips pictures; one that cannot even
// keep up with the small messages is dropped, because it would otherwise grow
// without limit.
func (v *viewer) send(m outMsg) {
	select {
	case v.out <- m:
	default:
		if !m.binary {
			v.cancel()
		}
	}
}

func (v *viewer) sendJSON(x any) {
	data, err := json.Marshal(x)
	if err != nil {
		return
	}
	v.send(outMsg{data: data})
}

type hub struct {
	s   *tools.Session
	o   Options
	ctx context.Context
	end context.CancelFunc

	kickCh chan struct{}

	mu         sync.Mutex
	viewers    map[*viewer]bool
	controller *viewer
	events     []eventMsg

	castPage *chrome.Page
	castStop func()
	last     []byte
	lastAt   time.Time
	meta     metaMsg
}

func newHub(s *tools.Session, o Options) *hub {
	ctx, end := context.WithCancel(context.Background())
	h := &hub{s: s, o: o, ctx: ctx, end: end, viewers: map[*viewer]bool{}, kickCh: make(chan struct{}, 1)}
	go h.run()
	return h
}

func (h *hub) close() {
	h.end()
	h.mu.Lock()
	for v := range h.viewers {
		v.cancel()
	}
	h.mu.Unlock()
}

func (h *hub) kick() {
	select {
	case h.kickCh <- struct{}{}:
	default:
	}
}

func (h *hub) run() {
	go h.followEvents()
	go h.followApprovals()
	// The view has nothing to show once the browser is gone.
	go func() {
		select {
		case <-h.s.Browser().Done():
			h.end()
		case <-h.ctx.Done():
		}
	}()

	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-h.ctx.Done():
			h.stopCast()
			return
		case <-t.C:
		case <-h.kickCh:
		}
		h.tick()
	}
}

func (h *hub) followEvents() {
	ch, cancel := h.s.Subscribe()
	defer cancel()
	for {
		select {
		case <-h.ctx.Done():
			return
		case e := <-ch:
			m := eventMsg{T: "event", Event: e, ElapsedMS: e.Elapsed.Milliseconds()}
			h.mu.Lock()
			h.events = append(h.events, m)
			if len(h.events) > logSize {
				h.events = h.events[len(h.events)-logSize:]
			}
			h.mu.Unlock()
			h.broadcast(m)
		}
	}
}

func (h *hub) followApprovals() {
	if h.o.Approvals == nil {
		return
	}
	ch, cancel := h.o.Approvals.Subscribe()
	defer cancel()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ch:
			h.broadcast(approvalsMsg{T: "approvals", List: h.pending()})
		}
	}
}

func (h *hub) pending() []Pending {
	if h.o.Approvals == nil {
		return []Pending{}
	}
	p := h.o.Approvals.Pending()
	if p == nil {
		p = []Pending{}
	}
	return p
}

func (h *hub) broadcast(x any) {
	data, err := json.Marshal(x)
	if err != nil {
		return
	}
	h.mu.Lock()
	vs := make([]*viewer, 0, len(h.viewers))
	for v := range h.viewers {
		vs = append(vs, v)
	}
	h.mu.Unlock()
	for _, v := range vs {
		v.send(outMsg{data: data})
	}
}

func (h *hub) broadcastControl() {
	h.mu.Lock()
	who := "agent"
	if h.controller != nil || h.s.Held() {
		who = "person"
	}
	ctl := h.controller
	vs := make([]*viewer, 0, len(h.viewers))
	for v := range h.viewers {
		vs = append(vs, v)
	}
	h.mu.Unlock()
	for _, v := range vs {
		v.sendJSON(controlMsg{T: "control", Who: who, You: v == ctl})
	}
}

func (h *hub) viewerCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.viewers)
}

// casting reports whether a screencast is running, for tests.
func (h *hub) casting() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.castStop != nil
}

// tick makes the hub's picture of the world right: a screencast running on the
// current tab while anyone is watching and none otherwise, and a meta message
// whenever the tab, its address or its size changed.
func (h *hub) tick() {
	if h.viewerCount() == 0 {
		h.stopCast()
		return
	}
	p := h.s.Current()
	if p == nil {
		h.setMeta(metaMsg{T: "meta", Open: false, Title: "No tab is open yet"})
		return
	}
	ctx, cancel := context.WithTimeout(h.ctx, 4*time.Second)
	defer cancel()

	h.mu.Lock()
	same := h.castPage == p
	h.mu.Unlock()
	if !same {
		h.stopCast()
		h.startCast(ctx, p)
	}

	info, err := p.Info(ctx)
	if err != nil {
		return
	}
	w, ht, err := p.ViewportSize(ctx)
	if err != nil {
		return
	}
	h.setMeta(metaMsg{T: "meta", URL: info.URL, Title: info.Title, Width: w, Height: ht, Open: true})

	h.mu.Lock()
	stale := time.Since(h.lastAt) > stillEvery
	h.mu.Unlock()
	if stale {
		q := h.o.Quality
		if q <= 0 {
			q = 60
		}
		if data, err := p.Screenshot(ctx, chrome.ShotOptions{JPEG: true, Quality: q}); err == nil {
			h.deliver(data)
		}
	}
}

func (h *hub) setMeta(m metaMsg) {
	h.mu.Lock()
	changed := h.meta != m
	h.meta = m
	h.mu.Unlock()
	if changed {
		h.broadcast(m)
	}
}

func (h *hub) startCast(ctx context.Context, p *chrome.Page) {
	q := h.o.Quality
	if q <= 0 {
		q = 60
	}
	frames, stop, err := p.Screencast(ctx, q, 1600, 1200)
	if err != nil {
		return
	}
	h.mu.Lock()
	h.castPage, h.castStop = p, stop
	h.mu.Unlock()
	go func() {
		for f := range frames {
			h.deliver(f.JPEG)
		}
	}()
}

func (h *hub) stopCast() {
	h.mu.Lock()
	stop := h.castStop
	h.castPage, h.castStop = nil, nil
	h.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (h *hub) deliver(jpeg []byte) {
	h.mu.Lock()
	h.last, h.lastAt = jpeg, time.Now()
	vs := make([]*viewer, 0, len(h.viewers))
	for v := range h.viewers {
		vs = append(vs, v)
	}
	h.mu.Unlock()
	for _, v := range vs {
		v.send(outMsg{binary: true, data: jpeg})
	}
}

// serve runs one viewer until it disconnects. Whatever it held is given back.
func (h *hub) serve(parent context.Context, conn *websocket.Conn) {
	ctx, cancel := context.WithCancel(parent)
	v := &viewer{h: h, conn: conn, out: make(chan outMsg, 64), ctx: ctx, cancel: cancel}

	h.mu.Lock()
	h.viewers[v] = true
	events := append([]eventMsg(nil), h.events...)
	last := h.last
	meta := h.meta
	h.mu.Unlock()

	defer func() {
		cancel()
		h.mu.Lock()
		delete(h.viewers, v)
		h.mu.Unlock()
		// A closed laptop must not leave the agent waiting for a person who is gone.
		h.release(v)
		_ = conn.CloseNow()
		h.kick()
	}()

	v.sendJSON(helloMsg{T: "hello", DevTools: h.s.Browser().DebugURL()})
	if meta.T != "" {
		v.sendJSON(meta)
	}
	v.sendJSON(eventsMsg{T: "events", List: events})
	v.sendJSON(approvalsMsg{T: "approvals", List: h.pending()})
	if last != nil {
		v.send(outMsg{binary: true, data: last})
	}
	h.broadcastControl()
	h.kick()

	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-v.out:
				typ := websocket.MessageText
				if m.binary {
					typ = websocket.MessageBinary
				}
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Write(wctx, typ, m.data)
				wcancel()
				if err != nil {
					return
				}
			}
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var m clientMsg
		if json.Unmarshal(data, &m) != nil {
			v.sendJSON(errorMsg{T: "error", Message: "That message could not be read."})
			continue
		}
		h.handle(v, m)
	}
}

func (h *hub) take(v *viewer) error {
	h.mu.Lock()
	switch {
	case h.controller == v:
		h.mu.Unlock()
		return nil
	case h.controller != nil:
		h.mu.Unlock()
		return errors.New("someone else has control of the browser")
	case h.s.Held():
		h.mu.Unlock()
		return errors.New("the browser is already held by something else")
	}
	h.s.Hold()
	h.controller = v
	h.mu.Unlock()
	h.broadcastControl()
	return nil
}

func (h *hub) release(v *viewer) {
	h.mu.Lock()
	if h.controller != v {
		h.mu.Unlock()
		return
	}
	h.controller = nil
	h.s.Release()
	h.mu.Unlock()
	h.broadcastControl()
}

func (h *hub) handle(v *viewer, m clientMsg) {
	switch m.T {
	case "take":
		if err := h.take(v); err != nil {
			v.sendJSON(errorMsg{T: "error", Message: err.Error()})
		}
	case "release":
		h.release(v)
	case "resolve":
		if h.o.Approvals == nil {
			v.sendJSON(errorMsg{T: "error", Message: "Nothing is waiting for approval."})
			return
		}
		if err := h.o.Approvals.Resolve(m.ID, m.Approve); err != nil {
			v.sendJSON(errorMsg{T: "error", Message: err.Error()})
		}
	case "pointer", "wheel", "key", "text":
		// Input goes to the page only from the viewer that holds the browser.
		// The buttons in the page are a courtesy; this check is the rule.
		h.mu.Lock()
		allowed := h.controller == v
		h.mu.Unlock()
		if !allowed {
			v.sendJSON(errorMsg{T: "error", Message: "Take control before sending input to the page."})
			return
		}
		if err := h.input(v, m); err != nil {
			v.sendJSON(errorMsg{T: "error", Message: err.Error()})
		}
	default:
		v.sendJSON(errorMsg{T: "error", Message: "Unknown message."})
	}
}

// clamp keeps a fraction of the view inside the view.
func clamp(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// input turns what a person did on the picture into events for the tab. A
// pointer position arrives as a fraction of the picture, so it lands on the same
// spot of the page whatever size the picture is shown at.
func (h *hub) input(v *viewer, m clientMsg) error {
	p := h.s.Current()
	if p == nil {
		return errNoPage
	}
	ctx, cancel := context.WithTimeout(v.ctx, 5*time.Second)
	defer cancel()

	h.mu.Lock()
	w, ht := h.meta.Width, h.meta.Height
	h.mu.Unlock()
	if w <= 0 || ht <= 0 {
		var err error
		if w, ht, err = p.ViewportSize(ctx); err != nil {
			return err
		}
	}
	x, y := clamp(m.X)*w, clamp(m.Y)*ht

	switch m.T {
	case "pointer":
		typ := map[string]string{"move": "mouseMoved", "down": "mousePressed", "up": "mouseReleased"}[m.Type]
		if typ == "" {
			return errors.New("unknown pointer event")
		}
		return p.Pointer(ctx, typ, x, y, m.Button, m.Clicks, m.Mods)
	case "wheel":
		return p.Wheel(ctx, x, y, m.DX, m.DY)
	case "key":
		typ := map[string]string{"down": "keyDown", "up": "keyUp"}[m.Type]
		if typ == "" {
			return errors.New("unknown key event")
		}
		// A key that makes no character goes as a raw key, so the page sees the key and nothing is typed.
		if typ == "keyDown" && m.Text == "" {
			typ = "rawKeyDown"
		}
		return p.RawKey(ctx, typ, m.Key, m.Code, m.VK, m.Text, m.Mods)
	case "text":
		if len([]rune(m.Text)) > 10000 {
			return errors.New("that text is too long to send at once")
		}
		return p.InsertText(ctx, m.Text)
	}
	return nil
}
