package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fclaude/rauli/internal/config"
	"github.com/fclaude/rauli/internal/rules"
	"github.com/fclaude/rauli/internal/tools"
)

// startAudit writes the audit log while the session runs, if the rules ask for
// one. The returned function ends the log and closes the file; call it before
// the session closes so the last events are written.
func startAudit(ctx context.Context, cfg config.Config, session *tools.Session) (func(), error) {
	path := cfg.Rules.Audit.File
	if path == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("audit log: %w", err)
	}
	// The log names pages and decisions, so it is for the person alone.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit log: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = rules.Audit(ctx, session, f)
	}()
	return func() {
		cancel()
		<-done
		f.Close()
	}, nil
}
