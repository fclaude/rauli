// Package vault is the keychain: the logins an agent may use without ever
// seeing them. It implements tools.Vault.
package vault

import (
	"context"

	"github.com/fclaude/rauli/internal/tools"
)

// Config is the keychain section of the configuration file.
type Config struct{}

// Open opens the keychain the Config names. A nil Vault with a nil error means
// no keychain is configured.
func Open(ctx context.Context, c Config) (tools.Vault, error) { return nil, nil }
