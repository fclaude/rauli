package vault

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/rauli/internal/tools"
)

const (
	defaultCommandTimeout = 10 * time.Second
	// maxCommandOutput is as much as a login's value may be. A password, a token
	// or a key is far shorter; more than this means the program is printing
	// something else, such as a whole vault.
	maxCommandOutput = 64 << 10
)

// validateCommand checks the settings of the command backend.
func (c Config) validateCommand() error {
	if len(c.Command) == 0 || c.Command[0] == "" {
		return configErr("keychain.command", "the command backend needs a program to run: command = [\"/path/to/program\"]")
	}
	if c.Timeout != "" {
		d, err := time.ParseDuration(c.Timeout)
		if err != nil || d <= 0 {
			return configErr("keychain.timeout", "%q is not a length of time such as 10s or 2m", c.Timeout)
		}
	}
	seen := map[string]bool{}
	for i, h := range c.Handles {
		at := func(key string) string { return fmt.Sprintf("keychain.handles[%d].%s", i, key) }
		if !nameRE.MatchString(h.Name) {
			return configErr(at("name"), "%q is not a name for a login: use lower-case letters, digits, dots, dashes and underscores, starting with a letter or digit", h.Name)
		}
		if seen[h.Name] {
			return configErr(at("name"), "there is already a login called %q", h.Name)
		}
		seen[h.Name] = true
		if len(h.Fields) == 0 {
			return configErr(at("fields"), "the login %q needs at least one field, such as fields = [\"user\", \"password\"]", h.Name)
		}
		fields := map[string]bool{}
		for _, f := range h.Fields {
			if !fieldRE.MatchString(f) {
				return configErr(at("fields"), "%q is not a name for a field: use lower-case letters, digits, dashes and underscores, starting with a letter", f)
			}
			if fields[f] {
				return configErr(at("fields"), "the field %q is named twice", f)
			}
			fields[f] = true
		}
		for _, o := range h.Origins {
			if _, err := parseOrigin(o); err != nil {
				return configErr(at("origins"), "%v", err)
			}
		}
		switch {
		case len(h.Origins) == 0 && !h.Anywhere:
			return configErr(at("origins"), "the login %q needs the origins where it may be used, such as origins = [\"https://example.com\"]. A login usable on every site is a decision to make on purpose: set anywhere = true if that is what you mean", h.Name)
		case len(h.Origins) > 0 && h.Anywhere:
			return configErr(at("anywhere"), "the login %q is for its origins or for anywhere. Set origins or anywhere = true and leave out the other", h.Name)
		}
	}
	return nil
}

// commandVault is the keychain whose secrets live elsewhere. It implements
// tools.Vault, and the command line uses its other methods.
type commandVault struct {
	command []string
	timeout time.Duration
	handles []HandleConfig

	// stderr receives what the program writes to its standard error. It is nil
	// while an agent is using the keychain, because that text can hint at what
	// the person's vault holds, and set to the person's terminal by the test command, so
	// they can see why a program failed.
	stderr io.Writer
}

var _ tools.Vault = (*commandVault)(nil)

func newCommandVault(c Config) (*commandVault, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	v := &commandVault{command: append([]string(nil), c.Command...), timeout: defaultCommandTimeout, handles: c.Handles}
	if c.Timeout != "" {
		v.timeout, _ = time.ParseDuration(c.Timeout)
	}
	return v, nil
}

func (v *commandVault) find(name string) (*HandleConfig, error) {
	for i := range v.handles {
		if v.handles[i].Name == name {
			return &v.handles[i], nil
		}
	}
	return nil, fmt.Errorf("there is no login called %q", name)
}

func asHandle(h HandleConfig) tools.Handle {
	return tools.Handle{Name: h.Name, Fields: append([]string(nil), h.Fields...), Origins: append([]string(nil), h.Origins...)}
}

// Handles lists the logins of the configuration, without values.
func (v *commandVault) Handles(context.Context) ([]tools.Handle, error) {
	out := make([]tools.Handle, 0, len(v.handles))
	for _, h := range v.handles {
		out = append(out, asHandle(h))
	}
	return out, nil
}

// Show returns what is known about one login, without values.
func (v *commandVault) Show(_ context.Context, name string) (tools.Handle, error) {
	h, err := v.find(name)
	if err != nil {
		return tools.Handle{}, err
	}
	return asHandle(*h), nil
}

// Secret returns one field of a login for use on the page at pageURL. The
// program is run only after the login's origins have allowed the page.
func (v *commandVault) Secret(ctx context.Context, handle, field, pageURL string) (string, error) {
	h, err := v.find(handle)
	if err != nil {
		return "", err
	}
	if err := checkUse(h.Name, h.Origins, h.Anywhere, h.Fields, field, pageURL); err != nil {
		return "", err
	}
	return v.fetch(ctx, h.Name, field, originOf(pageURL))
}

// Test checks that a login can be used on a page and that the program gives a
// value for each of its fields. It shows no value. The program's standard
// error goes to the person's terminal, for finding out why one failed.
func (v *commandVault) Test(ctx context.Context, name, pageURL string) ([]string, error) {
	h, err := v.find(name)
	if err != nil {
		return nil, err
	}
	var ok []string
	for _, f := range h.Fields {
		if err := checkUse(h.Name, h.Origins, h.Anywhere, h.Fields, f, pageURL); err != nil {
			return nil, err
		}
		val, err := v.fetch(ctx, h.Name, f, originOf(pageURL))
		if err != nil {
			return nil, fmt.Errorf("the field %q: %w", f, err)
		}
		_ = val
		ok = append(ok, f)
	}
	return ok, nil
}

// Add is not possible here: the logins are in the configuration.
func (v *commandVault) Add(context.Context, AddOptions) error { return errNotHere }

// Remove is not possible here: the logins are in the configuration.
func (v *commandVault) Remove(context.Context, string) error { return errNotHere }

// Close lets go of what the keychain holds, which is nothing.
func (v *commandVault) Close() error { return nil }

var errNotHere = errors.New("with the command backend the logins are the [[keychain.handles]] tables of the configuration, and their values are in your password manager, so rauli keychain cannot add or remove one: edit the configuration")

// originOf is a page's scheme, host and port. The program learns where the
// value is going, and nothing of the path or query, which can hold a token.
func originOf(pageURL string) string {
	u, err := url.Parse(pageURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

var errTooLong = errors.New("output too long")

// limited keeps what is written to it, up to a limit. Past the limit it fails
// the write and says so, which ends the program through onFull.
type limited struct {
	buf    []byte
	max    int
	over   bool
	onFull func()
}

func (l *limited) Write(p []byte) (int, error) {
	if len(l.buf)+len(p) > l.max {
		l.over = true
		l.onFull()
		return 0, errTooLong
	}
	l.buf = append(l.buf, p...)
	return len(p), nil
}

// fetch runs the program for one field and returns what it printed. Nothing it
// prints, to either stream, appears in an error: a failure says how the program
// ended and never why it says it did.
func (v *commandVault) fetch(ctx context.Context, handle, field, origin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()

	// An empty directory of its own, so the program cannot be affected by, or
	// read from, wherever Raulí happened to be started.
	dir, err := os.MkdirTemp("", "rauli-keychain-")
	if err != nil {
		return "", fmt.Errorf("the keychain command could not be started: %w", err)
	}
	defer os.RemoveAll(dir)

	args := append(append([]string(nil), v.command[1:]...), handle, field)
	cmd := exec.CommandContext(ctx, v.command[0], args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "RAULI_HANDLE="+handle, "RAULI_FIELD="+field, "RAULI_ORIGIN="+origin)
	cmd.Stdin = nil
	cmd.Stderr = v.stderr
	isolate(cmd)
	cmd.Cancel = func() error {
		// The whole group, so a program that started others leaves none running.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 2 * time.Second
	out := &limited{max: maxCommandOutput, onFull: cancel}
	cmd.Stdout = out
	defer func() {
		for i := range out.buf {
			out.buf[i] = 0
		}
	}()

	err = cmd.Run()
	switch {
	case out.over:
		return "", fmt.Errorf("the keychain command for %q printed more than %d bytes, which is too much for a login", handle, maxCommandOutput)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", fmt.Errorf("the keychain command for %q did not finish within %s", handle, v.timeout)
	case ctx.Err() != nil:
		return "", fmt.Errorf("the keychain command for %q was stopped: %w", handle, ctx.Err())
	case err != nil:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if code := exit.ExitCode(); code >= 0 {
				return "", fmt.Errorf("the keychain command for %q failed with exit status %d", handle, code)
			}
			return "", fmt.Errorf("the keychain command for %q was ended by a signal", handle)
		}
		return "", fmt.Errorf("the keychain command could not be run: %w", err)
	}

	val := string(out.buf)
	switch {
	case strings.HasSuffix(val, "\r\n"):
		val = val[:len(val)-2]
	case strings.HasSuffix(val, "\n"):
		val = val[:len(val)-1]
	}
	if val == "" {
		return "", fmt.Errorf("the keychain command for %q printed nothing for the field %q", handle, field)
	}
	return val, nil
}
