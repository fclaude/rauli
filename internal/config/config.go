// Package config reads Raulí's configuration file and builds a browser
// session from it.
//
// The file is TOML with four tables: browser, rules, keychain and watch.
// Unknown keys are errors, because a misspelt setting that is quietly ignored
// leaves a person believing a rule is in force when it is not.
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/fclaude/rauli/internal/chrome"
	"github.com/fclaude/rauli/internal/rules"
	"github.com/fclaude/rauli/internal/tools"
	"github.com/fclaude/rauli/internal/vault"
)

// Config is the whole configuration file.
type Config struct {
	Browser  Browser      `toml:"browser"`
	Rules    rules.Config `toml:"rules"`
	Keychain vault.Config `toml:"keychain"`
	Watch    Watch        `toml:"watch"`

	// Dir is the directory the file was read from. Relative paths in the file
	// mean paths below it. It is empty for a configuration that was not read from a file.
	Dir string `toml:"-"`
}

// Browser holds the settings for starting the browser.
type Browser struct {
	// Profile is a directory that keeps cookies and logins between runs. Empty
	// means a throwaway profile.
	Profile string `toml:"profile"`
	// Headed shows a real window.
	Headed bool `toml:"headed"`
	// Window is the size of the window and of new pages, such as "1280x800".
	Window string `toml:"window"`
	// Proxy is the proxy the browser sends its traffic through.
	Proxy string `toml:"proxy"`
	// DownloadDir is where downloads are saved. Empty means they are refused.
	DownloadDir string `toml:"download_dir"`
	// Timeout bounds each tool call. Zero means the default.
	Timeout Duration `toml:"timeout"`
}

// Watch holds the settings for the live view.
type Watch struct {
	// Listen is the address to listen on. Empty means 127.0.0.1.
	Listen string `toml:"listen"`
	// Port is the TCP port. Zero means a free one chosen at start.
	Port int `toml:"port"`
}

// Duration is a time.Duration written as text, such as "45s" or "2m".
type Duration time.Duration

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("%q is not a duration: use a number and a unit, such as 45s or 2m", b)
	}
	if v < 0 {
		return fmt.Errorf("a timeout cannot be negative: %q", b)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// ParseWindow reads a size such as "1280x800". An empty string means the
// browser's default and returns zeros.
func ParseWindow(s string) (width, height int, err error) {
	if s == "" {
		return 0, 0, nil
	}
	w, h, ok := strings.Cut(strings.ToLower(s), "x")
	if ok {
		width, err = strconv.Atoi(w)
		if err == nil {
			height, err = strconv.Atoi(h)
		}
	}
	if !ok || err != nil || width < 200 || height < 200 || width > 8000 || height > 8000 {
		return 0, 0, fmt.Errorf("%q is not a window size: write width and height in pixels, from 200 to 8000, such as 1280x800", s)
	}
	return width, height, nil
}

// DefaultPath is where the file is looked for when none is named:
// $XDG_CONFIG_HOME/rauli/config.toml, or ~/.config/rauli/config.toml.
func DefaultPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" && filepath.IsAbs(dir) {
		return filepath.Join(dir, "rauli", "config.toml")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "rauli", "config.toml")
	}
	return ""
}

// Load reads the configuration. With an empty path it reads the default file,
// and a missing one is fine: everything has a default. A file that was named
// on purpose must exist.
func Load(path string) (Config, error) {
	explicit := path != ""
	if !explicit {
		path = DefaultPath()
	}
	if path == "" {
		return Config{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !explicit {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("configuration file: %w", err)
	}
	return Parse(data, path)
}

// Parse reads configuration text. name is used in error messages and, made
// absolute, fixes the directory that relative paths are taken from.
func Parse(data []byte, name string) (Config, error) {
	var c Config
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, describe(name, err)
	}
	if name != "" {
		abs, err := filepath.Abs(name)
		if err == nil {
			c.Dir = filepath.Dir(abs)
		}
	}
	if _, _, err := ParseWindow(c.Browser.Window); err != nil {
		return Config{}, fmt.Errorf("%s: [browser] window: %w", name, err)
	}
	c.Browser.Profile = c.resolve(c.Browser.Profile)
	c.Browser.DownloadDir = c.resolve(c.Browser.DownloadDir)
	return c, nil
}

// resolve makes a path from the file absolute, taking relative ones from the
// file's own directory so the result does not depend on where rauli is run.
func (c Config) resolve(p string) string {
	if p == "" || filepath.IsAbs(p) || c.Dir == "" {
		return p
	}
	return filepath.Join(c.Dir, p)
}

// Resolve is resolve for the other packages' paths.
func (c Config) Resolve(p string) string { return c.resolve(p) }

// describe turns a decoder's error into one that names the file, the line and
// the key.
func describe(name string, err error) error {
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		var parts []string
		for _, e := range strict.Errors {
			line, _ := e.Position()
			parts = append(parts, fmt.Sprintf("%s:%d: there is no setting %q", name, line, strings.Join(e.Key(), ".")))
		}
		return errors.New(strings.Join(parts, "\n"))
	}
	var de *toml.DecodeError
	if errors.As(err, &de) {
		line, col := de.Position()
		if key := strings.Join(de.Key(), "."); key != "" {
			return fmt.Errorf("%s:%d:%d: setting %q: %s", name, line, col, key, strings.TrimPrefix(de.Error(), "toml: "))
		}
		return fmt.Errorf("%s:%d:%d: %s", name, line, col, strings.TrimPrefix(de.Error(), "toml: "))
	}
	return fmt.Errorf("%s: %w", name, err)
}

// ChromeOptions turns the browser settings into what the browser layer takes.
func (c Config) ChromeOptions() (chrome.Options, error) {
	w, h, err := ParseWindow(c.Browser.Window)
	if err != nil {
		return chrome.Options{}, err
	}
	return chrome.Options{
		ProfileDir:  c.Browser.Profile,
		Headed:      c.Browser.Headed,
		Width:       w,
		Height:      h,
		Proxy:       c.Browser.Proxy,
		DownloadDir: c.Browser.DownloadDir,
	}, nil
}

// BuildSession starts a browser session as the configuration describes: the
// rules are its Guard and the keychain is its Vault.
//
// This is the one place that joins the pieces. Anything that must be set up
// before a session starts, such as how a person is asked to approve an action,
// is added here, so every command that starts a browser behaves the same.
// approver may be nil, which makes an action that needs approval refuse with a
// message saying so.
func BuildSession(ctx context.Context, c Config, approver rules.Approver) (*tools.Session, error) {
	bo, err := c.ChromeOptions()
	if err != nil {
		return nil, err
	}
	guard, err := rules.New(c.Rules, approver)
	if err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	vlt, err := vault.Open(ctx, c.Keychain)
	if err != nil {
		return nil, fmt.Errorf("keychain: %w", err)
	}
	return tools.New(ctx, tools.Options{
		Browser: bo,
		Guard:   guard,
		Vault:   vlt,
		Timeout: time.Duration(c.Browser.Timeout),
	})
}
