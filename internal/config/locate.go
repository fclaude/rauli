package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/fclaude/rauli/internal/vault"
)

// keychainError reports a mistake in the keychain settings the way the decoder
// reports its own: with the file, the line and the key. The decoder knows the
// line of every key it reads, but a mistake in what the keys mean is found
// after decoding, so the line is looked for in the text.
func keychainError(name string, data []byte, err error) error {
	var ce *vault.ConfigError
	if !errors.As(err, &ce) {
		return fmt.Errorf("%s: [keychain]: %w", name, err)
	}
	if line := locate(data, ce.Key); line > 0 {
		return fmt.Errorf("%s:%d: setting %q: %s", name, line, ce.Key, ce.Msg)
	}
	return fmt.Errorf("%s: setting %q: %s", name, ce.Key, ce.Msg)
}

var handleKey = regexp.MustCompile(`^keychain\.handles\[(\d+)\](?:\.(\w+))?$`)

// locate finds the line of a key such as keychain.timeout or
// keychain.handles[1].origins, counting from 1, or 0 when it cannot say. It
// understands the usual layout, a [keychain] table followed by
// [[keychain.handles]] tables; a key written some other way gets the line of
// its table or none.
func locate(data []byte, key string) int {
	lines := strings.Split(string(data), "\n")
	header := func(i int) string { return strings.TrimSpace(lines[i]) }
	isHeader := func(s string) bool { return strings.HasPrefix(s, "[") }

	// keyIn looks for key = ... between a table's header and the next one.
	keyIn := func(from int, name string) int {
		re := regexp.MustCompile(`^\s*"?` + regexp.QuoteMeta(name) + `"?\s*=`)
		for i := from + 1; i < len(lines); i++ {
			if isHeader(header(i)) {
				break
			}
			if re.MatchString(lines[i]) {
				return i + 1
			}
		}
		return from + 1
	}

	if m := handleKey.FindStringSubmatch(key); m != nil {
		want, _ := strconv.Atoi(m[1])
		n := -1
		for i := range lines {
			if header(i) == "[[keychain.handles]]" {
				if n++; n == want {
					if m[2] == "" {
						return i + 1
					}
					return keyIn(i, m[2])
				}
			}
		}
		return 0
	}

	field, ok := strings.CutPrefix(key, "keychain.")
	if !ok {
		return 0
	}
	for i := range lines {
		if header(i) == "[keychain]" {
			return keyIn(i, field)
		}
	}
	// The table may be missing, with the keys written as keychain.backend = ...
	re := regexp.MustCompile(`^\s*keychain\.` + regexp.QuoteMeta(field) + `\s*=`)
	for i, l := range lines {
		if re.MatchString(l) {
			return i + 1
		}
	}
	return 0
}
