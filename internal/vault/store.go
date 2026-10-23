package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
)

var (
	nameRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	fieldRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

// errNotFound is what a store returns for a login it does not hold.
var errNotFound = errors.New("no such login")

// entry is one login: where it may be used, and its fields.
type entry struct {
	Name     string
	Origins  []string
	Anywhere bool
	Fields   map[string]Secret
}

// stored is an entry as it is written to a backend. It is the only place a
// value is a plain string, and it never leaves this file's functions.
type stored struct {
	Name     string            `json:"name"`
	Origins  []string          `json:"origins,omitempty"`
	Anywhere bool              `json:"anywhere,omitempty"`
	Fields   map[string]string `json:"fields"`
}

func (e *entry) marshal() ([]byte, error) {
	s := stored{Name: e.Name, Origins: e.Origins, Anywhere: e.Anywhere, Fields: map[string]string{}}
	for k, v := range e.Fields {
		s.Fields[k] = v.Reveal()
	}
	return json.Marshal(s)
}

func unmarshalEntry(data []byte) (*entry, error) {
	var s stored
	if err := json.Unmarshal(data, &s); err != nil {
		// The decoder's message can quote the text it choked on, which is a secret here.
		return nil, errors.New("a stored login does not read")
	}
	e := &entry{Name: s.Name, Origins: s.Origins, Anywhere: s.Anywhere, Fields: map[string]Secret{}}
	for k, v := range s.Fields {
		e.Fields[k] = Secret(v)
	}
	return e, nil
}

// wipe overwrites the entry's values.
func (e *entry) wipe() {
	for _, v := range e.Fields {
		v.Wipe()
	}
}

func (e *entry) validate() error {
	if !nameRE.MatchString(e.Name) {
		return fmt.Errorf("%q is not a name for a login: use lower-case letters, digits, dots, dashes and underscores, starting with a letter or digit", e.Name)
	}
	if len(e.Fields) == 0 {
		return errors.New("a login needs at least one field")
	}
	for f, v := range e.Fields {
		if !fieldRE.MatchString(f) {
			return fmt.Errorf("%q is not a name for a field: use lower-case letters, digits, dashes and underscores, starting with a letter", f)
		}
		if len(v) == 0 {
			return fmt.Errorf("the field %q is empty", f)
		}
	}
	return nil
}

func (e *entry) fieldNames() []string {
	names := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// store is where logins live.
type store interface {
	get(name string) (*entry, error)
	list() ([]string, error)
	put(e *entry) error
	del(name string) error
	close()
}

// memStore keeps logins in memory. It is for tests and for the one-off run.
type memStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string][]byte{}} }

func (m *memStore) get(name string) (*entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[name]
	if !ok {
		return nil, errNotFound
	}
	return unmarshalEntry(b)
}

func (m *memStore) list() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.data))
	for n := range m.data {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

func (m *memStore) put(e *entry) error {
	b, err := e.marshal()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[e.Name] = b
	return nil
}

func (m *memStore) del(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[name]; !ok {
		return errNotFound
	}
	delete(m.data, name)
	return nil
}

func (m *memStore) close() {}
