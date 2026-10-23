package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/zalando/go-keyring"
)

// indexKey names the entry that lists the others, because the operating
// system's stores can fetch a secret by name and cannot list them. Logins have
// names that cannot contain a tilde, so it never clashes with one.
const indexKey = "~index"

// keyringStore keeps one entry per login in the operating system's secret
// store: the Secret Service on Linux, the Keychain on macOS, the Credential
// Manager on Windows.
type keyringStore struct {
	service string
	mu      sync.Mutex
}

func newKeyringStore(service string) *keyringStore {
	if service == "" {
		service = "rauli"
	}
	return &keyringStore{service: service}
}

func (k *keyringStore) get(name string) (*entry, error) {
	v, err := keyring.Get(k.service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("the system keychain would not give up %q: %w", name, err)
	}
	b := []byte(v)
	defer func() {
		for i := range b {
			b[i] = 0
		}
	}()
	return unmarshalEntry(b)
}

func (k *keyringStore) index() ([]string, error) {
	v, err := keyring.Get(k.service, indexKey)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the system keychain would not list the logins: %w", err)
	}
	var names []string
	if err := json.Unmarshal([]byte(v), &names); err != nil {
		return nil, errors.New("the system keychain's list of logins does not read")
	}
	return names, nil
}

func (k *keyringStore) setIndex(names []string) error {
	sort.Strings(names)
	b, _ := json.Marshal(names)
	return keyring.Set(k.service, indexKey, string(b))
}

func (k *keyringStore) list() ([]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.index()
}

func (k *keyringStore) put(e *entry) error {
	b, err := e.marshal()
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := keyring.Set(k.service, e.Name, string(b)); err != nil {
		return fmt.Errorf("the system keychain would not store %q: %w", e.Name, err)
	}
	names, err := k.index()
	if err != nil {
		return err
	}
	for _, n := range names {
		if n == e.Name {
			return nil
		}
	}
	return k.setIndex(append(names, e.Name))
}

func (k *keyringStore) del(name string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := keyring.Delete(k.service, name); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return errNotFound
		}
		return fmt.Errorf("the system keychain would not remove %q: %w", name, err)
	}
	names, err := k.index()
	if err != nil {
		return err
	}
	kept := names[:0]
	for _, n := range names {
		if n != name {
			kept = append(kept, n)
		}
	}
	return k.setIndex(kept)
}

func (k *keyringStore) close() {}
