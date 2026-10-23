package main

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
)

func init() {
	register("version", "print the version", func(ctx context.Context, args []string, s streams) error {
		if len(args) > 0 {
			return usagef("version takes no arguments, but got %q", args[0])
		}
		fmt.Fprintf(s.out, "rauli %s (%s)\n", versionString(), runtime.Version())
		return nil
	})
}

// versionString is the module version the program was built from, with the
// commit when the build recorded one.
func versionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(unknown)"
	}
	v := info.Main.Version
	if v == "" {
		v = "(devel)"
	}
	var rev string
	dirty := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev != "" && v == "(devel)" {
		v += " " + rev
		if dirty {
			v += "+dirty"
		}
	}
	return v
}
