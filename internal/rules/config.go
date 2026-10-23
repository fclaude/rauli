package rules

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/fclaude/rauli/internal/urlmatch"
)

// Config is the rules section of the configuration file. In a file of its own,
// the same keys sit at the top level.
//
//	[rules.network]
//	allow = ["https://*.example.com", "http://localhost"]
//	deny  = ["https://ads.example.com"]
//	block_private = true
//
//	[rules.actions]
//	default = "allow"                  # allow, ask or deny
//
//	[[rules.actions.rule]]             # the first rule that matches decides
//	kind = "click"                     # navigate, click, type, key, select, upload, eval, credential, or *
//	element = "*pay*"                  # matched without regard to case against `button "Pay now"`
//	url = "https://shop.example.com/*" # the page the action happens on
//	decision = "ask"
//	reason = "payments need a person"
//
//	[rules.approval]
//	mode = "terminal"                  # deny, terminal or command
//	command = ["notify-approve"]       # for mode = "command"
//	timeout = "2m"
//
//	[rules.audit]
//	requests = "denied"                # or "all"
type Config struct {
	Network  NetworkConfig  `toml:"network"`
	Actions  ActionsConfig  `toml:"actions"`
	Approval ApprovalConfig `toml:"approval"`
	Audit    AuditConfig    `toml:"audit"`
}

// NetworkConfig limits which addresses the browser may reach.
type NetworkConfig struct {
	// Allow lists the addresses that may be reached. When it is not empty,
	// everything else is refused.
	Allow []string `toml:"allow"`
	// Deny lists addresses that are refused even if Allow lists them.
	Deny []string `toml:"deny"`
	// BlockPrivate refuses machines on the local network: localhost, private
	// and link-local addresses and names ending in .local. An address listed in
	// Allow stays reachable, so a development server is one line.
	BlockPrivate bool `toml:"block_private"`
}

// ActionsConfig says what the agent may do on a page.
type ActionsConfig struct {
	// Default applies to an action no rule matches. Left empty, everything is
	// allowed except eval and upload, which ask. Set it to allow to turn that off.
	Default string       `toml:"default"`
	Rule    []ActionRule `toml:"rule"`
}

// ActionRule matches actions. Every key that is set must match.
type ActionRule struct {
	Kind     string `toml:"kind"`
	Element  string `toml:"element"`
	URL      string `toml:"url"`
	Detail   string `toml:"detail"`
	Decision string `toml:"decision"`
	Reason   string `toml:"reason"`
}

// ApprovalConfig chooses how a person is asked.
type ApprovalConfig struct {
	Mode    string   `toml:"mode"`
	Command []string `toml:"command"`
	Timeout string   `toml:"timeout"`
}

// AuditConfig tunes what the audit log records.
type AuditConfig struct {
	// Requests is "denied" (the default) to log only the requests the rules
	// refused, or "all" to log every request a page makes.
	Requests string `toml:"requests"`
}

// Kinds are the kinds of action the tools report.
var Kinds = []string{"navigate", "click", "type", "key", "select", "upload", "eval", "credential"}

func validKind(k string) bool {
	if k == "" || k == "*" {
		return true
	}
	for _, v := range Kinds {
		if v == k {
			return true
		}
	}
	return false
}

func validDecision(d string) bool { return d == "allow" || d == "ask" || d == "deny" }

// Parse reads rules from TOML. Keys it does not know are errors, because a
// misspelt key in a file of rules would otherwise quietly leave a door open.
func Parse(data []byte) (Config, error) {
	var c Config
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return Config{}, fmt.Errorf("the rules file has keys that mean nothing:\n%s", strict.String())
		}
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return Config{}, fmt.Errorf("the rules file does not read, at line %d, column %d: %s", row, col, de.Error())
		}
		return Config{}, fmt.Errorf("the rules file does not read: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Load reads rules from a file.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	c, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Validate checks the whole Config and names the key at fault.
func (c Config) Validate() error {
	var errs []string
	add := func(where, format string, args ...any) {
		errs = append(errs, fmt.Sprintf("rules.%s: %s", where, fmt.Sprintf(format, args...)))
	}

	for i, p := range c.Network.Allow {
		if _, err := urlmatch.Parse(p, false); err != nil {
			add(fmt.Sprintf("network.allow[%d]", i), "%q: %v", p, err)
		}
	}
	for i, p := range c.Network.Deny {
		if _, err := urlmatch.Parse(p, false); err != nil {
			add(fmt.Sprintf("network.deny[%d]", i), "%q: %v", p, err)
		}
	}

	switch c.Actions.Default {
	case "", "allow", "ask", "deny":
	default:
		add("actions.default", "%q is not allow, ask or deny", c.Actions.Default)
	}
	for i, r := range c.Actions.Rule {
		where := fmt.Sprintf("actions.rule[%d]", i)
		if !validKind(r.Kind) {
			add(where+".kind", "%q is not one of %s or *", r.Kind, strings.Join(Kinds, ", "))
		}
		if !validDecision(r.Decision) {
			add(where+".decision", "%q is not allow, ask or deny", r.Decision)
		}
		if r.URL != "" && r.URL != "*" {
			if _, err := urlmatch.Parse(r.URL, false); err != nil {
				add(where+".url", "%q: %v", r.URL, err)
			}
		}
		if (r.Kind == "" || r.Kind == "*") && r.Element == "" && r.URL == "" && r.Detail == "" {
			add(where, "it matches every action, so it is a default: set rules.actions.default instead")
		}
	}

	switch c.Approval.Mode {
	case "", "deny", "terminal":
	case "command":
		if len(c.Approval.Command) == 0 {
			add("approval.command", "mode command needs a command")
		}
	default:
		add("approval.mode", "%q is not deny, terminal or command", c.Approval.Mode)
	}
	if c.Approval.Timeout != "" {
		if d, err := time.ParseDuration(c.Approval.Timeout); err != nil || d <= 0 {
			add("approval.timeout", "%q is not a duration such as 30s or 2m", c.Approval.Timeout)
		}
	}
	switch c.Audit.Requests {
	case "", "denied", "all":
	default:
		add("audit.requests", "%q is not denied or all", c.Audit.Requests)
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

// Approver builds the approver the Config names. Left empty, nobody can
// approve, and an action that asks is refused with a message that says so.
func (c Config) Approver() (Approver, error) {
	timeout := time.Duration(0)
	if c.Approval.Timeout != "" {
		d, err := time.ParseDuration(c.Approval.Timeout)
		if err != nil {
			return nil, fmt.Errorf("rules.approval.timeout: %w", err)
		}
		timeout = d
	}
	switch c.Approval.Mode {
	case "", "deny":
		return Deny{}, nil
	case "terminal":
		return &Terminal{Timeout: timeout}, nil
	case "command":
		return Command{Argv: c.Approval.Command, Timeout: timeout}, nil
	}
	return nil, fmt.Errorf("rules.approval.mode: %q is not deny, terminal or command", c.Approval.Mode)
}
