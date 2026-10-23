package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/rauli/internal/chrome"
)

const secret = "correct-horse-9"

func site(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	html := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		}
	}
	mux.HandleFunc("/", html(`<!doctype html><title>Shop</title><h1>Shop</h1>
<button onclick="document.title='paid'">Pay now</button>
<button onclick="document.title='saved'">Save</button>
<a href="/two">second page</a>
<a href="/popup" target="_blank">popup</a>`))
	mux.HandleFunc("/two", html(`<!doctype html><title>Two</title><h1>Two</h1>`))
	mux.HandleFunc("/popup", html(`<!doctype html><title>Popup</title><h1>Popup</h1>`))
	mux.HandleFunc("/login", html(`<!doctype html><title>Login</title><h1>Login</h1>
<form action="/welcome" method="post"><label for="u">User</label><input id="u" name="u">
<label for="p">Password</label><input id="p" name="p" type="password"><button>Sign in</button></form>`))
	mux.HandleFunc("/welcome", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>Welcome</title><h1>Welcome %s</h1><p>password %q</p>`, r.Form.Get("u"), r.Form.Get("p"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func session(t *testing.T, o Options) *Session {
	t.Helper()
	if _, err := chrome.Find(); err != nil {
		t.Skip(err)
	}
	s, err := New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func call(t *testing.T, s *Session, name string, args any) (Result, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	return s.Call(ctx, name, raw)
}

func must(t *testing.T, s *Session, name string, args any) Result {
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

func TestEveryToolIsDescribed(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range builtin() {
		if names[tool.Name] {
			t.Errorf("two tools are called %s", tool.Name)
		}
		names[tool.Name] = true
		if len(tool.Description) < 30 {
			t.Errorf("%s has no real description", tool.Name)
		}
		if tool.Schema["type"] != "object" || tool.Schema["additionalProperties"] != false {
			t.Errorf("%s: the schema must be a closed object: %v", tool.Name, tool.Schema)
		}
		if tool.run == nil {
			t.Errorf("%s has no implementation", tool.Name)
		}
		props, _ := tool.Schema["properties"].(map[string]any)
		for _, req := range requiredOf(tool.Schema) {
			if _, ok := props[req]; !ok {
				t.Errorf("%s requires %q, which is not a property", tool.Name, req)
			}
		}
	}
	for _, want := range []string{"navigate", "snapshot", "click", "type", "press", "scroll", "select", "screenshot", "tabs", "fill_credential", "credentials", "eval", "upload"} {
		if !names[want] {
			t.Errorf("tool %s is missing", want)
		}
	}
}

func requiredOf(schema map[string]any) []string {
	r, _ := schema["required"].([]string)
	return r
}

func TestOnlyWebAddressesAreOpened(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":             "https://example.com",
		"http://localhost:8080/a": "http://localhost:8080/a",
		" https://example.com/x ": "https://example.com/x",
		"about:blank":             "about:blank",
	} {
		got, err := normalise(in)
		if err != nil || got != want {
			t.Errorf("normalise(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "file:///etc/passwd", "javascript:alert(1)", "chrome://settings", "ftp://example.com"} {
		if _, err := normalise(bad); err == nil {
			t.Errorf("normalise(%q) should be refused", bad)
		}
	}
}

func TestNavigateReadsThePageAndMisspeltArgumentsAreRefused(t *testing.T) {
	srv := site(t)
	s := session(t, Options{})
	r := must(t, s, "navigate", map[string]any{"url": srv.URL})
	for _, want := range []string{"Page: Shop", `heading "Shop"`, `button "Pay now"`, "[ref=e"} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("navigate result lacks %q:\n%s", want, r.Text)
		}
	}
	if _, err := call(t, s, "click", map[string]any{"ref": "e1", "reff": true}); err == nil || !strings.Contains(err.Error(), "bad arguments") {
		t.Fatalf("a misspelt argument should be refused, got %v", err)
	}
	if _, err := call(t, s, "nonsense", nil); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("an unknown tool should say so, got %v", err)
	}
}

type recordingGuard struct {
	mu       sync.Mutex
	actions  []Action
	refuse   func(Action) error
	refusURL func(string) error
}

func (g *recordingGuard) CheckURL(_ context.Context, r chrome.Request) error {
	if g.refusURL != nil {
		return g.refusURL(r.URL)
	}
	return nil
}

func (g *recordingGuard) CheckAction(_ context.Context, a Action) error {
	g.mu.Lock()
	g.actions = append(g.actions, a)
	g.mu.Unlock()
	if g.refuse != nil {
		return g.refuse(a)
	}
	return nil
}

func TestAGuardRefusesAnActionBeforeItRuns(t *testing.T) {
	srv := site(t)
	g := &recordingGuard{refuse: func(a Action) error {
		if strings.Contains(a.Element, "Pay now") {
			return errors.New("payments need approval")
		}
		return nil
	}}
	s := session(t, Options{Guard: g})
	snap := must(t, s, "navigate", map[string]any{"url": srv.URL}).Text

	_, err := call(t, s, "click", map[string]any{"ref": refOf(t, snap, `button "Pay now"`)})
	if err == nil || !strings.Contains(err.Error(), "not allowed: payments need approval") {
		t.Fatalf("the click should be refused with the rule's reason, got %v", err)
	}
	// The button's handler sets the title; it must not have run.
	if text := must(t, s, "snapshot", nil).Text; !strings.Contains(text, "Page: Shop") {
		t.Fatalf("the refused click still happened:\n%s", text)
	}
	// A different button is allowed, and the guard saw what it was and where.
	must(t, s, "click", map[string]any{"ref": refOf(t, snap, `button "Save"`)})
	g.mu.Lock()
	defer g.mu.Unlock()
	last := g.actions[len(g.actions)-1]
	if last.Kind != "click" || last.Element != `button "Save"` || !strings.HasPrefix(last.URL, srv.URL) {
		t.Fatalf("the guard was told %+v", last)
	}
}

func TestAGuardFiltersRequestsToo(t *testing.T) {
	srv := site(t)
	g := &recordingGuard{refusURL: func(u string) error {
		if strings.HasSuffix(u, "/two") {
			return errors.New("that page is off limits")
		}
		return nil
	}}
	s := session(t, Options{Guard: g})
	snap := must(t, s, "navigate", map[string]any{"url": srv.URL}).Text
	// The click itself is allowed; the page it asks for is not. The browser shows
	// its error page, as it would to a person, and the agent is told why.
	r := must(t, s, "click", map[string]any{"ref": refOf(t, snap, "second page")})
	if !strings.Contains(r.Text, "Blocked by the rules: "+srv.URL+"/two: that page is off limits") {
		t.Fatalf("the agent was not told the rules blocked the page:\n%s", r.Text)
	}
	if strings.Contains(r.Text, "Page: Two") {
		t.Fatalf("the forbidden page loaded:\n%s", r.Text)
	}
	if !strings.Contains(r.Text, "error page") || !strings.Contains(r.Text, "back") {
		t.Fatalf("the agent should be told the tab shows an error page and how to leave it:\n%s", r.Text)
	}
	if back := must(t, s, "back", nil); !strings.Contains(back.Text, "Page: Shop") {
		t.Fatalf("back should return to the page the click started on:\n%s", back.Text)
	}

	// Asking for a forbidden address outright is refused before the tab moves,
	// so the agent keeps the page it was on and its references.
	snap = must(t, s, "snapshot", nil).Text
	_, err := call(t, s, "navigate", map[string]any{"url": srv.URL + "/two"})
	if err == nil || !strings.Contains(err.Error(), "that page is off limits") {
		t.Fatalf("navigating to a forbidden page should fail with the rule's reason, got %v", err)
	}
	time.Sleep(600 * time.Millisecond) // long enough for an error page to commit, had one been started
	after := must(t, s, "snapshot", nil).Text
	if !strings.Contains(after, "Page: Shop") || refOf(t, after, "second page") != refOf(t, snap, "second page") {
		t.Fatalf("a refused navigation must leave the tab and its references alone:\n%s", after)
	}
}

type testVault struct {
	handles []Handle
	values  map[string]string
}

func (v testVault) Handles(context.Context) ([]Handle, error) { return v.handles, nil }

func (v testVault) Secret(_ context.Context, handle, field, pageURL string) (string, error) {
	for _, h := range v.handles {
		if h.Name != handle {
			continue
		}
		if len(h.Origins) > 0 {
			u, _ := url.Parse(pageURL)
			ok := false
			for _, o := range h.Origins {
				if u != nil && u.Scheme+"://"+u.Host == o {
					ok = true
				}
			}
			if !ok {
				return "", fmt.Errorf("%s may not be used on %s", handle, pageURL)
			}
		}
		if val, ok := v.values[handle+"/"+field]; ok {
			return val, nil
		}
		return "", fmt.Errorf("%s has no field %q", handle, field)
	}
	return "", fmt.Errorf("there is no login called %q", handle)
}

func TestACredentialReachesThePageAndNothingElse(t *testing.T) {
	srv := site(t)
	vault := testVault{
		handles: []Handle{{Name: "shop", Fields: []string{"user", "password"}, Origins: []string{srv.URL}}},
		values:  map[string]string{"shop/user": "alice", "shop/password": secret},
	}
	s := session(t, Options{Vault: vault})
	events, cancel := s.Subscribe()
	defer cancel()

	var everything strings.Builder
	keep := func(r Result) { everything.WriteString(r.Text + "\n") }

	snap := must(t, s, "navigate", map[string]any{"url": srv.URL + "/login"})
	keep(snap)
	keep(must(t, s, "credentials", nil))
	r := must(t, s, "fill_credential", map[string]any{"ref": refOf(t, snap.Text, `textbox "User"`), "handle": "shop", "field": "user"})
	keep(r)
	pass := refOf(t, r.Text, `textbox "Password"`)
	r = must(t, s, "fill_credential", map[string]any{"ref": pass, "handle": "shop", "field": "password"})
	keep(r)
	// The page now holds the secret. Reading it back must not show it.
	keep(must(t, s, "snapshot", nil))
	keep(must(t, s, "text", nil))
	keep(must(t, s, "console", nil))
	shot := must(t, s, "screenshot", nil)
	keep(Result{Text: shot.Text})

	if strings.Contains(everything.String(), secret) {
		t.Fatalf("the secret appears in what the agent was told:\n%s", everything.String())
	}

	// Submitting proves the secret really reached the page.
	r = must(t, s, "click", map[string]any{"ref": refOf(t, must(t, s, "snapshot", nil).Text, `button "Sign in"`)})
	if !strings.Contains(r.Text, "Welcome alice") {
		t.Fatalf("the form did not receive the user:\n%s", r.Text)
	}
	// The welcome page echoes the posted password, which proves delivery. An agent
	// that reads it back sees it, because the site chose to show it; that is the
	// site's doing, and the tools could only hide it by reading less of the page.
	if !strings.Contains(r.Text, secret) {
		t.Fatalf("the password was not delivered to the form:\n%s", r.Text)
	}

	close(chanDrain(events))
}

// chanDrain exists so the subscription is read before the test ends; the
// events themselves are checked in TestEventsNeverHoldSecretsOrTypedText.
func chanDrain(ch <-chan Event) chan struct{} {
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
			case <-done:
				return
			}
		}
	}()
	return done
}

func TestEventsNeverHoldSecretsOrTypedText(t *testing.T) {
	srv := site(t)
	vault := testVault{
		handles: []Handle{{Name: "shop", Fields: []string{"user", "password"}}},
		values:  map[string]string{"shop/user": "alice", "shop/password": secret},
	}
	s := session(t, Options{Vault: vault})
	events, cancel := s.Subscribe()
	defer cancel()

	snap := must(t, s, "navigate", map[string]any{"url": srv.URL + "/login"}).Text
	must(t, s, "type", map[string]any{"ref": refOf(t, snap, `textbox "User"`), "text": "typed-by-the-agent"})
	must(t, s, "fill_credential", map[string]any{"ref": refOf(t, snap, `textbox "Password"`), "handle": "shop", "field": "password"})

	var all bytes.Buffer
	var typed string
	deadline := time.After(3 * time.Second)
	got := 0
	for got < 3 {
		select {
		case e := <-events:
			b, _ := json.Marshal(e)
			all.Write(b)
			if e.Tool == "type" {
				var args map[string]any
				json.Unmarshal(e.Args, &args)
				typed, _ = args["text"].(string)
			}
			got++
		case <-deadline:
			t.Fatalf("only %d events arrived", got)
		}
	}
	for _, banned := range []string{secret, "typed-by-the-agent"} {
		if strings.Contains(all.String(), banned) {
			t.Fatalf("an event holds %q:\n%s", banned, all.String())
		}
	}
	if typed != "<18 characters>" {
		t.Errorf("the typed text should be logged by length only, got %q", typed)
	}
}

func TestACredentialIsRefusedOffItsOrigin(t *testing.T) {
	srv := site(t)
	vault := testVault{
		handles: []Handle{{Name: "bank", Fields: []string{"password"}, Origins: []string{"https://bank.example"}}},
		values:  map[string]string{"bank/password": secret},
	}
	s := session(t, Options{Vault: vault})
	snap := must(t, s, "navigate", map[string]any{"url": srv.URL + "/login"}).Text
	_, err := call(t, s, "fill_credential", map[string]any{"ref": refOf(t, snap, `textbox "Password"`), "handle": "bank", "field": "password"})
	if err == nil || !strings.Contains(err.Error(), "may not be used on") {
		t.Fatalf("a credential must not be typed into another site, got %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("the refusal leaks the secret")
	}
	if _, err := call(t, s, "fill_credential", map[string]any{"ref": "e1", "handle": "nobody", "field": "x"}); err == nil {
		t.Fatal("an unknown login should fail")
	}
}

func TestWithoutAKeychainTheCredentialToolsSaySo(t *testing.T) {
	s := session(t, Options{})
	if r := must(t, s, "credentials", nil); !strings.Contains(r.Text, "No keychain") {
		t.Fatalf("got %q", r.Text)
	}
	if _, err := call(t, s, "fill_credential", map[string]any{"ref": "e1", "handle": "x", "field": "y"}); err == nil || !strings.Contains(err.Error(), "no keychain") {
		t.Fatalf("got %v", err)
	}
}

func TestTabsListOpenSwitchAndClose(t *testing.T) {
	srv := site(t)
	s := session(t, Options{})
	snap := must(t, s, "navigate", map[string]any{"url": srv.URL}).Text

	r := must(t, s, "click", map[string]any{"ref": refOf(t, snap, "popup")})
	if !strings.Contains(r.Text, "A new tab opened") {
		t.Fatalf("the agent was not told about the new tab:\n%s", r.Text)
	}
	list := must(t, s, "tabs", nil).Text
	if !strings.Contains(list, "Shop") || !strings.Contains(list, "Popup") {
		t.Fatalf("tabs: %s", list)
	}
	// Select the popup, then close it, which sends the tool back to the first tab.
	idx := -1
	for i, l := range strings.Split(list, "\n") {
		if strings.Contains(l, "Popup") {
			idx = i
		}
	}
	must(t, s, "tabs", map[string]any{"action": "select", "index": idx})
	if text := must(t, s, "snapshot", nil).Text; !strings.Contains(text, "Page: Popup") {
		t.Fatalf("selecting the tab did not switch to it:\n%s", text)
	}
	must(t, s, "tabs", map[string]any{"action": "close"})
	if text := must(t, s, "snapshot", nil).Text; !strings.Contains(text, "Page: Shop") {
		t.Fatalf("after closing, the tool should land on the remaining tab:\n%s", text)
	}
	if _, err := call(t, s, "tabs", map[string]any{"action": "select", "index": 9}); err == nil {
		t.Fatal("selecting a tab that does not exist should fail")
	}
	nt := must(t, s, "tabs", map[string]any{"action": "new", "url": srv.URL + "/two"}).Text
	if !strings.Contains(nt, "Two") {
		t.Fatalf("new tab: %s", nt)
	}
}

func TestScreenshotsComeBackAsImages(t *testing.T) {
	srv := site(t)
	s := session(t, Options{})
	must(t, s, "navigate", map[string]any{"url": srv.URL})
	r := must(t, s, "screenshot", nil)
	if len(r.Images) != 1 || r.Images[0].MIME != "image/png" {
		t.Fatalf("got %+v", r.Images)
	}
	if _, err := png.DecodeConfig(bytes.NewReader(r.Images[0].Data)); err != nil {
		t.Fatal(err)
	}
	j := must(t, s, "screenshot", map[string]any{"jpeg": true})
	if j.Images[0].MIME != "image/jpeg" || len(j.Images[0].Data) < 100 {
		t.Fatalf("jpeg: %+v", j.Images)
	}
}

func TestEvalIsAskedAboutFirst(t *testing.T) {
	srv := site(t)
	g := &recordingGuard{refuse: func(a Action) error {
		if a.Kind == "eval" {
			return errors.New("scripts are not allowed here")
		}
		return nil
	}}
	s := session(t, Options{Guard: g})
	must(t, s, "navigate", map[string]any{"url": srv.URL})
	if _, err := call(t, s, "eval", map[string]any{"expression": "1+1"}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("got %v", err)
	}
}

func TestAPersonCanTakeTheBrowserAndGiveItBack(t *testing.T) {
	srv := site(t)
	s := session(t, Options{Timeout: 3 * time.Second})
	must(t, s, "navigate", map[string]any{"url": srv.URL})

	s.Hold()
	if !s.Held() {
		t.Fatal("the browser should be held")
	}
	done := make(chan error, 1)
	go func() {
		_, err := call(t, s, "snapshot", nil)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("a call ran while a person had the browser: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	s.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the waiting call should run once the browser is back: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting call never ran")
	}

	// A person who never gives it back must not hang the agent for good.
	s.Hold()
	if _, err := call(t, s, "snapshot", nil); !errors.Is(err, ErrHeld) {
		t.Fatalf("got %v, want ErrHeld", err)
	}
	s.Release()
}

func TestCatalogueListsTheToolsInOrderWithoutABrowser(t *testing.T) {
	c := Catalogue()
	if len(c) != len(builtin()) {
		t.Fatalf("the catalogue has %d tools, the built-in set %d", len(c), len(builtin()))
	}
	for i := 1; i < len(c); i++ {
		if c[i-1].Name >= c[i].Name {
			t.Fatalf("%s should come before %s", c[i-1].Name, c[i].Name)
		}
	}
}
