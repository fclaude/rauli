// Package vault is the keychain: the logins an agent may use without ever
// seeing them. It implements tools.Vault.
//
// A login has a name, fields (user, password, a one-time code seed, anything
// else) and the origins where it may be used. The agent asks for a field by
// the login's name; the tool types the value into the page and reports only
// that it did. The vault refuses a page outside the login's origins, so a page
// that tricks an agent into visiting it cannot collect a password meant for
// another site. A login with no origins is refused at creation unless the
// person says anywhere on purpose.
//
// Four backends hold the logins: the operating system's secret store
// (keyring), an encrypted file (file), a program of the person's choosing that
// prints one field when asked (command), and memory (memory, for tests). The
// first three keep the values themselves; with the command backend the values
// stay in the person's password manager and Raulí only knows where each login
// may be used. The vault keeps no logins in memory beyond a single call.
package vault

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/fclaude/rauli/internal/tools"
	"github.com/fclaude/rauli/internal/urlmatch"
)

// Config is the keychain section of the configuration file.
//
//	[keychain]
//	backend = "file"                       # keyring, file, command or memory
//	path = "~/.config/rauli/keychain"      # for file
//	passphrase_env = "RAULI_PASSPHRASE"    # for file: where to read the passphrase
//	passphrase_command = ["fetch-secret", "rauli"]   # or a command that prints it
//	service = "rauli"                      # for keyring
//
// The file backend asks for its passphrase on the terminal when neither the
// environment variable nor the command gives one.
//
// The command backend keeps the logins' secrets elsewhere, in a password
// manager or any program that can print one value. Raulí holds each login's
// name, fields and origins in the configuration, and runs the command to fetch
// a single field at the moment it is typed into a page:
//
//	[keychain]
//	backend = "command"
//	command = ["/usr/local/bin/get-secret"]   # Raulí appends the login and the field
//	timeout = "10s"                           # optional, 10s by default
//
//	[[keychain.handles]]
//	name = "shop"
//	fields = ["user", "password", "totp"]
//	origins = ["https://shop.example.com"]    # or anywhere = true, on purpose
//
// For the login "shop" and its field "password" the program is run as
// get-secret shop password, with RAULI_HANDLE, RAULI_FIELD and RAULI_ORIGIN set
// in its environment, no standard input, and an empty directory to work in. It
// prints the value on standard output; one trailing newline is removed, and at
// most 64 KiB is read. A one-time-code field holds the code itself here, so the
// program is the one that works it out: a totp field is not a seed, as it is in
// the other backends. The program is only run for a page the login's origins
// allow, so a page outside them never reaches it.
type Config struct {
	Backend           string         `toml:"backend"`
	Service           string         `toml:"service"`
	Path              string         `toml:"path"`
	PassphraseEnv     string         `toml:"passphrase_env"`
	PassphraseCommand []string       `toml:"passphrase_command"`
	Command           []string       `toml:"command"`
	Timeout           string         `toml:"timeout"`
	Handles           []HandleConfig `toml:"handles"`

	mem *memStore // lets tests keep a memory keychain between runs
}

// HandleConfig is one login of the command backend: what it is called, which
// fields it has and where it may be used. It holds no secret.
type HandleConfig struct {
	Name     string   `toml:"name"`
	Fields   []string `toml:"fields"`
	Origins  []string `toml:"origins"`
	Anywhere bool     `toml:"anywhere"`
}

// ConfigError is a mistake in the keychain configuration. Key says where, as a
// path such as keychain.handles[1].origins, so whoever read the file can point
// at the line.
type ConfigError struct {
	Key string
	Msg string
}

func (e *ConfigError) Error() string { return e.Key + ": " + e.Msg }

func configErr(key, format string, args ...any) error {
	return &ConfigError{Key: key, Msg: fmt.Sprintf(format, args...)}
}

// Validate checks the Config.
func (c Config) Validate() error {
	switch c.Backend {
	case "", "keyring", "memory", "file", "command":
	default:
		return configErr("keychain.backend", "%q is not keyring, file, command or memory", c.Backend)
	}
	if c.Backend != "file" && (c.Path != "" || c.PassphraseEnv != "" || len(c.PassphraseCommand) > 0) {
		return errors.New("keychain: path and the passphrase settings belong to the file backend")
	}
	if c.Backend != "command" {
		switch {
		case len(c.Command) > 0:
			return configErr("keychain.command", "the command belongs to the command backend; set backend = \"command\" or remove it")
		case c.Timeout != "":
			return configErr("keychain.timeout", "the timeout belongs to the command backend; set backend = \"command\" or remove it")
		case len(c.Handles) > 0:
			return configErr("keychain.handles", "logins listed in the configuration belong to the command backend; set backend = \"command\" or remove them")
		}
		return nil
	}
	return c.validateCommand()
}

func (c Config) filePath() (string, error) {
	p := c.Path
	if p == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "rauli", "keychain"), nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p, nil
}

// passphrase gets the file's passphrase from the environment, a command or the
// terminal, in that order. creating asks twice on a terminal.
func (c Config) passphrase(ctx context.Context, creating bool) ([]byte, error) {
	if c.PassphraseEnv != "" {
		if v := os.Getenv(c.PassphraseEnv); v != "" {
			return []byte(v), nil
		}
	}
	if len(c.PassphraseCommand) > 0 {
		out, err := exec.CommandContext(ctx, c.PassphraseCommand[0], c.PassphraseCommand[1:]...).Output()
		if err != nil {
			return nil, fmt.Errorf("the passphrase command %q failed: %w", c.PassphraseCommand[0], err)
		}
		out = []byte(strings.TrimRight(string(out), "\r\n"))
		if len(out) == 0 {
			return nil, fmt.Errorf("the passphrase command %q printed nothing", c.PassphraseCommand[0])
		}
		return out, nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, errors.New("there is no passphrase for the keychain file: set keychain.passphrase_env or keychain.passphrase_command, or run where there is a terminal")
	}
	defer tty.Close()
	ask := func(prompt string) ([]byte, error) {
		fmt.Fprint(tty, prompt)
		b, err := term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(tty)
		return b, err
	}
	p, err := ask("Keychain passphrase: ")
	if err != nil {
		return nil, err
	}
	if creating {
		again, err := ask("Again: ")
		if err != nil {
			return nil, err
		}
		if string(again) != string(p) {
			return nil, errors.New("the two passphrases differ")
		}
	}
	if len(p) == 0 {
		return nil, errors.New("the passphrase is empty")
	}
	return p, nil
}

// Keychain is the open keychain. It implements tools.Vault, and the command
// line uses its other methods.
type Keychain struct {
	st  store
	now func() time.Time
}

var _ tools.Vault = (*Keychain)(nil)

// Open opens the keychain the Config names. A nil Vault with a nil error means
// no keychain is configured.
func Open(ctx context.Context, c Config) (tools.Vault, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Backend == "command" {
		return newCommandVault(c)
	}
	k, err := open(ctx, c)
	if err != nil || k == nil {
		return nil, err
	}
	return k, nil
}

func open(ctx context.Context, c Config) (*Keychain, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	switch c.Backend {
	case "":
		return nil, nil
	case "memory":
		if c.mem == nil {
			c.mem = newMemStore()
		}
		return &Keychain{st: c.mem, now: time.Now}, nil
	case "keyring":
		return &Keychain{st: newKeyringStore(c.Service), now: time.Now}, nil
	}
	path, err := c.filePath()
	if err != nil {
		return nil, err
	}
	_, statErr := os.Stat(path)
	pass, err := c.passphrase(ctx, errors.Is(statErr, os.ErrNotExist))
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range pass {
			pass[i] = 0
		}
	}()
	fs, err := openFile(path, pass)
	if err != nil {
		return nil, err
	}
	return &Keychain{st: fs, now: time.Now}, nil
}

// Close lets go of what the keychain holds.
func (k *Keychain) Close() error {
	k.st.close()
	return nil
}

// parseOrigin checks a place a login may be used. It names its scheme, so a
// login is never usable over a scheme the person did not think of.
func parseOrigin(s string) (*urlmatch.Pattern, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		return nil, fmt.Errorf("%q has no scheme: write it like https://example.com", s)
	}
	if strings.HasPrefix(s, "*://") {
		return nil, fmt.Errorf("%q: write the scheme as https or http", s)
	}
	p, err := urlmatch.Parse(s, true)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", s, err)
	}
	// localhost on any port would make a login usable by whatever listens there.
	if u, perr := url.Parse(strings.Replace(s, "*", "x", -1)); perr == nil && strings.EqualFold(u.Hostname(), "localhost") && u.Port() == "" {
		return nil, fmt.Errorf("%q: give the port of a localhost origin, such as http://localhost:3000, or :* for any", s)
	}
	return p, nil
}

func originsAllow(origins []string, pageURL string) bool {
	for _, o := range origins {
		if p, err := parseOrigin(o); err == nil && p.Match(pageURL) {
			return true
		}
	}
	return false
}

// shownOrigin is an address without its query, for errors.
func shownOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "that page"
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

// Handles lists the logins, without values.
func (k *Keychain) Handles(ctx context.Context) ([]tools.Handle, error) {
	names, err := k.st.list()
	if err != nil {
		return nil, err
	}
	out := make([]tools.Handle, 0, len(names))
	for _, n := range names {
		h, err := k.Show(ctx, n)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// Show returns what is known about one login, without values.
func (k *Keychain) Show(_ context.Context, name string) (tools.Handle, error) {
	e, err := k.st.get(name)
	if errors.Is(err, errNotFound) {
		return tools.Handle{}, fmt.Errorf("there is no login called %q", name)
	}
	if err != nil {
		return tools.Handle{}, err
	}
	defer e.wipe()
	return tools.Handle{Name: e.Name, Fields: e.fieldNames(), Origins: append([]string(nil), e.Origins...)}, nil
}

// Secret returns one field of a login for use on the page at pageURL.
func (k *Keychain) Secret(_ context.Context, handle, field, pageURL string) (string, error) {
	e, err := k.st.get(handle)
	if errors.Is(err, errNotFound) {
		return "", fmt.Errorf("there is no login called %q", handle)
	}
	if err != nil {
		return "", err
	}
	defer e.wipe()
	if err := k.check(e, field, pageURL); err != nil {
		return "", err
	}
	v := e.Fields[field]
	if field == "totp" {
		code, err := totpCode(v.Reveal(), k.now())
		if err != nil {
			return "", fmt.Errorf("the login %q: %w", handle, err)
		}
		return code, nil
	}
	return v.Reveal(), nil
}

// check says whether a field of an entry may be used on a page.
func (k *Keychain) check(e *entry, field, pageURL string) error {
	return checkUse(e.Name, e.Origins, e.Anywhere, e.fieldNames(), field, pageURL)
}

// checkUse says whether a field of a login may be used on a page. Every
// backend asks it before anything is read, so the rule is the same for all.
func checkUse(name string, origins []string, anywhere bool, fields []string, field, pageURL string) error {
	if !anywhere {
		if len(origins) == 0 {
			return fmt.Errorf("the login %q has no origins and is not marked as usable anywhere, so it cannot be used", name)
		}
		if !originsAllow(origins, pageURL) {
			return fmt.Errorf("the login %q may not be used on %s: it is for %s", name, shownOrigin(pageURL), strings.Join(origins, ", "))
		}
	}
	for _, f := range fields {
		if f == field {
			return nil
		}
	}
	return fmt.Errorf("the login %q has no field %q: it has %s", name, field, strings.Join(fields, ", "))
}

// Test says which fields of a login can be used on a page, without computing
// or showing any value except to check that a one-time code seed works.
func (k *Keychain) Test(_ context.Context, name, pageURL string) ([]string, error) {
	e, err := k.st.get(name)
	if errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("there is no login called %q", name)
	}
	if err != nil {
		return nil, err
	}
	defer e.wipe()
	var ok []string
	for _, f := range e.fieldNames() {
		if err := k.check(e, f, pageURL); err != nil {
			return nil, err
		}
		if f == "totp" {
			if _, err := totpCode(e.Fields[f].Reveal(), k.now()); err != nil {
				return nil, fmt.Errorf("the login %q: %w", name, err)
			}
		}
		ok = append(ok, f)
	}
	sort.Strings(ok)
	return ok, nil
}

// AddOptions describe a login to create.
type AddOptions struct {
	Name    string
	Origins []string
	// Anywhere says the login may be used on any site. Creating one without
	// origins needs it, because that is a decision to make on purpose.
	Anywhere bool
	// Replace allows overwriting a login that exists.
	Replace bool
	Fields  map[string]Secret
}

// Add creates a login.
func (k *Keychain) Add(_ context.Context, o AddOptions) error {
	e := &entry{Name: o.Name, Origins: o.Origins, Anywhere: o.Anywhere, Fields: o.Fields}
	if err := e.validate(); err != nil {
		return err
	}
	for _, origin := range o.Origins {
		if _, err := parseOrigin(origin); err != nil {
			return fmt.Errorf("origin %w", err)
		}
	}
	switch {
	case len(o.Origins) == 0 && !o.Anywhere:
		return errors.New("a login needs the origins where it may be used, such as --origin https://example.com. A login usable on every site is a decision to make on purpose: pass --anywhere if that is what you mean")
	case len(o.Origins) > 0 && o.Anywhere:
		return errors.New("a login is for its origins or for anywhere. Pass --origin or --anywhere and leave out the other")
	}
	if f, ok := e.Fields["totp"]; ok {
		if _, err := totpCode(f.Reveal(), k.now()); err != nil {
			return fmt.Errorf("the totp field: %w", err)
		}
	}
	if !o.Replace {
		if old, err := k.st.get(o.Name); err == nil {
			old.wipe()
			return fmt.Errorf("there is already a login called %q: pass --replace to overwrite it", o.Name)
		} else if !errors.Is(err, errNotFound) {
			return err
		}
	}
	return k.st.put(e)
}

// Remove deletes a login.
func (k *Keychain) Remove(_ context.Context, name string) error {
	err := k.st.del(name)
	if errors.Is(err, errNotFound) {
		return fmt.Errorf("there is no login called %q", name)
	}
	return err
}
