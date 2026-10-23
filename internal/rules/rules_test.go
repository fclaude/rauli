package rules

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/fclaude/rauli/internal/chrome"
	"github.com/fclaude/rauli/internal/tools"
)

const fullFile = `
[network]
allow = ["https://*.example.com", "http://localhost"]
deny = ["https://ads.example.com"]
block_private = true

[actions]
default = "allow"

[[actions.rule]]
kind = "click"
element = "*pay*"
url = "https://shop.example.com/*"
decision = "ask"
reason = "payments need a person"

[[actions.rule]]
kind = "eval"
decision = "deny"

[approval]
mode = "command"
command = ["approve-me", "--now"]
timeout = "30s"

[audit]
requests = "all"
`

func TestParseReadsAFullFile(t *testing.T) {
	c, err := Parse([]byte(fullFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Network.Allow) != 2 || !c.Network.BlockPrivate || len(c.Actions.Rule) != 2 ||
		c.Actions.Rule[0].Decision != "ask" || c.Approval.Mode != "command" || c.Audit.Requests != "all" {
		t.Fatalf("read wrongly: %+v", c)
	}
	ap, err := c.Approver()
	if err != nil {
		t.Fatal(err)
	}
	cmd, ok := ap.(Command)
	if !ok || cmd.Timeout != 30*time.Second || len(cmd.Argv) != 2 {
		t.Fatalf("approver: %#v", ap)
	}
}

func TestParseNamesWhatIsWrong(t *testing.T) {
	for name, tt := range map[string]struct{ in, want string }{
		"misspelt key":         {"[network]\nalow = [\"https://a.example\"]\n", "alow"},
		"misspelt table":       {"[netwrok]\nallow = []\n", "netwrok"},
		"bad allow pattern":    {"[network]\nallow = [\"https://ok.example\", \"ftp://nope\"]\n", "rules.network.allow[1]"},
		"bad deny pattern":     {"[network]\ndeny = [\"https://a.example:port\"]\n", "rules.network.deny[0]"},
		"bad default":          {"[actions]\ndefault = \"maybe\"\n", "rules.actions.default"},
		"bad decision":         {"[[actions.rule]]\nkind = \"click\"\ndecision = \"perhaps\"\n", "rules.actions.rule[0].decision"},
		"missing decision":     {"[[actions.rule]]\nkind = \"click\"\n", "rules.actions.rule[0].decision"},
		"bad kind":             {"[[actions.rule]]\nkind = \"tap\"\ndecision = \"deny\"\n", "rules.actions.rule[0].kind"},
		"bad rule url":         {"[[actions.rule]]\nurl = \"nonsense://x\"\ndecision = \"deny\"\n", "rules.actions.rule[0].url"},
		"rule matching all":    {"[[actions.rule]]\ndecision = \"deny\"\n", "set rules.actions.default"},
		"bad approval mode":    {"[approval]\nmode = \"telepathy\"\n", "rules.approval.mode"},
		"command without argv": {"[approval]\nmode = \"command\"\n", "rules.approval.command"},
		"bad timeout":          {"[approval]\ntimeout = \"soon\"\n", "rules.approval.timeout"},
		"bad audit":            {"[audit]\nrequests = \"some\"\n", "rules.audit.requests"},
		"not toml":             {"allow = = =", "line 1"},
	} {
		_, err := Parse([]byte(tt.in))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: got %v, want an error mentioning %q", name, err, tt.want)
		}
	}
	// Every problem is reported at once, so a person fixes the file in one pass.
	_, err := Parse([]byte("[network]\nallow = [\"ftp://a\"]\n[actions]\ndefault = \"x\"\n"))
	if err == nil || !strings.Contains(err.Error(), "network.allow[0]") || !strings.Contains(err.Error(), "actions.default") {
		t.Fatalf("both problems should be listed, got %v", err)
	}
}

func engine(t *testing.T, c Config, ap Approver) *Engine {
	t.Helper()
	e, err := New(c, ap)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestNetworkDecisions(t *testing.T) {
	type req struct {
		url        string
		navigation bool
		want       bool
	}
	tests := []struct {
		name string
		cfg  Config
		reqs []req
	}{
		{"with no rules the web is open and little else", Config{}, []req{
			{"https://example.com/a?x=1", true, true},
			{"http://localhost:3000/", true, true},
			{"wss://example.com/socket", false, true},
			{"about:blank", true, true},
			{"chrome-error://chromewebdata/", true, true},
			{"data:image/png;base64,AAAA", false, true},
			{"blob:https://example.com/uuid", false, true},
			{"data:text/html,<h1>hi</h1>", true, false},
			{"blob:https://example.com/uuid", true, false},
			{"file:///etc/passwd", true, false},
			{"ftp://example.com/", true, false},
			{"chrome://settings", true, false},
			{"javascript:alert(1)", true, false},
		}},
		{"link-local is never reachable, even when listed", Config{Network: NetworkConfig{Allow: []string{"http://169.254.169.254", "http://[fe80::1]"}}}, []req{
			{"http://169.254.169.254/latest/meta-data/", true, false},
			{"http://169.254.1.1/", false, false},
			{"http://[fe80::1]/", true, false},
		}},
		{"an allow list refuses everything else", Config{Network: NetworkConfig{Allow: []string{"https://*.example.com", "https://example.com"}}}, []req{
			{"https://example.com/", true, true},
			{"https://www.example.com/x", true, true},
			{"https://cdn.example.com/app.js", false, true},
			{"http://www.example.com/", true, false},
			{"https://example.org/", true, false},
			{"https://example.com.evil.net/", true, false},
			{"wss://www.example.com/s", false, true},
		}},
		{"deny wins over allow", Config{Network: NetworkConfig{
			Allow: []string{"https://*.example.com"}, Deny: []string{"https://ads.example.com", "https://*.example.com/admin/*"}}}, []req{
			{"https://www.example.com/", true, true},
			{"https://ads.example.com/banner.png", false, false},
			{"https://www.example.com/admin/users", true, false},
			{"https://www.example.com/administrators", true, true},
		}},
		{"a deny list alone leaves the rest open", Config{Network: NetworkConfig{Deny: []string{"https://*.tracker.example"}}}, []req{
			{"https://news.example/", true, true},
			{"https://pixel.tracker.example/p.gif", false, false},
		}},
		{"block_private keeps the local network closed", Config{Network: NetworkConfig{BlockPrivate: true}}, []req{
			{"http://localhost:3000/", true, false},
			{"http://app.localhost/", true, false},
			{"http://127.0.0.1/", true, false},
			{"http://[::1]:8080/", true, false},
			{"http://192.168.1.20/", true, false},
			{"http://10.1.2.3/", true, false},
			{"http://172.16.0.9/", true, false},
			{"http://printer.local/", true, false},
			{"http://wiki.internal/", true, false},
			{"http://0.0.0.0/", true, false},
			{"https://example.com/", true, true},
			{"http://8.8.8.8/", true, true},
		}},
		{"an allowed development server stays reachable under block_private", Config{Network: NetworkConfig{
			BlockPrivate: true, Allow: []string{"http://localhost:3000", "https://example.com"}}}, []req{
			{"http://localhost:3000/app", true, true},
			{"http://localhost:3001/app", true, false},
			{"http://127.0.0.1:3000/app", true, true},
			{"http://192.168.1.20/", true, false},
		}},
	}
	for _, tt := range tests {
		e := engine(t, tt.cfg, nil)
		for _, r := range tt.reqs {
			err := e.CheckURL(context.Background(), chrome.Request{URL: r.url, Method: "GET", Type: "Document", Navigation: r.navigation})
			if (err == nil) != r.want {
				t.Errorf("%s: %s: allowed=%v, want %v (%v)", tt.name, r.url, err == nil, r.want, err)
			}
		}
	}
}

func TestRefusalsGiveAReasonAndLeakNoQueryString(t *testing.T) {
	e := engine(t, Config{Network: NetworkConfig{Allow: []string{"https://ok.example"}}}, nil)
	ch, cancel := e.Subscribe()
	defer cancel()
	err := e.CheckURL(context.Background(), chrome.Request{URL: "https://other.example/path?token=SECRETVALUE#frag", Type: "Document", Navigation: true})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "https://other.example/path") || !strings.Contains(err.Error(), "list of sites") {
		t.Fatalf("the reason does not say what happened: %v", err)
	}
	d := <-ch
	if d.Type != "request" || d.Outcome != "deny" || d.Rule != "network.allow" {
		t.Fatalf("decision: %+v", d)
	}
	for _, s := range []string{err.Error(), d.URL, d.Reason} {
		if strings.Contains(s, "SECRETVALUE") || strings.Contains(s, "frag") {
			t.Fatalf("a query or fragment leaked into %q", s)
		}
	}
}

type answer struct {
	yes bool
	err error
	got *tools.Action
	why *string
}

func (a answer) Approve(_ context.Context, act tools.Action, reason string) (bool, error) {
	if a.got != nil {
		*a.got = act
	}
	if a.why != nil {
		*a.why = reason
	}
	return a.yes, a.err
}

func TestActionRulesFirstMatchWins(t *testing.T) {
	cfg := Config{Actions: ActionsConfig{Default: "allow", Rule: []ActionRule{
		{Kind: "click", Element: "*pay*", URL: "https://shop.example/*", Decision: "deny", Reason: "no payments here"},
		{Kind: "click", Element: "*pay*", Decision: "allow"},
		{Kind: "upload", Detail: "/home/*/Documents/*", Decision: "allow"},
		{Kind: "upload", Decision: "deny", Reason: "uploads are off"},
		{Kind: "*", Element: `*delete*`, Decision: "deny"},
		{URL: "https://bank.example/*", Decision: "deny", Reason: "the bank is off limits"},
	}}}
	e := engine(t, cfg, nil)
	tests := []struct {
		a    tools.Action
		want string // "" allowed, otherwise a part of the error
	}{
		{tools.Action{Kind: "click", Element: `button "Pay now"`, URL: "https://shop.example/cart"}, "refused by rule 1: no payments here"},
		{tools.Action{Kind: "click", Element: `button "PAY NOW"`, URL: "https://shop.example/cart"}, "rule 1"},
		{tools.Action{Kind: "click", Element: `button "Pay now"`, URL: "https://other.example/"}, ""},
		{tools.Action{Kind: "click", Element: `link "Home"`, URL: "https://shop.example/"}, ""},
		{tools.Action{Kind: "upload", Detail: "/home/me/Documents/cv.pdf"}, ""},
		{tools.Action{Kind: "upload", Detail: "/home/me/.ssh/id_rsa"}, "uploads are off"},
		{tools.Action{Kind: "type", Element: `button "Delete account"`}, "refused by rule 5"},
		{tools.Action{Kind: "type", Element: `textbox "Name"`, URL: "https://bank.example/login"}, "the bank is off limits"},
		{tools.Action{Kind: "click", Element: `link "Home"`, URL: "https://www.bank.example/"}, ""},
	}
	for _, tt := range tests {
		err := e.CheckAction(context.Background(), tt.a)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%+v: refused: %v", tt.a, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("%+v: got %v, want an error mentioning %q", tt.a, err, tt.want)
		}
	}
}

func TestBuiltInDefaultsAskAboutEvalAndUpload(t *testing.T) {
	e := engine(t, Config{}, nil) // nobody can approve
	for _, kind := range []string{"eval", "upload"} {
		err := e.CheckAction(context.Background(), tools.Action{Kind: kind})
		if err == nil || !strings.Contains(err.Error(), "needs a person's approval") || !strings.Contains(err.Error(), "rules.approval.mode") {
			t.Errorf("%s: %v", kind, err)
		}
	}
	for _, kind := range []string{"navigate", "click", "type", "key", "select", "credential"} {
		if err := e.CheckAction(context.Background(), tools.Action{Kind: kind}); err != nil {
			t.Errorf("%s should be allowed by default: %v", kind, err)
		}
	}
	// Choosing a default turns the built-in ones off, and rules still come first.
	open := engine(t, Config{Actions: ActionsConfig{Default: "allow"}}, nil)
	if err := open.CheckAction(context.Background(), tools.Action{Kind: "eval"}); err != nil {
		t.Fatalf("an explicit default of allow should allow eval: %v", err)
	}
	closed := engine(t, Config{Actions: ActionsConfig{Default: "deny"}}, nil)
	if err := closed.CheckAction(context.Background(), tools.Action{Kind: "click"}); err == nil || !strings.Contains(err.Error(), "the rules do not allow this") {
		t.Fatalf("a default of deny: %v", err)
	}
}

func TestAskGoesToTheApprover(t *testing.T) {
	cfg := Config{Actions: ActionsConfig{Rule: []ActionRule{{Kind: "click", Element: "*pay*", Decision: "ask", Reason: "payments need a person"}}}}
	act := tools.Action{Tool: "click", Kind: "click", Element: `button "Pay now"`, URL: "https://shop.example/cart"}

	var got tools.Action
	var why string
	e := engine(t, cfg, answer{yes: true, got: &got, why: &why})
	ch, cancel := e.Subscribe()
	defer cancel()
	if err := e.CheckAction(context.Background(), act); err != nil {
		t.Fatalf("approved but refused: %v", err)
	}
	if got != act || why != "payments need a person" {
		t.Fatalf("the approver was given %+v, %q", got, why)
	}
	if d := <-ch; d.Outcome != "approved" || d.Rule != "rule 1" || d.Reason != "payments need a person" || d.Element != `button "Pay now"` {
		t.Fatalf("decision: %+v", d)
	}

	e = engine(t, cfg, answer{yes: false})
	ch2, cancel2 := e.Subscribe()
	defer cancel2()
	err := e.CheckAction(context.Background(), act)
	if err == nil || !strings.Contains(err.Error(), "did not approve") || !strings.Contains(err.Error(), "payments need a person") {
		t.Fatalf("refused answer: %v", err)
	}
	if d := <-ch2; d.Outcome != "refused" {
		t.Fatalf("decision: %+v", d)
	}

	e = engine(t, cfg, answer{err: ErrNoAnswer})
	ch3, cancel3 := e.Subscribe()
	defer cancel3()
	err = e.CheckAction(context.Background(), act)
	if !errors.Is(err, ErrNoAnswer) || !strings.Contains(err.Error(), "the answer did not come") {
		t.Fatalf("silence: %v", err)
	}
	if d := <-ch3; d.Outcome != "unanswered" {
		t.Fatalf("decision: %+v", d)
	}
}

func TestTheConfigNamesTheApprover(t *testing.T) {
	cfg := Config{Approval: ApprovalConfig{Mode: "terminal", Timeout: "10s"}, Actions: ActionsConfig{Default: "ask"}}
	e := engine(t, cfg, nil)
	if tm, ok := e.approver.(*Terminal); !ok || tm.Timeout != 10*time.Second {
		t.Fatalf("approver: %#v", e.approver)
	}
	if _, ok := engine(t, Config{}, nil).approver.(Deny); !ok {
		t.Fatal("the default approver should be Deny")
	}
	// An approver passed in wins over the file.
	q := NewQueue(time.Second)
	if engine(t, cfg, q).approver != Approver(q) {
		t.Fatal("an approver passed to New should be used")
	}
}

func TestDecisionsRecordRequestsOnlyWhenAsked(t *testing.T) {
	for _, mode := range []string{"", "all"} {
		e := engine(t, Config{Audit: AuditConfig{Requests: mode}}, nil)
		ch, cancel := e.Subscribe()
		e.CheckURL(context.Background(), chrome.Request{URL: "https://example.com/x?y=1", Type: "Script"})
		select {
		case d := <-ch:
			if mode == "" {
				t.Fatalf("an allowed request was recorded by default: %+v", d)
			}
			if d.Outcome != "allow" || d.Resource != "Script" || strings.Contains(d.URL, "?") {
				t.Fatalf("decision: %+v", d)
			}
		case <-time.After(100 * time.Millisecond):
			if mode == "all" {
				t.Fatal("audit.requests = all should record allowed requests")
			}
		}
		cancel()
	}
}

// The configuration file holds more than rules. Config must read from its own
// table, and an unknown key inside it must still be an error when the whole
// file is read strictly, which is how the program reads it.
func TestConfigReadsFromTheRulesTableOfALargerFile(t *testing.T) {
	type file struct {
		Browser map[string]any `toml:"browser"`
		Rules   Config         `toml:"rules"`
	}
	read := func(doc string) (file, error) {
		var f file
		dec := toml.NewDecoder(strings.NewReader(doc))
		dec.DisallowUnknownFields()
		return f, dec.Decode(&f)
	}
	f, err := read("[browser]\nheaded = true\n\n[rules.network]\nallow = [\"https://a.example\"]\n\n[[rules.actions.rule]]\nkind = \"eval\"\ndecision = \"deny\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Rules.Network.Allow) != 1 || len(f.Rules.Actions.Rule) != 1 || f.Rules.Validate() != nil {
		t.Fatalf("read wrongly: %+v", f.Rules)
	}
	// The error's own text is generic; String() on the strict-mode error names the key.
	_, err = read("[rules.network]\nalow = []\n")
	var strict *toml.StrictMissingError
	if !errors.As(err, &strict) || !strings.Contains(strict.String(), "alow") {
		t.Fatalf("a misspelt key in the rules table should be an error naming it, got %v", err)
	}
}
