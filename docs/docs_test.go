// Package docs holds the documentation pages. Its tests read them, so that an
// example a page shows is one the program accepts and a claim about the rules
// is one the rules keep.
package docs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/fclaude/rauli/internal/chrome"
	"github.com/fclaude/rauli/internal/config"
	"github.com/fclaude/rauli/internal/rules"
	"github.com/fclaude/rauli/internal/tools"
	"github.com/fclaude/rauli/internal/urlmatch"
)

func pages(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("no pages found: %v", err)
	}
	out := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[f] = string(b)
	}
	return out
}

// fenced returns the bodies of the code blocks of one language.
func fenced(page, lang string) []string {
	var out []string
	for _, m := range regexp.MustCompile("(?s)```"+lang+"\n(.*?)```").FindAllStringSubmatch(page, -1) {
		out = append(out, m[1])
	}
	return out
}

// Every TOML example is a configuration file the program reads, so a key
// that was renamed breaks the page and not a reader.
func TestEveryTOMLExampleIsAConfigurationFile(t *testing.T) {
	seen := 0
	for name, page := range pages(t) {
		for i, block := range fenced(page, "toml") {
			seen++
			if _, err := config.Parse([]byte(block), name); err != nil {
				t.Errorf("%s: TOML example %d is not a configuration file: %v\n%s", name, i+1, err, block)
			}
		}
	}
	if seen < 8 {
		t.Errorf("expected the pages to hold many TOML examples, found %d", seen)
	}
}

// The table of patterns on the rules page is checked row by row.
func TestThePatternTableIsRight(t *testing.T) {
	page := pages(t)["rules.md"]
	row := regexp.MustCompile("(?m)^\\| `([^`]+)` \\| (.+?) \\| (.+?) \\|$")
	code := regexp.MustCompile("`([^`]+)`")
	rows := 0
	for _, m := range row.FindAllStringSubmatch(page, -1) {
		if !strings.Contains(m[1], "://") {
			continue
		}
		p, err := urlmatch.Parse(m[1], false)
		if err != nil {
			t.Errorf("%q does not parse: %v", m[1], err)
			continue
		}
		rows++
		for _, a := range code.FindAllStringSubmatch(m[2], -1) {
			if !p.Match(a[1]) {
				t.Errorf("the page says %q matches %q, and it does not", m[1], a[1])
			}
		}
		for _, a := range code.FindAllStringSubmatch(m[3], -1) {
			if p.Match(a[1]) {
				t.Errorf("the page says %q does not match %q, and it does", m[1], a[1])
			}
		}
	}
	if rows != 5 {
		t.Errorf("expected 5 rows in the table of patterns, read %d", rows)
	}
}

type answer bool

func (a answer) Approve(context.Context, tools.Action, string) (bool, error) { return bool(a), nil }

// recipe is the configuration of the rules page's block that holds the key.
func recipe(t *testing.T, key string) rules.Config {
	t.Helper()
	for _, block := range fenced(pages(t)["rules.md"], "toml") {
		if strings.Contains(block, key) {
			c, err := config.Parse([]byte(block), "rules.md")
			if err != nil {
				t.Fatal(err)
			}
			return c.Rules
		}
	}
	t.Fatalf("the rules page has no example with %q", key)
	return rules.Config{}
}

func TestTheRecipesDoWhatTheyPromise(t *testing.T) {
	ctx := context.Background()
	rulesPage := pages(t)["rules.md"]
	said := func(err error) {
		t.Helper()
		if err == nil {
			t.Fatal("expected a refusal")
		}
		if !strings.Contains(rulesPage, err.Error()) {
			t.Errorf("the rules page does not show the refusal %q", err)
		}
	}

	t.Run("one site", func(t *testing.T) {
		e, err := rules.New(recipe(t, "network"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.CheckURL(ctx, chrome.Request{URL: "http://127.0.0.1:8765/", Navigation: true}); err != nil {
			t.Errorf("the allowed site was refused: %v", err)
		}
		said(e.CheckURL(ctx, chrome.Request{URL: "http://127.0.0.1:8766/", Navigation: true}))
	})

	t.Run("payments ask", func(t *testing.T) {
		c := recipe(t, `"*pay*"`)
		for _, yes := range []bool{false, true} {
			e, err := rules.New(c, answer(yes))
			if err != nil {
				t.Fatal(err)
			}
			pay := tools.Action{Tool: "click", Kind: "click", URL: "http://127.0.0.1:8765/pay.html", Element: `button "Pay now"`}
			err = e.CheckAction(ctx, pay)
			if yes && err != nil {
				t.Errorf("an approved payment was refused: %v", err)
			}
			if !yes {
				said(err)
			}
			other := pay
			other.Element = `button "Continue shopping"`
			if err := e.CheckAction(ctx, other); err != nil {
				t.Errorf("a click that is not a payment was stopped: %v", err)
			}
		}
	})

	t.Run("uploads are refused", func(t *testing.T) {
		e, err := rules.New(recipe(t, `kind = "upload"`), answer(true))
		if err != nil {
			t.Fatal(err)
		}
		said(e.CheckAction(ctx, tools.Action{Tool: "upload", Kind: "upload", URL: "http://127.0.0.1:8765/upload.html", Detail: "/tmp/note.txt"}))
	})

	t.Run("a fresh install", func(t *testing.T) {
		e, err := rules.New(rules.Config{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range []string{"https://example.com/", "http://localhost:3000/", "http://192.168.1.10/"} {
			if err := e.CheckURL(ctx, chrome.Request{URL: u, Navigation: true}); err != nil {
				t.Errorf("%s should be reachable with no rules: %v", u, err)
			}
		}
		if err := e.CheckURL(ctx, chrome.Request{URL: "http://169.254.169.254/latest/meta-data/", Navigation: true}); err == nil {
			t.Error("a link-local address should never be reachable")
		}
		for _, kind := range []string{"eval", "upload"} {
			if err := e.CheckAction(ctx, tools.Action{Tool: kind, Kind: kind, URL: "https://example.com/"}); err == nil {
				t.Errorf("%s should ask, and with no way to ask it is refused", kind)
			} else if !strings.Contains(err.Error(), "no way to ask one is set up") {
				t.Errorf("%s: the refusal does not say how to fix it: %v", kind, err)
			} else if kind == "upload" {
				said(err)
			}
		}
		for _, kind := range []string{"click", "type", "navigate"} {
			if err := e.CheckAction(ctx, tools.Action{Tool: kind, Kind: kind, URL: "https://example.com/"}); err != nil {
				t.Errorf("%s should be allowed with no rules: %v", kind, err)
			}
		}
	})

	t.Run("block_private", func(t *testing.T) {
		e, err := rules.New(rules.Config{Network: rules.NetworkConfig{BlockPrivate: true}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range []string{"http://127.0.0.1/", "http://192.168.1.10/", "http://printer.local/", "http://app.internal/", "http://dev.localhost/"} {
			if err := e.CheckURL(ctx, chrome.Request{URL: u, Navigation: true}); err == nil {
				t.Errorf("%s should be refused with block_private", u)
			}
		}
		if err := e.CheckURL(ctx, chrome.Request{URL: "https://example.com/", Navigation: true}); err != nil {
			t.Errorf("a public address was refused: %v", err)
		}
	})
}

// A link to a heading in another page has to find the heading.
func TestLinksToHeadingsFindThem(t *testing.T) {
	all := pages(t)
	slug := func(h string) string {
		h = strings.ToLower(strings.TrimSpace(h))
		h = regexp.MustCompile(`[^a-z0-9 -]`).ReplaceAllString(h, "")
		return strings.ReplaceAll(h, " ", "-")
	}
	headings := map[string]map[string]bool{}
	for name, page := range all {
		headings[name] = map[string]bool{}
		for _, m := range regexp.MustCompile(`(?m)^#{2,4} (.+)$`).FindAllStringSubmatch(page, -1) {
			headings[name][slug(strings.ReplaceAll(m[1], "`", ""))] = true
		}
	}
	link := regexp.MustCompile(`\]\(([a-z-]+\.md)#([a-z0-9-]+)\)`)
	for name, page := range all {
		for _, m := range link.FindAllStringSubmatch(page, -1) {
			target, ok := headings[m[1]]
			if !ok {
				t.Errorf("%s links to %s, which is not a page", name, m[1])
			} else if !target[m[2]] {
				t.Errorf("%s links to %s#%s, and that page has no such heading", name, m[1], m[2])
			}
		}
	}
}

// The example the getting-started page includes is a program that builds.
func TestTheExampleProgramBuilds(t *testing.T) {
	if !strings.Contains(pages(t)["getting-started.md"], "docs/examples/readpage/main.go") {
		t.Fatal("the getting-started page does not include the example")
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "readpage"), "./docs/examples/readpage")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the example does not build: %v\n%s", err, out)
	}
}

// settings lists the keys a configuration file can hold, as the tables on the
// configuration page write them: the section, then the key below it.
func settings(t reflect.Type, prefix string, out map[string]bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		key := prefix + tag
		ft := f.Type
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "time" && ft.Name() != "Duration" {
			settings(ft, key+".", out)
			continue
		}
		out[key] = true
	}
}

// Every setting has a row on the configuration page, and every row is a setting.
func TestTheConfigurationPageListsExactlyTheSettings(t *testing.T) {
	want := map[string]bool{}
	settings(reflect.TypeOf(config.Config{}), "", want)

	got := map[string]bool{}
	section := ""
	for _, line := range strings.Split(pages(t)["configuration.md"], "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			section = h
			continue
		}
		m := regexp.MustCompile("^\\| `([a-z_.]+)` \\|").FindStringSubmatch(line)
		if m == nil || section == "" {
			continue
		}
		key := section + "." + m[1]
		if section == "rules" {
			key = "rules." + m[1]
		}
		got[key] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("the configuration page has no row for %s", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("the configuration page documents %s, which is not a setting", k)
		}
	}
}
