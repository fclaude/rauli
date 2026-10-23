package tools

import (
	"flag"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/reference.golden.md")

// The reference is what the documentation site prints for the Tools page. This
// pins it, so a change to a tool's description or arguments shows up as a diff
// in review. Run `go test ./internal/tools -run Reference -update` after
// changing a tool on purpose.
func TestReferenceMatchesTheGoldenFile(t *testing.T) {
	got := Markdown()
	if *update {
		if err := os.WriteFile("testdata/reference.golden.md", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile("testdata/reference.golden.md")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("the tool reference changed; run the test with -update if that was on purpose.\n--- got ---\n%s", got)
	}
}

func TestReferenceMentionsEveryTool(t *testing.T) {
	md := Markdown()
	for _, name := range Names() {
		if !strings.Contains(md, "### `"+name+"`") {
			t.Errorf("tool %s has no entry in the reference", name)
		}
	}
	if len(Names()) < 20 {
		t.Errorf("only %d tools; did builtin() lose some?", len(Names()))
	}
}
