package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/fclaude/rauli/internal/config"
)

func init() {
	register("snapshot", "print a page as text, with a reference on each element you can act on", snapshot)
	register("shot", "save a screenshot of a page to a file", shot)
}

func snapshot(ctx context.Context, args []string, s streams) error {
	var lf launchFlags
	fs := flagSet("snapshot", "rauli snapshot [options] URL",
		`Opens the page, waits for it to load, and prints it as text: the headings and
paragraphs, the links with their addresses, and the form controls. Each element
you can act on has a reference such as e12.`, s)
	lf.add(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usagef("give one address to open, such as rauli snapshot https://example.com")
	}
	cfg, err := lf.load(fs)
	if err != nil {
		return err
	}
	session, err := config.BuildSession(ctx, cfg, nil)
	if err != nil {
		return err
	}
	defer session.Close()

	res, err := session.Call(ctx, "navigate", mustJSON(map[string]any{"url": fs.Arg(0)}))
	if err != nil {
		return err
	}
	fmt.Fprintln(s.out, res.Text)
	return nil
}

func shot(ctx context.Context, args []string, s streams) error {
	var lf launchFlags
	var full, jpeg bool
	fs := flagSet("shot", "rauli shot [options] URL FILE",
		`Opens the page, waits for it to load, and saves a picture of it to FILE. No
window opens. The picture is a PNG unless you ask for a JPEG, which is smaller.`, s)
	lf.add(fs)
	fs.BoolVar(&full, "full-page", false, "capture the whole scrollable page, beyond the part you would see")
	fs.BoolVar(&jpeg, "jpeg", false, "save a JPEG instead of a PNG")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return usagef("give an address and a file name, such as rauli shot https://example.com page.png")
	}
	cfg, err := lf.load(fs)
	if err != nil {
		return err
	}
	session, err := config.BuildSession(ctx, cfg, nil)
	if err != nil {
		return err
	}
	defer session.Close()

	if _, err := session.Call(ctx, "navigate", mustJSON(map[string]any{"url": fs.Arg(0), "snapshot": false})); err != nil {
		return err
	}
	res, err := session.Call(ctx, "screenshot", mustJSON(map[string]any{"fullPage": full, "jpeg": jpeg}))
	if err != nil {
		return err
	}
	if len(res.Images) == 0 {
		return fmt.Errorf("the browser returned no picture")
	}
	if err := os.WriteFile(fs.Arg(1), res.Images[0].Data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "Saved %s. %s\n", fs.Arg(1), res.Text)
	return nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
