package main

import (
	"context"
	"errors"
)

func init() {
	register("keychain", "manage the logins an agent may use (not wired up yet)", func(ctx context.Context, args []string, s streams) error {
		// The keychain commands arrive with the keychain itself. Until then this
		// says so plainly and fails, so nobody believes a login was stored.
		return errors.New("the keychain is not wired up yet in this build")
	})
}
