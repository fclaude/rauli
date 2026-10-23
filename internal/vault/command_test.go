package vault

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const secretValue = "s3cret-pw-1"

// program writes a stand-in for a password manager's command: a shell script
// that sees the login and the field as its last two arguments, writes a line to
// the file named by RAULI_TEST_LOG each time it runs, and then does body.
func program(t *testing.T, body string) string {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh to write a fake keychain command with")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "ran.log")
	t.Setenv("RAULI_TEST_LOG", log)
	path := filepath.Join(dir, "get-secret")
	script := "#!/bin/sh\n" +
		`echo "ran h=$RAULI_HANDLE f=$RAULI_FIELD o=$RAULI_ORIGIN pwd=$(pwd) args=$#" >> "$RAULI_TEST_LOG"` + "\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func shopConfig(command ...string) Config {
	return Config{
		Backend: "command",
		Command: command,
		Handles: []HandleConfig{{
			Name:    "shop",
			Fields:  []string{"user", "password", "totp", "one", "none", "crlf"},
			Origins: []string{"https://shop.example"},
		}},
	}
}

func openCommand(t *testing.T, cfg Config) *commandVault {
	t.Helper()
	v, err := newCommandVault(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func ran(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(os.Getenv("RAULI_TEST_LOG"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const page = "https://shop.example/login"

func TestTheCommandIsRunForOneFieldWithTheLoginAndTheFieldAsItsLastArguments(t *testing.T) {
	prog := program(t, `
case "$2" in
  password) echo `+secretValue+` ;;
  user) echo alice ;;
esac
`)
	v := openCommand(t, shopConfig(prog))
	got, err := v.Secret(context.Background(), "shop", "password", "https://shop.example/login?token=abc#frag")
	if err != nil || got != secretValue {
		t.Fatalf("got %q, %v", got, err)
	}
	log := ran(t)
	// $1 is the login and $2 the field, because the command had no leading arguments.
	if !strings.Contains(log, "h=shop f=password o=https://shop.example ") {
		t.Fatalf("the environment the program saw: %q", log)
	}
	if strings.Contains(log, "token") || strings.Contains(log, "frag") {
		t.Fatalf("the program was told more of the page than its origin: %q", log)
	}
	if !strings.Contains(log, "args=2") {
		t.Fatalf("the program should get just the login and the field: %q", log)
	}
	// It runs in an empty directory of its own, not where the test is.
	wd, _ := os.Getwd()
	if strings.Contains(log, "pwd="+wd+" ") || !strings.Contains(log, "rauli-keychain-") {
		t.Fatalf("the program ran in %q", log)
	}
	if _, err := os.Stat(strings.TrimSpace(strings.SplitN(strings.SplitN(log, "pwd=", 2)[1], " ", 2)[0])); err == nil {
		t.Error("the directory the program ran in is still there")
	}
}

func TestLeadingArgumentsComeFirstAndAreNeverReadByAShell(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "PWNED")
	prog := program(t, `echo `+secretValue+"\n")
	// The second word of the command is one argument, whatever it contains.
	v := openCommand(t, shopConfig(prog, "a b; touch "+marker, "$(touch "+marker+")"))
	if got, err := v.Secret(context.Background(), "shop", "password", page); err != nil || got != secretValue {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command line was read by a shell")
	}
	if !strings.Contains(ran(t), "args=4") {
		t.Fatalf("two leading arguments, the login and the field make four: %q", ran(t))
	}
}

func TestExactlyOneTrailingNewlineIsRemoved(t *testing.T) {
	prog := program(t, `
case "$2" in
  one) printf 'pw\n\n' ;;
  none) printf 'pw' ;;
  crlf) printf 'pw\r\n' ;;
esac
`)
	v := openCommand(t, shopConfig(prog))
	for field, want := range map[string]string{"one": "pw\n", "none": "pw", "crlf": "pw"} {
		got, err := v.Secret(context.Background(), "shop", field, page)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", field, got, err, want)
		}
	}
}

func TestAPageOutsideTheOriginsNeverReachesTheCommand(t *testing.T) {
	prog := program(t, `echo `+secretValue+"\n")
	v := openCommand(t, shopConfig(prog))
	for _, c := range []struct{ login, field, page, want string }{
		{"shop", "password", "https://evil.example/login", "may not be used on https://evil.example/login"},
		{"shop", "password", "http://shop.example/login", "may not be used"},
		{"shop", "nothing", page, `has no field "nothing"`},
		{"nobody", "password", page, `no login called "nobody"`},
	} {
		got, err := v.Secret(context.Background(), c.login, c.field, c.page)
		if err == nil || !strings.Contains(err.Error(), c.want) || got != "" {
			t.Errorf("%+v: got %q, %v", c, got, err)
		}
	}
	if log := ran(t); log != "" {
		t.Fatalf("the command ran for a request that should have been refused first: %q", log)
	}
}

func TestACommandThatNeverFinishesIsEndedWithEverythingItStarted(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pid")
	t.Setenv("RAULI_TEST_PIDS", pids)
	prog := program(t, `sleep 60 & echo $! > "$RAULI_TEST_PIDS"; wait`+"\n")
	cfg := shopConfig(prog)
	cfg.Timeout = "400ms"
	v := openCommand(t, cfg)

	start := time.Now()
	got, err := v.Secret(context.Background(), "shop", "password", page)
	if err == nil || !strings.Contains(err.Error(), "did not finish within 400ms") || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
	if took := time.Since(start); took > 6*time.Second {
		t.Fatalf("the timeout took %v", took)
	}
	b, err := os.ReadFile(pids)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("a process the command started (%d) is still running", pid)
}

func TestTooMuchOutputIsRefusedWithoutQuotingIt(t *testing.T) {
	prog := program(t, `head -c 200000 /dev/zero | tr '\0' 'x'`+"\n")
	v := openCommand(t, shopConfig(prog))
	got, err := v.Secret(context.Background(), "shop", "password", page)
	if err == nil || !strings.Contains(err.Error(), "more than 65536 bytes") || got != "" {
		t.Fatalf("got %d bytes, %v", len(got), err)
	}
	if strings.Contains(err.Error(), "xxxx") {
		t.Fatal("the error quotes the output")
	}
}

func TestNothingPrintedAndAFailureAreErrors(t *testing.T) {
	prog := program(t, `
case "$2" in
  user) exit 0 ;;
  password) echo `+secretValue+`; echo "hint: the vault is locked, run unlock-xyz" >&2; exit 3 ;;
  totp) printf '\n' ;;
esac
`)
	v := openCommand(t, shopConfig(prog))
	for field, want := range map[string]string{
		"user":     "printed nothing",
		"totp":     "printed nothing",
		"password": "failed with exit status 3",
	} {
		got, err := v.Secret(context.Background(), "shop", field, page)
		if err == nil || !strings.Contains(err.Error(), want) || got != "" {
			t.Fatalf("%s: got %q, %v; want %q", field, got, err, want)
		}
		// What a failing program says is no business of an agent: it can hint at the person's vault.
		for _, leaked := range []string{secretValue, "unlock-xyz", "hint"} {
			if strings.Contains(err.Error(), leaked) {
				t.Fatalf("%s: the error repeats %q: %v", field, leaked, err)
			}
		}
	}
}

func TestAProgramThatCannotBeRunIsAnError(t *testing.T) {
	v := openCommand(t, shopConfig("/nonexistent/get-secret"))
	_, err := v.Secret(context.Background(), "shop", "password", page)
	if err == nil || !strings.Contains(err.Error(), "could not be run") {
		t.Fatalf("got %v", err)
	}
}

func TestTheKeychainCommandLineShowsNoValueAndAddAndRemoveSayWhy(t *testing.T) {
	prog := program(t, `
echo `+secretValue+`
echo "debug: asked for $2 of $1" >&2
`)
	cfg := shopConfig(prog)
	run := func(args ...string) (string, string, error) {
		var out, errOut bytes.Buffer
		err := Run(context.Background(), cfg, args, &noInput{t}, &out, &errOut)
		return out.String(), errOut.String(), err
	}

	out, errOut, err := run("list")
	if err != nil || !strings.Contains(out, "shop") || !strings.Contains(out, "https://shop.example") {
		t.Fatalf("list: %q %q %v", out, errOut, err)
	}
	out2, errOut2, err := run("show", "shop")
	if err != nil || !strings.Contains(out2, "fields: user, password") {
		t.Fatalf("show: %q %q %v", out2, errOut2, err)
	}
	// Test runs the program for every field, shows no value, and lets its standard error through.
	out3, errOut3, err := run("test", "shop", "https://shop.example/login")
	if err != nil || !strings.Contains(out3, "ok: shop can fill") {
		t.Fatalf("test: %q %q %v", out3, errOut3, err)
	}
	if !strings.Contains(errOut3, "debug: asked for password of shop") {
		t.Fatalf("the program's standard error should reach the person running test: %q", errOut3)
	}
	for _, stream := range []string{out, errOut, out2, errOut2, out3, errOut3} {
		if strings.Contains(stream, secretValue) {
			t.Fatalf("a stream holds the value: %q", stream)
		}
	}
	if _, _, err := run("test", "shop", "https://evil.example/"); err == nil || !strings.Contains(err.Error(), "may not be used") {
		t.Fatalf("test on the wrong page: %v", err)
	}

	for _, args := range [][]string{{"add", "--origin", "https://x.example", "x", "password"}, {"remove", "shop"}} {
		_, _, err := run(args...)
		if err == nil || !strings.Contains(err.Error(), "[[keychain.handles]]") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

// noInput fails the test if the command line reads from it: add and remove must
// say no before they ask for a value that has nowhere to go.
type noInput struct{ t *testing.T }

func (n *noInput) Read([]byte) (int, error) {
	n.t.Helper()
	n.t.Error("standard input was read")
	return 0, errors.New("not to be read")
}

func TestTheAgentSeesNothingOfAFailingProgramsStandardError(t *testing.T) {
	prog := program(t, `echo "hint: unlock-xyz" >&2; exit 1`+"\n")
	v := openCommand(t, shopConfig(prog))
	var errOut bytes.Buffer
	v.stderr = nil
	if _, err := v.Secret(context.Background(), "shop", "password", page); err == nil || strings.Contains(err.Error(), "unlock-xyz") {
		t.Fatalf("got %v", err)
	}
	// While a person tests the login, the same text is for them.
	v.stderr = &errOut
	_, _ = v.Test(context.Background(), "shop", page)
	if !strings.Contains(errOut.String(), "unlock-xyz") {
		t.Fatalf("the person should see why the program failed: %q", errOut.String())
	}
}

func TestOpenGivesATypedVaultWithoutTouchingTheOtherBackends(t *testing.T) {
	prog := program(t, `echo `+secretValue+"\n")
	vault, err := Open(context.Background(), shopConfig(prog))
	if err != nil {
		t.Fatal(err)
	}
	hs, err := vault.Handles(context.Background())
	if err != nil || len(hs) != 1 || hs[0].Name != "shop" || len(hs[0].Fields) != 6 {
		t.Fatalf("handles: %+v %v", hs, err)
	}
	got, err := vault.Secret(context.Background(), "shop", "password", page)
	if err != nil || got != secretValue {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestTheCommandBackendsSettingsAreChecked(t *testing.T) {
	ok := func() Config {
		return Config{Backend: "command", Command: []string{"get"}, Handles: []HandleConfig{{Name: "shop", Fields: []string{"user"}, Origins: []string{"https://shop.example"}}}}
	}
	cases := []struct {
		name string
		mod  func(*Config)
		key  string
		text string
	}{
		{"no program", func(c *Config) { c.Command = nil }, "keychain.command", "needs a program"},
		{"empty program", func(c *Config) { c.Command = []string{""} }, "keychain.command", "needs a program"},
		{"bad timeout", func(c *Config) { c.Timeout = "soon" }, "keychain.timeout", "not a length of time"},
		{"negative timeout", func(c *Config) { c.Timeout = "-1s" }, "keychain.timeout", "not a length of time"},
		{"bad name", func(c *Config) { c.Handles[0].Name = "Shop!" }, "keychain.handles[0].name", "not a name"},
		{"twice", func(c *Config) { c.Handles = append(c.Handles, c.Handles[0]) }, "keychain.handles[1].name", "already a login"},
		{"no fields", func(c *Config) { c.Handles[0].Fields = nil }, "keychain.handles[0].fields", "at least one field"},
		{"bad field", func(c *Config) { c.Handles[0].Fields = []string{"User"} }, "keychain.handles[0].fields", "not a name for a field"},
		{"field twice", func(c *Config) { c.Handles[0].Fields = []string{"user", "user"} }, "keychain.handles[0].fields", "named twice"},
		{"bad origin", func(c *Config) { c.Handles[0].Origins = []string{"shop.example"} }, "keychain.handles[0].origins", "no scheme"},
		{"no origins", func(c *Config) { c.Handles[0].Origins = nil }, "keychain.handles[0].origins", "anywhere = true"},
		{"both", func(c *Config) { c.Handles[0].Anywhere = true }, "keychain.handles[0].anywhere", "or for anywhere"},
	}
	if err := ok().Validate(); err != nil {
		t.Fatalf("the good configuration is refused: %v", err)
	}
	for _, c := range cases {
		cfg := ok()
		c.mod(&cfg)
		err := cfg.Validate()
		var ce *ConfigError
		if !errors.As(err, &ce) || ce.Key != c.key || !strings.Contains(ce.Msg, c.text) {
			t.Errorf("%s: got %v, want key %s and %q", c.name, err, c.key, c.text)
		}
	}
	// A login usable anywhere is accepted when it says so.
	cfg := ok()
	cfg.Handles[0].Origins, cfg.Handles[0].Anywhere = nil, true
	if err := cfg.Validate(); err != nil {
		t.Errorf("anywhere = true: %v", err)
	}
	// The command's settings mean nothing to the other backends.
	for key, c := range map[string]Config{
		"keychain.command": {Backend: "file", Command: []string{"get"}},
		"keychain.timeout": {Backend: "keyring", Timeout: "5s"},
		"keychain.handles": {Backend: "memory", Handles: ok().Handles},
	} {
		var ce *ConfigError
		if err := c.Validate(); !errors.As(err, &ce) || ce.Key != key {
			t.Errorf("%s on %s: %v", key, c.Backend, err)
		}
	}
}
