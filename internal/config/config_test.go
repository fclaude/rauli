package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAFileSetsTheBrowserAndRelativePathsFollowTheFile(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "config.toml", `
[browser]
profile = "state/profile"
download_dir = "/var/downloads"
headed = true
window = "1024x768"
proxy = "http://127.0.0.1:3128"
timeout = "90s"

[watch]
listen = "127.0.0.1"
port = 8123
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "state", "profile"); c.Browser.Profile != want {
		t.Errorf("profile = %q, want %q", c.Browser.Profile, want)
	}
	if c.Browser.DownloadDir != "/var/downloads" {
		t.Errorf("an absolute path must stay as written: %q", c.Browser.DownloadDir)
	}
	if !c.Browser.Headed || c.Browser.Proxy != "http://127.0.0.1:3128" || time.Duration(c.Browser.Timeout) != 90*time.Second {
		t.Errorf("browser = %+v", c.Browser)
	}
	if c.Watch.Port != 8123 || c.Watch.Listen != "127.0.0.1" {
		t.Errorf("watch = %+v", c.Watch)
	}
	o, err := c.ChromeOptions()
	if err != nil {
		t.Fatal(err)
	}
	if o.Width != 1024 || o.Height != 768 || !o.Headed || o.ProfileDir != c.Browser.Profile {
		t.Errorf("chrome options = %+v", o)
	}
}

func TestAnUnknownKeyIsAnErrorThatNamesTheKeyAndTheLine(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "config.toml", "[browser]\nheaded = true\nprofil = \"x\"\n\n[nonsense]\nx = 1\n")
	_, err := Load(p)
	if err == nil {
		t.Fatal("an unknown key was accepted")
	}
	msg := err.Error()
	for _, want := range []string{p + ":3", `"browser.profil"`, p + ":5", `"nonsense"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error lacks %q:\n%s", want, msg)
		}
	}
}

func TestBadValuesSaySoPlainly(t *testing.T) {
	dir := t.TempDir()
	for body, want := range map[string]string{
		"[browser]\ntimeout = \"soon\"\n":   "not a duration",
		"[browser]\ntimeout = \"-5s\"\n":    "cannot be negative",
		"[browser]\nwindow = \"wide\"\n":    "not a window size",
		"[browser]\nwindow = \"100x100\"\n": "not a window size",
		"[browser]\nheaded = \"yes\"\n":     "headed",
		"[browser\nheaded = true\n":         "config.toml",
	} {
		_, err := Parse([]byte(body), filepath.Join(dir, "config.toml"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want it to mention %q", body, err, want)
		}
	}
}

func TestTheDefaultFileMayBeMissingButANamedOneMayNot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	c, err := Load("")
	if err != nil {
		t.Fatalf("a missing default file is fine: %v", err)
	}
	if c.Browser.Headed || c.Browser.Profile != "" {
		t.Errorf("defaults should be empty: %+v", c.Browser)
	}
	if _, err := Load(filepath.Join(dir, "nope.toml")); err == nil || !strings.Contains(err.Error(), "nope.toml") {
		t.Errorf("a named file that does not exist must be an error, got %v", err)
	}

	// With the default file present, it is read.
	if err := os.MkdirAll(filepath.Join(dir, "rauli"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "rauli"), "config.toml", "[browser]\nheaded = true\n")
	c, err = Load("")
	if err != nil || !c.Browser.Headed {
		t.Fatalf("the default file was not read: %v %+v", err, c.Browser)
	}
}

func TestDefaultPathUsesXDGOnlyWhenItIsAbsolute(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/somewhere/config")
	if got := DefaultPath(); got != "/somewhere/config/rauli/config.toml" {
		t.Errorf("got %q", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative/dir")
	t.Setenv("HOME", "/home/someone")
	if got := DefaultPath(); got != "/home/someone/.config/rauli/config.toml" {
		t.Errorf("a relative XDG_CONFIG_HOME must be ignored, got %q", got)
	}
}

func TestParseWindow(t *testing.T) {
	for in, want := range map[string][2]int{"": {0, 0}, "1280x800": {1280, 800}, "1920X1080": {1920, 1080}} {
		w, h, err := ParseWindow(in)
		if err != nil || w != want[0] || h != want[1] {
			t.Errorf("ParseWindow(%q) = %d, %d, %v", in, w, h, err)
		}
	}
	for _, bad := range []string{"1280", "x800", "axb", "1280x", "9000x9000", "199x500"} {
		if _, _, err := ParseWindow(bad); err == nil {
			t.Errorf("ParseWindow(%q) should fail", bad)
		}
	}
}

func TestAMistakeInTheKeychainNamesTheFileTheLineAndTheKey(t *testing.T) {
	good := "[keychain]\nbackend = \"command\"\ncommand = [\"get\"]\n\n[[keychain.handles]]\nname = \"shop\"\nfields = [\"user\"]\norigins = [\"https://shop.example\"]\n"
	if _, err := Parse([]byte(good), "config.toml"); err != nil {
		t.Fatalf("a good keychain is refused: %v", err)
	}
	cases := []struct {
		name, text, line, key string
	}{
		{"timeout", strings.Replace(good, "command = [\"get\"]", "command = [\"get\"]\ntimeout = \"soon\"", 1), "config.toml:4:", "keychain.timeout"},
		{"second origin", good + "\n[[keychain.handles]]\nname = \"bank\"\nfields = [\"user\"]\norigins = [\"bank.example\"]\n", "config.toml:13:", "keychain.handles[1].origins"},
		{"no program", strings.Replace(good, "command = [\"get\"]", "", 1), "", "keychain.command"},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.text), "config.toml")
		if err == nil || !strings.Contains(err.Error(), c.line) || !strings.Contains(err.Error(), `"`+c.key+`"`) {
			t.Errorf("%s: got %v, want %q and %q", c.name, err, c.line, c.key)
		}
	}
}
