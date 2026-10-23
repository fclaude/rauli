package vault

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// run calls the command line and returns what it wrote to each stream.
func run(t *testing.T, cfg Config, stdin string, args ...string) (out, errOut string, err error) {
	t.Helper()
	var o, e bytes.Buffer
	err = Run(ctx, cfg, args, strings.NewReader(stdin), &o, &e)
	return o.String(), e.String(), err
}

func TestTheCommandLineManagesLoginsOnMemoryAndFileBackends(t *testing.T) {
	t.Setenv("RAULI_TEST_PASSPHRASE", passwd)
	configs := map[string]Config{
		"memory": {Backend: "memory", mem: newMemStore()},
		"file":   {Backend: "file", Path: filepath.Join(t.TempDir(), "kc"), PassphraseEnv: "RAULI_TEST_PASSPHRASE"},
	}
	for name, cfg := range configs {
		t.Run(name, func(t *testing.T) {
			var streams []string
			collect := func(out, errOut string) { streams = append(streams, out, errOut) }

			out, errOut, err := run(t, cfg, "alice\n"+pw+"\n"+rfcSeed+"\n",
				"add", "--origin", "https://bank.example", "--origin", "https://*.bank.example", "bank", "user", "password", "totp")
			collect(out, errOut)
			if err != nil || !strings.Contains(out, "Added bank (user, password, totp)") || !strings.Contains(out, "https://bank.example, https://*.bank.example") {
				t.Fatalf("add: %q %q %v", out, errOut, err)
			}

			out, errOut, err = run(t, cfg, "", "list")
			collect(out, errOut)
			for _, want := range []string{"NAME", "bank", "password, totp, user", "https://bank.example, https://*.bank.example"} {
				if err != nil || !strings.Contains(out, want) {
					t.Fatalf("list lacks %q: %q %v", want, out, err)
				}
			}

			out, errOut, err = run(t, cfg, "", "show", "bank")
			collect(out, errOut)
			if err != nil || !strings.Contains(out, "name: bank") || !strings.Contains(out, "fields: password, totp, user") {
				t.Fatalf("show: %q %v", out, err)
			}

			out, errOut, err = run(t, cfg, "", "test", "bank", "https://www.bank.example/login?x=1")
			collect(out, errOut)
			if err != nil || !strings.Contains(out, "ok: bank can fill password, totp, user on https://www.bank.example/login") || strings.Contains(out, "x=1") {
				t.Fatalf("test: %q %v", out, err)
			}
			_, _, err = run(t, cfg, "", "test", "bank", "https://evil.example/")
			if err == nil || !strings.Contains(err.Error(), "may not be used") {
				t.Fatalf("test on the wrong page: %v", err)
			}

			// Mistakes: each is an error that says what to do, and none touches the keychain.
			for desc, tt := range map[string]struct {
				stdin string
				args  []string
				want  string
			}{
				"no origins":        {"x\n", []string{"add", "loose", "password"}, "--anywhere"},
				"a duplicate":       {"a\nb\nc\n", []string{"add", "--origin", "https://bank.example", "bank", "user", "password", "totp"}, "--replace"},
				"flags after name":  {"x\n", []string{"add", "w", "password", "--origin", "https://a.example"}, "put options before the name"},
				"no fields":         {"", []string{"add", "--origin", "https://a.example", "w"}, "name its fields"},
				"repeated field":    {"a\nb\n", []string{"add", "--origin", "https://a.example", "w", "password", "password"}, "named twice"},
				"missing value":     {"only-one\n", []string{"add", "--origin", "https://a.example", "w", "user", "password"}, "expected a value for password"},
				"bad origin":        {"x\n", []string{"add", "--origin", "example.com", "w", "password"}, "no scheme"},
				"unknown command":   {"", []string{"frobnicate"}, "not a keychain command"},
				"remove unknown":    {"", []string{"remove", "nobody"}, "no login called"},
				"show without name": {"", []string{"show"}, "name the login"},
				"bad option":        {"", []string{"add", "--bogus", "x", "password"}, "bogus"},
			} {
				out, errOut, err := run(t, cfg, tt.stdin, tt.args...)
				collect(out, errOut)
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Errorf("%s: got %v, want an error mentioning %q", desc, err, tt.want)
				}
				if err != nil {
					streams = append(streams, err.Error())
				}
			}
			if out, _, _ := run(t, cfg, "", "list"); strings.Contains(out, "loose") || strings.Contains(out, " w ") {
				t.Errorf("a refused add left a login behind:\n%s", out)
			}

			// A login for anywhere is created only on purpose, and replacing needs a flag.
			if _, _, err := run(t, cfg, "s3\n", "add", "--anywhere", "loose", "password"); err != nil {
				t.Fatalf("add --anywhere: %v", err)
			}
			if out, _, _ := run(t, cfg, "", "show", "loose"); !strings.Contains(out, "origins: anywhere") {
				t.Fatalf("show: %q", out)
			}
			if _, _, err := run(t, cfg, "new-one\n", "add", "--anywhere", "--replace", "loose", "password"); err != nil {
				t.Fatalf("replace: %v", err)
			}

			out, errOut, err = run(t, cfg, "", "remove", "bank")
			collect(out, errOut)
			if err != nil || !strings.Contains(out, "Removed bank") {
				t.Fatalf("remove: %q %v", out, err)
			}
			if out, _, _ := run(t, cfg, "", "list"); strings.Contains(out, "bank") {
				t.Fatalf("a removed login is still listed:\n%s", out)
			}

			// Not one stream, in the whole run, held a value.
			all := strings.Join(streams, "\n")
			for _, secret := range []string{pw, rfcSeed, "alice", "only-one", "s3", "new-one"} {
				if strings.Contains(all, secret) {
					t.Errorf("a value reached the output: %q", secret)
				}
			}
		})
	}
}

func TestTheCommandLineWithNoKeychainSaysHowToSetOneUp(t *testing.T) {
	_, _, err := run(t, Config{}, "", "list")
	if err == nil || !strings.Contains(err.Error(), "[keychain]") {
		t.Fatalf("got %v", err)
	}
	out, _, err := run(t, Config{}, "", "help")
	if err != nil || !strings.Contains(out, "Usage: rauli keychain") || !strings.Contains(out, "--anywhere") {
		t.Fatalf("help: %q %v", out, err)
	}
	out, _, err = run(t, Config{}, "")
	if err == nil || !strings.Contains(out, "Usage") {
		t.Fatalf("no arguments should show the usage and an error, got %q %v", out, err)
	}
}

func TestTheKeyringBackendListsThroughItsIndex(t *testing.T) {
	cfg := Config{Backend: "keyring", Service: "rauli-cli-test"}
	keyringMock(t)
	if _, _, err := run(t, cfg, "pw1\n", "add", "--origin", "https://a.example", "a", "password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, cfg, "pw2\n", "add", "--origin", "https://b.example", "b", "password"); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, cfg, "", "list")
	if err != nil || !strings.Contains(out, "a  ") || !strings.Contains(out, "b  ") {
		t.Fatalf("list: %q %v", out, err)
	}
	if _, _, err := run(t, cfg, "", "remove", "a"); err != nil {
		t.Fatal(err)
	}
	if out, _, _ := run(t, cfg, "", "list"); strings.Contains(out, "https://a.example") || !strings.Contains(out, "https://b.example") {
		t.Fatalf("after removal: %q", out)
	}
}
