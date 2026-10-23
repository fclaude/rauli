package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/rauli/internal/chrome"
	"github.com/fclaude/rauli/internal/tools"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func do(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	code = run(ctx, args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func needBrowser(t *testing.T) {
	t.Helper()
	if _, err := chrome.Find(); err != nil {
		t.Skip(err)
	}
}

func fixtureSite(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Command fixture</title><h1>Hello from the command</h1><p>Some text.</p><div style="height:2500px"></div>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHelpListsEveryCommandAndNeedsNoBrowser(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		code, out, _ := do(t, "", args...)
		if code != 0 {
			t.Errorf("%v exits %d", args, code)
		}
		for _, name := range []string{"doctor", "keychain", "serve", "shot", "snapshot", "tools", "version", "watch"} {
			if !strings.Contains(out, "  "+name+" ") {
				t.Errorf("%v does not list %s:\n%s", args, name, out)
			}
		}
	}
	// With no command at all, the help goes to standard error and the status is 2.
	if code, out, errs := do(t, ""); code != 2 || out != "" || !strings.Contains(errs, "Usage:") {
		t.Errorf("no arguments: code %d, stdout %q, stderr %q", code, out, errs)
	}
	code, _, errs := do(t, "", "nonsense")
	if code != 2 || !strings.Contains(errs, `no command "nonsense"`) {
		t.Errorf("unknown command: code %d, stderr %q", code, errs)
	}
	// "help serve" is "serve -h".
	code, _, errs = do(t, "", "help", "serve")
	if code != 0 || !strings.Contains(errs, "Usage: rauli serve") || !strings.Contains(errs, "-profile") {
		t.Errorf("help serve: code %d, stderr %q", code, errs)
	}
}

func TestVersion(t *testing.T) {
	code, out, _ := do(t, "", "version")
	if code != 0 || !strings.HasPrefix(out, "rauli ") {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, _, _ := do(t, "", "version", "extra"); code != 2 {
		t.Errorf("version with an argument should be a usage error, got %d", code)
	}
}

func TestToolsListsJSONAndMarkdown(t *testing.T) {
	code, out, _ := do(t, "", "tools")
	if code != 0 {
		t.Fatal(code)
	}
	for _, c := range tools.Catalogue() {
		if !strings.Contains(out, c.Name) {
			t.Errorf("the listing lacks %s", c.Name)
		}
	}

	code, out, _ = do(t, "", "tools", "--json")
	if code != 0 {
		t.Fatal(code)
	}
	var defs []struct {
		Name        string         `json:"name"`
		InputSchema map[string]any `json:"inputSchema"`
		ReadOnly    bool           `json:"readOnly"`
	}
	if err := json.Unmarshal([]byte(out), &defs); err != nil {
		t.Fatalf("--json is not JSON: %v", err)
	}
	if len(defs) != len(tools.Catalogue()) || defs[0].InputSchema["type"] != "object" {
		t.Errorf("--json: %d definitions, first %+v", len(defs), defs[0])
	}

	if code, _, errs := do(t, "", "tools", "--json", "--markdown"); code != 2 || !strings.Contains(errs, "choose --json or --markdown") {
		t.Errorf("both formats: %d %q", code, errs)
	}
}

// The reference the documentation publishes is pinned to a file, so it cannot
// drift from the tools. After changing a tool, run this test with -update.
func TestTheMarkdownReferenceMatchesItsGoldenFile(t *testing.T) {
	code, out, _ := do(t, "", "tools", "--markdown")
	if code != 0 {
		t.Fatal(code)
	}
	golden := filepath.Join("testdata", "tools.md")
	if *update {
		if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v: run the test with -update to create it", err)
	}
	if string(want) != out {
		t.Fatalf("the tool reference changed: run the test with -update and read the difference in git")
	}
	// Spot checks on the shape, so a golden file made from a broken generator is caught.
	for _, want := range []string{"# Tool reference", "## navigate", "| `url` | string | yes |", "This tool only reads.", "It takes no arguments.", "One of: list, new, select, close."} {
		if !strings.Contains(out, want) {
			t.Errorf("the reference lacks %q", want)
		}
	}
	if strings.Contains(out, "  \n") || strings.Contains(out, "\t") {
		t.Error("the reference has stray whitespace")
	}
}

func TestFlagsOverrideTheFileAndOnlyWhenGiven(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "[browser]\nproxy = \"http://from-file:1\"\nheaded = true\nwindow = \"1000x700\"\nprofile = \"p\"\ntimeout = \"10s\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	load := func(args ...string) *launchFlags {
		var lf launchFlags
		fs := flagSet("test", "rauli test", "", streams{err: &bytes.Buffer{}})
		lf.add(fs)
		if err := parse(fs, append([]string{"--config", path}, args...)); err != nil {
			t.Fatal(err)
		}
		cfg, err := lf.load(fs)
		if err != nil {
			t.Fatal(err)
		}
		lf.config = ""
		lf.proxy, lf.window = cfg.Browser.Proxy, cfg.Browser.Window
		lf.headed, lf.profile = cfg.Browser.Headed, cfg.Browser.Profile
		lf.timeout = time.Duration(cfg.Browser.Timeout)
		return &lf
	}

	// Nothing given: the file decides, and its relative profile follows the file.
	lf := load()
	if lf.proxy != "http://from-file:1" || !lf.headed || lf.window != "1000x700" || lf.timeout != 10*time.Second {
		t.Errorf("the file was not used: %+v", lf)
	}
	if lf.profile != filepath.Join(dir, "p") {
		t.Errorf("profile = %q", lf.profile)
	}

	// Flags given: they win, including turning a setting off.
	lf = load("--proxy", "http://from-flag:2", "--headed=false", "--window", "800x600", "--timeout", "5s", "--profile", "other")
	if lf.proxy != "http://from-flag:2" || lf.headed || lf.window != "800x600" || lf.timeout != 5*time.Second {
		t.Errorf("flags did not win: %+v", lf)
	}
	if !filepath.IsAbs(lf.profile) || filepath.Base(lf.profile) != "other" {
		t.Errorf("a profile named on the command line should be made absolute: %q", lf.profile)
	}

	// A bad flag value is a usage error that names the flag.
	var l2 launchFlags
	fs := flagSet("test", "rauli test", "", streams{err: &bytes.Buffer{}})
	l2.add(fs)
	parse(fs, []string{"--window", "wide"})
	if _, err := l2.load(fs); err == nil || !strings.Contains(err.Error(), "--window") {
		t.Errorf("a bad --window should be reported, got %v", err)
	}
}

func TestAMissingNamedConfigFileIsAnError(t *testing.T) {
	code, _, errs := do(t, "", "snapshot", "--config", filepath.Join(t.TempDir(), "missing.toml"), "http://127.0.0.1:1/")
	if code != 1 || !strings.Contains(errs, "missing.toml") {
		t.Errorf("code %d, stderr %q", code, errs)
	}
}

func TestPlaceholdersSayTheyAreNotWiredYet(t *testing.T) {
	for _, name := range []string{"keychain", "watch"} {
		code, _, errs := do(t, "", name)
		if code != 1 || !strings.Contains(errs, "not wired up yet") {
			t.Errorf("%s: code %d, stderr %q", name, code, errs)
		}
	}
}

func TestUsageErrorsExitWithTwo(t *testing.T) {
	for _, args := range [][]string{
		{"snapshot"}, {"snapshot", "a", "b"}, {"shot", "onlyone"}, {"serve", "extra"}, {"doctor", "extra"}, {"tools", "extra"}, {"snapshot", "--no-such-flag", "x"},
	} {
		if code, _, errs := do(t, "", args...); code != 2 {
			t.Errorf("%v: code %d, stderr %q", args, code, errs)
		}
	}
}

// The four tests below start a real headless browser each. They are the only
// ones that do, and they run one after the other.

func TestDoctorStartsTheBrowser(t *testing.T) {
	needBrowser(t)
	code, out, errs := do(t, "", "doctor")
	if code != 0 {
		t.Fatalf("doctor: %d\n%s\n%s", code, out, errs)
	}
	for _, want := range []string{"browser:", "version:", "user agent:", "devtools:    http://127.0.0.1:", "ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Headless") {
		t.Errorf("the user agent still says headless:\n%s", out)
	}
}

func TestSnapshotAndShotOfARealPage(t *testing.T) {
	needBrowser(t)
	srv := fixtureSite(t)

	code, out, errs := do(t, "", "snapshot", srv.URL)
	if code != 0 {
		t.Fatalf("snapshot: %d\n%s", code, errs)
	}
	for _, want := range []string{"Page: Command fixture", `heading "Hello from the command"`, "Some text."} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot lacks %q:\n%s", want, out)
		}
	}

	file := filepath.Join(t.TempDir(), "page.png")
	code, out, errs = do(t, "", "shot", "--full-page", srv.URL, file)
	if code != 0 {
		t.Fatalf("shot: %d\n%s", code, errs)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the file is not a PNG: %v", err)
	}
	if cfg.Height < 2500 {
		t.Errorf("--full-page gave a picture %d pixels tall; the page is over 2500", cfg.Height)
	}
	if !strings.Contains(out, "Saved "+file) {
		t.Errorf("shot output: %q", out)
	}
}

func TestServeAnswersTheProtocolAndWritesNothingElseToStandardOutput(t *testing.T) {
	needBrowser(t)
	srv := fixtureSite(t)
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"navigate","arguments":{"url":%q}}}`, srv.URL),
	}, "\n") + "\n"

	code, out, errs := do(t, in, "serve")
	if code != 0 {
		t.Fatalf("serve: %d\n%s", code, errs)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected three replies, got %d:\n%s", len(lines), out)
	}
	var replies []map[string]any
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("standard output holds a line that is not JSON: %q", l)
		}
		replies = append(replies, m)
	}
	info := replies[0]["result"].(map[string]any)["serverInfo"].(map[string]any)
	if info["name"] != "rauli" {
		t.Errorf("serverInfo %v", info)
	}
	if !strings.Contains(replies[0]["result"].(map[string]any)["instructions"].(string), "snapshot") {
		t.Errorf("the instructions do not mention snapshots")
	}
	if n := len(replies[1]["result"].(map[string]any)["tools"].([]any)); n != len(tools.Catalogue()) {
		t.Errorf("serve lists %d tools, the catalogue has %d", n, len(tools.Catalogue()))
	}
	text := replies[2]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Command fixture") {
		t.Errorf("navigate result: %s", text)
	}
}
