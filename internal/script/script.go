// Package script runs recipes: small Scheme programs, written by the person,
// that drive the browser and are offered to agents as extra tools.
//
// A recipe is a procedure registered with define-recipe. It reaches the browser
// only by calling the same tools an agent has, through the same Backend, so
// whatever the rules refuse, the approvals ask about and the audit log records
// applies to a recipe step by step. The interpreter is Brote's, with its pure
// language and nothing that touches the machine: its gate refuses every host
// operation, so a recipe cannot read a file or open a connection of its own.
package script

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/brote/scheme"

	"github.com/fclaude/rauli/internal/tools"
)

// Backend is what a recipe drives. *tools.Session, wrapped by SessionBackend,
// is the real one; a test can supply a smaller one.
type Backend interface {
	// Call runs a built-in tool by name, exactly as an agent would.
	Call(ctx context.Context, tool string, args json.RawMessage) (tools.Result, error)
	// Info says where the current tab is and what it is called.
	Info(ctx context.Context) (title, url string, err error)
}

// Options bound a run. The zero value means the defaults.
type Options struct {
	// Deadline is how long one run may take, steps in the browser included.
	// Zero means 120 seconds.
	Deadline time.Duration
	// MaxOutput is how many characters a run may return. Zero means 100000.
	MaxOutput int
	// MaxDepth is how deeply recipes may call one another. Zero means 8.
	MaxDepth int
}

func (o Options) deadline() time.Duration {
	if o.Deadline <= 0 {
		return 120 * time.Second
	}
	return o.Deadline
}

func (o Options) maxOutput() int {
	if o.MaxOutput <= 0 {
		return 100000
	}
	return o.MaxOutput
}

func (o Options) maxDepth() int {
	if o.MaxDepth <= 0 {
		return 8
	}
	return o.MaxDepth
}

// ToolPrefix starts the name of every tool a recipe makes.
const ToolPrefix = "recipe_"

// maxImages bounds the pictures one run may return, so a loop that takes a
// screenshot each time round cannot fill the reply.
const maxImages = 8

// ParamTypes are the types a recipe's parameters may have.
var ParamTypes = []string{"string", "number", "boolean"}

// Param is one parameter of a recipe.
type Param struct {
	Name string
	// Type is string, number or boolean.
	Type string
	Doc  string
	// Default is the value used when the caller leaves the parameter out. A
	// parameter with none is required.
	Default    scheme.Value
	HasDefault bool
}

// Recipe is a tool made from a Scheme procedure.
type Recipe struct {
	Name        string
	Description string
	Params      []Param
	// Source is the file that defined it.
	Source string

	proc scheme.Value
}

// ToolName is the name the recipe has as a tool.
func (rc *Recipe) ToolName() string { return ToolPrefix + rc.Name }

var recipeName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

// Runtime holds the interpreter and the recipes loaded into it.
type Runtime struct {
	backend Backend
	opts    Options
	in      *scheme.Interp
	sink    *sink

	// mu lets one run use the interpreter at a time, because it is not safe for
	// two goroutines.
	mu sync.Mutex

	state   sync.Mutex
	loading bool
	source  string
	recipes []*Recipe
	byName  map[string]*Recipe
}

// New builds a Runtime. backend may be nil for a runtime that only loads and
// lists recipes: every step that needs a browser then fails with a clear error.
func New(backend Backend, opts Options) (*Runtime, error) {
	r := &Runtime{backend: backend, opts: opts, sink: &sink{}, byName: map[string]*Recipe{}, loading: true}
	r.in = scheme.New(scheme.WithOutput(r.sink))
	r.in.SetGate(gate{})
	for _, p := range r.primitives() {
		if err := r.in.Register(p); err != nil {
			return nil, err
		}
	}
	// The helpers are Scheme over the primitives, so a person can read exactly
	// what each does.
	if _, err := r.in.EvalString(context.Background(), prelude); err != nil {
		return nil, fmt.Errorf("the recipe helpers did not load: %w", err)
	}
	r.in.Freeze()
	return r, nil
}

// gate is the policy in front of every primitive. Recipes get the language and
// the primitives of this package; anything that reaches the operating system,
// the network or something irreversible is refused wherever it is called from.
type gate struct{}

func (gate) Before(_ context.Context, p *scheme.Prim, _ []scheme.Value) error {
	switch {
	case p.Meta.Host:
		return &scheme.Denied{Prim: p.Name, Reason: "a recipe cannot use the machine's files, programs or network; it can only drive the browser through the browser tools"}
	case p.Meta.Egress:
		return &scheme.Denied{Prim: p.Name, Reason: "a recipe cannot send data off the machine on its own"}
	case p.Meta.Irreversible:
		return &scheme.Denied{Prim: p.Name, Reason: "a recipe cannot make irreversible changes on its own"}
	}
	return nil
}

func (gate) RegisterChecker(string, string, scheme.Value) error {
	return errors.New("a recipe cannot add policy checks")
}

// Recipes lists the recipes in the order they were defined.
func (r *Runtime) Recipes() []*Recipe {
	r.state.Lock()
	defer r.state.Unlock()
	return append([]*Recipe(nil), r.recipes...)
}

// Lookup finds a recipe by its name, with or without the tool prefix.
func (r *Runtime) Lookup(name string) *Recipe {
	r.state.Lock()
	defer r.state.Unlock()
	return r.byName[strings.TrimPrefix(name, ToolPrefix)]
}

// Has says whether a tool name belongs to a recipe.
func (r *Runtime) Has(tool string) bool {
	return strings.HasPrefix(tool, ToolPrefix) && r.Lookup(tool) != nil
}

// Finish ends loading. After it, define-recipe is refused: the tools an agent
// is offered cannot change while it works.
func (r *Runtime) Finish() {
	r.state.Lock()
	r.loading = false
	r.state.Unlock()
}

// LoadPaths loads recipe files. A path may be a file, or a directory whose
// .scm files are loaded in name order. A path that does not exist is an error
// that names it: a recipe that silently failed to load would leave an agent
// without a tool nobody knows is missing.
func (r *Runtime) LoadPaths(ctx context.Context, paths []string) error {
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("recipe path %s: %w", p, errors.Unwrap(err))
		}
		if !info.IsDir() {
			if err := r.LoadFile(ctx, p); err != nil {
				return err
			}
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return fmt.Errorf("recipe directory %s: %w", p, err)
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".scm") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			if err := r.LoadFile(ctx, filepath.Join(p, n)); err != nil {
				return err
			}
		}
	}
	return nil
}

// LoadFile loads one recipe file. Its definitions run under the time limit,
// since a loop at the top of a file would otherwise stop the server starting.
func (r *Runtime) LoadFile(ctx context.Context, path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.Lock()
	if !r.loading {
		r.state.Unlock()
		return errors.New("recipes can only be loaded before the server starts")
	}
	r.source = path
	r.state.Unlock()

	ctx, cancel := context.WithTimeout(ctx, r.opts.deadline())
	defer cancel()
	if err := r.in.LoadFile(ctx, path); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("loading %s took longer than %v: a definition that loops forever?", path, r.opts.deadline())
		}
		return fmt.Errorf("loading %s: %w", path, err)
	}
	return nil
}

// Tools describes the recipes as tools, for the protocol's tool list. They carry
// no implementation of their own: Call runs them.
func (r *Runtime) Tools() []tools.Tool {
	var out []tools.Tool
	for _, rc := range r.Recipes() {
		out = append(out, tools.Tool{Name: rc.ToolName(), Description: rc.Description, Schema: rc.Schema()})
	}
	return out
}

// Schema is the JSON Schema of the recipe's arguments.
func (rc *Recipe) Schema() map[string]any {
	props := map[string]any{}
	var required []string
	for _, p := range rc.Params {
		typ := map[string]any{"type": p.Type, "description": p.Doc}
		if p.HasDefault {
			typ["default"] = valueToAny(p.Default)
		} else {
			required = append(required, p.Name)
		}
		props[p.Name] = typ
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// runState travels in the context of a run, so a primitive knows which run it
// belongs to and, outside one, can say so.
type runState struct {
	recipe string
	depth  int
	images []tools.Image
	steps  int
}

type runKey struct{}

func current(ctx context.Context) *runState {
	s, _ := ctx.Value(runKey{}).(*runState)
	return s
}

// Call runs a recipe by its tool name with JSON arguments.
func (r *Runtime) Call(ctx context.Context, tool string, args json.RawMessage) (tools.Result, error) {
	rc := r.Lookup(tool)
	if rc == nil {
		return tools.Result{}, fmt.Errorf("%w %q", tools.ErrUnknownTool, tool)
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	values, err := rc.bind(args)
	if err != nil {
		return tools.Result{}, fmt.Errorf("%s: %w", rc.ToolName(), err)
	}
	return r.run(ctx, rc.Name, func(ctx context.Context) (scheme.Value, error) {
		return r.in.Apply(ctx, rc.proc, values)
	})
}

// HasMain says whether the loaded files define a procedure called main.
func (r *Runtime) HasMain() bool {
	_, ok := r.in.Global().Lookup("main")
	return ok
}

// RunMain runs the procedure called main, which a script may define in place of
// a recipe, with no arguments.
func (r *Runtime) RunMain(ctx context.Context) (tools.Result, error) {
	main, ok := r.in.Global().Lookup("main")
	if !ok {
		return tools.Result{}, errors.New("the script defines no procedure called main")
	}
	return r.run(ctx, "main", func(ctx context.Context) (scheme.Value, error) {
		return r.in.Apply(ctx, main, nil)
	})
}

// run is the one place a top-level run starts: it takes the interpreter, sets the
// time limit and the capture of what the script prints, and turns whatever
// happens, including a panic, into a result or a readable error.
func (r *Runtime) run(ctx context.Context, name string, f func(context.Context) (scheme.Value, error)) (res tools.Result, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	limit := r.opts.deadline()
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	st := &runState{recipe: name}
	ctx = context.WithValue(ctx, runKey{}, st)

	capture := newCapture(r.opts.maxOutput())
	r.sink.set(capture)
	defer r.sink.set(nil)

	defer func() {
		if p := recover(); p != nil {
			res, err = tools.Result{}, fmt.Errorf("recipe %s: an internal error stopped it: %v", name, p)
		}
	}()

	v, ferr := f(ctx)
	if ferr != nil {
		switch {
		case errors.Is(ferr, context.DeadlineExceeded):
			return tools.Result{}, fmt.Errorf("recipe %s stopped after %v, its time limit (%d steps in the browser so far)", name, limit, st.steps)
		case errors.Is(ferr, context.Canceled):
			return tools.Result{}, fmt.Errorf("recipe %s was cancelled", name)
		}
		var denied *scheme.Denied
		if errors.As(ferr, &denied) {
			return tools.Result{}, fmt.Errorf("recipe %s: %s: %s", name, denied.Prim, denied.Reason)
		}
		return tools.Result{}, fmt.Errorf("recipe %s: %w", name, ferr)
	}

	var text strings.Builder
	text.WriteString(capture.String())
	if v.Kind != scheme.KVoid {
		if text.Len() > 0 && !strings.HasSuffix(text.String(), "\n") {
			text.WriteByte('\n')
		}
		text.WriteString(scheme.Display(v))
	}
	out := text.String()
	if max := r.opts.maxOutput(); len([]rune(out)) > max || capture.cut {
		runes := []rune(out)
		if len(runes) > max {
			runes = runes[:max]
		}
		out = string(runes) + fmt.Sprintf("\n[output cut at %d characters]", max)
	}
	return tools.Result{Text: strings.TrimRight(out, "\n"), Images: st.images}, nil
}

// bind turns JSON arguments into the Scheme values the procedure is called
// with, in the order the parameters were declared.
func (rc *Recipe) bind(raw json.RawMessage) ([]scheme.Value, error) {
	var given map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&given); err != nil {
		return nil, fmt.Errorf("the arguments must be a JSON object: %w", err)
	}
	known := map[string]bool{}
	for _, p := range rc.Params {
		known[p.Name] = true
	}
	var unknown []string
	for k := range given {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("there is no parameter %q; the parameters are %s", unknown[0], rc.paramNames())
	}
	values := make([]scheme.Value, 0, len(rc.Params))
	for _, p := range rc.Params {
		rawv, ok := given[p.Name]
		if !ok || string(rawv) == "null" {
			if !p.HasDefault {
				return nil, fmt.Errorf("the parameter %q is required", p.Name)
			}
			values = append(values, p.Default)
			continue
		}
		v, err := decodeParam(p, rawv)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, nil
}

func (rc *Recipe) paramNames() string {
	if len(rc.Params) == 0 {
		return "none"
	}
	var names []string
	for _, p := range rc.Params {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

func decodeParam(p Param, raw json.RawMessage) (scheme.Value, error) {
	wrong := func(got string) error {
		return fmt.Errorf("the parameter %q must be a %s, but got %s", p.Name, p.Type, got)
	}
	switch p.Type {
	case "string":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return scheme.Value{}, wrong(describeJSON(raw))
		}
		return scheme.Str(s), nil
	case "boolean":
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return scheme.Value{}, wrong(describeJSON(raw))
		}
		return scheme.Bool(b), nil
	case "number":
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			return scheme.Value{}, wrong(describeJSON(raw))
		}
		if i, err := n.Int64(); err == nil {
			return scheme.Int(i), nil
		}
		f, err := n.Float64()
		if err != nil {
			return scheme.Value{}, wrong(describeJSON(raw))
		}
		return scheme.Float(f), nil
	}
	return scheme.Value{}, fmt.Errorf("the parameter %q has the unknown type %q", p.Name, p.Type)
}

func describeJSON(raw json.RawMessage) string {
	switch t := strings.TrimSpace(string(raw)); {
	case strings.HasPrefix(t, `"`):
		return "a string"
	case strings.HasPrefix(t, "["):
		return "a list"
	case strings.HasPrefix(t, "{"):
		return "an object"
	case t == "true" || t == "false":
		return "a boolean"
	default:
		return "a number"
	}
}

// ParseArgs turns name=value pairs from a command line into the JSON arguments
// of a recipe, reading each value as the type its parameter declares.
func (rc *Recipe) ParseArgs(pairs []string) (json.RawMessage, error) {
	byName := map[string]Param{}
	for _, p := range rc.Params {
		byName[p.Name] = p
	}
	obj := map[string]any{}
	for _, pair := range pairs {
		name, value, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("%q is not name=value", pair)
		}
		p, known := byName[name]
		if !known {
			return nil, fmt.Errorf("the recipe %s has no parameter %q; its parameters are %s", rc.Name, name, rc.paramNames())
		}
		switch p.Type {
		case "string":
			obj[name] = value
		case "boolean":
			switch strings.ToLower(value) {
			case "true", "yes", "1":
				obj[name] = true
			case "false", "no", "0":
				obj[name] = false
			default:
				return nil, fmt.Errorf("%s takes true or false; got %q", name, value)
			}
		case "number":
			n := json.Number(value)
			if _, err := n.Float64(); err != nil {
				return nil, fmt.Errorf("%s takes a number; got %q", name, value)
			}
			obj[name] = n
		}
	}
	return json.Marshal(obj)
}
