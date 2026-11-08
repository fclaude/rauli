// Package browser is Raulí for a Go program: a browser session and the tools
// that drive it, with the Guard and the Vault a program supplies.
//
// It is the one package outside internal/, so that another program can link
// Raulí rather than run `rauli serve` and speak the Model Context Protocol to
// it. Everything here is an alias of the type the rest of Raulí uses, so a
// program sees exactly what the command does and nothing is copied: the
// layers below (the DevTools client, the browser launcher) stay internal, and
// their types are reached only through these names and the methods on them.
package browser

import (
	"context"

	"github.com/fclaude/rauli/internal/chrome"
	"github.com/fclaude/rauli/internal/tools"
)

type (
	// Session is a browser and the tools to drive it. Its methods are those
	// of the tools package: Call, Tools, Current, Subscribe, Hold, Release and Close.
	Session = tools.Session
	// Options say how to build a Session: how to start the browser, and the
	// Guard and Vault that decide what it may do.
	Options = tools.Options
	// LaunchOptions say how to start the browser: its binary, profile,
	// proxy, extra flags and download folder. The Filter field is set by New
	// from the Guard and need not be given.
	LaunchOptions = chrome.Options
	// Request is a request the browser is about to make, as CheckURL sees it.
	Request = chrome.Request
	// Guard decides what the agent may do: every request the browser makes,
	// and every action before a tool carries it out.
	Guard = tools.Guard
	// Action is what a Guard is asked to approve before a tool acts.
	Action = tools.Action
	// Vault hands one field of one credential to a tool, for one page.
	Vault = tools.Vault
	// Handle names a credential in a Vault, without its secret.
	Handle = tools.Handle
	// Tool is one operation, with its name, description and argument schema.
	Tool = tools.Tool
	// Result is what a tool gives back: text, and pictures for a screenshot.
	Result = tools.Result
	// Image is a picture in a Result.
	Image = tools.Image
	// Event is one finished tool call, as Subscribe delivers it.
	Event = tools.Event
	// AllowAll is the Guard that allows everything.
	AllowAll = tools.AllowAll
)

var (
	// ErrHeld is returned when a person has the browser and did not give it
	// back in time.
	ErrHeld = tools.ErrHeld
	// ErrUnknownTool is returned for a name that is not a tool.
	ErrUnknownTool = tools.ErrUnknownTool
)

// New starts the browser. The Guard's CheckURL becomes the browser's request
// filter, so it is asked about scripts, images and frames as well as typed
// addresses. The browser lives until Close is called or ctx ends.
func New(ctx context.Context, opts Options) (*Session, error) { return tools.New(ctx, opts) }

// Catalogue lists the tools in name order without starting a browser. The
// tools it returns describe themselves but cannot be run.
func Catalogue() []Tool { return tools.Catalogue() }

// Find returns the browser New would start: $RAULI_CHROME if set, then a
// Chromium-based browser on PATH, then one downloaded for Playwright.
func Find() (string, error) { return chrome.Find() }
