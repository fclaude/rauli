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

// The same login as the whole-stack test, but the keychain is a program the
// person wrote, the way a password manager's command line would be. The value
// has to get from that program into the page without passing through anything
// the agent, the log or the audit file can read.
func TestTheWholeThingWorksWithACommandKeychain(t *testing.T) {
	needBrowser(t)
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh to write a keychain command with")
	}
	const password = "hunter2-from-a-command"

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Shop</title><h1>Shop</h1>
<form action="/welcome" method="post">
<label for="u">User</label><input id="u" name="u">
<label for="p">Password</label><input id="p" name="p" type="password">
<button>Sign in</button></form>`)
	})
	mux.HandleFunc("/welcome", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>Welcome</title><h1>Welcome %s</h1><p>password correct: %v</p>`, r.Form.Get("u"), r.Form.Get("p") == password)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	ranLog := filepath.Join(dir, "ran.log")
	prog := filepath.Join(dir, "get-secret")
	script := fmt.Sprintf(`#!/bin/sh
echo "$1 $2 $RAULI_ORIGIN" >> %q
echo "this goes to the person, never to an agent" >&2
case "$2" in
  user) echo alice ;;
  password) echo %s ;;
esac
`, ranLog, password)
	if err := os.WriteFile(prog, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	conf := filepath.Join(dir, "config.toml")
	audit := filepath.Join(dir, "audit.jsonl")
	text := fmt.Sprintf(`
[keychain]
backend = "command"
command = [%q]
timeout = "10s"

[[keychain.handles]]
name = "shop"
fields = ["user", "password"]
origins = [%q]

[[keychain.handles]]
name = "elsewhere"
fields = ["user", "password"]
origins = ["https://other.example"]

[rules.network]
allow = [%q]

[rules.audit]
file = "audit.jsonl"
`, prog, srv.URL, srv.URL+"/*")
	if err := os.WriteFile(conf, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	call := func(id int, name string, args map[string]any) string {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		return string(b)
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		call(2, "navigate", map[string]any{"url": srv.URL + "/"}),
		call(3, "fill_credential", map[string]any{"ref": "e1", "handle": "elsewhere", "field": "user", "snapshot": false}),
		call(4, "fill_credential", map[string]any{"ref": "e1", "handle": "shop", "field": "user", "snapshot": false}),
		call(5, "fill_credential", map[string]any{"ref": "e2", "handle": "shop", "field": "password", "snapshot": false}),
		call(6, "click", map[string]any{"ref": "e3"}),
	}, "\n") + "\n"

	code, out, errs := do(t, input, "serve", "--config", conf)
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
	body := func(id int) string {
		if c := replies[id].Result.Content; len(c) > 0 {
			return c[0].Text
		}
		return ""
	}

	if replies[2].Result.IsError {
		t.Fatalf("the allowed site did not load: %q", body(2))
	}
	if !replies[3].Result.IsError || !strings.Contains(body(3), "may not be used") {
		t.Fatalf("a login for another site should be refused here, got: %q", body(3))
	}
	if replies[4].Result.IsError || replies[5].Result.IsError {
		t.Fatalf("filling the login failed: %q / %q", body(4), body(5))
	}
	if r := replies[6]; r.Result.IsError || !strings.Contains(body(6), "Welcome alice") || !strings.Contains(body(6), "password correct: true") {
		t.Fatalf("signing in with the command's values did not work: %q", body(6))
	}

	// The program ran for the two fields it was allowed to give, and not at all
	// for the login that belongs to another site.
	b, err := os.ReadFile(ranLog)
	if err != nil {
		t.Fatal(err)
	}
	ran := string(b)
	if strings.Count(ran, "\n") != 2 || !strings.Contains(ran, "shop user "+srv.URL) || !strings.Contains(ran, "shop password "+srv.URL) || strings.Contains(ran, "elsewhere") {
		t.Fatalf("the program was run as:\n%s", ran)
	}

	raw, err := os.ReadFile(audit)
	if err != nil {
		t.Fatalf("no audit log was written: %v", err)
	}
	for name, stream := range map[string]string{"stdout": out, "stderr": errs, "audit log": string(raw)} {
		if strings.Contains(stream, password) {
			t.Errorf("%s holds the password", name)
		}
		if strings.Contains(stream, "this goes to the person") && name != "stderr" {
			t.Errorf("the program's own standard error reached the %s", name)
		}
	}
}
