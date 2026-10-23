package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testRecipes = `
(define-recipe "open-shop" "Open the shop and say what is on the page."
  '(("url" "string" "The shop's address") ("shout" "boolean" "Answer in capitals" #f))
  (lambda (url shout)
    (navigate url)
    (let ((page (snapshot)))
      (if shout (string-upcase (page-title)) (page-title)))))

(define-recipe "delete-it" "Press the delete button."
  '(("url" "string" "The shop's address"))
  (lambda (url)
    (navigate url)
    (click (find-ref (snapshot) "button \"Delete account\""))))

(define-recipe "wander" "Go somewhere the rules do not allow."
  '() (lambda () (navigate "https://example.com/")))
`

func shopSite(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Shop</title><h1>Shop</h1><button onclick="document.title='deleted'">Delete account</button>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRecipesListsWithoutABrowser(t *testing.T) {
	dir := writeFiles(t, map[string]string{"shop.scm": testRecipes})
	code, out, errs := do(t, "", "recipes", filepath.Join(dir, "shop.scm"))
	if code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	for _, want := range []string{"open-shop\n    Open the shop and say what is on the page.", "url (string): The shop's address [required]", "shout (boolean): Answer in capitals [default"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = do(t, "", "recipes", "--json", dir)
	var list []struct {
		Name       string         `json:"name"`
		Parameters map[string]any `json:"parameters"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &list) != nil || len(list) != 3 || list[0].Name != "recipe_open-shop" {
		t.Fatalf("json listing: %d %s", code, out)
	}
	if code, _, errs = do(t, "", "recipes", "/no/such/recipes"); code == 0 || !strings.Contains(errs, "/no/such/recipes") {
		t.Fatalf("a missing path should be named: %d %q", code, errs)
	}
	if code, _, _ = do(t, ""); code == 0 {
		t.Fatal("no arguments should not succeed")
	}
	if code, _, errs = do(t, "", "recipes"); code != 2 {
		t.Fatalf("naming nothing is a usage error, got %d %q", code, errs)
	}
}

func TestRunExecutesARecipeWithArguments(t *testing.T) {
	needBrowser(t)
	srv := shopSite(t)
	dir := writeFiles(t, map[string]string{"shop.scm": testRecipes})
	file := filepath.Join(dir, "shop.scm")
	code, out, errs := do(t, "", "run", file, "--recipe", "open-shop", "--arg", "url="+srv.URL, "--arg", "shout=true")
	if code != 0 || strings.TrimSpace(out) != "SHOP" {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	if code, _, errs = do(t, "", "run", file, "--recipe", "nothing"); code == 0 || !strings.Contains(errs, `there is no recipe called "nothing"; the recipes are open-shop, delete-it, wander`) {
		t.Fatalf("%d %q", code, errs)
	}
	if code, _, errs = do(t, "", "run", file); code == 0 || !strings.Contains(errs, "defines no main and 3 recipes; name one with --recipe") {
		t.Fatalf("%d %q", code, errs)
	}
	if code, _, errs = do(t, "", "run", file, "--recipe", "open-shop", "--arg", "oops"); code != 2 {
		t.Fatalf("a malformed --arg is a usage error, got %d %q", code, errs)
	}
	main := writeFiles(t, map[string]string{"main.scm": `(define (main) (navigate "` + srv.URL + `") (page-title))`})
	if code, out, errs = do(t, "", "run", filepath.Join(main, "main.scm")); code != 0 || strings.TrimSpace(out) != "Shop" {
		t.Fatalf("main: %d %q %q", code, out, errs)
	}
}

func TestServeOffersRecipesAndTheRulesAndAuditSeeEveryStep(t *testing.T) {
	needBrowser(t)
	srv := shopSite(t)
	dir := writeFiles(t, map[string]string{"shop.scm": testRecipes})
	audit := filepath.Join(dir, "audit.jsonl")
	conf := filepath.Join(dir, "config.toml")
	text := fmt.Sprintf(`
[recipes]
paths = ["shop.scm"]
deadline = "30s"

[rules.network]
allow = [%q]

[[rules.actions.rule]]
kind = "click"
element = "*Delete*"
decision = "deny"
reason = "deleting an account needs a person"

[rules.audit]
file = "audit.jsonl"
`, srv.URL+"/*")
	if err := os.WriteFile(conf, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	call := func(id int, name string, args map[string]any) string {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		return string(b)
	}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		call(3, "recipe_open-shop", map[string]any{"url": srv.URL}),
		call(4, "recipe_delete-it", map[string]any{"url": srv.URL}),
		call(5, "recipe_wander", nil),
		call(6, "recipe_open-shop", map[string]any{"url": 5}),
		call(7, "snapshot", nil),
	}, "\n") + "\n"
	code, out, errs := do(t, in, "serve", "--config", conf)
	if code != 0 {
		t.Fatalf("serve: %d %q", code, errs)
	}

	type reply struct {
		ID     int `json:"id"`
		Result struct {
			IsError bool `json:"isError"`
			Tools   []struct {
				Name string `json:"name"`
			} `json:"tools"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	replies := map[int]reply{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var r reply
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("not a protocol message: %q", line)
		}
		replies[r.ID] = r
	}
	body := func(id int) string {
		if c := replies[id].Result.Content; len(c) > 0 {
			return c[0].Text
		}
		return ""
	}
	names := map[string]bool{}
	for _, tl := range replies[2].Result.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"recipe_open-shop", "recipe_delete-it", "recipe_wander", "snapshot", "navigate"} {
		if !names[want] {
			t.Errorf("tools/list lacks %s", want)
		}
	}
	if r := replies[3]; r.Result.IsError || body(3) != "Shop" {
		t.Fatalf("the recipe through the protocol: %v %q", r.Result.IsError, body(3))
	}
	if !replies[4].Result.IsError || !strings.Contains(body(4), "deleting an account needs a person") || !strings.Contains(body(4), "delete-it") {
		t.Fatalf("a rule should stop the click inside a recipe, with its reason and the recipe's name: %q", body(4))
	}
	if !replies[5].Result.IsError || !strings.Contains(body(5), "example.com") {
		t.Fatalf("the network rule should stop a step inside a recipe: %q", body(5))
	}
	if !replies[6].Result.IsError || !strings.Contains(body(6), `"url" must be a string`) {
		t.Fatalf("a wrong parameter type should be explained: %q", body(6))
	}
	if replies[7].Result.IsError {
		t.Fatalf("the ordinary tools still work beside recipes: %q", body(7))
	}

	raw, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	for _, want := range []string{`"navigate"`, `"snapshot"`, `"click"`} {
		if !strings.Contains(log, want) {
			t.Errorf("the audit log lacks the step %s a recipe took:\n%s", want, log)
		}
	}
}

func TestAMissingRecipePathStopsServeAndNamesIt(t *testing.T) {
	dir := writeFiles(t, map[string]string{"config.toml": "[recipes]\npaths = [\"nothing-here.scm\"]\n"})
	conf := filepath.Join(dir, "config.toml")
	needBrowser(t)
	code, _, errs := do(t, "", "serve", "--config", conf)
	if code == 0 || !strings.Contains(errs, filepath.Join(dir, "nothing-here.scm")) {
		t.Fatalf("%d %q", code, errs)
	}
}
