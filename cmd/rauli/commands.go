package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
)

// streams are the standard streams a command works with. Commands never touch
// os.Stdin and the like directly, so tests can supply their own.
type streams struct {
	in  io.Reader
	out io.Writer
	err io.Writer
}

// usageError is returned for a command line that cannot be understood, which
// exits with status 2 instead of 1.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return usageError{fmt.Sprintf(format, args...)} }

// command is one subcommand of rauli.
type command struct {
	name    string
	summary string
	run     func(ctx context.Context, args []string, s streams) error
}

// commands is filled by register, which each command's file calls from init.
// A new command is a new file; nothing here needs to change.
var commands []command

func register(name, summary string, run func(ctx context.Context, args []string, s streams) error) {
	commands = append(commands, command{name: name, summary: summary, run: run})
}

func find(name string) *command {
	for i := range commands {
		if commands[i].name == name {
			return &commands[i]
		}
	}
	return nil
}

// flagSet makes a flag set for a command. Its usage text starts with the
// command's own description, so -h says what the command is for before it
// lists the options.
func flagSet(name, usage, description string, s streams) *flag.FlagSet {
	fs := flag.NewFlagSet("rauli "+name, flag.ContinueOnError)
	fs.SetOutput(s.err)
	fs.Usage = func() {
		fmt.Fprintf(s.err, "Usage: %s\n\n%s\n", usage, description)
		fmt.Fprintf(s.err, "\nOptions:\n")
		fs.PrintDefaults()
	}
	return fs
}

// parse parses a command's arguments, turning a bad flag into a usage error.
// Flags may come before, between or after the other arguments, because that is
// how people type them: the standard library would stop at the first address
// and take everything after it for a name.
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(interleave(fs, args)); err != nil {
		if err == flag.ErrHelp {
			return err
		}
		return usageError{err.Error()}
	}
	return nil
}

// interleave moves the flags of args in front of its positional arguments. A
// flag that takes a value brings the next argument with it, unless the value
// is attached with "=". Everything after a bare "--" stays positional.
func interleave(fs *flag.FlagSet, args []string) []string {
	boolean := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			boolean[f.Name] = true
		}
	})
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			rest = append(rest, args[i:]...)
			i = len(args)
		case len(a) > 1 && a[0] == '-':
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if strings.Contains(name, "=") || boolean[name] {
				continue
			}
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		default:
			rest = append(rest, a)
		}
	}
	return append(flags, rest...)
}
