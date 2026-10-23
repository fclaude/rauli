package tools

import (
	"fmt"
	"sort"
	"strings"
)

// Markdown renders the reference for every built-in tool: a heading, what the
// tool does, and a table of its arguments. It is the only place the reference is
// written, so the documentation cannot say something the schemas do not.
func Markdown() string {
	list := builtin()
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	var b strings.Builder
	for i, t := range list {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "### `%s`\n\n%s\n\n", t.Name, t.Description)
		if t.ReadOnly {
			b.WriteString("It only looks: it changes nothing in the page, so a policy can let it through where it would stop an action.\n\n")
		}
		b.WriteString(argumentTable(t.Schema))
	}
	return b.String()
}

func argumentTable(schema map[string]any) string {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return "It takes no arguments.\n"
	}
	required := map[string]bool{}
	for _, r := range requiredNames(schema) {
		required[r] = true
	}
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	// Required arguments first, then the rest, each group in name order.
	sort.Slice(names, func(i, j int) bool {
		if required[names[i]] != required[names[j]] {
			return required[names[i]]
		}
		return names[i] < names[j]
	})

	var b strings.Builder
	// Three columns fit a phone without sideways scrolling; "required" goes
	// with the type, where it is read together with it.
	b.WriteString("| Argument | Type | What it is |\n| --- | --- | --- |\n")
	for _, n := range names {
		p, _ := props[n].(map[string]any)
		desc, _ := p["description"].(string)
		if enum, ok := p["enum"].([]string); ok {
			desc += " One of: " + strings.Join(enum, ", ") + "."
		}
		typ := typeName(p)
		if required[n] {
			typ += ", required"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", n, typ, strings.ReplaceAll(desc, "|", "\\|"))
	}
	return b.String()
}

func typeName(p map[string]any) string {
	t, _ := p["type"].(string)
	if t == "array" {
		if items, ok := p["items"].(map[string]any); ok {
			if it, _ := items["type"].(string); it != "" {
				return "list of " + it
			}
		}
	}
	return t
}

func requiredNames(schema map[string]any) []string {
	switch r := schema["required"].(type) {
	case []string:
		return r
	case []any:
		out := make([]string, 0, len(r))
		for _, v := range r {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Names lists the built-in tools in name order.
func Names() []string {
	list := builtin()
	names := make([]string, len(list))
	for i, t := range list {
		names[i] = t.Name
	}
	sort.Strings(names)
	return names
}
