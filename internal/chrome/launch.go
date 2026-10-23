// Package chrome starts a Chromium-based browser and drives it over the
// DevTools Protocol. It is the part of Raulí that touches the browser; the
// agent interfaces and the rules sit on top of it.
package chrome

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/rauli/internal/cdp"
)

// Options say how to start the browser. The zero value is a headless browser
// with a throwaway profile, found on this machine.
type Options struct {
	// Path is the browser binary. Empty means look for one (see Find).
	Path string
	// ProfileDir keeps cookies, logins and storage between runs. Empty means a
	// new temporary profile that is deleted when the browser closes.
	ProfileDir string
	// Headed shows a real window. Headless is the default.
	Headed bool
	// Width and Height set the size of the window and of new pages.
	Width, Height int
	// Proxy is passed as --proxy-server, for example "http://127.0.0.1:3128".
	Proxy string
	// Args are extra command-line flags.
	Args []string
	// Stderr receives what the browser writes to its standard error, if set.
	Stderr io.Writer
	// Filter is asked about every request a page makes: the document, scripts,
	// images, frames and fetches alike. A non-nil error blocks the request, and
	// its text is what the page's console reports. A nil Filter allows all.
	Filter func(ctx context.Context, r Request) error
	// DownloadDir is where downloads are saved. Empty means downloads are
	// refused, which is the safe default: a page can start one without anyone asking.
	DownloadDir string
}

// Request describes a request the browser is about to make.
type Request struct {
	URL    string
	Method string
	// Type is the kind of resource: Document, Script, Image, XHR, Fetch and so on.
	Type string
	// Frame is true for a request that loads a page into the tab or one of its frames.
	Navigation bool
}

func (o *Options) size() (int, int) {
	w, h := o.Width, o.Height
	if w <= 0 {
		w = 1280
	}
	if h <= 0 {
		h = 800
	}
	return w, h
}

// candidates are searched in order. A browser the person already has comes
// before one that happens to be in a test cache, because a profile made by one
// version of Chromium is best reopened by the same or a newer one.
var candidates = []string{
	"google-chrome-stable", "google-chrome", "chromium", "chromium-browser", "chrome",
}

// Find returns the browser to use: $RAULI_CHROME if set, then the first
// well-known name on PATH, then a Chromium downloaded for Playwright.
func Find() (string, error) {
	if p := os.Getenv("RAULI_CHROME"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("RAULI_CHROME=%s: %w", p, err)
		}
		return p, nil
	}
	for _, name := range candidates {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, pattern := range []string{
			filepath.Join(home, ".cache", "ms-playwright", "chromium-*", "chrome-linux*", "chrome"),
			filepath.Join(home, ".cache", "ms-playwright", "chromium_headless_shell-*", "*", "chrome-headless-shell"),
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		} {
			if m, _ := filepath.Glob(pattern); len(m) > 0 {
				return m[len(m)-1], nil
			}
		}
	}
	return "", errors.New("no Chromium-based browser found: install one, or set RAULI_CHROME to its path")
}

var listening = regexp.MustCompile(`DevTools listening on (ws://\S+)`)

// Browser is a running browser and its DevTools connection.
type Browser struct {
	conn        *cdp.Conn
	cmd         *exec.Cmd
	profile     string
	tempProfile bool
	opts        Options
	userAgent   string
	debugURL    string

	mu     sync.Mutex
	closed bool
	exited chan struct{}
	tabs   map[string]*Page // attached tabs by target id

	exitErr error // how the process ended, once exited is closed
}

// Launch starts the browser and connects to it. The browser lives until Close
// is called or ctx ends.
func Launch(ctx context.Context, opts Options) (*Browser, error) {
	path := opts.Path
	if path == "" {
		var err error
		if path, err = Find(); err != nil {
			return nil, err
		}
	}

	profile := opts.ProfileDir
	temp := false
	if profile == "" {
		dir, err := os.MkdirTemp("", "rauli-profile-")
		if err != nil {
			return nil, err
		}
		profile, temp = dir, true
	} else if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, err
	}

	w, h := opts.size()
	args := []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=Translate,MediaRouter",
		"--password-store=basic",
		"--use-mock-keychain",
		fmt.Sprintf("--window-size=%d,%d", w, h),
	}
	if !opts.Headed {
		args = append(args, "--headless=new", "--hide-scrollbars", "--mute-audio")
	}
	if opts.Proxy != "" {
		args = append(args, "--proxy-server="+opts.Proxy)
	}
	// Chromium's sandbox needs kernel features that containers and root
	// users often lack. It is only dropped where it could not work anyway.
	if os.Geteuid() == 0 || os.Getenv("RAULI_NO_SANDBOX") != "" {
		args = append(args, "--no-sandbox")
	}
	args = append(args, opts.Args...)
	args = append(args, "about:blank")

	cmd := exec.Command(path, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cleanup(profile, temp)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cleanup(profile, temp)
		return nil, fmt.Errorf("start %s: %w", path, err)
	}

	b := &Browser{cmd: cmd, profile: profile, tempProfile: temp, opts: opts, exited: make(chan struct{}), tabs: map[string]*Page{}}
	go func() {
		err := cmd.Wait()
		b.mu.Lock()
		b.exitErr = err
		b.mu.Unlock()
		close(b.exited)
	}()

	wsURL, err := readDevToolsURL(ctx, stderr, opts.Stderr, b.exited)
	if err != nil {
		b.kill()
		cleanup(profile, temp)
		return nil, err
	}

	conn, err := cdp.Dial(ctx, wsURL)
	if err != nil {
		b.kill()
		cleanup(profile, temp)
		return nil, err
	}
	b.conn = conn
	if u, err := url.Parse(wsURL); err == nil {
		b.debugURL = "http://" + u.Host
	}

	behavior := map[string]any{"behavior": "deny"}
	if opts.DownloadDir != "" {
		abs, err := filepath.Abs(opts.DownloadDir)
		if err != nil {
			_ = b.Close()
			return nil, err
		}
		if err := os.MkdirAll(abs, 0o700); err != nil {
			_ = b.Close()
			return nil, err
		}
		behavior = map[string]any{"behavior": "allow", "downloadPath": abs}
	}
	if err := conn.Call(ctx, "", "Browser.setDownloadBehavior", behavior, nil); err != nil {
		_ = b.Close()
		return nil, err
	}

	var v struct {
		UserAgent string `json:"userAgent"`
	}
	if err := conn.Call(ctx, "", "Browser.getVersion", nil, &v); err != nil {
		_ = b.Close()
		return nil, err
	}
	// A headless browser announces itself in its user agent, and some sites
	// turn such visitors away. The rest of the string is the real one.
	b.userAgent = strings.Replace(v.UserAgent, "HeadlessChrome", "Chrome", 1)

	go func() {
		select {
		case <-ctx.Done():
			_ = b.Close()
		case <-b.exited:
		}
	}()
	return b, nil
}

// readDevToolsURL waits for the line Chromium prints once it is listening,
// and keeps copying the rest of its output to tee, if there is one.
func readDevToolsURL(ctx context.Context, r io.Reader, tee io.Writer, exited <-chan struct{}) (string, error) {
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		sent := false
		for sc.Scan() {
			line := sc.Text()
			if tee != nil {
				fmt.Fprintln(tee, line)
			}
			if !sent {
				if m := listening.FindStringSubmatch(line); m != nil {
					found <- m[1]
					sent = true
				}
			}
		}
	}()
	select {
	case u := <-found:
		return u, nil
	case <-exited:
		return "", errors.New("the browser exited before it was ready")
	case <-time.After(30 * time.Second):
		return "", errors.New("the browser did not report its DevTools address within 30 seconds")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func cleanup(profile string, temp bool) {
	if temp {
		_ = os.RemoveAll(profile)
	}
}

func (b *Browser) kill() {
	if b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
	}
	<-b.exited
}

// ExitError says how the browser process ended: nil for a clean exit, otherwise
// the exit status or the signal that ended it. It is only meaningful once Done
// is closed.
func (b *Browser) ExitError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exitErr
}

// Done is closed when the browser process has exited.
func (b *Browser) Done() <-chan struct{} { return b.exited }

// UserAgent is the user agent the browser's pages present.
func (b *Browser) UserAgent() string { return b.userAgent }

// DebugURL is the HTTP address of the browser's DevTools endpoint, for
// attaching the browser's own developer tools to it.
func (b *Browser) DebugURL() string { return b.debugURL }

// Port is the TCP port of the DevTools endpoint.
func (b *Browser) Port() int {
	i := strings.LastIndex(b.debugURL, ":")
	n, _ := strconv.Atoi(b.debugURL[i+1:])
	return n
}

// Close ends the browser and removes a temporary profile.
func (b *Browser) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()

	if b.conn != nil {
		// Ask politely first, so the profile is written out cleanly.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = b.conn.Call(ctx, "", "Browser.close", nil, nil)
		cancel()
		_ = b.conn.Close()
	}
	select {
	case <-b.exited:
	case <-time.After(5 * time.Second):
		b.kill()
	}
	cleanup(b.profile, b.tempProfile)
	return nil
}
