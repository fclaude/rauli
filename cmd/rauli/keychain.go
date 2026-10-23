package main

import (
	"context"
	"strings"

	"github.com/fclaude/rauli/internal/config"
	"github.com/fclaude/rauli/internal/vault"
)

func init() {
	register("keychain", "manage the logins an agent may use", keychain)
}

// keychain runs the keychain's own command line against the keychain the
// configuration names. Only --config is taken here, wherever it appears; the
// rest belongs to the keychain, which has options of its own.
func keychain(ctx context.Context, args []string, s streams) error {
	path, rest := takeConfig(args)
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	return vault.Run(ctx, cfg.Keychain, rest, s.in, s.out, s.err)
}

// takeConfig removes --config FILE (or --config=FILE, or the single-dash
// forms) from args and returns the file named.
func takeConfig(args []string) (string, []string) {
	var path string
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name := strings.TrimLeft(a, "-")
		switch {
		case a == "--":
			return path, append(rest, args[i:]...)
		case strings.HasPrefix(a, "-") && name == "config" && i+1 < len(args):
			path = args[i+1]
			i++
		case strings.HasPrefix(a, "-") && strings.HasPrefix(name, "config="):
			path = strings.TrimPrefix(name, "config=")
		default:
			rest = append(rest, a)
		}
	}
	return path, rest
}
