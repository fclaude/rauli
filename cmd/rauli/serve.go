package main

import (
	"context"
	"fmt"

	"github.com/fclaude/rauli/internal/config"
	"github.com/fclaude/rauli/internal/mcp"
)

// instructions are shown to the model that uses the tools. They say how the
// pieces fit together, which the individual tool descriptions cannot.
const instructions = `Raulí drives a real browser. Take a snapshot to read a page. Every element you can act on has a reference such as e12, which click, type and the other tools take. References end when the page changes, so take a new snapshot after anything that loads a page. Passwords and other secrets are never yours to type: fill_credential takes them from the keychain and puts them in the page without showing them to you.`

func init() {
	register("serve", "serve the browser tools to a program over standard input and output", serve)
}

func serve(ctx context.Context, args []string, s streams) error {
	var lf launchFlags
	fs := flagSet("serve", "rauli serve [options]",
		`Starts a browser and answers requests on standard input and output, using the
Model Context Protocol. A program that speaks it can list the tools and call
them. Standard output carries protocol messages and nothing else; anything
Raulí has to say goes to standard error.

The browser closes when the program closes standard input.`, s)
	lf.add(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("serve takes no arguments, but got %q", fs.Arg(0))
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

	srv := &mcp.Server{Backend: session, Name: "rauli", Version: versionString(), Instructions: instructions, Log: s.err}
	if err := srv.Serve(ctx, s.in, s.out); err != nil && ctx.Err() == nil {
		return fmt.Errorf("the protocol stream failed: %w", err)
	}
	return nil
}
