package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The command page is written by hand, so these tests hold it to the program:
// a command or an option that the program has and the page lacks is a failure.

func readDoc(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../docs/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// helpOf is what `rauli <args> -h` prints, from whichever stream it chose.
func helpOf(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errs := do(t, "", append(args, "-h")...)
	if code != 0 {
		t.Fatalf("rauli %v -h exits %d: %s", args, code, errs)
	}
	return out + errs
}

func TestTheCommandPageNamesEveryCommand(t *testing.T) {
	page := readDoc(t, "command.md")
	_, help, _ := do(t, "", "help")
	listed := regexp.MustCompile(`(?m)^  ([a-z]+)  `).FindAllStringSubmatch(help, -1)
	if len(listed) < 8 {
		t.Fatalf("expected the help to list the commands, got %q", help)
	}
	for _, m := range listed {
		name := m[1]
		if !strings.Contains(page, "\n## "+name+"\n") {
			t.Errorf("docs/command.md has no section for the command %q", name)
		}
		if !strings.Contains(page, "| `"+name+"` |") {
			t.Errorf("docs/command.md has no row for %q in its table of commands", name)
		}
	}
}

func TestTheCommandPageNamesEveryOption(t *testing.T) {
	page := readDoc(t, "command.md")
	flagLine := regexp.MustCompile(`(?m)^  -([a-z-]+)`)
	for _, command := range []string{"doctor", "snapshot", "shot", "serve", "watch", "tools"} {
		for _, m := range flagLine.FindAllStringSubmatch(helpOf(t, command), -1) {
			if !strings.Contains(page, "-"+m[1]) {
				t.Errorf("docs/command.md does not mention the option -%s of %s", m[1], command)
			}
		}
	}
}

func TestTheKeychainPageNamesEverySubcommand(t *testing.T) {
	page := readDoc(t, "keychain.md")
	help := helpOf(t, "keychain")
	for _, m := range regexp.MustCompile(`(?m)^  ([a-z]+) `).FindAllStringSubmatch(help, -1) {
		if !strings.Contains(page, "rauli keychain") || !strings.Contains(page, m[1]) {
			t.Errorf("docs/keychain.md does not cover %q", m[1])
		}
		if !strings.Contains(readDoc(t, "command.md"), m[1]) {
			t.Errorf("docs/command.md does not list the keychain subcommand %q", m[1])
		}
	}
	for _, line := range []string{"rauli keychain test shop", "rauli keychain remove shop", "keychain list", "keychain show shop"} {
		if !strings.Contains(page, line) {
			t.Errorf("docs/keychain.md lacks the example %q", line)
		}
	}
}

// The JSON a program needs to start the server has to be JSON.
func TestTheServerConfigurationSnippetsAreJSON(t *testing.T) {
	for _, name := range []string{"command.md", "getting-started.md"} {
		page := readDoc(t, name)
		blocks := regexp.MustCompile("(?s)```json\n(.*?)```").FindAllStringSubmatch(page, -1)
		if len(blocks) == 0 {
			t.Fatalf("docs/%s has no JSON example", name)
		}
		for _, b := range blocks {
			var v struct {
				Servers map[string]struct {
					Command string   `json:"command"`
					Args    []string `json:"args"`
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal([]byte(b[1]), &v); err != nil {
				t.Errorf("docs/%s: the example is not JSON: %v", name, err)
				continue
			}
			s := v.Servers["browser"]
			if s.Command != "rauli" || len(s.Args) == 0 || s.Args[0] != "serve" {
				t.Errorf("docs/%s: the example does not start rauli serve: %+v", name, s)
			}
		}
	}
}
