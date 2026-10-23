package vault

import (
	"fmt"
	"strings"
)

// Secret holds a credential's value. It prints as a placeholder however it is
// formatted, and encodes as one, so a value cannot reach a log, an error or a
// debugging dump by accident. The only way to the text is Reveal.
//
// A Secret is kept as bytes so that Wipe can overwrite it. Go cannot promise
// that no other copy exists, so this is a precaution with limits, and the
// vault's real protection is that it keeps nothing it does not need.
type Secret []byte

const placeholder = "[secret]"

func (s Secret) String() string               { return placeholder }
func (s Secret) GoString() string             { return placeholder }
func (s Secret) Format(f fmt.State, _ rune)   { fmt.Fprint(f, placeholder) }
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + placeholder + `"`), nil }
func (s Secret) MarshalText() ([]byte, error) { return []byte(placeholder), nil }

// Reveal returns the value as text. Callers pass it on and let go of it.
func (s Secret) Reveal() string { return string(s) }

// Wipe overwrites the value.
func (s Secret) Wipe() {
	for i := range s {
		s[i] = 0
	}
}

// isVisible says whether a field's value is something a person types where they
// can see it, such as a user name, and not something to hide as it is typed.
func isVisible(field string) bool {
	switch strings.ToLower(field) {
	case "user", "username", "login", "email":
		return true
	}
	return false
}
