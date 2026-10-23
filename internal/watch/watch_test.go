package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/fclaude/rauli/internal/chrome"
	"github.com/fclaude/rauli/internal/tools"
)

// One session and one browser for the viewer serve the whole package: a browser
// per test would triple the run time and the load on the machine.
var (
	sessOnce   sync.Once
	sess       *tools.Session
	sessErr    error
	viewOnce   sync.Once
	viewBrowse *chrome.Browser
	viewErr    error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if sess != nil {
		sess.Close()
	}
	if viewBrowse != nil {
		viewBrowse.Close()
	}
	os.Exit(code)
}

func session(t *testing.T) *tools.Session {
	t.Helper()
	if _, err := chrome.Find(); err != nil {
		t.Skip(err)
	}
	sessOnce.Do(func() { sess, sessErr = tools.New(context.Background(), tools.Options{}) })
	if sessErr != nil {
		t.Fatal(sessErr)
	}
	t.Cleanup(sess.Release)
	return sess
}

func viewBrowser(t *testing.T) *chrome.Browser {
	t.Helper()
	if _, err := chrome.Find(); err != nil {
		t.Skip(err)
	}
	viewOnce.Do(func() {
		viewBrowse, viewErr = chrome.Launch(context.Background(), chrome.Options{Width: 1400, Height: 1500})
		if viewErr == nil {
			// A headless browser quits when its last tab closes, and tests close
			// the tabs they opened, so one blank tab stays open for the whole run.
			_, viewErr = viewBrowse.NewPage(context.Background())
		}
	})
	if viewErr != nil {
		t.Fatal(viewErr)
	}
	return viewBrowse
}

// site is what the agent browses in these tests.
func site(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		}
	}
	// A page that is one big red button, so a click anywhere reaches it.
	mux.HandleFunc("/", page(`<!doctype html><title>Red</title><body style="margin:0;background:#e02020">
<button id="b" onclick="document.title='clicked'" style="position:fixed;inset:0;width:100%;height:100%;background:#e02020;border:0;font-size:40px;color:#fff">Hit</button>`))
	mux.HandleFunc("/blue", page(`<!doctype html><title>Blue</title><body style="margin:0;background:#2040e0"><h1 style="color:#fff">Blue</h1>`))
	// A page that is one big text field.
	mux.HandleFunc("/form", page(`<!doctype html><title>Form</title><body style="margin:0"><input id="q" autofocus style="position:fixed;inset:0;width:100%;height:100%;font-size:40px">`))
	mux.HandleFunc("/hostile", page(`<!doctype html><title>&lt;b&gt;bold&lt;/b&gt;&lt;img src=x onerror=document.title='pwned'&gt;</title><h1>Hostile</h1>`))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, s *tools.Session, name string, args any) tools.Result {
	t.Helper()
	raw, _ := json.Marshal(args)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	r, err := s.Call(ctx, name, raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return r
}

// view serves the live view for the shared session and returns it with the
// httptest server that fronts it.
func view(t *testing.T, o Options) (*server, *httptest.Server) {
	t.Helper()
	sv := newServer(session(t), o)
	ts := httptest.NewServer(sv)
	t.Cleanup(func() { ts.Close(); sv.Close() })
	return sv, ts
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// open loads the view in the second browser and waits for its first picture.
func open(t *testing.T, sv *server, ts *httptest.Server) (*chrome.Page, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	p, err := viewBrowser(t).NewPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		p.Close(c)
	})
	if err := p.Navigate(ctx, ts.URL+"/?token="+sv.token); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first picture", 15*time.Second, func() bool { return evalInt(ctx, p, "+document.body.dataset.frames||0") >= 1 })
	return p, ctx
}

func evalInt(ctx context.Context, p *chrome.Page, js string) int {
	raw, err := p.Eval(ctx, js)
	if err != nil {
		return -1
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil {
		return -1
	}
	return int(f)
}

func evalString(ctx context.Context, p *chrome.Page, js string) string {
	raw, err := p.Eval(ctx, js)
	if err != nil {
		return ""
	}
	var s string
	json.Unmarshal(raw, &s)
	return s
}

func refOf(t *testing.T, ctx context.Context, p *chrome.Page, line string) string {
	t.Helper()
	snap, err := p.Snapshot(ctx, chrome.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(snap, "\n") {
		if strings.Contains(l, line) {
			if i := strings.Index(l, "[ref="); i >= 0 {
				return l[i+5 : i+strings.Index(l[i:], "]")]
			}
		}
	}
	t.Fatalf("no element %q in the view:\n%s", line, snap)
	return ""
}

func control(ctx context.Context, p *chrome.Page) string {
	return evalString(ctx, p, "document.body.dataset.control")
}

// canvasColour is the colour three quarters of the way into the live picture,
// away from any text the fixture pages draw in the middle or at the top left.
func canvasColour(ctx context.Context, p *chrome.Page) [3]int {
	raw, err := p.Eval(ctx, `(() => { const c=document.getElementById('view'); const d=c.getContext('2d').getImageData((c.width*3)>>2,(c.height*3)>>2,1,1).data; return [d[0],d[1],d[2]]; })()`)
	var rgb [3]int
	if err == nil {
		json.Unmarshal(raw, &rgb)
	}
	return rgb
}

// ---- security ----

func cookieFor(t *testing.T, ts *httptest.Server, sv *server) *http.Cookie {
	t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(ts.URL + "/?token=" + sv.token)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, ck := range resp.Cookies() {
		if ck.Name == cookieName {
			return ck
		}
	}
	t.Fatal("no cookie was set for the right token")
	return nil
}

func get(t *testing.T, url string, mutate func(*http.Request)) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if mutate != nil {
		mutate(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestNothingOpensWithoutTheToken(t *testing.T) {
	sv, ts := view(t, Options{})
	for _, path := range []string{"/", "/app.js", "/app.css", "/ws"} {
		if resp := get(t, ts.URL+path, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without a cookie: %d, want 401", path, resp.StatusCode)
		}
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(ts.URL + "/?token=wrong")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || len(resp.Cookies()) != 0 {
		t.Errorf("a wrong token: %d with %d cookies, want 403 with none", resp.StatusCode, len(resp.Cookies()))
	}
	// A cookie nobody issued is no better than none.
	resp = get(t, ts.URL+"/", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookieName, Value: sv.token}) })
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the token itself is not a session cookie: %d", resp.StatusCode)
	}
}

func TestTheCookieIsHardenedAndTheTokenLeavesTheAddress(t *testing.T) {
	sv, ts := view(t, Options{})
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(ts.URL + "/?token=" + sv.token)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("got %d to %q, want a redirect to /", resp.StatusCode, resp.Header.Get("Location"))
	}
	raw := resp.Header.Get("Set-Cookie")
	for _, want := range []string{"HttpOnly", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(raw, want) {
			t.Errorf("cookie %q lacks %s", raw, want)
		}
	}
	if strings.Contains(raw, sv.token) {
		t.Error("the cookie carries the token itself")
	}
	ck := cookieFor(t, ts, sv)
	body, _ := io.ReadAll(get(t, ts.URL+"/", func(r *http.Request) { r.AddCookie(ck) }).Body)
	if !strings.Contains(string(body), "Raulí live view") {
		t.Errorf("the page did not open with the cookie: %.80s", body)
	}
}

func TestTheHostHeaderMustBeTheMachinesOwn(t *testing.T) {
	sv, ts := view(t, Options{AllowedHosts: []string{"tunnel.example:8443"}})
	ck := cookieFor(t, ts, sv)
	try := func(host string) int {
		return get(t, ts.URL+"/", func(r *http.Request) { r.Host = host; r.AddCookie(ck) }).StatusCode
	}
	for _, bad := range []string{"evil.example", "evil.example:80", "127.0.0.1.evil.example", "localhost.evil.example:3000", "10.0.0.5:80", "tunnel.example"} {
		if code := try(bad); code != http.StatusForbidden {
			t.Errorf("Host %q: %d, want 403", bad, code)
		}
	}
	for _, good := range []string{"127.0.0.1:1234", "localhost:1234", "LOCALHOST", "[::1]:80", "tunnel.example:8443"} {
		if code := try(good); code != http.StatusOK {
			t.Errorf("Host %q: %d, want 200", good, code)
		}
	}
}

func TestOnlyReadsAreAccepted(t *testing.T) {
	sv, ts := view(t, Options{})
	ck := cookieFor(t, ts, sv)
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		req, _ := http.NewRequest(method, ts.URL+"/", strings.NewReader("x"))
		req.AddCookie(ck)
		req.Header.Set("Origin", ts.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d, want 405", method, resp.StatusCode)
		}
	}
}

func TestAnotherSitesRequestsAreRefused(t *testing.T) {
	sv, ts := view(t, Options{})
	ck := cookieFor(t, ts, sv)
	for _, origin := range []string{"http://evil.example", "https://127.0.0.1", "null", "file://"} {
		resp := get(t, ts.URL+"/app.js", func(r *http.Request) { r.Header.Set("Origin", origin); r.AddCookie(ck) })
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Origin %q: %d, want 403", origin, resp.StatusCode)
		}
	}
	if resp := get(t, ts.URL+"/app.js", func(r *http.Request) { r.Header.Set("Origin", ts.URL); r.AddCookie(ck) }); resp.StatusCode != http.StatusOK {
		t.Errorf("the page's own origin: %d, want 200", resp.StatusCode)
	}
}

func TestTheWebSocketOpensOnlyFromTheViewsOwnPage(t *testing.T) {
	sv, ts := view(t, Options{})
	ck := cookieFor(t, ts, sv)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	dial := func(cookie bool, origin string) (*http.Response, error) {
		h := http.Header{}
		if cookie {
			h.Set("Cookie", ck.Name+"="+ck.Value)
		}
		if origin != "" {
			h.Set("Origin", origin)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: h})
		if err == nil {
			c.CloseNow()
		}
		return resp, err
	}
	for name, tc := range map[string]struct {
		cookie bool
		origin string
		want   int
	}{
		"no cookie":        {false, ts.URL, http.StatusUnauthorized},
		"no origin":        {true, "", http.StatusForbidden},
		"another origin":   {true, "http://evil.example", http.StatusForbidden},
		"another port":     {true, "http://127.0.0.1:1", http.StatusForbidden},
		"a hostile scheme": {true, "javascript:", http.StatusForbidden},
	} {
		resp, err := dial(tc.cookie, tc.origin)
		if err == nil || resp == nil || resp.StatusCode != tc.want {
			code := 0
			if resp != nil {
				code = resp.StatusCode
			}
			t.Errorf("%s: status %d, error %v, want %d", name, code, err, tc.want)
		}
	}
	if _, err := dial(true, ts.URL); err != nil {
		t.Errorf("the view's own page could not open the socket: %v", err)
	}
}

func TestTheResponsesCarryAStrictPolicy(t *testing.T) {
	sv, ts := view(t, Options{})
	ck := cookieFor(t, ts, sv)
	for _, path := range []string{"/", "/app.js", "/app.css"} {
		h := get(t, ts.URL+path, func(r *http.Request) { r.AddCookie(ck) }).Header
		csp := h.Get("Content-Security-Policy")
		directives := map[string]string{}
		for _, d := range strings.Split(csp, ";") {
			d = strings.TrimSpace(d)
			if name, rest, ok := strings.Cut(d, " "); ok {
				directives[name] = rest
			}
		}
		host := strings.TrimPrefix(ts.URL, "http://")
		for name, want := range map[string]string{
			"default-src": "'none'", "script-src": "'self'", "style-src": "'self'", "font-src": "'none'",
			"frame-ancestors": "'none'", "base-uri": "'none'", "form-action": "'none'",
			"connect-src": "'self' ws://" + host, "img-src": "'self' blob: data:",
		} {
			if directives[name] != want {
				t.Errorf("%s: %s is %q, want %q", path, name, directives[name], want)
			}
		}
		for _, banned := range []string{"unsafe-inline", "unsafe-eval", "*", "https:", "http:"} {
			if strings.Contains(csp, banned) {
				t.Errorf("%s: the policy contains %q: %s", path, banned, csp)
			}
		}
		for header, want := range map[string]string{
			"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer",
			"X-Frame-Options": "DENY", "Cache-Control": "no-store",
		} {
			if h.Get(header) != want {
				t.Errorf("%s: %s is %q, want %q", path, header, h.Get(header), want)
			}
		}
	}
	// Refusals carry the headers too.
	if h := get(t, ts.URL+"/", nil).Header; h.Get("Content-Security-Policy") == "" {
		t.Error("a refusal has no policy")
	}
}

func TestTheViewListensOnLoopbackOnly(t *testing.T) {
	s := session(t)
	for _, bad := range []string{"0.0.0.0:0", ":0", "192.168.1.5:0", "example.com:80", "nonsense"} {
		if _, err := Serve(context.Background(), s, Options{Addr: bad}); err == nil {
			t.Errorf("Serve accepted %q", bad)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, good := range []string{"", "127.0.0.1:0", "localhost:0", "[::1]:0"} {
		url, err := Serve(ctx, s, Options{Addr: good})
		if err != nil {
			if good == "[::1]:0" {
				continue // a machine without IPv6 loopback
			}
			t.Errorf("Serve(%q): %v", good, err)
			continue
		}
		if !regexp.MustCompile(`^http://(127\.0\.0\.1|localhost|\[::1\]):\d+/\?token=[A-Za-z0-9_-]{30,}$`).MatchString(url) {
			t.Errorf("Serve(%q) returned %q", good, url)
		}
	}
}

func TestThePageLoadsNothingFromElsewhereAndReadsNoMarkup(t *testing.T) {
	read := func(name string) string {
		b, err := assetFiles.ReadFile("assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	html, css, js := read("index.html"), read("app.css"), read("app.js")
	for _, s := range []string{html, css} {
		if strings.Contains(s, "://") || strings.Contains(s, "@import") {
			t.Error("the page refers to an address; it must load nothing from elsewhere")
		}
	}
	if regexp.MustCompile(`(?i)<script(\s[^>]*)?>[^<]`).MatchString(html) || strings.Contains(html, " style=") || regexp.MustCompile(`(?i)\son[a-z]+=`).MatchString(html) {
		t.Error("the page has inline script, style or event handlers, which the policy would block")
	}
	for _, banned := range []string{"eval(", "new Function", "innerHTML", "outerHTML", "insertAdjacentHTML", "document.write"} {
		if strings.Contains(js, banned) {
			t.Errorf("app.js uses %s, which would read server text as markup or code", banned)
		}
	}
}

func TestEqualComparesTokens(t *testing.T) {
	if !equal("abc", "abc") || equal("abc", "abd") || equal("abc", "ab") || equal("", "a") {
		t.Fatal("equal does not compare tokens correctly")
	}
}

// ---- contrast ----

func luminance(hex string) float64 {
	n, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	lin := func(c uint64) float64 {
		f := float64(c) / 255
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(n>>16&255) + 0.7152*lin(n>>8&255) + 0.0722*lin(n&255)
}

func ratio(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func TestColoursMeetWCAGAAInBothSchemes(t *testing.T) {
	b, _ := assetFiles.ReadFile("assets/app.css")
	css := string(b)
	i := strings.Index(css, "@media (prefers-color-scheme: dark)")
	if i < 0 {
		t.Fatal("no dark scheme")
	}
	vars := func(block string) map[string]string {
		m := map[string]string{}
		for _, x := range regexp.MustCompile(`--([a-z-]+):\s*(#[0-9a-fA-F]{6});`).FindAllStringSubmatch(block, -1) {
			m[x[1]] = x[2]
		}
		return m
	}
	light := vars(css[:i])
	dark := vars(css[i:])
	for k, v := range light {
		if _, ok := dark[k]; !ok {
			dark[k] = v
		}
	}
	// Text needs 4.5 to 1, and what a person must see to use a control needs 3 to 1.
	pairs := []struct {
		fg, bg string
		min    float64
		what   string
	}{
		{"fg", "bg", 4.5, "text on the page"}, {"fg", "surface", 4.5, "text on a panel"},
		{"muted", "bg", 4.5, "quiet text on the page"}, {"muted", "surface", 4.5, "quiet text on a panel"},
		{"on-accent", "accent", 4.5, "button label"}, {"danger", "surface", 4.5, "error text on a panel"},
		{"focus", "bg", 3, "focus ring on the page"}, {"focus", "surface", 3, "focus ring on a panel"},
		{"border", "bg", 3, "control border on the page"}, {"border", "surface", 3, "panel and control border"},
		{"accent", "surface", 3, "button against its panel"},
	}
	for scheme, v := range map[string]map[string]string{"light": light, "dark": dark} {
		for _, p := range pairs {
			r := ratio(v[p.fg], v[p.bg])
			t.Logf("%-5s %-26s %s on %s = %.2f : 1 (needs %.1f)", scheme, p.what, v[p.fg], v[p.bg], r, p.min)
			if r < p.min {
				t.Errorf("%s: %s is %.2f : 1, below %.1f", scheme, p.what, r, p.min)
			}
		}
	}
}

// ---- behaviour, in a real browser ----

func TestFramesArriveAndFollowTheTab(t *testing.T) {
	s := session(t)
	fx := site(t)
	call(t, s, "navigate", map[string]any{"url": fx.URL})
	sv, ts := view(t, Options{})
	p, ctx := open(t, sv, ts)

	waitFor(t, "the red page", 10*time.Second, func() bool { c := canvasColour(ctx, p); return c[0] > 180 && c[1] < 90 && c[2] < 90 })
	if title := evalString(ctx, p, "document.getElementById('title').textContent"); title != "Red" {
		t.Errorf("the view shows the title %q", title)
	}
	if url := evalString(ctx, p, "document.getElementById('url').textContent"); url != fx.URL+"/" {
		t.Errorf("the view shows the address %q", url)
	}

	// The agent opens another tab; the view follows it.
	call(t, s, "tabs", map[string]any{"action": "new", "url": fx.URL + "/blue"})
	waitFor(t, "the blue page", 10*time.Second, func() bool { c := canvasColour(ctx, p); return c[2] > 150 && c[0] < 110 })
	if title := evalString(ctx, p, "document.getElementById('title').textContent"); title != "Blue" {
		t.Errorf("after the switch the view shows %q", title)
	}
	call(t, s, "tabs", map[string]any{"action": "close"})
}

func TestTakingControlHoldsTheAgentAndInputReachesThePage(t *testing.T) {
	s := session(t)
	fx := site(t)
	call(t, s, "navigate", map[string]any{"url": fx.URL})
	sv, ts := view(t, Options{})
	p, ctx := open(t, sv, ts)

	if got := control(ctx, p); got != "agent" {
		t.Fatalf("at the start %q has control", got)
	}
	if err := p.Click(ctx, refOf(t, ctx, p, `button "Take control"`), chrome.ClickOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "control to pass to the person", 5*time.Second, func() bool { return control(ctx, p) == "you" })
	if !s.Held() {
		t.Fatal("the session is not held")
	}
	if b := evalString(ctx, p, "document.getElementById('banner').textContent"); !strings.Contains(b, "You have control") {
		t.Errorf("the banner says %q", b)
	}

	// An agent call now waits.
	done := make(chan error, 1)
	go func() {
		raw, _ := json.Marshal(map[string]any{})
		_, err := s.Call(context.Background(), "snapshot", raw)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the agent's call ran while a person had control: %v", err)
	case <-time.After(800 * time.Millisecond):
	}

	// A click in the middle of the picture lands on the page's button, however big the picture is shown.
	rect := evalString(ctx, p, `(() => { const r=document.getElementById('view').getBoundingClientRect(); return [r.left,r.top,r.width,r.height].join(','); })()`)
	var x, y, w, h float64
	fmt.Sscanf(strings.ReplaceAll(rect, ",", " "), "%f %f %f %f", &x, &y, &w, &h)
	if w < 200 {
		t.Fatalf("the picture is only %v wide", w)
	}
	if err := p.ClickAt(ctx, x+w/2, y+h/2, chrome.ClickOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the page to see the click", 5*time.Second, func() bool {
		info, err := s.Current().Info(context.Background())
		return err == nil && info.Title == "clicked"
	})

	// Giving back lets the waiting call run.
	if err := p.Click(ctx, refOf(t, ctx, p, `button "Give back"`), chrome.ClickOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the waiting call failed after the browser came back: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting call never ran after Give back")
	}
	if s.Held() || control(ctx, p) != "agent" {
		t.Errorf("after giving back: held %v, control %q", s.Held(), control(ctx, p))
	}
}

func TestTypedTextAndKeysReachTheFocusedField(t *testing.T) {
	s := session(t)
	fx := site(t)
	call(t, s, "navigate", map[string]any{"url": fx.URL + "/form"})
	sv, ts := view(t, Options{})
	p, ctx := open(t, sv, ts)
	if err := p.Click(ctx, refOf(t, ctx, p, `button "Take control"`), chrome.ClickOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "control", 5*time.Second, func() bool { return control(ctx, p) == "you" })

	field := func() string {
		raw, _ := s.Current().Eval(context.Background(), "document.getElementById('q').value")
		var v string
		json.Unmarshal(raw, &v)
		return v
	}
	// The text box in the view sends whole strings.
	if err := p.Type(ctx, refOf(t, ctx, p, `textbox "Send text to the page"`), "hello", chrome.TypeOptions{Submit: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the sent text", 5*time.Second, func() bool { return field() == "hello" })

	// Keys on the picture go as key presses: a click focuses the picture (and the page's field under it), then a key types.
	rect := evalString(ctx, p, `(() => { const r=document.getElementById('view').getBoundingClientRect(); return [r.left,r.top,r.width,r.height].join(' '); })()`)
	var x, y, w, h float64
	fmt.Sscanf(rect, "%f %f %f %f", &x, &y, &w, &h)
	p.ClickAt(ctx, x+w/2, y+h/2, chrome.ClickOptions{})
	if err := p.Press(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the pressed key", 5*time.Second, func() bool { return field() == "hellox" })
	// Shift+Escape moves focus out of the picture, so the keyboard is never trapped.
	if err := p.Press(ctx, "Shift+Escape"); err != nil {
		t.Fatal(err)
	}
	if got := evalString(ctx, p, "document.activeElement.id"); got == "view" {
		t.Error("focus is still on the picture after Shift+Escape")
	}
	if field() != "hellox" {
		t.Errorf("Shift+Escape reached the page: the field holds %q", field())
	}
}

func TestClosingTheViewGivesTheBrowserBack(t *testing.T) {
	s := session(t)
	call(t, s, "navigate", map[string]any{"url": site(t).URL})
	sv, ts := view(t, Options{})
	p, ctx := open(t, sv, ts)
	p.Click(ctx, refOf(t, ctx, p, `button "Take control"`), chrome.ClickOptions{})
	waitFor(t, "the hold", 5*time.Second, func() bool { return s.Held() })

	c, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	p.Close(c)
	waitFor(t, "the browser to come back after the view closed", 5*time.Second, func() bool { return !s.Held() })

	// Leaving the page releases it too.
	p2, ctx2 := open(t, sv, ts)
	p2.Click(ctx2, refOf(t, ctx2, p2, `button "Take control"`), chrome.ClickOptions{})
	waitFor(t, "the second hold", 5*time.Second, func() bool { return s.Held() })
	p2.Navigate(ctx2, "about:blank")
	waitFor(t, "release after navigating away", 5*time.Second, func() bool { return !s.Held() })
}

func TestOnlyTheViewerWithControlSendsInput(t *testing.T) {
	s := session(t)
	fx := site(t)
	call(t, s, "navigate", map[string]any{"url": fx.URL})
	sv, ts := view(t, Options{})
	ck := cookieFor(t, ts, sv)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	dial := func() *websocket.Conn {
		h := http.Header{}
		h.Set("Cookie", ck.Name+"="+ck.Value)
		h.Set("Origin", ts.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: h})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}
	send := func(c *websocket.Conn, m map[string]any) {
		data, _ := json.Marshal(m)
		c.Write(context.Background(), websocket.MessageText, data)
	}
	// expect reads messages until one of type typ arrives, and returns it.
	expect := func(c *websocket.Conn, typ, contains string) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		for {
			kind, data, err := c.Read(ctx)
			if err != nil {
				return false
			}
			if kind == websocket.MessageText && strings.Contains(string(data), `"t":"`+typ+`"`) && strings.Contains(string(data), contains) {
				return true
			}
		}
	}
	a, b := dial(), dial()

	click := map[string]any{"t": "pointer", "type": "down", "x": 0.5, "y": 0.5, "button": "left", "clicks": 1}
	send(a, click)
	if !expect(a, "error", "Take control") {
		t.Fatal("input without control was not refused")
	}
	send(a, map[string]any{"t": "pointer", "type": "up", "x": 0.5, "y": 0.5, "button": "left", "clicks": 1})
	time.Sleep(400 * time.Millisecond)
	if info, _ := s.Current().Info(context.Background()); info.Title != "Red" {
		t.Fatalf("a viewer without control changed the page: %q", info.Title)
	}

	send(a, map[string]any{"t": "take"})
	if !expect(a, "control", `"you":true`) {
		t.Fatal("the first viewer could not take control")
	}
	send(b, map[string]any{"t": "take"})
	if !expect(b, "error", "someone else") {
		t.Fatal("a second viewer took control from the first")
	}
	// The second viewer's input is refused, and the first viewer's gets through.
	send(b, click)
	if !expect(b, "error", "Take control") {
		t.Fatal("input from the viewer without control was not refused")
	}
	send(a, click)
	send(a, map[string]any{"t": "pointer", "type": "up", "x": 0.5, "y": 0.5, "button": "left", "clicks": 1})
	waitFor(t, "the controlling viewer's click", 5*time.Second, func() bool {
		info, _ := s.Current().Info(context.Background())
		return info.Title == "clicked"
	})
	// The one who does not hold the browser cannot give it back for the one who does.
	send(b, map[string]any{"t": "release"})
	time.Sleep(300 * time.Millisecond)
	if !s.Held() {
		t.Fatal("a viewer without control released the browser")
	}
	send(a, map[string]any{"t": "release"})
	waitFor(t, "the release", 3*time.Second, func() bool { return !s.Held() })
}

type fakeApprovals struct {
	mu       sync.Mutex
	list     []Pending
	resolved []string
	subs     []chan struct{}
}

func (f *fakeApprovals) Pending() []Pending {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Pending(nil), f.list...)
}

func (f *fakeApprovals) Resolve(id string, approve bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.list {
		if p.ID == id {
			f.list = append(f.list[:i], f.list[i+1:]...)
			f.resolved = append(f.resolved, fmt.Sprintf("%s:%v", id, approve))
			for _, s := range f.subs {
				select {
				case s <- struct{}{}:
				default:
				}
			}
			return nil
		}
	}
	return fmt.Errorf("no request %q is waiting", id)
}

func (f *fakeApprovals) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	f.mu.Lock()
	f.subs = append(f.subs, ch)
	f.mu.Unlock()
	return ch, func() {}
}

func (f *fakeApprovals) add(p Pending) {
	f.mu.Lock()
	f.list = append(f.list, p)
	for _, s := range f.subs {
		select {
		case s <- struct{}{}:
		default:
		}
	}
	f.mu.Unlock()
}

func TestApprovalsAreShownAndResolvedFromTheView(t *testing.T) {
	s := session(t)
	call(t, s, "navigate", map[string]any{"url": site(t).URL})
	fa := &fakeApprovals{list: []Pending{
		{ID: "a1", Tool: "click", Element: `button "Pay now"`, Reason: "payments need approval", Since: time.Now()},
		{ID: "a2", Tool: "upload", Detail: "/home/me/tax.pdf", Reason: "files need approval", Since: time.Now()},
	}}
	sv, ts := view(t, Options{Approvals: fa})
	p, ctx := open(t, sv, ts)

	text := func() string { return evalString(ctx, p, "document.getElementById('approvals').textContent") }
	waitFor(t, "the pending requests", 5*time.Second, func() bool { return strings.Contains(text(), "Pay now") && strings.Contains(text(), "tax.pdf") })
	if evalString(ctx, p, "String(document.getElementById('approvals-empty').hidden)") != "true" {
		t.Error("the empty message shows while requests are waiting")
	}
	if err := p.Click(ctx, refOf(t, ctx, p, `button "Approve click button \"Pay now\""`), chrome.ClickOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "approval of the first", 5*time.Second, func() bool { return !strings.Contains(text(), "Pay now") })
	if err := p.Click(ctx, refOf(t, ctx, p, `button "Deny upload`), chrome.ClickOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "refusal of the second", 5*time.Second, func() bool { return text() == "" })
	fa.mu.Lock()
	got := strings.Join(fa.resolved, " ")
	fa.mu.Unlock()
	if got != "a1:true a2:false" {
		t.Errorf("the queue was told %q", got)
	}
	if evalString(ctx, p, "String(document.getElementById('approvals-empty').hidden)") != "false" {
		t.Error("the empty message is hidden when nothing is waiting")
	}
	// A request that arrives later appears without reloading.
	fa.add(Pending{ID: "a3", Tool: "eval", Reason: "scripts need approval", Since: time.Now()})
	waitFor(t, "the late request", 5*time.Second, func() bool { return strings.Contains(text(), "eval") })
}

func TestTheActionLogShowsWhatTheAgentDid(t *testing.T) {
	s := session(t)
	fx := site(t)
	sv, ts := view(t, Options{})
	call(t, s, "navigate", map[string]any{"url": fx.URL})
	p, ctx := open(t, sv, ts)
	logText := func() string { return evalString(ctx, p, "document.getElementById('log').textContent") }
	waitFor(t, "the earlier navigation", 5*time.Second, func() bool { return strings.Contains(logText(), "navigate") })

	call(t, s, "screenshot", map[string]any{})
	if _, err := s.Call(context.Background(), "click", json.RawMessage(`{"ref":"e999"}`)); err == nil {
		t.Fatal("a click on a missing element should fail")
	}
	waitFor(t, "the new entries", 5*time.Second, func() bool {
		l := logText()
		return strings.Contains(l, "screenshot") && strings.Contains(l, "Error: ")
	})
	if strings.Contains(logText(), "The agent has not done anything yet") {
		t.Error("the empty message is still in the log")
	}
}

func TestTheDeveloperToolsAddressIsShown(t *testing.T) {
	s := session(t)
	sv, ts := view(t, Options{})
	p, ctx := open(t, sv, ts)
	waitFor(t, "the address", 5*time.Second, func() bool {
		return evalString(ctx, p, "document.getElementById('devtools').textContent") == s.Browser().DebugURL()
	})
}

func TestTheScreencastRunsOnlyWhileSomeoneWatches(t *testing.T) {
	s := session(t)
	call(t, s, "navigate", map[string]any{"url": site(t).URL})
	sv, ts := view(t, Options{})
	if sv.hub.casting() {
		t.Fatal("a screencast runs with nobody watching")
	}
	p, _ := open(t, sv, ts)
	waitFor(t, "the screencast to start", 5*time.Second, func() bool { return sv.hub.casting() })
	c, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	p.Close(c)
	waitFor(t, "the screencast to stop", 5*time.Second, func() bool { return !sv.hub.casting() && sv.hub.viewerCount() == 0 })
}

func TestServerTextIsShownAsTextAndNotRead(t *testing.T) {
	s := session(t)
	fx := site(t)
	call(t, s, "navigate", map[string]any{"url": fx.URL + "/hostile"})
	sv, ts := view(t, Options{})
	p, ctx := open(t, sv, ts)
	want := "<b>bold</b><img src=x onerror=document.title='pwned'>"
	waitFor(t, "the hostile title", 5*time.Second, func() bool { return evalString(ctx, p, "document.getElementById('title').textContent") == want })
	if n := evalInt(ctx, p, "document.querySelectorAll('#title *, #url *').length"); n != 0 {
		t.Errorf("the title was read as markup: %d elements inside it", n)
	}
	if got := evalString(ctx, p, "document.title"); got != "Raulí live view" {
		t.Errorf("the page's own title became %q", got)
	}
}

func TestAViewerThatLosesItsConnectionComesBack(t *testing.T) {
	s := session(t)
	call(t, s, "navigate", map[string]any{"url": site(t).URL})
	sv, ts := view(t, Options{})
	p, ctx := open(t, sv, ts)
	before := evalInt(ctx, p, "+document.body.dataset.frames")

	sv.hub.mu.Lock()
	for v := range sv.hub.viewers {
		v.conn.CloseNow()
	}
	sv.hub.mu.Unlock()
	waitFor(t, "the viewer to notice", 5*time.Second, func() bool { return control(ctx, p) == "none" || sv.hub.viewerCount() == 0 })
	waitFor(t, "the viewer to reconnect", 10*time.Second, func() bool { return sv.hub.viewerCount() == 1 && control(ctx, p) == "agent" })
	waitFor(t, "pictures after reconnecting", 10*time.Second, func() bool { return evalInt(ctx, p, "+document.body.dataset.frames") > before })
}
