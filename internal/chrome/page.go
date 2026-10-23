package chrome

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/rauli/internal/cdp"
)

// Page is one tab. Its methods act on whatever the tab currently shows.
type Page struct {
	b        *Browser
	conn     *cdp.Conn
	targetID string
	session  string

	mu      sync.Mutex
	refs    map[string]refInfo // element references from the last snapshot
	console []string
	blocked []string
	dialogs []Dialog
	closed  bool
}

// Dialog is a JavaScript alert, confirm, prompt or beforeunload prompt that a
// page raised. The browser answers it so the page cannot hang the session,
// and the dialog is kept here so the caller can see that it happened.
type Dialog struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	// Accepted is what the browser answered.
	Accepted bool `json:"accepted"`
}

// NewPage opens a blank tab.
func (b *Browser) NewPage(ctx context.Context) (*Page, error) {
	var res struct {
		TargetID string `json:"targetId"`
	}
	if err := b.conn.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &res); err != nil {
		return nil, err
	}
	return b.attach(ctx, res.TargetID)
}

func (b *Browser) attach(ctx context.Context, targetID string) (*Page, error) {
	var res struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.conn.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, &res); err != nil {
		return nil, err
	}
	p := &Page{b: b, conn: b.conn, targetID: targetID, session: res.SessionID, refs: map[string]refInfo{}}
	if err := p.init(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.tabs[targetID] = p
	b.mu.Unlock()
	return p, nil
}

func (p *Page) call(ctx context.Context, method string, params, out any) error {
	return p.conn.Call(ctx, p.session, method, params, out)
}

func (p *Page) init(ctx context.Context) error {
	for _, m := range []string{"Page.enable", "DOM.enable", "Runtime.enable", "Accessibility.enable"} {
		if err := p.call(ctx, m, nil, nil); err != nil {
			return err
		}
	}
	if err := p.call(ctx, "Page.setLifecycleEventsEnabled", map[string]any{"enabled": true}, nil); err != nil {
		return err
	}
	w, h := p.b.opts.size()
	if err := p.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": w, "height": h, "deviceScaleFactor": 1, "mobile": false,
	}, nil); err != nil {
		return err
	}
	// A tab in a window nobody has focused ignores key presses on some systems.
	// Pretending the page has focus is what makes input reliable there.
	if err := p.call(ctx, "Emulation.setFocusEmulationEnabled", map[string]any{"enabled": true}, nil); err != nil {
		return err
	}
	if ua := p.b.userAgent; ua != "" {
		if err := p.call(ctx, "Network.setUserAgentOverride", map[string]any{"userAgent": ua}, nil); err != nil {
			return err
		}
	}

	if p.b.opts.Filter != nil {
		paused, _ := p.conn.Subscribe(p.session, "Fetch.requestPaused")
		if err := p.call(ctx, "Fetch.enable", map[string]any{"patterns": []map[string]any{{"urlPattern": "*"}}}, nil); err != nil {
			return err
		}
		go p.filter(paused)
	}

	dialogs, _ := p.conn.Subscribe(p.session, "Page.javascriptDialogOpening")
	console, _ := p.conn.Subscribe(p.session, "Runtime.consoleAPICalled")
	go p.watch(dialogs, console)
	return nil
}

// watch answers dialogs and keeps a short console log. It ends with the
// connection.
func (p *Page) watch(dialogs, console <-chan json.RawMessage) {
	for {
		select {
		case <-p.conn.Done():
			return
		case raw := <-dialogs:
			var d struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}
			if json.Unmarshal(raw, &d) != nil {
				continue
			}
			// An alert has nothing to decide, and leaving a page is
			// what navigating away asks for. Anything that wants an answer
			// from the person gets "no" until a rule says otherwise.
			accept := d.Type == "alert" || d.Type == "beforeunload"
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = p.call(ctx, "Page.handleJavaScriptDialog", map[string]any{"accept": accept}, nil)
			cancel()
			p.mu.Lock()
			p.dialogs = append(p.dialogs, Dialog{Type: d.Type, Message: d.Message, Accepted: accept})
			p.mu.Unlock()
		case raw := <-console:
			var c struct {
				Type string `json:"type"`
				Args []struct {
					Value       any    `json:"value"`
					Description string `json:"description"`
				} `json:"args"`
			}
			if json.Unmarshal(raw, &c) != nil {
				continue
			}
			parts := make([]string, 0, len(c.Args))
			for _, a := range c.Args {
				switch {
				case a.Value != nil:
					parts = append(parts, fmt.Sprint(a.Value))
				default:
					parts = append(parts, a.Description)
				}
			}
			p.mu.Lock()
			p.console = append(p.console, c.Type+": "+strings.Join(parts, " "))
			if len(p.console) > 200 {
				p.console = p.console[len(p.console)-200:]
			}
			p.mu.Unlock()
		}
	}
}

// ID is the tab's target id, which names it in DevTools.
func (p *Page) ID() string { return p.targetID }

// Dialogs returns the JavaScript dialogs that appeared since the last call.
func (p *Page) Dialogs() []Dialog {
	p.mu.Lock()
	defer p.mu.Unlock()
	d := p.dialogs
	p.dialogs = nil
	return d
}

// Blocked returns the requests the browser's filter refused since the last
// call, each with the reason, so an action's result can say why a page did not load.
func (p *Page) Blocked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.blocked
	p.blocked = nil
	return b
}

// Console returns what the page wrote to its console since the last call.
func (p *Page) Console() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.console
	p.console = nil
	return c
}

// Info is where the tab is and what it is called.
type Info struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// Info reads the tab's current address and title.
func (p *Page) Info(ctx context.Context) (Info, error) {
	var res struct {
		TargetInfo Info `json:"targetInfo"`
	}
	if err := p.b.conn.Call(ctx, "", "Target.getTargetInfo", map[string]any{"targetId": p.targetID}, &res); err != nil {
		return Info{}, err
	}
	return res.TargetInfo, nil
}

func (p *Page) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// Close closes the tab.
func (p *Page) Close(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	return p.b.conn.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": p.targetID}, nil)
}

type lifecycle struct {
	LoaderID string `json:"loaderId"`
	Name     string `json:"name"`
}

// Navigate loads a URL and returns once the page has loaded. A page that
// never goes quiet is not an error: the load event is what counts, and the
// network is given a moment longer to settle.
func (p *Page) Navigate(ctx context.Context, url string) error {
	events, cancel := p.conn.Subscribe(p.session, "Page.lifecycleEvent")
	defer cancel()

	var res struct {
		LoaderID  string `json:"loaderId"`
		ErrorText string `json:"errorText"`
	}
	if err := p.call(ctx, "Page.navigate", map[string]any{"url": url}, &res); err != nil {
		return err
	}
	// A link that turns out to be a download is reported as an aborted
	// navigation. The tab stays where it was and there is nothing to wait for.
	if res.ErrorText != "" && res.ErrorText != "net::ERR_ABORTED" {
		return fmt.Errorf("could not load %s: %s", url, res.ErrorText)
	}
	p.forgetRefs()
	if res.LoaderID == "" || res.ErrorText != "" {
		// A change of fragment, or a download: there is no new document to wait for.
		return nil
	}
	return waitLoaded(ctx, events, res.LoaderID)
}

// waitLoaded reads lifecycle events until the document with the given loader
// has fired load, then gives the network up to a second and a half to go idle.
func waitLoaded(ctx context.Context, events <-chan json.RawMessage, loader string) error {
	loaded := false
	var idle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the page to load: %w", ctx.Err())
		case <-idle:
			return nil
		case raw := <-events:
			var e lifecycle
			if json.Unmarshal(raw, &e) != nil || e.LoaderID != loader {
				continue
			}
			switch e.Name {
			case "load":
				loaded = true
				idle = time.After(1500 * time.Millisecond)
			case "networkIdle":
				if loaded {
					return nil
				}
			}
		}
	}
}

// settle waits out whatever an action started. Most clicks load nothing, so it
// first waits briefly to see whether a navigation begins; if one does, it waits
// for that page to finish loading.
//
// A page restored from the browser's back/forward cache fires no load event at
// all, so the end of loading is taken from three signs: the load event, the
// frame reporting that it stopped loading, and the document's own readyState.
func (p *Page) settle(ctx context.Context, act func() error) error {
	started, cancelStarted := p.conn.Subscribe(p.session, "Page.frameStartedLoading")
	defer cancelStarted()
	navigated, cancelNavigated := p.conn.Subscribe(p.session, "Page.frameNavigated")
	defer cancelNavigated()
	within, cancelWithin := p.conn.Subscribe(p.session, "Page.navigatedWithinDocument")
	defer cancelWithin()
	events, cancelEvents := p.conn.Subscribe(p.session, "Page.lifecycleEvent")
	defer cancelEvents()
	stopped, cancelStopped := p.conn.Subscribe(p.session, "Page.frameStoppedLoading")
	defer cancelStopped()

	if err := act(); err != nil {
		return err
	}
	select {
	case <-started:
	case <-navigated:
	case <-within:
		// A change of fragment or a pushState has nothing to load.
		p.forgetRefs()
		return nil
	case <-time.After(400 * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	p.forgetRefs()

	loaded := false
	var idle <-chan time.Time
	finish := func() {
		if !loaded {
			loaded = true
			idle = time.After(1500 * time.Millisecond)
		}
	}
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the page to load: %w", ctx.Err())
		case <-idle:
			return nil
		case <-stopped:
			finish()
		case <-poll.C:
			if !loaded {
				if raw, err := p.Eval(ctx, "document.readyState"); err == nil && string(raw) == `"complete"` {
					finish()
				}
			}
		case raw := <-events:
			var e lifecycle
			if json.Unmarshal(raw, &e) != nil {
				continue
			}
			switch e.Name {
			case "load":
				finish()
			case "networkIdle":
				if loaded {
					return nil
				}
			}
		}
	}
}

// History moves through the tab's session history: -1 is back, 1 is forward.
func (p *Page) History(ctx context.Context, delta int) error {
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := p.call(ctx, "Page.getNavigationHistory", nil, &h); err != nil {
		return err
	}
	i := h.CurrentIndex + delta
	if i < 0 || i >= len(h.Entries) {
		return errors.New("there is no page in that direction")
	}
	return p.settle(ctx, func() error {
		return p.call(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": h.Entries[i].ID}, nil)
	})
}

// Reload loads the current page again.
func (p *Page) Reload(ctx context.Context) error {
	return p.settle(ctx, func() error { return p.call(ctx, "Page.reload", nil, nil) })
}

// Eval runs a JavaScript expression in the page and returns its value as JSON.
func (p *Page) Eval(ctx context.Context, expr string) (json.RawMessage, error) {
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	err := p.call(ctx, "Runtime.evaluate", map[string]any{
		"expression": expr, "returnByValue": true, "awaitPromise": true,
	}, &res)
	if err != nil {
		return nil, err
	}
	if res.Exception != nil {
		msg := res.Exception.Exception.Description
		if msg == "" {
			msg = res.Exception.Text
		}
		return nil, errors.New(msg)
	}
	return res.Result.Value, nil
}

// Text is the text of the page as a reader would see it.
func (p *Page) Text(ctx context.Context) (string, error) {
	raw, err := p.Eval(ctx, "document.body ? document.body.innerText : ''")
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}

// WaitForText waits until the page's text contains s.
func (p *Page) WaitForText(ctx context.Context, s string) error {
	for {
		if t, err := p.Text(ctx); err == nil && strings.Contains(t, s) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the text %q did not appear: %w", s, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// ShotOptions say what a screenshot covers.
type ShotOptions struct {
	// FullPage captures the whole scrollable page, not only what is visible.
	FullPage bool
	// Ref captures one element from the last snapshot.
	Ref string
	// JPEG makes a JPEG with the given quality (1 to 100) instead of a PNG.
	JPEG    bool
	Quality int
}

// Screenshot returns an image of the page. It works the same with no window.
func (p *Page) Screenshot(ctx context.Context, o ShotOptions) ([]byte, error) {
	params := map[string]any{"format": "png"}
	if o.JPEG {
		params["format"] = "jpeg"
		q := o.Quality
		if q <= 0 || q > 100 {
			q = 80
		}
		params["quality"] = q
	}

	switch {
	case o.Ref != "":
		x, y, w, h, err := p.bounds(ctx, o.Ref)
		if err != nil {
			return nil, err
		}
		// Clip rectangles are in page coordinates, element boxes in the viewport's.
		var m metrics
		if err := p.call(ctx, "Page.getLayoutMetrics", nil, &m); err != nil {
			return nil, err
		}
		params["clip"] = map[string]any{
			"x": x + m.Visual.PageX, "y": y + m.Visual.PageY, "width": w, "height": h, "scale": 1,
		}
		params["captureBeyondViewport"] = true
	case o.FullPage:
		var m metrics
		if err := p.call(ctx, "Page.getLayoutMetrics", nil, &m); err != nil {
			return nil, err
		}
		params["clip"] = map[string]any{"x": 0, "y": 0, "width": m.Content.Width, "height": m.Content.Height, "scale": 1}
		params["captureBeyondViewport"] = true
	}

	var res struct {
		Data string `json:"data"`
	}
	if err := p.call(ctx, "Page.captureScreenshot", params, &res); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(res.Data)
}

type metrics struct {
	Content struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"cssContentSize"`
	Visual struct {
		PageX        float64 `json:"pageX"`
		PageY        float64 `json:"pageY"`
		ClientWidth  float64 `json:"clientWidth"`
		ClientHeight float64 `json:"clientHeight"`
	} `json:"cssVisualViewport"`
}

// filter answers every paused request with the browser's Filter. Requests are
// decided one at a time and in order, which keeps the rules simple; a page
// loads a few dozen requests, and a rule is a string match.
func (p *Page) filter(paused <-chan json.RawMessage) {
	for {
		select {
		case <-p.conn.Done():
			return
		case raw := <-paused:
			var e struct {
				RequestID    string `json:"requestId"`
				ResourceType string `json:"resourceType"`
				Request      struct {
					URL    string `json:"url"`
					Method string `json:"method"`
				} `json:"request"`
			}
			if json.Unmarshal(raw, &e) != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := p.b.opts.Filter(ctx, Request{
				URL: e.Request.URL, Method: e.Request.Method, Type: e.ResourceType,
				Navigation: e.ResourceType == "Document",
			})
			if err != nil {
				_ = p.call(ctx, "Fetch.failRequest", map[string]any{"requestId": e.RequestID, "errorReason": "BlockedByClient"}, nil)
				p.mu.Lock()
				if e.ResourceType == "Document" {
					p.blocked = append(p.blocked, e.Request.URL+": "+err.Error())
				} else {
					// A blocked image or script is routine; it is kept where a person
					// looking for it would, and the audit log has it too.
					p.console = append(p.console, "blocked: "+e.Request.URL+": "+err.Error())
				}
				p.mu.Unlock()
			} else {
				_ = p.call(ctx, "Fetch.continueRequest", map[string]any{"requestId": e.RequestID}, nil)
			}
			cancel()
		}
	}
}
