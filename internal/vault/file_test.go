package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newFile(t *testing.T) (path string, k *Keychain) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "sub", "keychain")
	t.Setenv("RAULI_TEST_PASSPHRASE", passwd)
	k, err := open(ctx, Config{Backend: "file", Path: path, PassphraseEnv: "RAULI_TEST_PASSPHRASE"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	return path, k
}

func addShop(t *testing.T, k *Keychain) {
	t.Helper()
	if err := k.Add(ctx, AddOptions{Name: "shop", Origins: []string{"https://shop.example.com"}, Fields: fields("user", "alice", "password", pw)}); err != nil {
		t.Fatal(err)
	}
}

func TestTheFileIsSealedPrivateAndSurvivesReopening(t *testing.T) {
	path, k := newFile(t)
	addShop(t, k)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the keychain file has mode %v, want 0600", info.Mode().Perm())
	}
	dir, _ := os.Stat(filepath.Dir(path))
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("a directory made for the keychain has mode %v, want 0700", dir.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	for _, plain := range []string{pw, "alice", "shop", "shop.example.com", "password", "RAULIVLT" + "x"} {
		if bytes.Contains(raw, []byte(plain)) {
			t.Errorf("the file holds %q in the clear", plain)
		}
	}
	if !bytes.HasPrefix(raw, []byte("RAULIVLT")) {
		t.Error("the file does not start with its magic")
	}
	// Atomic writes leave no temporary files behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("the directory holds %d files, want only the keychain", len(entries))
	}

	k.Close()
	k2, err := open(ctx, Config{Backend: "file", Path: path, PassphraseEnv: "RAULI_TEST_PASSPHRASE"})
	if err != nil {
		t.Fatal(err)
	}
	defer k2.Close()
	if got, err := k2.Secret(ctx, "shop", "password", "https://shop.example.com/"); err != nil || got != pw {
		t.Fatalf("after reopening: %q, %v", got, err)
	}
}

func TestEveryWriteUsesAFreshNonce(t *testing.T) {
	path, k := newFile(t)
	addShop(t, k)
	a, _ := os.ReadFile(path)
	if err := k.Add(ctx, AddOptions{Name: "shop", Origins: []string{"https://shop.example.com"}, Replace: true, Fields: fields("user", "alice", "password", pw)}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if bytes.Equal(a, b) || bytes.Equal(a[headerLen:headerLen+24], b[headerLen:headerLen+24]) {
		t.Fatal("rewriting the same content must produce a different nonce and ciphertext")
	}
	if !bytes.Equal(a[:headerLen], b[:headerLen]) {
		t.Fatal("the header, with its salt, should stay the same for the life of the file")
	}
}

func TestAWrongPassphraseIsRefusedAtOpening(t *testing.T) {
	path, k := newFile(t)
	addShop(t, k)
	k.Close()

	t.Setenv("RAULI_TEST_PASSPHRASE", "not-the-passphrase")
	_, err := open(ctx, Config{Backend: "file", Path: path, PassphraseEnv: "RAULI_TEST_PASSPHRASE"})
	if err == nil || !strings.Contains(err.Error(), "passphrase is wrong, or the file has been changed") {
		t.Fatalf("got %v", err)
	}
	for _, s := range []string{pw, passwd, "not-the-passphrase", "alice"} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("the error holds %q", s)
		}
	}
}

func TestAnyChangeToTheFileIsDetected(t *testing.T) {
	path, k := newFile(t)
	addShop(t, k)
	k.Close()
	orig, _ := os.ReadFile(path)

	mutate := map[string]func([]byte) []byte{
		"flip a magic byte":      func(b []byte) []byte { b[0] ^= 1; return b },
		"change the version":     func(b []byte) []byte { b[8] = 2; return b },
		"raise the cost":         func(b []byte) []byte { b[9]++; return b },
		"lower the cost":         func(b []byte) []byte { b[9]--; return b },
		"change the block size":  func(b []byte) []byte { b[10]++; return b },
		"flip a salt byte":       func(b []byte) []byte { b[14] ^= 1; return b },
		"flip a nonce byte":      func(b []byte) []byte { b[headerLen+3] ^= 1; return b },
		"flip a ciphertext byte": func(b []byte) []byte { b[headerLen+30] ^= 1; return b },
		"flip the last byte":     func(b []byte) []byte { b[len(b)-1] ^= 1; return b },
		"cut the end":            func(b []byte) []byte { return b[:len(b)-1] },
		"cut to the header":      func(b []byte) []byte { return b[:headerLen] },
		"append bytes":           func(b []byte) []byte { return append(b, 0) },
		"empty":                  func(b []byte) []byte { return nil },
	}
	for name, f := range mutate {
		data := f(append([]byte(nil), orig...))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		k2, err := open(ctx, Config{Backend: "file", Path: path, PassphraseEnv: "RAULI_TEST_PASSPHRASE"})
		if err == nil {
			k2.Close()
			t.Errorf("%s: the changed file opened", name)
			continue
		}
		for _, s := range []string{pw, passwd, "alice"} {
			if strings.Contains(err.Error(), s) {
				t.Errorf("%s: the error holds %q", name, s)
			}
		}
	}
}

func TestAFileThatIsNotAKeychainOrAskForAHugeKeyIsRefused(t *testing.T) {
	path, k := newFile(t)
	addShop(t, k)
	k.Close()
	orig, _ := os.ReadFile(path)

	os.WriteFile(path, []byte("just a text file, long enough to look like it might hold something at all"), 0o600)
	if _, err := open(ctx, Config{Backend: "file", Path: path, PassphraseEnv: "RAULI_TEST_PASSPHRASE"}); err == nil || !strings.Contains(err.Error(), "not a Raulí keychain") {
		t.Fatalf("got %v", err)
	}
	huge := append([]byte(nil), orig...)
	huge[9] = 30 // 2^30 blocks of memory would be a gigabyte and more
	os.WriteFile(path, huge, 0o600)
	if _, err := open(ctx, Config{Backend: "file", Path: path, PassphraseEnv: "RAULI_TEST_PASSPHRASE"}); err == nil || !strings.Contains(err.Error(), "will not run") {
		t.Fatalf("got %v", err)
	}
}

func TestAFileReplacedWhileOpenIsNotTrusted(t *testing.T) {
	path, k := newFile(t)
	addShop(t, k)
	// Another keychain, with another salt, takes the file's place.
	other := filepath.Join(t.TempDir(), "other")
	o, err := open(ctx, Config{Backend: "file", Path: other, PassphraseEnv: "RAULI_TEST_PASSPHRASE"})
	if err != nil {
		t.Fatal(err)
	}
	o.Add(ctx, AddOptions{Name: "evil", Anywhere: true, Fields: fields("password", "x")})
	o.Close()
	data, _ := os.ReadFile(other)
	os.WriteFile(path, data, 0o600)
	if _, err := k.Secret(ctx, "shop", "password", "https://shop.example.com/"); err == nil || !strings.Contains(err.Error(), "different key") {
		t.Fatalf("got %v", err)
	}
}

func TestThePassphraseCanComeFromACommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kc")
	cfg := Config{Backend: "file", Path: path, PassphraseCommand: []string{"printf", "%s\n", passwd}}
	k, err := open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	addShop(t, k)
	k.Close()
	t.Setenv("RAULI_TEST_PASSPHRASE", passwd)
	k2, err := open(ctx, Config{Backend: "file", Path: path, PassphraseEnv: "RAULI_TEST_PASSPHRASE"})
	if err != nil {
		t.Fatalf("a passphrase from a command, with its newline trimmed, should match the same one from the environment: %v", err)
	}
	k2.Close()

	_, err = open(ctx, Config{Backend: "file", Path: path, PassphraseCommand: []string{"/nonexistent/passphrase"}})
	if err == nil || strings.Contains(err.Error(), passwd) {
		t.Fatalf("got %v", err)
	}
	_, err = open(ctx, Config{Backend: "file", Path: path, PassphraseCommand: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "printed nothing") {
		t.Fatalf("got %v", err)
	}
}
