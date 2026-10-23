package main

import (
	"context"
	"errors"
)

func init() {
	register("watch", "open a live view of the browser (not wired up yet)", func(ctx context.Context, args []string, s streams) error {
		// The live view arrives with its own package. Until then this says so
		// plainly and fails, so nobody waits for a page that is not coming.
		return errors.New("the live view is not wired up yet in this build")
	})
}
