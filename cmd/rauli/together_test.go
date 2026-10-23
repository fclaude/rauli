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

// The pieces were built apart: the tools, the rules, the keychain, the audit
// log and the protocol. This runs them as one, through the command a program
// would start. A login page, rules that let the agent reach only that site and
// forbid one button, and a keychain that holds the login; then a conversation
// that tries to do the allowed things and the forbidden ones.
func TestTheWholeThingHangsTogether(t *testing.T) {
	needBrowser(t)
	const password = "hunter2-for-the-test"

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Shop</title><h1>Shop</h1>
<form action="/welcome" method="post">
<label for="u">User</label><input id="u" name="u">
<label for="p">Password</label><input id="p" name="p" type="password">
<button>Sign in</button></form>
<button onclick="document.title='deleted'">Delete account</button>`)
	})
	mux.HandleFunc("/welcome", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		ok := r.Form.Get("p") == password
		fmt.Fprintf(w, `<!doctype html><title>Welcome</title><h1>Welcome %s</h1><p>password correct: %v</p>`, r.Form.Get("u"), ok)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	conf := filepath.Join(dir, "config.toml")
	audit := filepath.Join(dir, "audit.jsonl")
	text := fmt.Sprintf(`
[keychain]
backend = "file"
path = "keys"
passphrase_env = "RAULI_TEST_PASSPHRASE"

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
	t.Setenv("RAULI_TEST_PASSPHRASE", "a passphrase for the test")

	// The person stores the login. Its values go in over standard input, one
	// per line, and are never shown again.
	if code, out, errs := do(t, "alice\n"+password+"\n", "keychain", "--config", conf, "add", "--origin", srv.URL, "shop", "user", "password"); code != 0 {
		t.Fatalf("keychain add: %d %q %q", code, out, errs)
	}

	call := func(id int, name string, args map[string]any) string {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		return string(b)
	}
	script := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		call(2, "navigate", map[string]any{"url": srv.URL + "/"}),
		call(3, "navigate", map[string]any{"url": "https://example.com/"}),
		call(4, "fill_credential", map[string]any{"ref": "e1", "handle": "shop", "field": "user", "snapshot": false}),
		call(5, "fill_credential", map[string]any{"ref": "e2", "handle": "shop", "field": "password", "snapshot": false}),
		call(6, "click", map[string]any{"ref": "e4"}),
		call(7, "click", map[string]any{"ref": "e3"}),
	}, "\n") + "\n"

	code, out, errs := do(t, script, "serve", "--config", conf)
	if code != 0 {
		t.Fatalf("serve: %d %q", code, errs)
	}

	type reply struct {
		ID     int `json:"id"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	replies := map[int]reply{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var r reply
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("stdout carries something that is not a protocol message: %q", line)
		}
		replies[r.ID] = r
	}
	text2 := func(id int) string {
		r := replies[id]
		if len(r.Result.Content) == 0 {
			return ""
		}
		return r.Result.Content[0].Text
	}

	if r := replies[2]; r.Result.IsError || !strings.Contains(text2(2), `button "Delete account"`) {
		t.Fatalf("the allowed site did not load: %+v\n%s", r.Result.IsError, text2(2))
	}
	if !replies[3].Result.IsError || !strings.Contains(text2(3), "example.com") {
		t.Fatalf("a site outside the allowlist should be refused, got: %q", text2(3))
	}
	if replies[4].Result.IsError || replies[5].Result.IsError {
		t.Fatalf("filling the login failed: %q / %q", text2(4), text2(5))
	}
	if !replies[6].Result.IsError || !strings.Contains(text2(6), "deleting an account needs a person") {
		t.Fatalf("the forbidden button should be refused with the rule's reason, got: %q", text2(6))
	}
	if r := replies[7]; r.Result.IsError || !strings.Contains(text2(7), "Welcome alice") || !strings.Contains(text2(7), "password correct: true") {
		t.Fatalf("signing in with the keychain's login did not work: %q", text2(7))
	}

	raw, err := os.ReadFile(audit)
	if err != nil {
		t.Fatalf("no audit log was written: %v", err)
	}
	log := string(raw)
	for _, want := range []string{`"navigate"`, `"fill_credential"`, `"click"`} {
		if !strings.Contains(log, want) {
			t.Errorf("the audit log lacks %s:\n%s", want, log)
		}
	}
	for name, stream := range map[string]string{"stdout": out, "stderr": errs, "audit log": log} {
		if strings.Contains(stream, password) {
			t.Errorf("%s holds the password", name)
		}
	}
	if info, err := os.Stat(audit); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the audit log should be private to the person: %v %v", info, err)
	}
}
