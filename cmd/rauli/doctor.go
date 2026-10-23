package main

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/fclaude/rauli/internal/chrome"
)

func init() {
	register("doctor", "check that a browser can be found and started", doctor)
}

var chromeVersion = regexp.MustCompile(`Chrome/(\S+)`)

func doctor(ctx context.Context, args []string, s streams) error {
	var lf launchFlags
	fs := flagSet("doctor", "rauli doctor [options]",
		`Finds the browser, starts it, reads its version and the address of its
developer tools, and closes it again. If something is wrong, it says what.`, s)
	lf.add(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("doctor takes no arguments, but got %q", fs.Arg(0))
	}
	cfg, err := lf.load(fs)
	if err != nil {
		return err
	}
	path, err := chrome.Find()
	if err != nil {
		return err
	}
	fmt.Fprintf(s.out, "browser:     %s\n", path)

	opts, err := cfg.ChromeOptions()
	if err != nil {
		return err
	}
	opts.Path = path
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	b, err := chrome.Launch(ctx, opts)
	if err != nil {
		return fmt.Errorf("the browser was found but would not start: %w", err)
	}
	defer b.Close()

	version := "unknown"
	if m := chromeVersion.FindStringSubmatch(b.UserAgent()); m != nil {
		version = m[1]
	}
	fmt.Fprintf(s.out, "version:     %s\n", version)
	fmt.Fprintf(s.out, "user agent:  %s\n", b.UserAgent())
	fmt.Fprintf(s.out, "devtools:    %s\n", b.DebugURL())

	// Opening a page proves more than starting the browser does.
	p, err := b.NewPage(ctx)
	if err != nil {
		return fmt.Errorf("the browser started but a page would not open: %w", err)
	}
	if err := p.Navigate(ctx, "about:blank"); err != nil {
		return fmt.Errorf("a page opened but would not load: %w", err)
	}
	fmt.Fprintln(s.out, "ok")
	return nil
}
