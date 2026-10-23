package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fclaude/rauli/internal/tools"
)

func init() {
	register("tools", "list the tools a program can use, as text, JSON or a Markdown reference", toolsCommand)
}

func toolsCommand(ctx context.Context, args []string, s streams) error {
	var asJSON, asMarkdown bool
	fs := flagSet("tools", "rauli tools [--json | --markdown]",
		`Lists the tools. It does not start a browser. With no option it prints one line
for each tool. --json prints the full definitions, with their argument schemas.
--markdown prints the tool reference that the documentation publishes, so the
reference always matches what the program does.`, s)
	fs.BoolVar(&asJSON, "json", false, "print the definitions as JSON")
	fs.BoolVar(&asMarkdown, "markdown", false, "print the reference as Markdown")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("tools takes no arguments, but got %q", fs.Arg(0))
	}
	if asJSON && asMarkdown {
		return usagef("choose --json or --markdown, because they print different things")
	}
	list := tools.Catalogue()
	switch {
	case asJSON:
		type def struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
			ReadOnly    bool           `json:"readOnly"`
		}
		defs := make([]def, 0, len(list))
		for _, t := range list {
			defs = append(defs, def{t.Name, t.Description, t.Schema, t.ReadOnly})
		}
		enc := json.NewEncoder(s.out)
		enc.SetIndent("", "  ")
		return enc.Encode(defs)
	case asMarkdown:
		fmt.Fprint(s.out, toolsMarkdown(list))
	default:
		width := 0
		for _, t := range list {
			width = max(width, len(t.Name))
		}
		for _, t := range list {
			fmt.Fprintf(s.out, "%-*s  %s\n", width, t.Name, firstSentence(t.Description))
		}
	}
	return nil
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	return s
}

// toolsMarkdown writes the tool reference. It is deterministic: tools come in
// name order and each tool's arguments in name order, so a test can pin the
// output to a file and the published reference cannot drift from the code.
func toolsMarkdown(list []tools.Tool) string {
	var b strings.Builder
	b.WriteString("# Tool reference\n\n")
	b.WriteString("Every tool takes a JSON object. An argument marked required has to be there. An argument a tool does not list is refused, so a misspelt name is reported and never ignored.\n")
	for _, t := range list {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", t.Name, t.Description)
		if t.ReadOnly {
			b.WriteString("\nThis tool only reads. It changes nothing on the page.\n")
		}
		props, _ := t.Schema["properties"].(map[string]any)
		if len(props) == 0 {
			b.WriteString("\nIt takes no arguments.\n")
			continue
		}
		required := map[string]bool{}
		for _, r := range stringList(t.Schema["required"]) {
			required[r] = true
		}
		names := make([]string, 0, len(props))
		for n := range props {
			names = append(names, n)
		}
		sort.Strings(names)
		b.WriteString("\n| Argument | Type | Required | What it does |\n| --- | --- | --- | --- |\n")
		for _, n := range names {
			p, _ := props[n].(map[string]any)
			req := "no"
			if required[n] {
				req = "yes"
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", n, typeName(p), req, cell(p))
		}
	}
	return b.String()
}

func stringList(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func typeName(p map[string]any) string {
	t, _ := p["type"].(string)
	if t == "array" {
		if items, ok := p["items"].(map[string]any); ok {
			if it, _ := items["type"].(string); it != "" {
				return "array of " + it
			}
		}
	}
	return t
}

// cell is a property's description for a table cell, with its choices if it has any.
func cell(p map[string]any) string {
	d, _ := p["description"].(string)
	if choices := stringList(p["enum"]); len(choices) > 0 {
		d += " One of: " + strings.Join(choices, ", ") + "."
	}
	return strings.ReplaceAll(strings.TrimSpace(d), "|", `\|`)
}
