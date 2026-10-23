package chrome

import (
	"context"
	"fmt"
	"strings"
)

// A snapshot is the page as text: the accessibility tree, trimmed to what a
// reader or an agent needs. The tree is the right source because it is what
// the browser already works out for screen readers. It names buttons by their
// labels, knows which text is a heading and which a link, and leaves out the
// wrapper elements that carry no meaning.
//
// Every element that can be acted on gets a reference such as e12. The
// reference is good until the next snapshot or navigation; asking for a
// stale one is an error that says so, and never lands a click on whatever
// moved into its place.

type axValue struct {
	Value any `json:"value"`
}

type axProperty struct {
	Name  string  `json:"name"`
	Value axValue `json:"value"`
}

type axNode struct {
	NodeID     string       `json:"nodeId"`
	Ignored    bool         `json:"ignored"`
	Role       axValue      `json:"role"`
	Name       axValue      `json:"name"`
	Value      axValue      `json:"value"`
	Properties []axProperty `json:"properties"`
	ChildIDs   []string     `json:"childIds"`
	BackendID  int64        `json:"backendDOMNodeId"`
}

func (n *axNode) role() string  { return str(n.Role.Value) }
func (n *axNode) name() string  { return strings.TrimSpace(str(n.Name.Value)) }
func (n *axNode) value() string { return str(n.Value.Value) }

func (n *axNode) prop(name string) (any, bool) {
	for _, p := range n.Properties {
		if p.Name == name {
			return p.Value.Value, true
		}
	}
	return nil, false
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprint(int64(t))
		}
		return fmt.Sprint(t)
	default:
		return fmt.Sprint(t)
	}
}

// interactive roles get a reference.
var interactive = map[string]bool{
	"link": true, "button": true, "textbox": true, "searchbox": true, "combobox": true,
	"checkbox": true, "radio": true, "switch": true, "slider": true, "spinbutton": true,
	"menuitem": true, "menuitemcheckbox": true, "menuitemradio": true, "tab": true,
	"option": true, "treeitem": true, "listbox": true, "textarea": true,
}

// namedByContent roles are described by their own text, so their text
// children are not repeated beneath them.
var namedByContent = map[string]bool{
	"link": true, "button": true, "heading": true, "tab": true, "menuitem": true,
	"menuitemcheckbox": true, "menuitemradio": true, "option": true, "treeitem": true,
	"checkbox": true, "radio": true, "switch": true,
}

// transparent roles add nothing of their own; their children are shown in their place.
var transparent = map[string]bool{
	"generic": true, "none": true, "presentation": true, "Section": true,
	"paragraph": true, "LineBreak": true, "InlineTextBox": true, "group": false,
}

// SnapshotOptions tune what a snapshot includes.
type SnapshotOptions struct {
	// MaxChars cuts the text off at about this many characters. Zero means 60000.
	MaxChars int
}

// Snapshot reads the page as text and refreshes the element references.
func (p *Page) Snapshot(ctx context.Context, o SnapshotOptions) (string, error) {
	var res struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := p.call(ctx, "Accessibility.getFullAXTree", nil, &res); err != nil {
		return "", err
	}
	info, _ := p.Info(ctx)

	byID := make(map[string]*axNode, len(res.Nodes))
	for i := range res.Nodes {
		byID[res.Nodes[i].NodeID] = &res.Nodes[i]
	}

	r := &renderer{byID: byID, refs: map[string]refInfo{}}
	if len(res.Nodes) > 0 {
		r.walk(&res.Nodes[0], 0, false)
	}

	p.mu.Lock()
	p.refs = r.refs
	p.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "Page: %s\nURL: %s\n\n", info.Title, info.URL)
	b.WriteString(r.out.String())

	max := o.MaxChars
	if max <= 0 {
		max = 60000
	}
	text := b.String()
	if len(text) > max {
		cut := strings.LastIndex(text[:max], "\n")
		if cut < 0 {
			cut = max
		}
		text = text[:cut] + fmt.Sprintf("\n[snapshot cut after %d characters; %d more]\n", cut, len(text)-cut)
	}
	return text, nil
}

// refInfo is what a reference stands for: the DOM node, and a short
// description such as `button "Pay now"` for rules and logs to read.
type refInfo struct {
	backend int64
	desc    string
}

type renderer struct {
	byID map[string]*axNode
	refs map[string]refInfo
	out  strings.Builder
	next int
}

func (r *renderer) children(n *axNode) []*axNode {
	out := make([]*axNode, 0, len(n.ChildIDs))
	for _, id := range n.ChildIDs {
		if c := r.byID[id]; c != nil {
			out = append(out, c)
		}
	}
	return out
}

func (r *renderer) line(depth int, format string, args ...any) {
	r.out.WriteString(strings.Repeat("  ", depth))
	fmt.Fprintf(&r.out, format, args...)
	r.out.WriteByte('\n')
}

// walk prints a node and its children. quiet is set beneath a node that is
// already named by its content, where text and images would only repeat it.
func (r *renderer) walk(n *axNode, depth int, quiet bool) {
	role := n.role()

	if n.Ignored || transparent[role] {
		for _, c := range r.children(n) {
			r.walk(c, depth, quiet)
		}
		return
	}

	switch role {
	case "StaticText", "text":
		if quiet {
			return
		}
		if t := n.name(); t != "" {
			r.line(depth, "- text: %q", t)
		}
		return
	case "img", "image":
		if quiet {
			return
		}
		if t := n.name(); t != "" {
			r.line(depth, "- img %q", t)
		}
		return
	case "RootWebArea", "WebArea":
		for _, c := range r.children(n) {
			r.walk(c, depth, quiet)
		}
		return
	}

	var attrs []string
	if interactive[role] && n.BackendID != 0 {
		r.next++
		ref := fmt.Sprintf("e%d", r.next)
		desc := role
		if name := n.name(); name != "" {
			desc += fmt.Sprintf(" %q", name)
		}
		r.refs[ref] = refInfo{backend: n.BackendID, desc: desc}
		attrs = append(attrs, "ref="+ref)
	}
	if v, ok := n.prop("level"); ok {
		attrs = append(attrs, "level="+str(v))
	}
	for _, name := range []string{"checked", "expanded", "selected", "pressed"} {
		if v, ok := n.prop(name); ok {
			attrs = append(attrs, name+"="+str(v))
		}
	}
	for _, name := range []string{"disabled", "required", "readonly", "invalid"} {
		if v, ok := n.prop(name); ok && str(v) != "false" {
			attrs = append(attrs, name)
		}
	}

	head := "- " + role
	if name := n.name(); name != "" {
		head += fmt.Sprintf(" %q", name)
	}
	if len(attrs) > 0 {
		head += " [" + strings.Join(attrs, "] [") + "]"
	}
	if v, ok := n.prop("url"); ok && role == "link" {
		if u := str(v); u != "" {
			head += " -> " + u
		}
	}
	if val := n.value(); val != "" && role != "link" {
		head += fmt.Sprintf(": %q", val)
	}

	kids := r.children(n)
	r.line(depth, "%s", head)
	childQuiet := quiet || namedByContent[role]
	for _, c := range kids {
		r.walk(c, depth+1, childQuiet)
	}
}

// node resolves a reference from the last snapshot.
func (p *Page) node(ref string) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	info, ok := p.refs[ref]
	if !ok {
		return 0, fmt.Errorf("there is no element %q on this page: take a new snapshot, because references end when the page changes", ref)
	}
	return info.backend, nil
}

// Describe says what a reference from the last snapshot stands for, such as
// `button "Pay now"`. It is empty for a reference that is no longer valid.
func (p *Page) Describe(ref string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refs[ref].desc
}

func (p *Page) forgetRefs() {
	p.mu.Lock()
	p.refs = map[string]refInfo{}
	p.mu.Unlock()
}
