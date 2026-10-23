package rules

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/fclaude/rauli/internal/tools"
)

var urlText = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>)\]]+`)

// scrub removes queries, fragments and logins from every address in a piece of
// text. An address in a log is read by more people than the page was, and a
// token in a query string is the usual way a session is handed over.
func scrub(s string) string {
	return urlText.ReplaceAllStringFunc(s, func(m string) string {
		u, err := url.Parse(m)
		if err != nil || u.Host == "" {
			return m
		}
		return shown(m)
	})
}

type toolLine struct {
	Type      string          `json:"type"`
	Time      time.Time       `json:"time"`
	Tool      string          `json:"tool"`
	Args      json.RawMessage `json:"args,omitempty"`
	URL       string          `json:"url,omitempty"`
	Title     string          `json:"title,omitempty"`
	Summary   string          `json:"summary,omitempty"`
	Error     string          `json:"error,omitempty"`
	ElapsedMS int64           `json:"elapsed_ms"`
}

type decisionLine struct {
	Type string `json:"type"`
	Decision
}

// summaryless are the tools whose result is page content, which the log keeps out.
var summaryless = map[string]bool{"eval": true, "text": true, "console": true}

// Audit writes one JSON line for each tool call and for each decision the rules
// made, until ctx ends. It is meant to run in its own goroutine.
//
// The lines never hold a secret or typed text: the tools log typed text by
// length, a credential is named and never shown, and Audit strips queries,
// fragments and logins from every address. It does not write what eval, text
// and console returned, because that is the content of a page.
func Audit(ctx context.Context, s *tools.Session, w io.Writer) error {
	events, cancelEvents := s.Subscribe()
	defer cancelEvents()
	var decisions <-chan Decision
	if e, ok := s.Guard().(*Engine); ok {
		var cancel func()
		decisions, cancel = e.Subscribe()
		defer cancel()
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	for {
		var line any
		select {
		case <-ctx.Done():
			return nil
		case ev := <-events:
			l := toolLine{Type: "tool", Time: ev.Time, Tool: ev.Tool, Args: scrubArgs(ev.Args), URL: shown(ev.URL),
				Title: ev.Title, Error: scrub(ev.Error), ElapsedMS: ev.Elapsed.Milliseconds()}
			if !summaryless[ev.Tool] {
				l.Summary = scrub(ev.Summary)
			}
			line = l
		case d := <-decisions:
			d.Reason = scrub(d.Reason)
			line = decisionLine{Type: "decision", Decision: d}
		}
		if err := enc.Encode(line); err != nil {
			return err
		}
	}
}

// scrubArgs removes queries from addresses in a tool's arguments.
func scrubArgs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	for k, v := range m {
		if s, ok := v.(string); ok && (k == "url" || strings.Contains(s, "://")) {
			m[k] = scrub(s)
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return out
}
