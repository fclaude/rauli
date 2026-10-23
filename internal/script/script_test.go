package script

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/rauli/internal/chrome"
	"github.com/fclaude/rauli/internal/tools"
)

// site serves the pages the recipes read and fill in.
func site(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		}
	}
	mux.HandleFunc("/", page(`<!doctype html><title>Home</title><h1>Home</h1><p>Hello.</p>`))
	mux.HandleFunc("/newsroom", page(`<!doctype html><title>Newsroom</title><h1>The Newsroom</h1>
<h2>Trees grow quietly</h2><p>text</p><h2>The shoot and the beech</h2><p>text</p><h2>A browser for everyone</h2>
<h3>Not a headline</h3>`))
	mux.HandleFunc("/signup", page(`<!doctype html><title>Sign up</title><h1>Sign up</h1>
<form action="/thanks" method="post"><label for="n">Name</label><input id="n" name="n">
<label for="e">Email</label><input id="e" name="e"><button>Sign up</button></form>`))
	mux.HandleFunc("/thanks", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>Thanks</title><h1>Thanks %s</h1><p>We will write to %s.</p>`, r.Form.Get("n"), r.Form.Get("e"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

var (
	sharedOnce sync.Once
	shared     *tools.Session
	sharedErr  error
)

// sharedSession starts one browser for the whole package: nothing here needs a
// browser of its own, and starting one per test would triple the run time.
func sharedSession(t *testing.T) *tools.Session {
	t.Helper()
	if _, err := chrome.Find(); err != nil {
		t.Skip(err)
	}
	sharedOnce.Do(func() {
		shared, sharedErr = tools.New(context.Background(), tools.Options{})
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
	os.Exit(code)
}

func recipeFile(t *testing.T, src string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "recipe.scm")
	if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// load builds a runtime on the shared browser and loads the recipe source.
func load(t *testing.T, opts Options, src string) *Runtime {
	t.Helper()
	rt, err := New(SessionBackend{S: sharedSession(t)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.LoadFile(context.Background(), recipeFile(t, src)); err != nil {
		t.Fatal(err)
	}
	rt.Finish()
	return rt
}

func call(t *testing.T, rt *Runtime, name string, args any) (tools.Result, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return rt.Call(ctx, name, raw)
}

func TestARecipeRunsAMultiStepFlow(t *testing.T) {
	srv := site(t)
	rt := load(t, Options{}, `
(define-recipe "who-says-what" "Open the sign-up page and report its title and heading."
  '(("url" "string" "the site"))
  (lambda (url)
    (navigate (string-append url "/signup"))
    (display "title: ") (display (page-title)) (newline)
    (let ((page (snapshot)))
      (string-append "has a Sign up button: " (if (find-ref page "button \"Sign up\"") "yes" "no")))))`)
	res, err := call(t, rt, "recipe_who-says-what", map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "title: Sign up\nhas a Sign up button: yes" {
		t.Fatalf("got %q", res.Text)
	}
}

func TestTheExampleRecipesLoadAndRun(t *testing.T) {
	srv := site(t)
	rt, err := New(SessionBackend{S: sharedSession(t)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.LoadPaths(context.Background(), []string{"../../docs/examples/recipes"}); err != nil {
		t.Fatalf("the examples no longer load: %v", err)
	}
	rt.Finish()
	if got := len(rt.Recipes()); got != 2 {
		t.Fatalf("the examples define %d recipes, want 2", got)
	}

	res, err := call(t, rt, "recipe_headlines", map[string]any{"url": srv.URL + "/newsroom"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Trees grow quietly\nThe shoot and the beech\nA browser for everyone"; res.Text != want {
		t.Fatalf("headlines:\n%s", res.Text)
	}
	res, err = call(t, rt, "recipe_headlines", map[string]any{"url": srv.URL + "/newsroom", "limit": 2})
	if err != nil || strings.Count(res.Text, "\n") != 1 {
		t.Fatalf("limit 2: %q %v", res.Text, err)
	}

	res, err = call(t, rt, "recipe_signup", map[string]any{"url": srv.URL + "/signup", "name": "Raulí", "email": "r@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Thanks Raulí") || !strings.Contains(res.Text, "r@example.org") {
		t.Fatalf("signup: %q", res.Text)
	}
	if _, err := call(t, rt, "recipe_signup", map[string]any{"url": srv.URL + "/", "name": "x", "email": "y"}); err == nil || !strings.Contains(err.Error(), "does not have a Name field") {
		t.Fatalf("a page without the form should give the recipe's own message, got %v", err)
	}
}

func TestParametersAreCheckedAndDefaultsFilled(t *testing.T) {
	rt := load(t, Options{}, `
(define-recipe "echo" "Return its arguments as text."
  '(("word" "string" "a word") ("times" "number" "how often" 2) ("loud" "boolean" "shout" #f))
  (lambda (word times loud)
    (string-append (number->string times) " " (if loud (string-upcase word) word))))`)
	cases := []struct {
		args    string
		want    string
		wantErr string
	}{
		{`{"word":"hi"}`, "2 hi", ""},
		{`{"word":"hi","times":3.5,"loud":true}`, "3.5 HI", ""},
		{`{}`, "", `the parameter "word" is required`},
		{`{"word":7}`, "", `the parameter "word" must be a string, but got a number`},
		{`{"word":"a","times":"many"}`, "", `the parameter "times" must be a number, but got a string`},
		{`{"word":"a","loud":"yes"}`, "", `the parameter "loud" must be a boolean, but got a string`},
		{`{"word":"a","colour":"red"}`, "", `there is no parameter "colour"; the parameters are word, times, loud`},
		{`[1]`, "", "the arguments must be a JSON object"},
	}
	for _, c := range cases {
		res, err := rt.Call(context.Background(), "recipe_echo", json.RawMessage(c.args))
		switch {
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: got %v, want an error containing %q", c.args, err, c.wantErr)
		case c.wantErr == "" && (err != nil || res.Text != c.want):
			t.Errorf("%s: got %q, %v; want %q", c.args, res.Text, err, c.want)
		}
	}
}

func TestARecipeCannotReachTheMachine(t *testing.T) {
	rt := load(t, Options{}, `
(define-recipe "peek" "Try to read a file." '() (lambda () (load "/etc/passwd")))
(define-recipe "slurp" "Try the command line's file reader." '() (lambda () (read-file "/etc/passwd")))
(define-recipe "call" "Try to reach the network." '() (lambda () (http-request "GET" "http://127.0.0.1:1/")))`)
	for name, want := range map[string]string{
		"recipe_peek":  "cannot use the machine's files, programs or network",
		"recipe_slurp": "unbound variable",
		"recipe_call":  "unbound variable",
	} {
		if _, err := call(t, rt, name, nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", name, err, want)
		}
	}
	// Whatever the interpreter offers that touches the machine is refused by the
	// gate, wherever it is called from: check every such primitive.
	for _, p := range rt.in.Prims() {
		if p.Meta.Host || p.Meta.Egress || p.Meta.Irreversible {
			if err := (gate{}).Before(context.Background(), p, nil); err == nil {
				t.Errorf("the gate lets %s through", p.Name)
			}
		}
	}
}

func TestARunawayRecipeStopsAndTheNextRunWorks(t *testing.T) {
	rt := load(t, Options{Deadline: 400 * time.Millisecond}, `
(define-recipe "forever" "Loop without end." '() (lambda () (let loop () (loop))))
(define-recipe "fine" "Return at once." '() (lambda () "still here"))`)
	started := time.Now()
	_, err := call(t, rt, "recipe_forever", nil)
	if err == nil || !strings.Contains(err.Error(), "recipe forever stopped after 400ms") {
		t.Fatalf("a runaway loop should be stopped at the time limit, got %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatalf("it took %v to stop", time.Since(started))
	}
	if res, err := call(t, rt, "recipe_fine", nil); err != nil || res.Text != "still here" {
		t.Fatalf("the interpreter should work after a stopped run: %q %v", res.Text, err)
	}
}

func TestOutputIsCapped(t *testing.T) {
	rt := load(t, Options{MaxOutput: 100}, `
(define-recipe "chatty" "Print a lot." '()
  (lambda () (let loop ((i 0)) (if (< i 1000) (begin (display "0123456789") (loop (+ i 1))))) "done"))
(define-recipe "long" "Return a long string." '()
  (lambda () (string-pad-left "" 5000 "x")))`)
	for _, name := range []string{"recipe_chatty", "recipe_long"} {
		res, err := call(t, rt, name, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(res.Text, "[output cut at 100 characters]") || len([]rune(res.Text)) > 140 {
			t.Errorf("%s: %d characters, ends %q", name, len([]rune(res.Text)), res.Text[max(0, len(res.Text)-40):])
		}
	}
}

func TestRecipesLoadOnlyWhenTheyAreMeantToAndOnlyOnce(t *testing.T) {
	rt, err := New(nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	first := recipeFile(t, `(define-recipe "a" "One." '() (lambda () "x"))`)
	if err := rt.LoadFile(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := recipeFile(t, `(define-recipe "a" "Again." '() (lambda () "y"))`)
	if err := rt.LoadFile(context.Background(), second); err == nil || !strings.Contains(err.Error(), `there is already a recipe called "a", defined in `+first) {
		t.Fatalf("a second recipe with the same name should be refused and name the first, got %v", err)
	}
	rt.Finish()
	if err := rt.LoadFile(context.Background(), second); err == nil {
		t.Fatal("recipes must not load after the server has started")
	}
	// The browser helpers refuse to run outside a recipe.
	rt2, _ := New(nil, Options{})
	err = rt2.LoadFile(context.Background(), recipeFile(t, `(navigate "https://example.com")`))
	if err == nil || !strings.Contains(err.Error(), "only works while a recipe runs") {
		t.Fatalf("driving the browser while loading should be refused, got %v", err)
	}
	// And define-recipe refuses to run inside a run.
	rt3, _ := New(nil, Options{})
	rt3.LoadFile(context.Background(), recipeFile(t, `(define-recipe "sneaky" "Defines another." '() (lambda () (define-recipe "late" "Too late." '() (lambda () 1))))`))
	rt3.Finish()
	if _, err := call(t, rt3, "recipe_sneaky", nil); err == nil || !strings.Contains(err.Error(), "only be defined while the recipe files load") {
		t.Fatalf("got %v", err)
	}
}

func TestBadDefinitionsAreExplained(t *testing.T) {
	for src, want := range map[string]string{
		`(define-recipe "Bad Name" "x" '() (lambda () 1))`:                                 `is not a recipe name`,
		`(define-recipe "ok" "" '() (lambda () 1))`:                                        `needs a description`,
		`(define-recipe "ok" "d" '(("a" "string")) (lambda (a) a))`:                        `write it as (name type doc)`,
		`(define-recipe "ok" "d" '(("a" "colour" "doc")) (lambda (a) a))`:                  `is not one of string, number, boolean`,
		`(define-recipe "ok" "d" '(("a" "string" "doc" 5)) (lambda (a) a))`:                `the default of "a" must be a string`,
		`(define-recipe "ok" "d" '(("a" "string" "x") ("a" "string" "y")) (lambda (a) a))`: `two parameters called "a"`,
	} {
		rt, _ := New(nil, Options{})
		err := rt.LoadFile(context.Background(), recipeFile(t, src))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s\n  got %v, want %q", src, err, want)
		}
	}
}

func TestToolsDescribeRecipesWithASchema(t *testing.T) {
	rt, _ := New(nil, Options{})
	if err := rt.LoadFile(context.Background(), recipeFile(t, `
(define-recipe "find-it" "Find a thing." '(("query" "string" "what to find") ("limit" "number" "how many" 5) ("exact" "boolean" "exact match" #t))
  (lambda (q l e) q))`)); err != nil {
		t.Fatal(err)
	}
	list := rt.Tools()
	if len(list) != 1 || list[0].Name != "recipe_find-it" || list[0].Description != "Find a thing." {
		t.Fatalf("tools: %+v", list)
	}
	raw, _ := json.Marshal(list[0].Schema)
	var s struct {
		Type       string                    `json:"type"`
		Required   []string                  `json:"required"`
		Extra      bool                      `json:"additionalProperties"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s.Type != "object" || len(s.Required) != 1 || s.Required[0] != "query" || s.Extra {
		t.Fatalf("schema: %s", raw)
	}
	if s.Properties["limit"]["type"] != "number" || s.Properties["limit"]["default"] != float64(5) || s.Properties["exact"]["type"] != "boolean" || s.Properties["exact"]["default"] != true {
		t.Fatalf("properties: %s", raw)
	}
}

func TestRecipesMayCallEachOtherOnlySoDeep(t *testing.T) {
	rt := load(t, Options{MaxDepth: 3}, `
(define-recipe "again" "Call itself." '(("n" "number" "depth" 0))
  (lambda (n) (recipe "again" (list (cons "n" (+ n 1))))))
(define-recipe "leaf" "A recipe that ends." '() (lambda () "leaf"))
(define-recipe "outer" "Call a leaf." '() (lambda () (string-append "outer then " (recipe "leaf"))))`)
	if _, err := call(t, rt, "recipe_again", nil); err == nil || !strings.Contains(err.Error(), "nested too deeply: the limit is 3") {
		t.Fatalf("unbounded recursion should be stopped, got %v", err)
	}
	if res, err := call(t, rt, "recipe_outer", nil); err != nil || res.Text != "outer then leaf" {
		t.Fatalf("a recipe calling another: %q %v", res.Text, err)
	}
}

func TestFailuresInTheBrowserNameTheRecipeAndTheStep(t *testing.T) {
	srv := site(t)
	rt := load(t, Options{}, `
(define-recipe "bad-click" "Click something that is not there." '(("url" "string" "site"))
  (lambda (url) (navigate url) (click "e99")))`)
	_, err := call(t, rt, "recipe_bad-click", map[string]any{"url": srv.URL})
	if err == nil || !strings.Contains(err.Error(), "recipe bad-click") || !strings.Contains(err.Error(), "click") || !strings.Contains(err.Error(), "take a new snapshot") {
		t.Fatalf("the error should name the recipe and the step and say what to do, got %v", err)
	}
}

func TestScreenshotsComeBackWithTheResult(t *testing.T) {
	srv := site(t)
	rt := load(t, Options{}, `
(define-recipe "shoot" "Photograph a page." '(("url" "string" "site"))
  (lambda (url) (navigate url) (screenshot) "photographed"))`)
	res, err := call(t, rt, "recipe_shoot", map[string]any{"url": srv.URL})
	if err != nil || res.Text == "" || len(res.Images) != 1 || res.Images[0].MIME != "image/png" {
		t.Fatalf("%q %d images %v", res.Text, len(res.Images), err)
	}
}

func TestMissingRecipePathsNameThemAndDirectoriesLoadInOrder(t *testing.T) {
	rt, _ := New(nil, Options{})
	if err := rt.LoadPaths(context.Background(), []string{"/no/such/recipes"}); err == nil || !strings.Contains(err.Error(), "/no/such/recipes") {
		t.Fatalf("a missing path must be named, got %v", err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "b.scm"), []byte(`(define-recipe "second" "S." '() (lambda () 2))`), 0o600)
	os.WriteFile(filepath.Join(dir, "a.scm"), []byte(`(define-recipe "first" "F." '() (lambda () 1))`), 0o600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(`ignored`), 0o600)
	if err := rt.LoadPaths(context.Background(), []string{dir}); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, rc := range rt.Recipes() {
		names = append(names, rc.Name)
	}
	if strings.Join(names, ",") != "first,second" {
		t.Fatalf("recipes loaded in the order %v", names)
	}
}

func TestACommandLineBecomesTypedArguments(t *testing.T) {
	rt, _ := New(nil, Options{})
	if err := rt.LoadFile(context.Background(), recipeFile(t, `(define-recipe "r" "R." '(("a" "string" "A") ("n" "number" "N" 1) ("b" "boolean" "B" #f)) (lambda (a n b) a))`)); err != nil {
		t.Fatal(err)
	}
	rc := rt.Lookup("r")
	raw, err := rc.ParseArgs([]string{"a=hello world", "n=2.5", "b=yes"})
	if err != nil || string(raw) != `{"a":"hello world","b":true,"n":2.5}` {
		t.Fatalf("%s %v", raw, err)
	}
	for _, bad := range []string{"nope", "z=1", "n=many", "b=maybe"} {
		if _, err := rc.ParseArgs([]string{bad}); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}
