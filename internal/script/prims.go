package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/fclaude/brote/scheme"

	"github.com/fclaude/rauli/internal/tools"
)

// prelude is the friendly layer: each helper is one call of the generic
// browser primitive, written in Scheme so that what it does can be read. The
// helpers that change the page ask for no snapshot, which keeps a recipe's
// replies small; (snapshot) reads the page when the recipe needs it.
const prelude = `
(define (navigate url) (browser "navigate" (list (cons "url" url) (cons "snapshot" #f))))
(define (snapshot) (browser "snapshot" '()))
(define (text) (browser "text" '()))
(define (click ref) (browser "click" (list (cons "ref" ref) (cons "snapshot" #f))))
(define (type-text ref str) (browser "type" (list (cons "ref" ref) (cons "text" str) (cons "snapshot" #f))))
(define (type-text-and-submit ref str) (browser "type" (list (cons "ref" ref) (cons "text" str) (cons "submit" #t) (cons "snapshot" #f))))
(define (press key) (browser "press" (list (cons "key" key) (cons "snapshot" #f))))
(define (screenshot) (browser "screenshot" '()))
(define (wait-for-text str) (browser "wait" (list (cons "text" str) (cons "snapshot" #f))))
(define (pause seconds) (browser "wait" (list (cons "seconds" seconds) (cons "snapshot" #f))))
`

var (
	errNotRunning = errors.New("this only works while a recipe runs; define the recipe with define-recipe and let a program call it")
	errNoBrowser  = errors.New("no browser is attached to this runtime")
)

// sink receives what a script prints. There is one interpreter and one run at
// a time, so one capture is current at a time.
type sink struct {
	mu sync.Mutex
	c  *capture
}

func (s *sink) set(c *capture) {
	s.mu.Lock()
	s.c = c
	s.mu.Unlock()
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	c := s.c
	s.mu.Unlock()
	if c != nil {
		c.write(p)
	}
	return len(p), nil
}

// capture keeps what a run printed, up to a limit. A script that prints without
// end must not be able to grow memory without end.
type capture struct {
	mu  sync.Mutex
	b   strings.Builder
	max int
	cut bool
}

func newCapture(max int) *capture { return &capture{max: max} }

func (c *capture) write(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := c.max*4 - c.b.Len() // runes are up to four bytes
	if room <= 0 {
		c.cut = true
		return
	}
	if len(p) > room {
		p = p[:room]
		c.cut = true
	}
	c.b.Write(p)
}

func (c *capture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.String()
}

func (r *Runtime) primitives() []*scheme.Prim {
	return []*scheme.Prim{
		{
			Name: "define-recipe",
			Meta: scheme.Meta{
				Doc: `Define a recipe: a tool an agent can call, made from the procedure PROC. PARAMS is a list of (name type doc) or (name type doc default), with type "string", "number" or "boolean"; a parameter with a default may be left out. PROC takes one argument per parameter, in order. Only while the recipe files load.`,
				Params: []scheme.Param{
					{Name: "name", Type: "string", Doc: "lowercase letters, digits, - and _, starting with a letter, up to 40 characters"},
					{Name: "description", Type: "string", Doc: "what the recipe does, as an agent should read it"},
					{Name: "params", Type: "list", Doc: "the parameters"},
					{Name: "proc", Type: "any", Doc: "the procedure to run"},
				},
				Origin: "builtin",
			},
			Handler: r.defineRecipe,
		},
		{
			Name: "browser",
			Meta: scheme.Meta{
				Doc: `Call a browser tool by name, as an agent would, and return its result text. ARGS is a list of ("name" . value) pairs; a value is a string, a number, a boolean or a list of those. Every rule, approval and audit entry that applies to the tool applies here.`,
				Params: []scheme.Param{
					{Name: "tool", Type: "string", Doc: "the tool, such as navigate or click"},
					{Name: "args", Type: "alist", Doc: "its arguments", Optional: true},
				},
				Origin: "builtin",
			},
			Handler: r.browserCall,
		},
		{
			Name: "recipe",
			Meta: scheme.Meta{
				Doc: `Run another recipe by name and return its result as text. ARGS is a list of ("name" . value) pairs. Recipes may nest only so deep.`,
				Params: []scheme.Param{
					{Name: "name", Type: "string", Doc: "the recipe"},
					{Name: "args", Type: "alist", Doc: "its arguments", Optional: true},
				},
				Origin: "builtin",
			},
			Handler: r.nestedRecipe,
		},
		{
			Name: "find-ref",
			Meta: scheme.Meta{
				Doc: `Find the reference of the first element in SNAPSHOT whose line contains TEXT, such as button "Sign in", or #f if there is none.`,
				Params: []scheme.Param{
					{Name: "snapshot", Type: "string", Doc: "the text a snapshot returned"},
					{Name: "text", Type: "string", Doc: "what the element's line contains"},
				},
				Origin: "builtin",
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				snap, err := c.String(0)
				if err != nil {
					return scheme.Value{}, err
				}
				needle, err := c.String(1)
				if err != nil {
					return scheme.Value{}, err
				}
				for _, line := range strings.Split(snap, "\n") {
					if !strings.Contains(line, needle) {
						continue
					}
					if i := strings.Index(line, "[ref="); i >= 0 {
						if j := strings.Index(line[i:], "]"); j > 0 {
							return scheme.Str(line[i+5 : i+j]), nil
						}
					}
				}
				return scheme.Bool(false), nil
			},
		},
		{
			Name: "page-url",
			Meta: scheme.Meta{Doc: "The address of the current page.", Origin: "builtin"},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				_, url, err := r.info(c.Ctx)
				return scheme.Str(url), err
			},
		},
		{
			Name: "page-title",
			Meta: scheme.Meta{Doc: "The title of the current page.", Origin: "builtin"},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				title, _, err := r.info(c.Ctx)
				return scheme.Str(title), err
			},
		},
	}
}

func (r *Runtime) info(ctx context.Context) (string, string, error) {
	if current(ctx) == nil {
		return "", "", errNotRunning
	}
	if r.backend == nil {
		return "", "", errNoBrowser
	}
	return r.backend.Info(ctx)
}

func (r *Runtime) defineRecipe(c *scheme.Call) (scheme.Value, error) {
	r.state.Lock()
	loading, source := r.loading, r.source
	r.state.Unlock()
	if !loading {
		return scheme.Value{}, errors.New("recipes can only be defined while the recipe files load")
	}
	name, err := c.String(0)
	if err != nil {
		return scheme.Value{}, err
	}
	if !recipeName.MatchString(name) {
		return scheme.Value{}, fmt.Errorf("%q is not a recipe name: use lowercase letters, digits, - and _, starting with a letter, up to 40 characters", name)
	}
	desc, err := c.String(1)
	if err != nil {
		return scheme.Value{}, err
	}
	if strings.TrimSpace(desc) == "" {
		return scheme.Value{}, fmt.Errorf("the recipe %q needs a description: an agent chooses tools by reading it", name)
	}
	items, err := c.List(2)
	if err != nil {
		return scheme.Value{}, err
	}
	proc, err := c.Proc(3)
	if err != nil {
		return scheme.Value{}, err
	}

	rc := &Recipe{Name: name, Description: desc, Source: source, proc: proc}
	seen := map[string]bool{}
	for i, item := range items {
		p, err := parseParam(item)
		if err != nil {
			return scheme.Value{}, fmt.Errorf("parameter %d of %q: %w", i+1, name, err)
		}
		if seen[p.Name] {
			return scheme.Value{}, fmt.Errorf("the recipe %q has two parameters called %q", name, p.Name)
		}
		seen[p.Name] = true
		rc.Params = append(rc.Params, p)
	}

	r.state.Lock()
	defer r.state.Unlock()
	if prev, dup := r.byName[name]; dup {
		return scheme.Value{}, fmt.Errorf("there is already a recipe called %q, defined in %s", name, prev.Source)
	}
	r.byName[name] = rc
	r.recipes = append(r.recipes, rc)
	return scheme.Void, nil
}

// parseParam reads (name type doc) or (name type doc default).
func parseParam(v scheme.Value) (Param, error) {
	parts, proper := v.Slice()
	if v.Kind != scheme.KPair || !proper || len(parts) < 3 || len(parts) > 4 {
		return Param{}, errors.New(`write it as (name type doc) or (name type doc default)`)
	}
	for _, s := range parts[:3] {
		if s.Kind != scheme.KString && s.Kind != scheme.KSymbol {
			return Param{}, errors.New("the name, type and description must be strings")
		}
	}
	p := Param{Name: parts[0].S, Type: parts[1].S, Doc: parts[2].S}
	if !recipeName.MatchString(p.Name) {
		return Param{}, fmt.Errorf("%q is not a parameter name: use lowercase letters, digits, - and _, starting with a letter", p.Name)
	}
	ok := false
	for _, t := range ParamTypes {
		ok = ok || t == p.Type
	}
	if !ok {
		return Param{}, fmt.Errorf("the type %q is not one of %s", p.Type, strings.Join(ParamTypes, ", "))
	}
	if len(parts) == 4 {
		d := parts[3]
		want := map[string][]scheme.Kind{
			"string":  {scheme.KString},
			"boolean": {scheme.KBool},
			"number":  {scheme.KInt, scheme.KFloat},
		}[p.Type]
		fits := false
		for _, k := range want {
			fits = fits || d.Kind == k
		}
		if !fits {
			return Param{}, fmt.Errorf("the default of %q must be a %s", p.Name, p.Type)
		}
		p.Default, p.HasDefault = d, true
	}
	return p, nil
}

// valueToAny turns a Scheme value into what encoding/json writes: a string, a
// number, a boolean, or a list of those.
func valueToAny(v scheme.Value) any {
	switch v.Kind {
	case scheme.KString, scheme.KSymbol:
		return v.S
	case scheme.KInt:
		return v.I
	case scheme.KFloat:
		return v.F
	case scheme.KBool:
		return v.I != 0
	case scheme.KNil:
		return []any{}
	case scheme.KPair:
		elems, _ := v.Slice()
		out := make([]any, 0, len(elems))
		for _, e := range elems {
			out = append(out, valueToAny(e))
		}
		return out
	}
	return scheme.Display(v)
}

// alist reads a list of ("name" . value) pairs. A pair whose second part is a
// list holds a list: ("paths" "a" "b") is the name paths with the list a, b.
func alist(v scheme.Value) (map[string]any, error) {
	out := map[string]any{}
	if v.Kind == scheme.KNil {
		return out, nil
	}
	pairs, proper := v.Slice()
	if !proper {
		return nil, errors.New(`the arguments must be a list of ("name" . value) pairs`)
	}
	for _, e := range pairs {
		if e.Kind != scheme.KPair || (e.P.Car.Kind != scheme.KString && e.P.Car.Kind != scheme.KSymbol) {
			return nil, errors.New(`the arguments must be a list of ("name" . value) pairs`)
		}
		out[e.P.Car.S] = valueToAny(e.P.Cdr)
	}
	return out, nil
}

func (r *Runtime) browserCall(c *scheme.Call) (scheme.Value, error) {
	st := current(c.Ctx)
	if st == nil {
		return scheme.Value{}, errNotRunning
	}
	tool, err := c.String(0)
	if err != nil {
		return scheme.Value{}, err
	}
	args := map[string]any{}
	if len(c.Args) > 1 {
		if args, err = alist(c.Args[1]); err != nil {
			return scheme.Value{}, err
		}
	}
	res, err := r.callTool(c.Ctx, st, tool, args)
	if err != nil {
		return scheme.Value{}, err
	}
	return scheme.Str(res.Text), nil
}

// callTool is the one way a recipe reaches the browser: the same call an agent
// makes, so the rules, the approvals and the audit log see every step.
func (r *Runtime) callTool(ctx context.Context, st *runState, tool string, args map[string]any) (tools.Result, error) {
	if r.backend == nil {
		return tools.Result{}, errNoBrowser
	}
	if strings.HasPrefix(tool, ToolPrefix) {
		return tools.Result{}, fmt.Errorf("%s is a recipe; call it with (recipe %q ...)", tool, strings.TrimPrefix(tool, ToolPrefix))
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return tools.Result{}, err
	}
	st.steps++
	res, err := r.backend.Call(ctx, tool, raw)
	if err != nil {
		return tools.Result{}, fmt.Errorf("%s: %w", tool, err)
	}
	for _, img := range res.Images {
		if len(st.images) < maxImages {
			st.images = append(st.images, img)
		}
	}
	return res, nil
}

func (r *Runtime) nestedRecipe(c *scheme.Call) (scheme.Value, error) {
	st := current(c.Ctx)
	if st == nil {
		return scheme.Value{}, errNotRunning
	}
	name, err := c.String(0)
	if err != nil {
		return scheme.Value{}, err
	}
	rc := r.Lookup(name)
	if rc == nil {
		return scheme.Value{}, fmt.Errorf("there is no recipe called %q", name)
	}
	if st.depth >= r.opts.maxDepth() {
		return scheme.Value{}, fmt.Errorf("recipes are nested too deeply: the limit is %d, and %q would be one more", r.opts.maxDepth(), name)
	}
	args := map[string]any{}
	if len(c.Args) > 1 {
		if args, err = alist(c.Args[1]); err != nil {
			return scheme.Value{}, err
		}
	}
	raw, _ := json.Marshal(args)
	values, err := rc.bind(raw)
	if err != nil {
		return scheme.Value{}, fmt.Errorf("%s: %w", rc.ToolName(), err)
	}
	st.depth++
	defer func() { st.depth-- }()
	v, err := r.in.Apply(c.Ctx, rc.proc, values)
	if err != nil {
		return scheme.Value{}, err
	}
	if v.Kind == scheme.KVoid {
		return scheme.Str(""), nil
	}
	return scheme.Str(scheme.Display(v)), nil
}

// SessionBackend lets recipes drive a tools.Session.
type SessionBackend struct{ S *tools.Session }

func (b SessionBackend) Call(ctx context.Context, tool string, args json.RawMessage) (tools.Result, error) {
	return b.S.Call(ctx, tool, args)
}

func (b SessionBackend) Info(ctx context.Context) (string, string, error) {
	p, err := b.S.Page(ctx)
	if err != nil {
		return "", "", err
	}
	info, err := p.Info(ctx)
	return info.Title, info.URL, err
}

var _ Backend = SessionBackend{}
