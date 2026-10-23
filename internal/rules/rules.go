// Package rules decides what an agent may do in the browser: which addresses it
// may reach, which actions it may take, which need a person's approval, and
// which are refused outright. Its Engine implements tools.Guard.
//
// # Defaults
//
// With no configuration the Engine allows the web and little else.
//
//   - Only http, https, ws and wss addresses are reached. about:blank and the
//     browser's own error page are always fine. A data: or blob: address is
//     allowed inside a page, where it only carries its own content, and refused
//     as a page of its own, where it can show anything under any address.
//   - Link-local addresses (169.254.0.0/16 and fe80::/10) are never reached,
//     even if the rules allow them, because that is where cloud machines keep
//     their credentials.
//   - Every action is allowed except eval and upload, which ask. Running a script
//     in a page can do anything the page can, and an upload sends a file from this
//     machine to a website. Set rules.actions.default to allow to turn that off.
//   - Nobody is set up to approve, so an action that asks is refused, and the
//     refusal says why and how to change it.
//
// A real setup lists the sites the agent may visit under rules.network.allow.
// Everything else is then refused, which is the most useful rule here: a page
// the agent reads can contain text that tries to give it orders, and a short
// allow list is what limits where such an order can take it.
package rules

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/rauli/internal/chrome"
	"github.com/fclaude/rauli/internal/tools"
	"github.com/fclaude/rauli/internal/urlmatch"
)

// Approver asks a person whether an action may go ahead. It may block, and
// must give up when ctx ends.
type Approver interface {
	Approve(ctx context.Context, a tools.Action, reason string) (bool, error)
}

// Decision is one thing the Engine decided, kept for the audit log.
type Decision struct {
	Time time.Time `json:"time"`
	// Type is "request" for a request the browser made, or "action" for a tool.
	// In a log line it is called subject, because the line's own type is decision.
	Type string `json:"subject"`
	// Outcome is allow, deny, approved, refused or unanswered.
	Outcome string `json:"outcome"`
	// Rule says which rule decided, such as "rule 2" or "network.allow".
	Rule   string `json:"rule,omitempty"`
	Reason string `json:"reason,omitempty"`

	Tool    string `json:"tool,omitempty"`
	Kind    string `json:"kind,omitempty"`
	URL     string `json:"url,omitempty"`
	Element string `json:"element,omitempty"`
	Detail  string `json:"detail,omitempty"`
	// Resource is the kind of request, such as Document or Image.
	Resource string `json:"resource,omitempty"`
}

type compiledRule struct {
	ActionRule
	url *urlmatch.Pattern
}

// Engine applies a Config. It is safe for concurrent use.
type Engine struct {
	cfg      Config
	allow    []*urlmatch.Pattern
	deny     []*urlmatch.Pattern
	rules    []compiledRule
	approver Approver
	logAll   bool

	mu   sync.Mutex
	subs map[int]chan Decision
	seq  int
}

// New builds an Engine. A nil ap means the approver the Config names, and with
// none named, one that never approves.
func New(c Config, ap Approver) (*Engine, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if ap == nil {
		var err error
		if ap, err = c.Approver(); err != nil {
			return nil, err
		}
	}
	e := &Engine{cfg: c, approver: ap, logAll: c.Audit.Requests == "all", subs: map[int]chan Decision{}}
	for _, s := range c.Network.Allow {
		p, _ := urlmatch.Parse(s, false) // checked by Validate
		e.allow = append(e.allow, p)
	}
	for _, s := range c.Network.Deny {
		p, _ := urlmatch.Parse(s, false)
		e.deny = append(e.deny, p)
	}
	for _, r := range c.Actions.Rule {
		cr := compiledRule{ActionRule: r}
		if r.URL != "" && r.URL != "*" {
			cr.url, _ = urlmatch.Parse(r.URL, false)
		}
		e.rules = append(e.rules, cr)
	}
	return e, nil
}

var _ tools.Guard = (*Engine)(nil)

// Subscribe delivers every decision the Engine makes. A reader that falls
// behind loses decisions, so a slow log can never slow the browser.
func (e *Engine) Subscribe() (<-chan Decision, func()) {
	ch := make(chan Decision, 256)
	e.mu.Lock()
	e.seq++
	id := e.seq
	e.subs[id] = ch
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		delete(e.subs, id)
		e.mu.Unlock()
	}
}

func (e *Engine) record(d Decision) {
	d.Time = time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ch := range e.subs {
		select {
		case ch <- d:
		default:
		}
	}
}

// shown is an address as the rules and the logs show it: without its query,
// fragment or login, which is where tokens tend to be.
func shown(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		if i := strings.IndexAny(raw, "?#"); i >= 0 {
			return raw[:i]
		}
		return raw
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}

// CheckURL decides whether the browser may make a request.
func (e *Engine) CheckURL(ctx context.Context, r chrome.Request) error {
	deny := func(rule, format string, args ...any) error {
		reason := fmt.Sprintf(format, args...)
		e.record(Decision{Type: "request", Outcome: "deny", Rule: rule, Reason: reason, URL: shown(r.URL), Resource: r.Type, Kind: "navigate"})
		return errors.New(reason)
	}
	allow := func(rule string) error {
		if e.logAll {
			e.record(Decision{Type: "request", Outcome: "allow", Rule: rule, URL: shown(r.URL), Resource: r.Type})
		}
		return nil
	}

	if r.URL == "about:blank" || strings.HasPrefix(r.URL, "chrome-error:") {
		return nil
	}
	u, err := url.Parse(r.URL)
	if err != nil {
		return deny("scheme", "the address %q does not read as an address", shown(r.URL))
	}
	switch strings.ToLower(u.Scheme) {
	case "data", "blob":
		if r.Navigation {
			return deny("scheme", "a page of its own cannot be a %s: address", u.Scheme)
		}
		return nil
	case "http", "https", "ws", "wss":
	default:
		return deny("scheme", "addresses starting with %s: are not allowed", u.Scheme)
	}

	host := strings.ToLower(u.Hostname())
	ip, isIP := urlmatch.IsLiteralIP(host)
	if isIP && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
		return deny("link-local", "%s is a link-local address, which is never allowed because it is where cloud machines keep their credentials", host)
	}

	for _, p := range e.deny {
		if p.MatchURL(u) {
			return deny("network.deny", "%s matches the deny rule %q", shown(r.URL), p.Source)
		}
	}
	matched := ""
	for _, p := range e.allow {
		if p.MatchURL(u) {
			matched = p.Source
			break
		}
	}
	if matched != "" {
		return allow("network.allow")
	}
	if e.cfg.Network.BlockPrivate && isPrivate(host, ip, isIP) {
		return deny("network.block_private", "%s is on this machine or its local network, which the rules keep closed", host)
	}
	if len(e.allow) > 0 {
		return deny("network.allow", "%s is not on the list of sites the rules allow", shown(r.URL))
	}
	return allow("")
}

func isPrivate(host string, ip net.IP, isIP bool) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return true
	}
	return isIP && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified())
}

// builtin says what an action does when no rule matches and no default is set.
func builtin(kind string) (decision, reason string) {
	switch kind {
	case "eval":
		return "ask", "a script run in the page can do anything the page can"
	case "upload":
		return "ask", "an upload sends a file from this machine to a website"
	}
	return "allow", ""
}

func (r compiledRule) matches(a tools.Action) bool {
	if r.Kind != "" && r.Kind != "*" && r.Kind != a.Kind {
		return false
	}
	if r.Element != "" && !urlmatch.Glob(r.Element, a.Element, true) {
		return false
	}
	if r.Detail != "" && !urlmatch.Glob(r.Detail, a.Detail, true) {
		return false
	}
	if r.URL != "" && r.URL != "*" && (r.url == nil || !r.url.Match(a.URL)) {
		return false
	}
	return true
}

// CheckAction decides whether a tool may run.
func (e *Engine) CheckAction(ctx context.Context, a tools.Action) error {
	decision, reason, rule := "", "", ""
	for i, r := range e.rules {
		if r.matches(a) {
			decision, reason, rule = r.Decision, r.Reason, fmt.Sprintf("rule %d", i+1)
			break
		}
	}
	if decision == "" {
		if d := e.cfg.Actions.Default; d != "" {
			decision, rule = d, "default"
		} else {
			decision, reason = builtin(a.Kind)
			rule = "built-in"
		}
	}

	d := Decision{Type: "action", Rule: rule, Reason: reason, Tool: a.Tool, Kind: a.Kind,
		URL: shown(a.URL), Element: a.Element, Detail: scrub(a.Detail)}
	switch decision {
	case "allow":
		d.Outcome = "allow"
		e.record(d)
		return nil
	case "deny":
		d.Outcome = "deny"
		e.record(d)
		if reason == "" {
			reason = "the rules do not allow this"
		}
		return fmt.Errorf("refused by %s: %s", rule, reason)
	}

	// The rule asks. With nobody to ask, the refusal says what to set up.
	if _, none := e.approver.(Deny); none {
		d.Outcome = "refused"
		e.record(d)
		return fmt.Errorf("this needs a person's approval (%s) and no way to ask one is set up: choose rules.approval.mode in the rules, or change the rule", firstNonEmpty(reason, rule))
	}
	ok, err := e.approver.Approve(ctx, a, reason)
	switch {
	case err != nil:
		d.Outcome = "unanswered"
		d.Reason = firstNonEmpty(reason, err.Error())
		e.record(d)
		return fmt.Errorf("a person was asked (%s) and the answer did not come: %w", firstNonEmpty(reason, rule), err)
	case !ok:
		d.Outcome = "refused"
		e.record(d)
		return fmt.Errorf("a person did not approve this (%s)", firstNonEmpty(reason, rule))
	}
	d.Outcome = "approved"
	e.record(d)
	return nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
