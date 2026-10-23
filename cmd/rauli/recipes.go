package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fclaude/rauli/internal/config"
	"github.com/fclaude/rauli/internal/mcp"
	"github.com/fclaude/rauli/internal/script"
	"github.com/fclaude/rauli/internal/tools"
)

func init() {
	register("run", "run a Scheme script or one recipe against a fresh browser", runScript)
	register("recipes", "list the recipes a set of files defines", listRecipes)
}

// repeatedFlag is a flag that may be given more than once.
type repeatedFlag []string

func (l *repeatedFlag) String() string     { return strings.Join(*l, ",") }
func (l *repeatedFlag) Set(v string) error { *l = append(*l, v); return nil }

func scriptOptions(cfg config.Config) script.Options {
	return script.Options{Deadline: time.Duration(cfg.Recipes.Deadline), MaxOutput: cfg.Recipes.MaxOutput}
}

// recipePaths joins the paths of the configuration file with those given on the
// command line, which are taken from where the person is standing.
func recipePaths(cfg config.Config, flagged []string) []string {
	paths := append([]string(nil), cfg.Recipes.Paths...)
	for _, p := range flagged {
		paths = append(paths, absolute(p))
	}
	return paths
}

// withRecipes loads the recipes and returns a backend that offers them beside
// the session's own tools. With none to load it returns the session itself.
func withRecipes(ctx context.Context, cfg config.Config, flagged []string, session *tools.Session) (mcp.Backend, error) {
	paths := recipePaths(cfg, flagged)
	if len(paths) == 0 {
		return session, nil
	}
	rt, err := script.New(script.SessionBackend{S: session}, scriptOptions(cfg))
	if err != nil {
		return nil, err
	}
	if err := rt.LoadPaths(ctx, paths); err != nil {
		return nil, err
	}
	rt.Finish()
	return recipeBackend{session: session, rt: rt}, nil
}

// recipeBackend lists the recipes among the tools and sends their calls to the
// runtime. Every step a recipe takes still goes through the session's Call, so
// the rules, the approvals and the audit log see each one.
type recipeBackend struct {
	session *tools.Session
	rt      *script.Runtime
}

func (b recipeBackend) Tools() []tools.Tool {
	return append(b.session.Tools(), b.rt.Tools()...)
}

func (b recipeBackend) Call(ctx context.Context, name string, args json.RawMessage) (tools.Result, error) {
	if b.rt.Has(name) {
		return b.rt.Call(ctx, name, args)
	}
	return b.session.Call(ctx, name, args)
}

func runScript(ctx context.Context, args []string, s streams) error {
	var lf launchFlags
	var name, imageDir string
	var recipeArgs repeatedFlag
	fs := flagSet("run", "rauli run FILE.scm [--recipe NAME] [--arg name=value]... [options]",
		`Loads a Scheme file and runs it against a fresh browser, with the same rules
and keychain as serve. With --recipe it runs that recipe; without, it runs the
procedure called main if the file defines one, and the file's only recipe
otherwise. What the script returns goes to standard output.`, s)
	lf.add(fs)
	fs.StringVar(&name, "recipe", "", "run the recipe called `NAME`")
	fs.Var(&recipeArgs, "arg", "give a recipe parameter, as `name=value`; may be repeated")
	fs.StringVar(&imageDir, "save-images", "", "save the screenshots the run takes in this `directory`")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usagef("run takes one script file, but got %d arguments", fs.NArg())
	}
	cfg, err := lf.load(fs)
	if err != nil {
		return err
	}
	session, err := config.BuildSession(ctx, cfg, approverFor(cfg, nil))
	if err != nil {
		return err
	}
	defer session.Close()
	stopAudit, err := startAudit(ctx, cfg, session)
	if err != nil {
		return err
	}
	defer stopAudit()

	rt, err := script.New(script.SessionBackend{S: session}, scriptOptions(cfg))
	if err != nil {
		return err
	}
	if err := rt.LoadPaths(ctx, append(recipePaths(cfg, nil), absolute(fs.Arg(0)))); err != nil {
		return err
	}
	rt.Finish()

	// Without --recipe the script's main runs, and failing that its only recipe.
	if name == "" && !rt.HasMain() {
		if all := rt.Recipes(); len(all) == 1 {
			name = all[0].Name
		} else {
			return fmt.Errorf("the script defines no main and %s; name one with --recipe", countRecipes(len(all)))
		}
	}
	var res tools.Result
	if name == "" {
		if len(recipeArgs) > 0 {
			return usagef("--arg needs a recipe, because main takes no parameters")
		}
		res, err = rt.RunMain(ctx)
	} else {
		rc := rt.Lookup(name)
		if rc == nil {
			return fmt.Errorf("there is no recipe called %q; the recipes are %s", name, recipeNames(rt))
		}
		raw, perr := rc.ParseArgs(recipeArgs)
		if perr != nil {
			return usageError{perr.Error()}
		}
		res, err = rt.Call(ctx, rc.ToolName(), raw)
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(s.out, res.Text)
	return saveImages(imageDir, res.Images, s)
}

func saveImages(dir string, images []tools.Image, s streams) error {
	if dir == "" || len(images) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for i, img := range images {
		p := filepath.Join(dir, fmt.Sprintf("screenshot-%d.png", i+1))
		if err := os.WriteFile(p, img.Data, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(s.err, "Saved %s\n", p)
	}
	return nil
}

func recipeNames(rt *script.Runtime) string {
	var names []string
	for _, rc := range rt.Recipes() {
		names = append(names, rc.Name)
	}
	if len(names) == 0 {
		return "none (the file defines no recipes)"
	}
	return strings.Join(names, ", ")
}

func countRecipes(n int) string {
	if n == 0 {
		return "no recipes"
	}
	return fmt.Sprintf("%d recipes", n)
}

func listRecipes(ctx context.Context, args []string, s streams) error {
	var configFile string
	var asJSON bool
	fs := flagSet("recipes", "rauli recipes [FILE-OR-DIRECTORY]... [--json]",
		`Lists the recipes that load from the files named, and from the paths in the
configuration file. It starts no browser, so it works on any machine and shows
what serve would offer as tools.`, s)
	fs.StringVar(&configFile, "config", "", "read settings from this `file` (default ~/.config/rauli/config.toml)")
	fs.BoolVar(&asJSON, "json", false, "print the recipes as JSON")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := config.Load(configFile)
	if err != nil {
		return err
	}
	paths := append([]string(nil), cfg.Recipes.Paths...)
	for _, p := range fs.Args() {
		paths = append(paths, absolute(p))
	}
	if len(paths) == 0 {
		return usagef("name a recipe file or a directory of them, or set paths in the [recipes] table of the configuration file")
	}
	rt, err := script.New(nil, scriptOptions(cfg))
	if err != nil {
		return err
	}
	if err := rt.LoadPaths(ctx, paths); err != nil {
		return err
	}
	if asJSON {
		var out []map[string]any
		for _, t := range rt.Tools() {
			out = append(out, map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Schema})
		}
		enc := json.NewEncoder(s.out)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	for _, rc := range rt.Recipes() {
		fmt.Fprintf(s.out, "%s\n    %s\n", rc.Name, rc.Description)
		for _, p := range rc.Params {
			line := fmt.Sprintf("    %s (%s): %s", p.Name, p.Type, p.Doc)
			if p.HasDefault {
				line += fmt.Sprintf(" [default %s]", p.Default)
			} else {
				line += " [required]"
			}
			fmt.Fprintln(s.out, line)
		}
	}
	if len(rt.Recipes()) == 0 {
		return errors.New("the files define no recipes")
	}
	return nil
}
