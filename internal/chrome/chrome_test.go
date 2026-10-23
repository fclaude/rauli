package chrome

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// pages are the fixtures every test browses. They are small on purpose: each
// one has a single thing to find, so a failure says which operation broke.
var pages = map[string]string{
	"/": `<!doctype html><html lang="en"><head><title>Fixture home</title></head><body>
<h1>Fixture home</h1>
<p>A paragraph with a <a href="/two">link to page two</a> in it.</p>
<form action="/search" method="get">
  <label for="q">Search words</label>
  <input id="q" name="q" type="text">
  <button type="submit">Go</button>
</form>
<label><input type="checkbox" id="agree"> I agree</label>
<label for="colour">Colour</label>
<select id="colour"><option value="r">Red</option><option value="g">Green</option></select>
<a href="/popup" target="_blank">open in a new tab</a>
<a href="/file">download the report</a>
<button id="alert" onclick="alert('careful'); document.title='alerted'">Alert me</button>
<button id="log" onclick="console.log('clicked', 42)">Log it</button>
<div style="height:3000px">tall</div>
<p id="bottom">The very bottom</p>
</body></html>`,
	"/two":   `<!doctype html><title>Page two</title><h1>Page two</h1><p>Second page.</p><a href="/">back home</a>`,
	"/popup": `<!doctype html><title>Popup</title><h1>Popup page</h1>`,
	"/late": `<!doctype html><title>Late</title><h1>Late</h1><div id="x"></div>
<script>setTimeout(()=>{document.getElementById('x').textContent='arrived late'}, 600)</script>`,
	"/login": `<!doctype html><title>Login</title><h1>Login</h1>
<form action="/welcome" method="post">
<label for="u">User</label><input id="u" name="u"><label for="p">Password</label><input id="p" name="p" type="password">
<button>Sign in</button></form>`,
}

func fixture(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, body := range pages {
		body := body
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if path == "/" && r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		})
	}
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>Results</title><h1>Results for %s</h1>`, r.URL.Query().Get("q"))
	})
	mux.HandleFunc("/slowform", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Slow form</title><h1>Slow form</h1>
<form action="/slow" method="get"><label for="q">Question</label><input id="q" name="q"><button>Ask</button></form>`)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		// Nothing is written until the delay is over, so the browser has no page to show until then.
		time.Sleep(2500 * time.Millisecond)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>Slow</title><h1>Slow answer to %s</h1>`, r.URL.Query().Get("q"))
	})
	mux.HandleFunc("/busyform", func(w http.ResponseWriter, r *http.Request) {
		// On Enter the page queues a second and a half of its own work, which runs
		// before the form's submission task and so holds the navigation back.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Busy form</title><h1>Busy form</h1>
<form action="/search" method="get"><label for="q">Search words</label><input id="q" name="q"><button>Go</button></form>
<script>
document.getElementById('q').addEventListener('keydown', e => {
  if (e.key !== 'Enter') return;
  setTimeout(() => { const end = performance.now() + 1500; while (performance.now() < end) {} }, 0);
});
</script>`)
	})
	mux.HandleFunc("/cancelform", func(w http.ResponseWriter, r *http.Request) {
		// The form's navigation is announced when it is submitted, and then the
		// page's navigate handler cancels it, so it never begins.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Cancel form</title><h1>Cancel form</h1>
<form action="/search" method="get"><label for="q">Search words</label><input id="q" name="q"><button>Go</button></form>
<script>navigation.addEventListener('navigate', e => e.preventDefault());</script>`)
	})
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="report.txt"`)
		fmt.Fprint(w, "downloaded content")
	})
	mux.HandleFunc("/welcome", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>Welcome</title><h1>Welcome %s</h1><p>password length %d</p>`, r.Form.Get("u"), len(r.Form.Get("p")))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

var (
	sharedOnce sync.Once
	shared     *Browser
	sharedErr  error
)

// browser starts one headless browser for the whole package. Starting one per
// test would triple the run time and the load on the machine, and nothing
// here needs a browser of its own.
func browser(t *testing.T) *Browser {
	t.Helper()
	if _, err := Find(); err != nil {
		t.Skip(err)
	}
	sharedOnce.Do(func() {
		shared, sharedErr = Launch(context.Background(), Options{})
	})
	if sharedErr != nil {
		t.Fatal(sharedErr)
	}
	return shared
}

func TestMain(m *testing.M) {
	code := m.Run()
	if shared != nil {
		shared.Close()
	}
	// Exiting here skips the deferred cleanup of a failed start, which has
	// already removed its own profile.
	if code != 0 {
		println("chrome tests failed")
	}
	os.Exit(code)
}

func page(t *testing.T) (*Page, string, context.Context) {
	t.Helper()
	b := browser(t)
	srv := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	p, err := b.NewPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p.Close(c)
	})
	return p, srv.URL, ctx
}

func TestNavigateAndSnapshot(t *testing.T) {
	p, base, ctx := page(t)
	if err := p.Navigate(ctx, base+"/"); err != nil {
		t.Fatal(err)
	}
	snap, err := p.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Page: Fixture home",
		"URL: " + base + "/",
		`heading "Fixture home" [level=1]`,
		`link "link to page two"`,
		"-> " + base + "/two",
		`textbox "Search words"`,
		`button "Go"`,
		`checkbox "I agree"`,
	} {
		if !strings.Contains(snap, want) {
			t.Errorf("snapshot lacks %q:\n%s", want, snap)
		}
	}
	// A link's text belongs to the link, and is not printed again under it.
	if strings.Count(snap, "link to page two") != 1 {
		t.Errorf("the link text appears more than once:\n%s", snap)
	}
}

func TestClickFollowsALinkAndWaitsForThePage(t *testing.T) {
	p, base, ctx := page(t)
	if err := p.Navigate(ctx, base+"/"); err != nil {
		t.Fatal(err)
	}
	snap, _ := p.Snapshot(ctx, SnapshotOptions{})
	ref := refOf(t, snap, `link "link to page two"`)
	if err := p.Click(ctx, ref, ClickOptions{}); err != nil {
		t.Fatal(err)
	}
	info, err := p.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "Page two" || info.URL != base+"/two" {
		t.Fatalf("after the click the page is %+v", info)
	}
	// The old references are gone with the old page.
	if err := p.Click(ctx, ref, ClickOptions{}); err == nil || !strings.Contains(err.Error(), "take a new snapshot") {
		t.Fatalf("a stale reference should be refused with advice, got %v", err)
	}
}

func TestTypeAndSubmitAForm(t *testing.T) {
	p, base, ctx := page(t)
	p.Navigate(ctx, base+"/")
	snap, _ := p.Snapshot(ctx, SnapshotOptions{})
	if err := p.Type(ctx, refOf(t, snap, `textbox "Search words"`), "raulí trees", TypeOptions{Submit: true}); err != nil {
		t.Fatal(err)
	}
	text, _ := p.Text(ctx)
	if !strings.Contains(text, "Results for raulí trees") {
		info, _ := p.Info(ctx)
		value, _ := p.Eval(ctx, "document.getElementById('q') ? document.getElementById('q').value : '(no field: the page changed)'")
		t.Fatalf("the form was not submitted with the typed text.\ntab: %+v\nfield holds: %s\nconsole: %q\npage text: %q\nsettle: %s", info, value, p.Console(), text, strings.Join(p.settleLog, "\n  "))
	}
}

func TestTypeReplacesWhatWasThere(t *testing.T) {
	p, base, ctx := page(t)
	p.Navigate(ctx, base+"/")
	snap, _ := p.Snapshot(ctx, SnapshotOptions{})
	ref := refOf(t, snap, `textbox "Search words"`)
	p.Type(ctx, ref, "first", TypeOptions{})
	p.Type(ctx, ref, "second", TypeOptions{Replace: true})
	got, _ := p.Eval(ctx, "document.getElementById('q').value")
	if string(got) != `"second"` {
		t.Fatalf("the field holds %s", got)
	}
}

func TestSlowTypingSendsKeyEvents(t *testing.T) {
	p, base, ctx := page(t)
	p.Navigate(ctx, base+"/")
	p.Eval(ctx, `window.keys=[]; document.getElementById('q').addEventListener('keydown', e => window.keys.push(e.key))`)
	snap, _ := p.Snapshot(ctx, SnapshotOptions{})
	p.Type(ctx, refOf(t, snap, `textbox "Search words"`), "ab", TypeOptions{Slowly: true})
	got, _ := p.Eval(ctx, "window.keys.join('')")
	if string(got) != `"ab"` {
		t.Fatalf("the page saw keys %s", got)
	}
}

func TestCheckboxAndSelect(t *testing.T) {
	p, base, ctx := page(t)
	p.Navigate(ctx, base+"/")
	snap, _ := p.Snapshot(ctx, SnapshotOptions{})
	if err := p.Click(ctx, refOf(t, snap, `checkbox "I agree"`), ClickOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.Eval(ctx, "document.getElementById('agree').checked"); string(got) != "true" {
		t.Fatalf("the box is not checked: %s", got)
	}
	sel := refOf(t, snap, `combobox "Colour"`)
	if err := p.Select(ctx, sel, "Green"); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.Eval(ctx, "document.getElementById('colour').value"); string(got) != `"g"` {
		t.Fatalf("the drop-down holds %s", got)
	}
	if err := p.Select(ctx, sel, "Purple"); err == nil {
		t.Fatal("choosing an option that does not exist should fail")
	}
}

func TestScreenshotsNeedNoWindow(t *testing.T) {
	p, base, ctx := page(t)
	p.Navigate(ctx, base+"/")
	view, err := p.Screenshot(ctx, ShotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	vi, err := png.DecodeConfig(bytes.NewReader(view))
	if err != nil {
		t.Fatalf("the viewport shot is not a PNG: %v", err)
	}
	if vi.Width != 1280 || vi.Height != 800 {
		t.Errorf("viewport shot is %dx%d, want 1280x800", vi.Width, vi.Height)
	}
	full, err := p.Screenshot(ctx, ShotOptions{FullPage: true})
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := png.DecodeConfig(bytes.NewReader(full))
	if fi.Height < 3000 {
		t.Errorf("the full-page shot is only %d pixels tall; the page is over 3000", fi.Height)
	}
	snap, _ := p.Snapshot(ctx, SnapshotOptions{})
	el, err := p.Screenshot(ctx, ShotOptions{Ref: refOf(t, snap, `button "Go"`)})
	if err != nil {
		t.Fatal(err)
	}
	ei, _ := png.DecodeConfig(bytes.NewReader(el))
	if ei.Width >= 300 || ei.Width < 10 || ei.Height >= 100 {
		t.Errorf("the element shot is %dx%d, which is not the size of a button", ei.Width, ei.Height)
	}
}

func TestWaitForTextAndHistory(t *testing.T) {
	p, base, ctx := page(t)
	p.Navigate(ctx, base+"/late")
	if err := p.WaitForText(ctx, "arrived late"); err != nil {
		t.Fatal(err)
	}
	p.Navigate(ctx, base+"/two")
	if err := p.History(ctx, -1); err != nil {
		t.Fatal(err)
	}
	if info, _ := p.Info(ctx); info.Title != "Late" {
		t.Fatalf("back went to %+v", info)
	}
	if err := p.History(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if info, _ := p.Info(ctx); info.Title != "Page two" {
		t.Fatalf("forward went to %+v", info)
	}
	if err := p.History(ctx, 5); err == nil {
		t.Fatal("going past the end of the history should fail")
	}
}

func TestDialogsAreAnsweredAndRecorded(t *testing.T) {
	p, base, ctx := page(t)
	p.Navigate(ctx, base+"/")
	snap, _ := p.Snapshot(ctx, SnapshotOptions{})
	if err := p.Click(ctx, refOf(t, snap, `button "Alert me"`), ClickOptions{}); err != nil {
		t.Fatal(err)
	}
	d := p.Dialogs()
	if len(d) != 1 || d[0].Message != "careful" || !d[0].Accepted {
		t.Fatalf("dialogs: %+v", d)
	}
	if info, _ := p.Info(ctx); info.Title != "alerted" {
		t.Fatalf("the page did not carry on after the alert: %+v", info)
	}
}

func TestConsoleOutputIsKept(t *testing.T) {
	p, base, ctx := page(t)
	p.Navigate(ctx, base+"/")
	snap, _ := p.Snapshot(ctx, SnapshotOptions{})
	p.Click(ctx, refOf(t, snap, `button "Log it"`), ClickOptions{})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c := p.Console(); len(c) > 0 {
			if c[0] != "log: clicked 42" {
				t.Fatalf("console: %q", c)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("nothing reached the console log")
}

func TestScrollAndKeys(t *testing.T) {
	p, base, ctx := page(t)
	p.Navigate(ctx, base+"/")
	if err := p.Scroll(ctx, 0, 1200); err != nil {
		t.Fatal(err)
	}
	if y, _ := p.Eval(ctx, "Math.round(window.scrollY)"); string(y) == "0" {
		t.Fatal("the page did not scroll")
	}
	if err := p.Press(ctx, "End"); err != nil {
		t.Fatal(err)
	}
	if err := p.Press(ctx, "Control+a"); err != nil {
		t.Fatal(err)
	}
	if err := p.Press(ctx, "Nonsense"); err == nil {
		t.Fatal("an unknown key should be refused")
	}
}

func TestANewWindowIsAnotherTab(t *testing.T) {
	p, base, ctx := page(t)
	p.Navigate(ctx, base+"/")
	snap, _ := p.Snapshot(ctx, SnapshotOptions{})
	if err := p.Click(ctx, refOf(t, snap, `link "open in a new tab"`), ClickOptions{}); err != nil {
		t.Fatal(err)
	}
	var popup *Page
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && popup == nil {
		pages, err := p.b.Pages(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range pages {
			if info, _ := q.Info(ctx); info.Title == "Popup" {
				popup = q
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if popup == nil {
		t.Fatal("the new tab never showed up")
	}
	popup.Close(ctx)
}

func TestUserAgentIsNotHeadless(t *testing.T) {
	p, base, ctx := page(t)
	p.Navigate(ctx, base+"/")
	ua, _ := p.Eval(ctx, "navigator.userAgent")
	if strings.Contains(string(ua), "Headless") {
		t.Fatalf("the page can see a headless browser: %s", ua)
	}
}

func TestNavigationErrorsAreReported(t *testing.T) {
	p, _, ctx := page(t)
	// A port nothing listens on: ask the system for a free one and let it go.
	l, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatal(lerr)
	}
	addr := l.Addr().String()
	l.Close()
	err := p.Navigate(ctx, "http://"+addr+"/")
	if err == nil || !strings.Contains(err.Error(), "ERR_CONNECTION_REFUSED") {
		t.Fatalf("expected a refused connection, got %v", err)
	}
}

// refOf finds the reference printed on the snapshot line for an element.
func refOf(t *testing.T, snap, line string) string {
	t.Helper()
	for _, l := range strings.Split(snap, "\n") {
		if strings.Contains(l, line) {
			if i := strings.Index(l, "[ref="); i >= 0 {
				return l[i+5 : i+strings.Index(l[i:], "]")]
			}
		}
	}
	t.Fatalf("no line with %q and a reference in:\n%s", line, snap)
	return ""
}

// A filter sees every request, subresources included, and what it blocks
// never reaches the network.
func TestFilterBlocksRequestsBeforeTheyReachTheNetwork(t *testing.T) {
	if _, err := Find(); err != nil {
		t.Skip(err)
	}
	srv := fixture(t)
	var mu sync.Mutex
	var seen []string
	b, err := Launch(context.Background(), Options{Filter: func(_ context.Context, r Request) error {
		mu.Lock()
		seen = append(seen, r.Type+" "+r.URL)
		mu.Unlock()
		if strings.HasSuffix(r.URL, "/two") {
			return fmt.Errorf("the rules do not allow %s", r.URL)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := b.NewPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Navigate(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	err = p.Navigate(ctx, srv.URL+"/two")
	if err == nil || !strings.Contains(err.Error(), "ERR_BLOCKED_BY_CLIENT") {
		t.Fatalf("the blocked navigation should fail as blocked by the client, got %v", err)
	}
	if info, _ := p.Info(ctx); info.Title != "Fixture home" {
		t.Fatalf("the page left %q although its next page was blocked", info.Title)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || !strings.HasPrefix(seen[0], "Document ") {
		t.Fatalf("the filter was not asked about the first document: %v", seen)
	}
}

func TestDownloadsLandOnlyWhereTheyAreAllowed(t *testing.T) {
	if _, err := Find(); err != nil {
		t.Skip(err)
	}
	srv := fixture(t)
	dir := t.TempDir()
	b, err := Launch(context.Background(), Options{DownloadDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := b.NewPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Navigate(ctx, srv.URL+"/file"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(dir, "report.txt")); err == nil {
			if string(data) != "downloaded content" {
				t.Fatalf("the downloaded file holds %q", data)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the download never reached the allowed directory")
}

// Right after Close the browser still lists the tab. Callers must not be handed
// it, because acting on it fails with a session that no longer exists.
func TestAClosedTabIsNotListed(t *testing.T) {
	p, _, ctx := page(t)
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		pages, err := p.b.Pages(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range pages {
			if q == p {
				t.Fatalf("the closed tab was listed on call %d", i)
			}
		}
	}
}

// A page that is slow to begin answering has not replaced the old one yet, and
// until it does the old document still reports itself loaded. The call that
// submitted the form must wait for the new page, not take the old page's
// finished state for the end of the navigation.
func TestSubmittingToASlowPageWaitsForItToReplaceTheOldOne(t *testing.T) {
	p, base, ctx := page(t)
	p.Navigate(ctx, base+"/slowform")
	snap, _ := p.Snapshot(ctx, SnapshotOptions{})
	if err := p.Type(ctx, refOf(t, snap, `textbox "Question"`), "anyone there", TypeOptions{Submit: true}); err != nil {
		t.Fatal(err)
	}
	text, _ := p.Text(ctx)
	if !strings.Contains(text, "Slow answer to anyone there") {
		t.Fatalf("the call returned before the slow page arrived: %q", text)
	}
}

// A form submitted with Enter announces its navigation at once, but the
// navigation runs in a task the browser holds back after input, behind whatever
// the page queued first. This page queues a second and a half of work on Enter,
// as a page busy after a key press or a page on a loaded machine does, so the new
// page begins loading long after a wait for it to begin would have ended. The
// call must still wait for it rather than report the old page.
func TestASubmissionThePageHoldsUpIsStillWaitedFor(t *testing.T) {
	p, base, ctx := page(t)
	if err := p.Navigate(ctx, base+"/busyform"); err != nil {
		t.Fatal(err)
	}
	snap, err := p.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Type(ctx, refOf(t, snap, `textbox "Search words"`), "patience", TypeOptions{Submit: true}); err != nil {
		t.Fatal(err)
	}
	text, _ := p.Text(ctx)
	if !strings.Contains(text, "Results for patience") {
		info, _ := p.Info(ctx)
		t.Fatalf("Type returned before the held-up submission arrived.\ntab: %+v\npage text: %q\nsettle: %s", info, text, strings.Join(p.settleLog, "\n  "))
	}
}

// A navigation the page announces and then cancels never begins, and nothing
// says so. The call must come back once the wait for it is over, with the page
// left as it was, rather than wait for as long as the caller allows.
func TestASubmissionThePageCancelsDoesNotHoldTheCall(t *testing.T) {
	limit := requestedStart
	requestedStart = time.Second
	t.Cleanup(func() { requestedStart = limit })
	p, base, ctx := page(t)
	if err := p.Navigate(ctx, base+"/cancelform"); err != nil {
		t.Fatal(err)
	}
	snap, err := p.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if err := p.Type(ctx, refOf(t, snap, `textbox "Search words"`), "never mind", TypeOptions{Submit: true}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("the call took %v to come back", took)
	}
	if info, _ := p.Info(ctx); info.Title != "Cancel form" {
		t.Fatalf("the cancelled submission left the page: %+v", info)
	}
	if log := strings.Join(p.settleLog, "\n  "); !strings.Contains(log, "navigation requested") {
		t.Fatalf("the cancelled submission was never announced, so this test does not reach the bound:\n  %s", log)
	}
}

// The wait for a navigation to begin grows with how slowly the browser answers,
// so a busy machine is given the time it needs and a fast one is not slowed.
func TestTheWaitForANavigationToBeginScalesWithTheBrowsersSpeed(t *testing.T) {
	for _, c := range []struct {
		response time.Duration
		want     time.Duration
	}{
		{0, 400 * time.Millisecond},
		{time.Millisecond, 430 * time.Millisecond},
		{20 * time.Millisecond, time.Second},
		{100 * time.Millisecond, 3400 * time.Millisecond},
		{time.Second, 5 * time.Second},
	} {
		if got := startWindow(c.response); got != c.want {
			t.Errorf("startWindow(%v) = %v, want %v", c.response, got, c.want)
		}
	}
}

// An action that loads nothing must come back quickly on a machine that is not
// slow: the wait is only as long as it needs to be.
func TestAClickThatLoadsNothingDoesNotWaitLong(t *testing.T) {
	p, base, ctx := page(t)
	p.Navigate(ctx, base+"/")
	snap, _ := p.Snapshot(ctx, SnapshotOptions{})
	ref := refOf(t, snap, `button "Log it"`)
	began := time.Now()
	if err := p.Click(ctx, ref, ClickOptions{}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("a click that loads nothing took %v", took)
	}
}

// A link that turns out to be a download starts a navigation that never
// commits a page. The click must come back, and the tab must stay where it was.
func TestClickingADownloadLinkReturnsAndLeavesTheTabAlone(t *testing.T) {
	p, base, ctx := page(t)
	p.Navigate(ctx, base+"/")
	snap, _ := p.Snapshot(ctx, SnapshotOptions{})
	began := time.Now()
	if err := p.Click(ctx, refOf(t, snap, `link "download the report"`), ClickOptions{}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 6*time.Second {
		t.Fatalf("the click took %v to come back", took)
	}
	if info, _ := p.Info(ctx); info.Title != "Fixture home" {
		t.Fatalf("a download replaced the page: %+v", info)
	}
}
