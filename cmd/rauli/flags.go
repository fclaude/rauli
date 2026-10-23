package main

import (
	"flag"
	"path/filepath"
	"time"

	"github.com/fclaude/rauli/internal/config"
)

// launchFlags are the options shared by every command that starts a browser.
// Each can also be set in the configuration file; one given on the command
// line wins over the file.
type launchFlags struct {
	config      string
	profile     string
	headed      bool
	window      string
	proxy       string
	downloadDir string
	timeout     time.Duration
}

func (l *launchFlags) add(fs *flag.FlagSet) {
	fs.StringVar(&l.config, "config", "", "read settings from this `file` (default ~/.config/rauli/config.toml)")
	fs.StringVar(&l.profile, "profile", "", "keep cookies and logins in this `directory` between runs (default: a throwaway profile)")
	fs.BoolVar(&l.headed, "headed", false, "show a real browser window")
	fs.StringVar(&l.window, "window", "", "size of the window and of new pages, as `WxH` in pixels (default 1280x800)")
	fs.StringVar(&l.proxy, "proxy", "", "send the browser's traffic through this proxy `URL`")
	fs.StringVar(&l.downloadDir, "download-dir", "", "save downloads in this `directory` (default: downloads are refused)")
	fs.DurationVar(&l.timeout, "timeout", 0, "give up on a single tool call after this `duration` (default 45s)")
}

// load reads the configuration file and lays the flags that were actually
// given over it. A flag left at its default does not hide what the file says.
func (l *launchFlags) load(fs *flag.FlagSet) (config.Config, error) {
	c, err := config.Load(l.config)
	if err != nil {
		return config.Config{}, err
	}
	var bad error
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "profile":
			c.Browser.Profile = absolute(l.profile)
		case "headed":
			c.Browser.Headed = l.headed
		case "window":
			if _, _, err := config.ParseWindow(l.window); err != nil && bad == nil {
				bad = usagef("--window: %v", err)
			}
			c.Browser.Window = l.window
		case "proxy":
			c.Browser.Proxy = l.proxy
		case "download-dir":
			c.Browser.DownloadDir = absolute(l.downloadDir)
		case "timeout":
			if l.timeout < 0 && bad == nil {
				bad = usagef("--timeout cannot be negative: %v", l.timeout)
			}
			c.Browser.Timeout = config.Duration(l.timeout)
		}
	})
	if bad != nil {
		return config.Config{}, bad
	}
	return c, nil
}

// absolute resolves a path given on the command line against the directory the
// command was run in, which is the only sensible reading of it.
func absolute(p string) string {
	if p == "" {
		return p
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
