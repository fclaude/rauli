package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fclaude/rauli/internal/config"
	"github.com/fclaude/rauli/internal/rules"
)

func init() {
	register("watch", "open a live view of the browser, to look in and take over", watchCommand)
}

func watchCommand(ctx context.Context, args []string, s streams) error {
	var lf launchFlags
	var listen string
	fs := flagSet("watch", "rauli watch [options] [URL]",
		`Starts a browser and a live view of it, and prints the address of the view. Open
that address in a browser of your own: it shows the page, the actions taken, and
lets you take the tab over and click and type yourself. Use it to log in by hand
to a profile you keep (--profile), so that a program using that profile later
finds you already signed in.

An address given on the command line is opened first, if the rules allow it.
The view only listens on this machine, and its address carries a secret.
Press Ctrl-C to stop.`, s)
	lf.add(fs)
	fs.StringVar(&listen, "listen", "", "listen on this `address`, such as 127.0.0.1:8765 (default: a free port on this machine)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return usagef("watch takes at most one address, but got %d arguments", fs.NArg())
	}
	cfg, err := lf.load(fs)
	if err != nil {
		return err
	}

	queue := rules.NewQueue(0)
	session, err := config.BuildSession(ctx, cfg, approverFor(cfg, queue))
	if err != nil {
		return err
	}
	defer session.Close()
	stopAudit, err := startAudit(ctx, cfg, session)
	if err != nil {
		return err
	}
	defer stopAudit()

	url, err := startView(ctx, cfg, session, queue, listen)
	if err != nil {
		return err
	}
	fmt.Fprintf(s.out, "Live view: %s\n", url)

	if fs.NArg() == 1 {
		args, _ := json.Marshal(map[string]any{"url": fs.Arg(0), "snapshot": false})
		if _, err := session.Call(ctx, "navigate", args); err != nil {
			fmt.Fprintf(s.err, "rauli watch: could not open %s: %v\n", fs.Arg(0), err)
		}
	}
	fmt.Fprintln(s.err, "Press Ctrl-C to stop.")

	select {
	case <-ctx.Done():
	case <-session.Browser().Done():
		if err := session.Browser().ExitError(); err != nil {
			return fmt.Errorf("the browser ended: %w", err)
		}
	}
	return nil
}
