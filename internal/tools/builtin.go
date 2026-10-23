package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net/url"
	"strings"
	"time"

	"github.com/fclaude/rauli/internal/chrome"
)

// decode reads a tool's arguments, refusing names the tool does not have. A
// misspelt argument that is quietly ignored makes an agent believe it asked
// for something it did not.
func decode(args json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

func object(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func prop(typ, doc string) map[string]any { return map[string]any{"type": typ, "description": doc} }

var (
	refProp      = prop("string", "Element reference from the latest snapshot, such as e12.")
	snapshotProp = prop("boolean", "Return a fresh snapshot afterwards. Default true.")
)

func wantSnapshot(p *bool) bool { return p == nil || *p }

// guarded asks the Guard before an action runs.
func (s *Session) guarded(ctx context.Context, p *chrome.Page, tool, kind, element, detail string) error {
	info, _ := p.Info(ctx)
	err := s.guard.CheckAction(ctx, Action{Tool: tool, Kind: kind, URL: info.URL, Element: element, Detail: detail})
	if err != nil {
		return fmt.Errorf("not allowed: %w", err)
	}
	return nil
}

// report is what an action returns: where the tab is now, anything notable
// that happened (a dialog, a new tab), and optionally the page itself.
func (s *Session) report(ctx context.Context, p *chrome.Page, snapshot bool, lead string) (Result, error) {
	var b strings.Builder
	if lead != "" {
		b.WriteString(lead + "\n")
	}
	// Only requests for a page are worth saying; a blocked image or tracker is routine.
	for _, bl := range p.Blocked() {
		fmt.Fprintf(&b, "Blocked by the rules: %s\n", bl)
	}
	for _, d := range p.Dialogs() {
		verb := "dismissed"
		if d.Accepted {
			verb = "accepted"
		}
		fmt.Fprintf(&b, "A %s dialog appeared and was %s: %q\n", d.Type, verb, d.Message)
	}
	if pages, err := s.b.Pages(ctx); err == nil && len(pages) > 1 {
		s.mu.Lock()
		if s.known == nil {
			s.known = map[string]bool{}
		}
		for i, q := range pages {
			if !s.known[q.ID()] && q != p {
				info, _ := q.Info(ctx)
				fmt.Fprintf(&b, "A new tab opened: tab %d %q %s (switch with tabs)\n", i, info.Title, info.URL)
			}
		}
		for _, q := range pages {
			s.known[q.ID()] = true
		}
		s.mu.Unlock()
	}
	if snapshot {
		snap, err := p.Snapshot(ctx, chrome.SnapshotOptions{})
		if err != nil {
			return Result{}, err
		}
		b.WriteString(snap)
	} else if info, err := p.Info(ctx); err == nil {
		fmt.Fprintf(&b, "Page: %s\nURL: %s\n", info.Title, info.URL)
	}
	return Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

func builtin() []Tool {
	return []Tool{
		{
			Name:        "navigate",
			Description: "Go to a web address in the current tab and wait for the page to load. Addresses without a scheme are taken as https. Returns a snapshot of the page.",
			Schema:      object(map[string]any{"url": prop("string", "The address to open."), "snapshot": snapshotProp}, "url"),
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					URL      string `json:"url"`
					Snapshot *bool  `json:"snapshot"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				target, err := normalise(a.URL)
				if err != nil {
					return Result{}, err
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				if err := s.guarded(ctx, p, "navigate", "navigate", "", target); err != nil {
					return Result{}, err
				}
				if err := p.Navigate(ctx, target); err != nil {
					return Result{}, withBlocked(p, err)
				}
				return s.report(ctx, p, wantSnapshot(a.Snapshot), "")
			},
		},
		{
			Name:        "snapshot",
			Description: "Read the current page as text: headings, text, links with their addresses, and form controls. Every element you can act on has a reference such as e12, which the other tools take. References end when the page changes, so take a new snapshot after anything that loads a page.",
			Schema:      object(map[string]any{}),
			ReadOnly:    true,
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				return s.report(ctx, p, true, "")
			},
		},
		{
			Name:        "text",
			Description: "Read the visible text of the page, in reading order, without the structure a snapshot gives. Useful for long articles.",
			Schema:      object(map[string]any{}),
			ReadOnly:    true,
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				t, err := p.Text(ctx)
				return Result{Text: t}, err
			},
		},
		{
			Name:        "screenshot",
			Description: "Take a picture of the page, or of one element. It works without any window being open.",
			Schema: object(map[string]any{
				"fullPage": prop("boolean", "Capture the whole scrollable page rather than the visible part."),
				"ref":      prop("string", "Capture only this element."),
				"jpeg":     prop("boolean", "Return a JPEG, which is smaller, instead of a PNG."),
			}),
			ReadOnly: true,
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					FullPage bool   `json:"fullPage"`
					Ref      string `json:"ref"`
					JPEG     bool   `json:"jpeg"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				data, err := p.Screenshot(ctx, chrome.ShotOptions{FullPage: a.FullPage, Ref: a.Ref, JPEG: a.JPEG})
				if err != nil {
					return Result{}, err
				}
				mime, desc := "image/png", ""
				if a.JPEG {
					mime = "image/jpeg"
				} else if cfg, err := png.DecodeConfig(bytes.NewReader(data)); err == nil {
					desc = fmt.Sprintf(" (%dx%d)", cfg.Width, cfg.Height)
				}
				info, _ := p.Info(ctx)
				return Result{Text: fmt.Sprintf("Screenshot of %s%s", info.URL, desc), Images: []Image{{MIME: mime, Data: data}}}, nil
			},
		},
		{
			Name:        "click",
			Description: "Click an element, in the middle, with a real mouse click. If it loads a page, waits for it.",
			Schema: object(map[string]any{
				"ref": refProp, "double": prop("boolean", "Double-click."), "right": prop("boolean", "Use the right button."), "snapshot": snapshotProp,
			}, "ref"),
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					Ref      string `json:"ref"`
					Double   bool   `json:"double"`
					Right    bool   `json:"right"`
					Snapshot *bool  `json:"snapshot"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				if err := s.guarded(ctx, p, "click", "click", p.Describe(a.Ref), ""); err != nil {
					return Result{}, err
				}
				if err := p.Click(ctx, a.Ref, chrome.ClickOptions{Double: a.Double, Right: a.Right}); err != nil {
					return Result{}, err
				}
				return s.report(ctx, p, wantSnapshot(a.Snapshot), "")
			},
		},
		{
			Name:        "click_at",
			Description: "Click a point in the visible page, in pixels from its top left corner. For things a snapshot does not list, such as a drawing area or a custom control.",
			Schema:      object(map[string]any{"x": prop("number", "Pixels from the left."), "y": prop("number", "Pixels from the top."), "snapshot": snapshotProp}, "x", "y"),
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					X        float64 `json:"x"`
					Y        float64 `json:"y"`
					Snapshot *bool   `json:"snapshot"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				if err := s.guarded(ctx, p, "click_at", "click", fmt.Sprintf("point (%g, %g)", a.X, a.Y), ""); err != nil {
					return Result{}, err
				}
				if err := p.ClickAt(ctx, a.X, a.Y, chrome.ClickOptions{}); err != nil {
					return Result{}, err
				}
				return s.report(ctx, p, wantSnapshot(a.Snapshot), "")
			},
		},
		{
			Name:        "hover",
			Description: "Move the pointer over an element, which opens menus and tooltips.",
			Schema:      object(map[string]any{"ref": refProp, "snapshot": snapshotProp}, "ref"),
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					Ref      string `json:"ref"`
					Snapshot *bool  `json:"snapshot"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				if err := p.Hover(ctx, a.Ref); err != nil {
					return Result{}, err
				}
				return s.report(ctx, p, wantSnapshot(a.Snapshot), "")
			},
		},
		{
			Name:        "type",
			Description: "Type text into a field. By default it adds to what is there; set replace to start over. Set submit to press Enter afterwards. Do not use this for passwords: use fill_credential, which keeps the secret out of the conversation.",
			Schema: object(map[string]any{
				"ref": refProp, "text": prop("string", "What to type."),
				"replace":  prop("boolean", "Select the field's contents first, so the text replaces them."),
				"submit":   prop("boolean", "Press Enter afterwards."),
				"slowly":   prop("boolean", "Send each character as a key press, for pages that listen for keys."),
				"snapshot": snapshotProp,
			}, "ref", "text"),
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					Ref      string `json:"ref"`
					Text     string `json:"text"`
					Replace  bool   `json:"replace"`
					Submit   bool   `json:"submit"`
					Slowly   bool   `json:"slowly"`
					Snapshot *bool  `json:"snapshot"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				detail := ""
				if a.Submit {
					detail = "submit"
				}
				if err := s.guarded(ctx, p, "type", "type", p.Describe(a.Ref), detail); err != nil {
					return Result{}, err
				}
				if err := p.Type(ctx, a.Ref, a.Text, chrome.TypeOptions{Replace: a.Replace, Submit: a.Submit, Slowly: a.Slowly}); err != nil {
					return Result{}, err
				}
				return s.report(ctx, p, wantSnapshot(a.Snapshot), "")
			},
		},
		{
			Name:        "press",
			Description: "Press a key or a combination, such as Enter, Tab, Escape, ArrowDown, a single character, or Control+a.",
			Schema:      object(map[string]any{"key": prop("string", "The key or combination."), "snapshot": snapshotProp}, "key"),
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					Key      string `json:"key"`
					Snapshot *bool  `json:"snapshot"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				if err := s.guarded(ctx, p, "press", "key", "", a.Key); err != nil {
					return Result{}, err
				}
				if err := p.Press(ctx, a.Key); err != nil {
					return Result{}, err
				}
				return s.report(ctx, p, wantSnapshot(a.Snapshot), "")
			},
		},
		{
			Name:        "scroll",
			Description: "Scroll the page. A positive dy scrolls down. With no arguments it scrolls down 600 pixels.",
			Schema:      object(map[string]any{"dx": prop("number", "Pixels to the right."), "dy": prop("number", "Pixels down."), "snapshot": snapshotProp}),
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					DX       float64 `json:"dx"`
					DY       float64 `json:"dy"`
					Snapshot *bool   `json:"snapshot"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				if a.DX == 0 && a.DY == 0 {
					a.DY = 600
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				if err := p.Scroll(ctx, a.DX, a.DY); err != nil {
					return Result{}, err
				}
				return s.report(ctx, p, wantSnapshot(a.Snapshot), "")
			},
		},
		{
			Name:        "select",
			Description: "Choose an option of a drop-down list, by its visible text or its value.",
			Schema:      object(map[string]any{"ref": refProp, "option": prop("string", "The option's text or value."), "snapshot": snapshotProp}, "ref", "option"),
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					Ref      string `json:"ref"`
					Option   string `json:"option"`
					Snapshot *bool  `json:"snapshot"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				if err := s.guarded(ctx, p, "select", "select", p.Describe(a.Ref), a.Option); err != nil {
					return Result{}, err
				}
				if err := p.Select(ctx, a.Ref, a.Option); err != nil {
					return Result{}, err
				}
				return s.report(ctx, p, wantSnapshot(a.Snapshot), "")
			},
		},
		{
			Name:        "upload",
			Description: "Put files into a file input, as choosing them in the file dialog would.",
			Schema:      object(map[string]any{"ref": refProp, "paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Absolute paths of the files."}, "snapshot": snapshotProp}, "ref", "paths"),
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					Ref      string   `json:"ref"`
					Paths    []string `json:"paths"`
					Snapshot *bool    `json:"snapshot"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				if err := s.guarded(ctx, p, "upload", "upload", p.Describe(a.Ref), strings.Join(a.Paths, ", ")); err != nil {
					return Result{}, err
				}
				if err := p.SetFiles(ctx, a.Ref, a.Paths); err != nil {
					return Result{}, err
				}
				return s.report(ctx, p, wantSnapshot(a.Snapshot), "")
			},
		},
		{
			Name:        "back",
			Description: "Go back one page in the tab's history.",
			Schema:      object(map[string]any{"snapshot": snapshotProp}),
			run:         history(-1),
		},
		{
			Name:        "forward",
			Description: "Go forward one page in the tab's history.",
			Schema:      object(map[string]any{"snapshot": snapshotProp}),
			run:         history(1),
		},
		{
			Name:        "reload",
			Description: "Load the current page again and wait for it to finish.",
			Schema:      object(map[string]any{"snapshot": snapshotProp}),
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					Snapshot *bool `json:"snapshot"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				if err := p.Reload(ctx); err != nil {
					return Result{}, err
				}
				return s.report(ctx, p, wantSnapshot(a.Snapshot), "")
			},
		},
		{
			Name:        "wait",
			Description: "Wait for some text to appear on the page, or for a number of seconds (at most 30).",
			Schema:      object(map[string]any{"text": prop("string", "Text to wait for."), "seconds": prop("number", "Seconds to wait."), "snapshot": snapshotProp}),
			ReadOnly:    true,
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					Text     string  `json:"text"`
					Seconds  float64 `json:"seconds"`
					Snapshot *bool   `json:"snapshot"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				if a.Text == "" && a.Seconds <= 0 {
					return Result{}, errors.New("say what to wait for: text, or seconds")
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				d := time.Duration(a.Seconds * float64(time.Second))
				if d > 30*time.Second {
					d = 30 * time.Second
				}
				wctx, cancel := context.WithTimeout(ctx, max(d, time.Second))
				defer cancel()
				switch {
				case a.Text != "":
					if d == 0 {
						wctx, cancel = context.WithTimeout(ctx, 30*time.Second)
						defer cancel()
					}
					if err := p.WaitForText(wctx, a.Text); err != nil {
						return Result{}, err
					}
				default:
					select {
					case <-time.After(d):
					case <-ctx.Done():
						return Result{}, ctx.Err()
					}
				}
				return s.report(ctx, p, wantSnapshot(a.Snapshot), "")
			},
		},
		{
			Name:        "console",
			Description: "Read what the page has written to its console since the last time, and any dialogs it raised.",
			Schema:      object(map[string]any{}),
			ReadOnly:    true,
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				var b strings.Builder
				for _, l := range p.Console() {
					b.WriteString(l + "\n")
				}
				for _, d := range p.Dialogs() {
					fmt.Fprintf(&b, "dialog %s %q accepted=%v\n", d.Type, d.Message, d.Accepted)
				}
				if b.Len() == 0 {
					return Result{Text: "(nothing new)"}, nil
				}
				return Result{Text: strings.TrimRight(b.String(), "\n")}, nil
			},
		},
		{
			Name:        "tabs",
			Description: "List the open tabs, open a new one, switch to one, or close one. Tabs are numbered from 0; the one the other tools act on is marked with *.",
			Schema: object(map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"list", "new", "select", "close"}, "description": "What to do. Default list."},
				"index":  prop("integer", "The tab to select or close. Closing defaults to the current tab."),
				"url":    prop("string", "An address to open in the new tab."),
			}),
			run: tabs,
		},
		{
			Name:        "resize",
			Description: "Change the size of the page's viewport, in pixels.",
			Schema:      object(map[string]any{"width": prop("integer", "Width."), "height": prop("integer", "Height.")}, "width", "height"),
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					Width  int `json:"width"`
					Height int `json:"height"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				if err := p.Resize(ctx, a.Width, a.Height); err != nil {
					return Result{}, err
				}
				return Result{Text: fmt.Sprintf("The viewport is now %dx%d.", a.Width, a.Height)}, nil
			},
		},
		{
			Name:        "eval",
			Description: "Run a JavaScript expression in the page and return its value as JSON. A last resort, for reading something the snapshot does not show; rules may refuse it.",
			Schema:      object(map[string]any{"expression": prop("string", "The expression.")}, "expression"),
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					Expression string `json:"expression"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				if err := s.guarded(ctx, p, "eval", "eval", "", truncate(a.Expression, 200)); err != nil {
					return Result{}, err
				}
				v, err := p.Eval(ctx, a.Expression)
				if err != nil {
					return Result{}, err
				}
				return Result{Text: string(v)}, nil
			},
		},
		{
			Name:        "credentials",
			Description: "List the logins the keychain holds: their names and which fields each has (such as user, password, totp), and where each may be used. Never the values.",
			Schema:      object(map[string]any{}),
			ReadOnly:    true,
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				if s.vault == nil {
					return Result{Text: "No keychain is configured."}, nil
				}
				hs, err := s.vault.Handles(ctx)
				if err != nil {
					return Result{}, err
				}
				if len(hs) == 0 {
					return Result{Text: "The keychain holds no logins."}, nil
				}
				var b strings.Builder
				for _, h := range hs {
					fmt.Fprintf(&b, "- %s: %s", h.Name, strings.Join(h.Fields, ", "))
					if len(h.Origins) > 0 {
						fmt.Fprintf(&b, " (only on %s)", strings.Join(h.Origins, ", "))
					}
					b.WriteByte('\n')
				}
				return Result{Text: strings.TrimRight(b.String(), "\n")}, nil
			},
		},
		{
			Name:        "fill_credential",
			Description: "Fill a field from the keychain, for example the user or password of a login, or a one-time code. The secret goes from the keychain straight into the page; you never see it. Use credentials to see what is available.",
			Schema: object(map[string]any{
				"ref": refProp, "handle": prop("string", "The login's name."), "field": prop("string", "Which part: user, password, totp and so on."),
				"submit": prop("boolean", "Press Enter afterwards."), "snapshot": snapshotProp,
			}, "ref", "handle", "field"),
			run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
				var a struct {
					Ref      string `json:"ref"`
					Handle   string `json:"handle"`
					Field    string `json:"field"`
					Submit   bool   `json:"submit"`
					Snapshot *bool  `json:"snapshot"`
				}
				if err := decode(args, &a); err != nil {
					return Result{}, err
				}
				if s.vault == nil {
					return Result{}, errors.New("no keychain is configured")
				}
				p, err := s.Page(ctx)
				if err != nil {
					return Result{}, err
				}
				if err := s.guarded(ctx, p, "fill_credential", "credential", p.Describe(a.Ref), a.Handle+"/"+a.Field); err != nil {
					return Result{}, err
				}
				info, _ := p.Info(ctx)
				secret, err := s.vault.Secret(ctx, a.Handle, a.Field, info.URL)
				if err != nil {
					return Result{}, err
				}
				err = p.Type(ctx, a.Ref, secret, chrome.TypeOptions{Replace: true, Submit: a.Submit})
				secret = ""
				if err != nil {
					// The error comes from the browser and cannot contain the secret,
					// but say nothing about the text typed all the same.
					return Result{}, fmt.Errorf("could not fill %s: %w", p.Describe(a.Ref), err)
				}
				return s.report(ctx, p, wantSnapshot(a.Snapshot), fmt.Sprintf("Filled %s with the %s of %q.", p.Describe(a.Ref), a.Field, a.Handle))
			},
		},
	}
}

// withBlocked adds to a failed navigation the reasons the rules gave for
// blocking requests, so the agent learns why the page would not load and not
// only that the browser refused it.
func withBlocked(p *chrome.Page, err error) error {
	if bl := p.Blocked(); len(bl) > 0 {
		return fmt.Errorf("%w (blocked by the rules: %s)", err, strings.Join(bl, "; "))
	}
	return err
}

func history(delta int) func(context.Context, *Session, json.RawMessage) (Result, error) {
	return func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
		var a struct {
			Snapshot *bool `json:"snapshot"`
		}
		if err := decode(args, &a); err != nil {
			return Result{}, err
		}
		p, err := s.Page(ctx)
		if err != nil {
			return Result{}, err
		}
		if err := p.History(ctx, delta); err != nil {
			return Result{}, err
		}
		return s.report(ctx, p, wantSnapshot(a.Snapshot), "")
	}
}

func tabs(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
	var a struct {
		Action string `json:"action"`
		Index  *int   `json:"index"`
		URL    string `json:"url"`
	}
	if err := decode(args, &a); err != nil {
		return Result{}, err
	}
	cur, err := s.Page(ctx)
	if err != nil {
		return Result{}, err
	}
	pages, err := s.b.Pages(ctx)
	if err != nil {
		return Result{}, err
	}
	pick := func() (*chrome.Page, error) {
		if a.Index == nil {
			return cur, nil
		}
		if *a.Index < 0 || *a.Index >= len(pages) {
			return nil, fmt.Errorf("there is no tab %d: the tabs are numbered 0 to %d", *a.Index, len(pages)-1)
		}
		return pages[*a.Index], nil
	}
	switch a.Action {
	case "", "list":
	case "new":
		target := ""
		if a.URL != "" {
			if target, err = normalise(a.URL); err != nil {
				return Result{}, err
			}
			if err := s.guarded(ctx, cur, "tabs", "navigate", "", target); err != nil {
				return Result{}, err
			}
		}
		p, err := s.b.NewPage(ctx)
		if err != nil {
			return Result{}, err
		}
		s.setPage(p)
		if target != "" {
			if err := p.Navigate(ctx, target); err != nil {
				return Result{}, err
			}
		}
	case "select":
		p, err := pick()
		if err != nil {
			return Result{}, err
		}
		s.setPage(p)
	case "close":
		p, err := pick()
		if err != nil {
			return Result{}, err
		}
		if err := p.Close(ctx); err != nil {
			return Result{}, err
		}
		if p == cur {
			s.mu.Lock()
			s.cur = nil
			s.mu.Unlock()
		}
	default:
		return Result{}, fmt.Errorf("unknown action %q: use list, new, select or close", a.Action)
	}

	pages, err = s.b.Pages(ctx)
	if err != nil {
		return Result{}, err
	}
	now, _ := s.Page(ctx)
	var b strings.Builder
	for i, p := range pages {
		info, _ := p.Info(ctx)
		mark := " "
		if p == now {
			mark = "*"
		}
		fmt.Fprintf(&b, "%s %d: %s  %s\n", mark, i, info.Title, info.URL)
	}
	return Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

// normalise turns what an agent typed into an address the browser may open.
// Only web pages are allowed: a file: address would read the machine's own
// files, and nothing the agent was asked to do needs that.
func normalise(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("give an address to open")
	}
	if raw == "about:blank" {
		return raw, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not a web address", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("only http and https addresses can be opened, not %s", u.Scheme)
	}
	return u.String(), nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
