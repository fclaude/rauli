package vault

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/zalando/go-keyring"
)

const (
	pw     = "hunter2-correct-horse"
	passwd = "file-passphrase-xyzzy"
)

var ctx = context.Background()

// backends builds a fresh keychain on each backend the vault supports.
func backends(t *testing.T) map[string]*Keychain {
	t.Helper()
	keyring.MockInit()
	t.Setenv("RAULI_TEST_PASSPHRASE", passwd)
	open := func(c Config) *Keychain {
		k, err := open(ctx, c)
		if err != nil || k == nil {
			t.Fatalf("open %s: %v", c.Backend, err)
		}
		t.Cleanup(func() { k.Close() })
		return k
	}
	return map[string]*Keychain{
		"memory":  open(Config{Backend: "memory"}),
		"file":    open(Config{Backend: "file", Path: filepath.Join(t.TempDir(), "kc"), PassphraseEnv: "RAULI_TEST_PASSPHRASE"}),
		"keyring": open(Config{Backend: "keyring", Service: "rauli-test-" + t.Name()}),
	}
}

func fields(kv ...string) map[string]Secret {
	m := map[string]Secret{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = Secret(kv[i+1])
	}
	return m
}

func TestALoginRoundTripsOnEveryBackend(t *testing.T) {
	for name, k := range backends(t) {
		t.Run(name, func(t *testing.T) {
			err := k.Add(ctx, AddOptions{Name: "shop", Origins: []string{"https://shop.example.com"}, Fields: fields("user", "alice", "password", pw)})
			if err != nil {
				t.Fatal(err)
			}
			err = k.Add(ctx, AddOptions{Name: "mail", Origins: []string{"https://mail.example.com", "https://*.mail.example.com"}, Fields: fields("password", "other-secret")})
			if err != nil {
				t.Fatal(err)
			}

			hs, err := k.Handles(ctx)
			if err != nil || len(hs) != 2 || hs[0].Name != "mail" || hs[1].Name != "shop" {
				t.Fatalf("handles: %+v, %v", hs, err)
			}
			if strings.Join(hs[1].Fields, ",") != "password,user" || len(hs[0].Origins) != 2 {
				t.Fatalf("handles: %+v", hs)
			}
			j, _ := json.Marshal(hs)
			if strings.Contains(string(j), pw) || strings.Contains(string(j), "other-secret") || strings.Contains(string(j), "alice") {
				t.Fatalf("a handle carries a value: %s", j)
			}

			got, err := k.Secret(ctx, "shop", "password", "https://shop.example.com/login?next=/")
			if err != nil || got != pw {
				t.Fatalf("Secret = %q, %v", got, err)
			}
			if got, _ := k.Secret(ctx, "shop", "user", "https://shop.example.com/"); got != "alice" {
				t.Fatalf("user = %q", got)
			}

			// A login is not overwritten by accident.
			dup := AddOptions{Name: "shop", Origins: []string{"https://shop.example.com"}, Fields: fields("password", "new")}
			if err := k.Add(ctx, dup); err == nil || !strings.Contains(err.Error(), "--replace") {
				t.Fatalf("a duplicate should need --replace, got %v", err)
			}
			dup.Replace = true
			dup.Fields = fields("password", "replaced-value")
			if err := k.Add(ctx, dup); err != nil {
				t.Fatal(err)
			}
			if got, _ := k.Secret(ctx, "shop", "password", "https://shop.example.com/"); got != "replaced-value" {
				t.Fatalf("replace did not take: %q", got)
			}

			if err := k.Remove(ctx, "shop"); err != nil {
				t.Fatal(err)
			}
			if _, err := k.Secret(ctx, "shop", "password", "https://shop.example.com/"); err == nil || !strings.Contains(err.Error(), "no login called") {
				t.Fatalf("a removed login: %v", err)
			}
			if err := k.Remove(ctx, "shop"); err == nil {
				t.Fatal("removing twice should fail")
			}
			if hs, _ := k.Handles(ctx); len(hs) != 1 || hs[0].Name != "mail" {
				t.Fatalf("after removal: %+v", hs)
			}
		})
	}
}

func TestOriginsAreExactUnlessAPatternSaysOtherwise(t *testing.T) {
	k := backends(t)["memory"]
	add := func(name string, origins ...string) {
		t.Helper()
		if err := k.Add(ctx, AddOptions{Name: name, Origins: origins, Fields: fields("password", pw)}); err != nil {
			t.Fatal(err)
		}
	}
	add("exact", "https://mail.example.com")
	add("sub", "https://*.example.com")
	add("port", "https://example.com:8443")
	add("dev", "http://localhost:3000")
	add("any-dev-port", "http://localhost:*")
	add("two", "https://a.example", "https://b.example")

	for _, tt := range []struct {
		login, page string
		ok          bool
	}{
		{"exact", "https://mail.example.com/inbox?q=1#x", true},
		{"exact", "https://mail.example.com:443/", true},
		{"exact", "https://mail.example.com:8443/", false},
		{"exact", "http://mail.example.com/", false},
		{"exact", "https://www.mail.example.com/", false},
		{"exact", "https://example.com/", false},
		{"exact", "https://mail.example.com.evil.net/", false},
		{"exact", "https://evil.net/https://mail.example.com/", false},
		{"exact", "about:blank", false},
		{"exact", "file:///etc/passwd", false},
		{"sub", "https://www.example.com/", true},
		{"sub", "https://a.b.example.com/", true},
		{"sub", "https://example.com/", false},
		{"sub", "https://evil-example.com/", false},
		{"port", "https://example.com:8443/x", true},
		{"port", "https://example.com/x", false},
		{"dev", "http://localhost:3000/app", true},
		{"dev", "http://127.0.0.1:3000/app", true},
		{"dev", "http://localhost:3001/app", false},
		{"any-dev-port", "http://localhost:9999/", true},
		{"two", "https://b.example/", true},
		{"two", "https://c.example/", false},
	} {
		_, err := k.Secret(ctx, tt.login, "password", tt.page)
		if (err == nil) != tt.ok {
			t.Errorf("%s on %s: allowed=%v, want %v (%v)", tt.login, tt.page, err == nil, tt.ok, err)
		}
		if err != nil && strings.Contains(err.Error(), pw) {
			t.Errorf("an error holds the secret: %v", err)
		}
	}
	// The refusal names the login, the page without its query, and where it is for.
	_, err := k.Secret(ctx, "exact", "password", "https://evil.example/steal?token=ABC")
	for _, want := range []string{`"exact"`, "https://evil.example/steal", "https://mail.example.com"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "ABC") {
		t.Fatalf("the refusal repeats the query string: %v", err)
	}
}

func TestBadOriginsAreRefusedWhenAdding(t *testing.T) {
	k := backends(t)["memory"]
	for origin, want := range map[string]string{
		"example.com":                  "no scheme",
		"*://example.com":              "https or http",
		"https://example.com/docs/*":   "no path",
		"http://localhost":             "port",
		"ftp://example.com":            "scheme",
		"https://exa mple.com":         "characters",
		"https://example.com:notaport": "port",
	} {
		err := k.Add(ctx, AddOptions{Name: "x", Origins: []string{origin}, Fields: fields("password", pw)})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("origin %q: got %v, want an error mentioning %q", origin, err, want)
		}
	}
}

func TestALoginNeedsOriginsOrAnExplicitAnywhere(t *testing.T) {
	k := backends(t)["memory"]
	err := k.Add(ctx, AddOptions{Name: "loose", Fields: fields("password", pw)})
	if err == nil || !strings.Contains(err.Error(), "--anywhere") || !strings.Contains(err.Error(), "on purpose") {
		t.Fatalf("a login with no origins should be refused with advice, got %v", err)
	}
	if err := k.Add(ctx, AddOptions{Name: "both", Origins: []string{"https://a.example"}, Anywhere: true, Fields: fields("password", pw)}); err == nil {
		t.Fatal("origins and anywhere together make no sense")
	}
	if err := k.Add(ctx, AddOptions{Name: "loose", Anywhere: true, Fields: fields("password", pw)}); err != nil {
		t.Fatal(err)
	}
	if got, err := k.Secret(ctx, "loose", "password", "https://anything.example/x"); err != nil || got != pw {
		t.Fatalf("an anywhere login should work anywhere: %q, %v", got, err)
	}
	hs, _ := k.Handles(ctx)
	if len(hs) != 1 || len(hs[0].Origins) != 0 {
		t.Fatalf("handles: %+v", hs)
	}
}

func TestNamesAndFieldsAreChecked(t *testing.T) {
	k := backends(t)["memory"]
	o := []string{"https://a.example"}
	for _, tt := range []struct {
		name string
		f    map[string]Secret
		want string
	}{
		{"Upper", fields("password", pw), "not a name for a login"},
		{"has space", fields("password", pw), "not a name for a login"},
		{"~index", fields("password", pw), "not a name for a login"},
		{"", fields("password", pw), "not a name for a login"},
		{"ok", fields("Password", pw), "not a name for a field"},
		{"ok", fields("pass word", pw), "not a name for a field"},
		{"ok", fields("password", ""), "is empty"},
		{"ok", map[string]Secret{}, "at least one field"},
	} {
		err := k.Add(ctx, AddOptions{Name: tt.name, Origins: o, Fields: tt.f})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("name %q fields %v: got %v, want %q", tt.name, tt.f, err, tt.want)
		}
	}
}

func TestMissingFieldsAndLoginsAreErrorsWithoutValues(t *testing.T) {
	k := backends(t)["memory"]
	k.Add(ctx, AddOptions{Name: "shop", Origins: []string{"https://shop.example.com"}, Fields: fields("user", "alice", "password", pw)})
	_, err := k.Secret(ctx, "shop", "pin", "https://shop.example.com/")
	if err == nil || !strings.Contains(err.Error(), `no field "pin"`) || !strings.Contains(err.Error(), "password, user") {
		t.Fatalf("a missing field: %v", err)
	}
	if _, err := k.Secret(ctx, "nobody", "password", "https://shop.example.com/"); err == nil || !strings.Contains(err.Error(), `no login called "nobody"`) {
		t.Fatalf("a missing login: %v", err)
	}
	if _, err := k.Show(ctx, "nobody"); err == nil {
		t.Fatal("showing a missing login should fail")
	}
}

func TestATotpFieldGivesTheCurrentCodeAndNeverTheSeed(t *testing.T) {
	k := backends(t)["memory"]
	k.now = func() time.Time { return time.Unix(1111111109, 0) }
	err := k.Add(ctx, AddOptions{Name: "bank", Origins: []string{"https://bank.example"}, Fields: fields("password", pw, "totp", rfcSeed)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := k.Secret(ctx, "bank", "totp", "https://bank.example/login")
	if err != nil || got != "081804" {
		t.Fatalf("the code at t=1111111109 should be 081804, got %q, %v", got, err)
	}
	k.now = func() time.Time { return time.Unix(1234567890, 0) }
	if got, _ := k.Secret(ctx, "bank", "totp", "https://bank.example/login"); got != "005924" {
		t.Fatalf("the code at t=1234567890 should be 005924, got %q", got)
	}
	// An unusable seed is refused when the login is stored, not at the moment it is needed.
	err = k.Add(ctx, AddOptions{Name: "bad", Origins: []string{"https://a.example"}, Fields: fields("totp", "!!!not-base32!!!")})
	if err == nil || strings.Contains(err.Error(), "not-base32") {
		t.Fatalf("a bad seed should be refused without repeating it, got %v", err)
	}
	// The code is checked against the origins like any field.
	if _, err := k.Secret(ctx, "bank", "totp", "https://evil.example/"); err == nil {
		t.Fatal("a one-time code must not be given to another site")
	}
}

func TestTestReportsWhatCanBeUsedWithoutShowingIt(t *testing.T) {
	k := backends(t)["memory"]
	k.Add(ctx, AddOptions{Name: "bank", Origins: []string{"https://bank.example"}, Fields: fields("user", "alice", "password", pw, "totp", rfcSeed)})
	fs, err := k.Test(ctx, "bank", "https://bank.example/login")
	if err != nil || strings.Join(fs, ",") != "password,totp,user" {
		t.Fatalf("Test = %v, %v", fs, err)
	}
	if _, err := k.Test(ctx, "bank", "https://evil.example/"); err == nil {
		t.Fatal("Test should refuse a page outside the origins")
	}
}

func TestOpenWithoutABackendReturnsNoVault(t *testing.T) {
	v, err := Open(ctx, Config{})
	if err != nil || v != nil {
		t.Fatalf("Open with no backend = %v, %v; want a nil vault and no error", v, err)
	}
	// The nil must be a true nil, so callers can test it.
	if v != nil {
		t.Fatal("a typed nil leaked into the interface")
	}
	if err := (Config{Backend: "telepathy"}).Validate(); err == nil || !strings.Contains(err.Error(), "keychain.backend") {
		t.Fatalf("Validate: %v", err)
	}
	if err := (Config{Backend: "keyring", Path: "/x"}).Validate(); err == nil {
		t.Fatal("a path belongs to the file backend")
	}
}

func keyringMock(t *testing.T) {
	t.Helper()
	keyring.MockInit()
}

func TestConfigReadsFromTheKeychainTableOfALargerFile(t *testing.T) {
	type file struct {
		Keychain Config `toml:"keychain"`
	}
	var f file
	dec := toml.NewDecoder(strings.NewReader(`[keychain]
backend = "file"
path = "~/vault"
passphrase_env = "RAULI_PASSPHRASE"
passphrase_command = ["pass", "show", "rauli"]
service = "x"
`))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		t.Fatal(err)
	}
	c := f.Keychain
	if c.Backend != "file" || c.Path != "~/vault" || c.PassphraseEnv != "RAULI_PASSPHRASE" || len(c.PassphraseCommand) != 3 || c.Service != "x" {
		t.Fatalf("read wrongly: %+v", c)
	}
	if err := toml.NewDecoder(strings.NewReader("[keychain]\nbackned = \"file\"\n")).DisallowUnknownFields().Decode(&f); err == nil {
		t.Fatal("a misspelt key in the keychain table should be an error")
	}
	// The unexported test hook is not part of the file format.
	if err := toml.NewDecoder(strings.NewReader("[keychain]\nmem = 1\n")).DisallowUnknownFields().Decode(&f); err == nil {
		t.Fatal("the test hook must not be settable from a file")
	}
}
