package browser_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/rauli/browser"
)

// hostGuard allows the pages of one host and port and refuses the rest, and
// keeps the actions it was asked about. It is written outside Raulí's own
// packages, as a program that links Raulí would write it.
type hostGuard struct {
	allow string
	mu    sync.Mutex
	acts  []browser.Action
}

func (g *hostGuard) CheckURL(_ context.Context, r browser.Request) error {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host != g.allow {
		return errors.New(r.URL + " is not on the list")
	}
	return nil
}

func (g *hostGuard) CheckAction(_ context.Context, a browser.Action) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.acts = append(g.acts, a)
	return nil
}

func page(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, s *browser.Session, name string, args any) (browser.Result, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	return s.Call(ctx, name, raw)
}

// A program outside Raulí starts a session with its own Guard, reads a page,
// has an address refused, and sees the action it was asked about with the
// element described.
func TestAProgramDrivesTheBrowserWithItsOwnGuard(t *testing.T) {
	if _, err := browser.Find(); err != nil {
		t.Skip(err)
	}
	allowed := page(t, `<!doctype html><title>Allowed</title><h1>Hello</h1><button>Pay now</button>`)
	refused := page(t, `<!doctype html><title>Refused</title><h1>Secret</h1>`)
	host := strings.TrimPrefix(allowed.URL, "http://")
	guard := &hostGuard{allow: host}
	s, err := browser.New(context.Background(), browser.Options{Guard: guard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	res, err := call(t, s, "navigate", map[string]any{"url": allowed.URL})
	if err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if !strings.Contains(res.Text, "Hello") || !strings.Contains(res.Text, `button "Pay now"`) {
		t.Fatalf("the snapshot does not show the page:\n%s", res.Text)
	}
	if _, err := call(t, s, "navigate", map[string]any{"url": refused.URL}); err == nil || !strings.Contains(err.Error(), "not on the list") {
		t.Fatalf("navigating to a refused host: got %v, want the guard's refusal", err)
	}

	ref := ""
	for _, line := range strings.Split(res.Text, "\n") {
		if strings.Contains(line, "Pay now") {
			if i := strings.Index(line, "[ref="); i >= 0 {
				ref = strings.TrimSuffix(strings.Fields(line[i+len("[ref="):])[0], "]")
			}
		}
	}
	if ref == "" {
		t.Fatalf("no reference for the button in:\n%s", res.Text)
	}
	if got := s.Current().Describe(ref); got != `button "Pay now"` {
		t.Fatalf("Describe(%s) = %q, want the button", ref, got)
	}
	if _, err := call(t, s, "click", map[string]any{"ref": ref}); err != nil {
		t.Fatalf("click: %v", err)
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	var kinds []string
	for _, a := range guard.acts {
		kinds = append(kinds, a.Kind+" "+a.Element)
	}
	if want := "click " + `button "Pay now"`; !strings.Contains(strings.Join(kinds, "\n"), want) {
		t.Fatalf("the guard was asked about %q, want %q among them", kinds, want)
	}
}

// The catalogue lists the tools without a browser, as the command's tools
// listing does.
func TestCatalogueListsTheToolsWithoutABrowser(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range browser.Catalogue() {
		names[tool.Name] = true
	}
	for _, want := range []string{"navigate", "snapshot", "click", "type", "fill_credential"} {
		if !names[want] {
			t.Errorf("the catalogue has no %s", want)
		}
	}
}
