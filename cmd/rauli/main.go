// Command rauli drives a real browser for people, and for programs that act on
// their behalf.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run is main with its streams made explicit, so a test can drive it. It
// returns the exit status: 0 for success, 1 for a failure, 2 for a command line
// that could not be understood.
func run(ctx context.Context, args []string, in io.Reader, out, errw io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(errw, helpText())
		return 2
	}
	name := args[0]
	switch name {
	case "help", "-h", "--help", "-help":
		if len(args) > 1 {
			// "rauli help serve" is "rauli serve -h".
			return run(ctx, []string{args[1], "-h"}, in, out, errw)
		}
		fmt.Fprint(out, helpText())
		return 0
	}
	cmd := find(name)
	if cmd == nil {
		fmt.Fprintf(errw, "rauli: there is no command %q. Run rauli help to see the commands.\n", name)
		return 2
	}
	err := cmd.run(ctx, args[1:], streams{in: in, out: out, err: errw})
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, new(usageError)):
		fmt.Fprintf(errw, "rauli %s: %v\n", name, err)
		return 2
	default:
		fmt.Fprintf(errw, "rauli %s: %v\n", name, err)
		return 1
	}
}

func helpText() string {
	list := append([]command(nil), commands...)
	sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
	width := 0
	for _, c := range list {
		width = max(width, len(c.name))
	}
	s := `Raulí drives a real browser. People use it from the command line. Programs
use it as a set of tools: they read pages as text and act on them with real
clicks and key presses.

Usage:
  rauli <command> [options]

Commands:
`
	for _, c := range list {
		s += fmt.Sprintf("  %-*s  %s\n", width, c.name, c.summary)
	}
	s += `
Run rauli <command> -h to see the options of one command.

The browser is Chromium or one built on it. Raulí looks for one on this
machine; set RAULI_CHROME to the path of a particular one.
`
	return s
}
