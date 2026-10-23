// Package rules decides what an agent may do in the browser: which addresses it
// may reach, and which actions it may take, which need a person's approval, and
// which are refused outright. It implements tools.Guard.
package rules

import (
	"context"

	"github.com/fclaude/rauli/internal/chrome"
	"github.com/fclaude/rauli/internal/tools"
)

// Config is the rules section of the configuration file.
type Config struct{}

// Approver asks a person whether an action may go ahead. It may block, and
// must give up when ctx ends.
type Approver interface {
	Approve(ctx context.Context, a tools.Action, reason string) (bool, error)
}

// Engine applies a Config.
type Engine struct{}

// New builds an Engine. ap may be nil, in which case an action that needs
// approval is refused with a message saying so.
func New(c Config, ap Approver) (*Engine, error) { return &Engine{}, nil }

func (e *Engine) CheckURL(ctx context.Context, r chrome.Request) error  { return nil }
func (e *Engine) CheckAction(ctx context.Context, a tools.Action) error { return nil }

var _ tools.Guard = (*Engine)(nil)
