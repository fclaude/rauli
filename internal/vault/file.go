package vault

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

// The file holds every login, sealed with XChaCha20-Poly1305 under a key that
// scrypt derives from the passphrase. Its layout:
//
//	"RAULIVLT"   8 bytes  magic
//	version      1 byte   1
//	log2(N)      1 byte   scrypt cost, 15 by default
//	r, p         2 bytes  scrypt block size and parallelism, 8 and 1
//	salt         16 bytes
//	nonce        24 bytes
//	ciphertext   the rest, including the 16 byte tag
//
// The header up to and including the salt is authenticated as additional data,
// so changing the cost to make a file cheaper to guess is detected like any
// other change. The nonce is new at every write.
const (
	fileMagic   = "RAULIVLT"
	fileVersion = 1
	headerLen   = 8 + 1 + 1 + 1 + 1 + 16
	saltLen     = 16
	defaultLogN = 15
)

// errWrongKey is what opening a file returns when the seal does not verify. A
// wrong passphrase and a changed file look the same, and the text says both.
var errWrongKey = errors.New("the keychain file could not be opened: the passphrase is wrong, or the file has been changed")

type fileStore struct {
	mu   sync.Mutex
	path string

	key        []byte
	salt       []byte
	logN, r, p byte
}

type fileBody struct {
	Version int      `json:"version"`
	Entries []stored `json:"entries"`
}

// openFile opens the file at path with a passphrase, creating nothing until the
// first write. An existing file is opened and read at once, so a wrong
// passphrase is found now and not at the first login. The passphrase is not kept.
func openFile(path string, pass []byte) (*fileStore, error) {
	fs := &fileStore{path: path, logN: defaultLogN, r: 8, p: 1}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		fs.salt = make([]byte, saltLen)
		if _, err := rand.Read(fs.salt); err != nil {
			return nil, err
		}
		if fs.key, err = fs.derive(pass); err != nil {
			return nil, err
		}
		return fs, nil
	case err != nil:
		return nil, err
	}
	if err := fs.readHeader(data); err != nil {
		return nil, err
	}
	if fs.key, err = fs.derive(pass); err != nil {
		return nil, err
	}
	body, err := fs.open(data)
	if err != nil {
		fs.close()
		return nil, err
	}
	wipeBody(body)
	return fs, nil
}

func (f *fileStore) derive(pass []byte) ([]byte, error) {
	return scrypt.Key(pass, f.salt, 1<<f.logN, int(f.r), int(f.p), chacha20poly1305.KeySize)
}

func (f *fileStore) readHeader(data []byte) error {
	if len(data) < headerLen+chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead || string(data[:8]) != fileMagic {
		return errors.New("that file is not a Raulí keychain")
	}
	if data[8] != fileVersion {
		return fmt.Errorf("that keychain file is version %d, and this program reads version %d", data[8], fileVersion)
	}
	f.logN, f.r, f.p = data[9], data[10], data[11]
	// Bounds protect against a file made to cost a minute of memory to open.
	if f.logN < 14 || f.logN > 22 || f.r < 1 || f.r > 16 || f.p < 1 || f.p > 4 {
		return errors.New("that keychain file asks for a key derivation this program will not run")
	}
	f.salt = append([]byte(nil), data[12:headerLen]...)
	return nil
}

func (f *fileStore) open(data []byte) (*fileBody, error) {
	aead, err := chacha20poly1305.NewX(f.key)
	if err != nil {
		return nil, err
	}
	nonce := data[headerLen : headerLen+aead.NonceSize()]
	plain, err := aead.Open(nil, nonce, data[headerLen+aead.NonceSize():], data[:headerLen])
	if err != nil {
		return nil, errWrongKey
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	var body fileBody
	if err := json.Unmarshal(plain, &body); err != nil {
		return nil, errors.New("the keychain file opened but its contents do not read")
	}
	return &body, nil
}

func wipeBody(b *fileBody) {
	for _, s := range b.Entries {
		for k := range s.Fields {
			s.Fields[k] = ""
		}
	}
}

// load reads the current contents. It reads the file each time, so a change
// made by another run of the program is seen.
func (f *fileStore) load() (map[string]stored, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]stored{}, nil
	}
	if err != nil {
		return nil, err
	}
	var h fileStore
	if err := h.readHeader(data); err != nil {
		return nil, err
	}
	if !bytes.Equal(h.salt, f.salt) || h.logN != f.logN || h.r != f.r || h.p != f.p {
		return nil, errors.New("the keychain file was replaced by one with a different key: open it again")
	}
	body, err := f.open(data)
	if err != nil {
		return nil, err
	}
	out := make(map[string]stored, len(body.Entries))
	for _, s := range body.Entries {
		out[s.Name] = s
	}
	return out, nil
}

// save writes the whole file anew, through a temporary file in the same
// directory, so a crash leaves either the old file or the new one.
func (f *fileStore) save(all map[string]stored) error {
	body := fileBody{Version: fileVersion}
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		body.Entries = append(body.Entries, all[n])
	}
	plain, err := json.Marshal(body)
	if err != nil {
		return err
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()

	aead, err := chacha20poly1305.NewX(f.key)
	if err != nil {
		return err
	}
	header := make([]byte, 0, headerLen)
	header = append(header, fileMagic...)
	header = append(header, fileVersion, f.logN, f.r, f.p)
	header = append(header, f.salt...)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	out := append(append(header, nonce...), aead.Seal(nil, nonce, plain, header)...)

	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".keychain-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(name) }
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, f.path); err != nil {
		os.Remove(name)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync() // makes the rename durable; not every system allows it
		d.Close()
	}
	return nil
}

func (f *fileStore) get(name string) (*entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all, err := f.load()
	if err != nil {
		return nil, err
	}
	s, ok := all[name]
	if !ok {
		return nil, errNotFound
	}
	e := &entry{Name: s.Name, Origins: s.Origins, Anywhere: s.Anywhere, Fields: map[string]Secret{}}
	for k, v := range s.Fields {
		e.Fields[k] = Secret(v)
	}
	return e, nil
}

func (f *fileStore) list() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all, err := f.load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

func (f *fileStore) put(e *entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	all, err := f.load()
	if err != nil {
		return err
	}
	s := stored{Name: e.Name, Origins: e.Origins, Anywhere: e.Anywhere, Fields: map[string]string{}}
	for k, v := range e.Fields {
		s.Fields[k] = v.Reveal()
	}
	all[e.Name] = s
	return f.save(all)
}

func (f *fileStore) del(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	all, err := f.load()
	if err != nil {
		return err
	}
	if _, ok := all[name]; !ok {
		return errNotFound
	}
	delete(all, name)
	return f.save(all)
}

// close forgets the key.
func (f *fileStore) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.key {
		f.key[i] = 0
	}
	f.key = nil
}
