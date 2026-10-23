package chrome

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Input goes through the browser's own input pipeline: real mouse and keyboard
// events, not calls into the page's scripts. A page cannot tell it from a
// person clicking, which is what lets agents do what a person can do, and what
// keeps sites that check for real events working.

// bounds is the element's box on screen, scrolled into view first.
func (p *Page) bounds(ctx context.Context, ref string) (x, y, w, h float64, err error) {
	id, err := p.node(ref)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	// Some nodes cannot be scrolled to (a hidden one, say); the box below says so better.
	_ = p.call(ctx, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": id}, nil)

	var res struct {
		Quads [][]float64 `json:"quads"`
	}
	if err := p.call(ctx, "DOM.getContentQuads", map[string]any{"backendNodeId": id}, &res); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("element %s is no longer on the page: %w", ref, err)
	}
	for _, q := range res.Quads {
		if len(q) < 8 {
			continue
		}
		minX, maxX, minY, maxY := q[0], q[0], q[1], q[1]
		for i := 0; i < 8; i += 2 {
			minX, maxX = min(minX, q[i]), max(maxX, q[i])
			minY, maxY = min(minY, q[i+1]), max(maxY, q[i+1])
		}
		if maxX-minX > 0 && maxY-minY > 0 {
			return minX, minY, maxX - minX, maxY - minY, nil
		}
	}
	return 0, 0, 0, 0, fmt.Errorf("element %s has no size on the page, so it is hidden or collapsed", ref)
}

func (p *Page) mouse(ctx context.Context, typ string, x, y float64, extra map[string]any) error {
	params := map[string]any{"type": typ, "x": x, "y": y}
	for k, v := range extra {
		params[k] = v
	}
	return p.call(ctx, "Input.dispatchMouseEvent", params, nil)
}

// ClickOptions say how to click.
type ClickOptions struct {
	// Double makes it a double click.
	Double bool
	// Right uses the right button.
	Right bool
}

// Click clicks the middle of an element and waits for any page load it causes.
func (p *Page) Click(ctx context.Context, ref string, o ClickOptions) error {
	x, y, w, h, err := p.bounds(ctx, ref)
	if err != nil {
		return err
	}
	return p.ClickAt(ctx, x+w/2, y+h/2, o)
}

// ClickAt clicks a point in the viewport, for things the accessibility tree
// does not describe, such as a canvas or a custom widget.
func (p *Page) ClickAt(ctx context.Context, x, y float64, o ClickOptions) error {
	button := "left"
	if o.Right {
		button = "right"
	}
	count := 1
	if o.Double {
		count = 2
	}
	return p.settle(ctx, func() error {
		if err := p.mouse(ctx, "mouseMoved", x, y, nil); err != nil {
			return err
		}
		for i := 1; i <= count; i++ {
			extra := map[string]any{"button": button, "clickCount": i}
			if err := p.mouse(ctx, "mousePressed", x, y, extra); err != nil {
				return err
			}
			if err := p.mouse(ctx, "mouseReleased", x, y, extra); err != nil {
				return err
			}
		}
		return nil
	})
}

// Hover moves the pointer over an element, which opens menus and tooltips.
func (p *Page) Hover(ctx context.Context, ref string) error {
	x, y, w, h, err := p.bounds(ctx, ref)
	if err != nil {
		return err
	}
	return p.mouse(ctx, "mouseMoved", x+w/2, y+h/2, nil)
}

// TypeOptions say how to type.
type TypeOptions struct {
	// Replace selects what is in the field first, so the text replaces it.
	Replace bool
	// Submit presses Enter afterwards.
	Submit bool
	// Slowly sends each character as a key press, for pages that listen for
	// key events. The default inserts the whole text at once, which is faster
	// and still raises the usual input events.
	Slowly bool
}

// Type focuses an element and types into it.
func (p *Page) Type(ctx context.Context, ref, text string, o TypeOptions) error {
	id, err := p.node(ref)
	if err != nil {
		return err
	}
	if err := p.call(ctx, "DOM.focus", map[string]any{"backendNodeId": id}, nil); err != nil {
		return fmt.Errorf("element %s cannot take focus: %w", ref, err)
	}
	if o.Replace {
		var obj struct {
			Object struct {
				ObjectID string `json:"objectId"`
			} `json:"object"`
		}
		if err := p.call(ctx, "DOM.resolveNode", map[string]any{"backendNodeId": id}, &obj); err != nil {
			return err
		}
		// select() covers inputs and textareas; the range covers editable regions.
		err := p.call(ctx, "Runtime.callFunctionOn", map[string]any{
			"objectId": obj.Object.ObjectID,
			"functionDeclaration": `function() {
				if (typeof this.select === 'function') { this.select(); return; }
				const r = document.createRange(); r.selectNodeContents(this);
				const s = getSelection(); s.removeAllRanges(); s.addRange(r);
			}`,
		}, nil)
		if err != nil {
			return err
		}
	}
	return p.settle(ctx, func() error {
		if o.Slowly {
			for _, r := range text {
				s := string(r)
				if err := p.key(ctx, "keyDown", keyDef{key: s, text: s}, 0); err != nil {
					return err
				}
				if err := p.key(ctx, "keyUp", keyDef{key: s}, 0); err != nil {
					return err
				}
			}
		} else if text != "" {
			if err := p.call(ctx, "Input.insertText", map[string]any{"text": text}, nil); err != nil {
				return err
			}
		}
		if o.Submit {
			return p.pressKey(ctx, "Enter")
		}
		return nil
	})
}

type keyDef struct {
	key, code string
	vk        int
	text      string
}

var keys = map[string]keyDef{
	"Enter":      {"Enter", "Enter", 13, "\r"},
	"Tab":        {"Tab", "Tab", 9, ""},
	"Escape":     {"Escape", "Escape", 27, ""},
	"Backspace":  {"Backspace", "Backspace", 8, ""},
	"Delete":     {"Delete", "Delete", 46, ""},
	"Space":      {" ", "Space", 32, " "},
	"ArrowUp":    {"ArrowUp", "ArrowUp", 38, ""},
	"ArrowDown":  {"ArrowDown", "ArrowDown", 40, ""},
	"ArrowLeft":  {"ArrowLeft", "ArrowLeft", 37, ""},
	"ArrowRight": {"ArrowRight", "ArrowRight", 39, ""},
	"Home":       {"Home", "Home", 36, ""},
	"End":        {"End", "End", 35, ""},
	"PageUp":     {"PageUp", "PageUp", 33, ""},
	"PageDown":   {"PageDown", "PageDown", 34, ""},
}

const (
	modAlt     = 1
	modControl = 2
	modMeta    = 4
	modShift   = 8
)

func (p *Page) key(ctx context.Context, typ string, k keyDef, mods int) error {
	params := map[string]any{"type": typ, "modifiers": mods, "key": k.key}
	if k.code != "" {
		params["code"] = k.code
	}
	if k.vk != 0 {
		params["windowsVirtualKeyCode"] = k.vk
	}
	if typ == "keyDown" && k.text != "" && mods&(modControl|modAlt|modMeta) == 0 {
		params["text"] = k.text
	}
	return p.call(ctx, "Input.dispatchKeyEvent", params, nil)
}

func parseKey(spec string) (keyDef, int, error) {
	parts := strings.Split(spec, "+")
	name := parts[len(parts)-1]
	if name == "" && len(parts) > 1 {
		name = "+"
		parts = parts[:len(parts)-1]
	}
	mods := 0
	for _, m := range parts[:len(parts)-1] {
		switch strings.ToLower(m) {
		case "alt", "option":
			mods |= modAlt
		case "control", "ctrl":
			mods |= modControl
		case "meta", "cmd", "command":
			mods |= modMeta
		case "shift":
			mods |= modShift
		default:
			return keyDef{}, 0, fmt.Errorf("unknown modifier %q in %q", m, spec)
		}
	}
	if k, ok := keys[name]; ok {
		return k, mods, nil
	}
	if r := []rune(name); len(r) == 1 {
		s := string(r)
		k := keyDef{key: s, text: s}
		if (r[0] >= 'a' && r[0] <= 'z') || (r[0] >= 'A' && r[0] <= 'Z') {
			k.code = "Key" + strings.ToUpper(s)
			k.vk = int(strings.ToUpper(s)[0])
		} else if r[0] >= '0' && r[0] <= '9' {
			k.code = "Digit" + s
			k.vk = int(r[0])
		}
		return k, mods, nil
	}
	return keyDef{}, 0, fmt.Errorf("unknown key %q: use a name such as Enter, Tab, Escape or ArrowDown, a single character, or a combination such as Control+a", spec)
}

// Press presses a key or a combination such as "Enter" or "Control+a" and
// waits for any page load it causes.
func (p *Page) Press(ctx context.Context, spec string) error {
	return p.settle(ctx, func() error { return p.pressKey(ctx, spec) })
}

func (p *Page) pressKey(ctx context.Context, spec string) error {
	k, mods, err := parseKey(spec)
	if err != nil {
		return err
	}
	if err := p.key(ctx, "keyDown", k, mods); err != nil {
		return err
	}
	return p.key(ctx, "keyUp", k, mods)
}

// Scroll scrolls the page by a number of pixels; a positive dy goes down.
func (p *Page) Scroll(ctx context.Context, dx, dy float64) error {
	var m metrics
	if err := p.call(ctx, "Page.getLayoutMetrics", nil, &m); err != nil {
		return err
	}
	x, y := m.Visual.ClientWidth/2, m.Visual.ClientHeight/2
	if err := p.mouse(ctx, "mouseWheel", x, y, map[string]any{"deltaX": dx, "deltaY": dy}); err != nil {
		return err
	}
	// A wheel event scrolls smoothly; give it time to land before anything reads the page.
	select {
	case <-time.After(250 * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Select chooses an option of a drop-down by its visible text or its value.
func (p *Page) Select(ctx context.Context, ref, option string) error {
	id, err := p.node(ref)
	if err != nil {
		return err
	}
	var obj struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	if err := p.call(ctx, "DOM.resolveNode", map[string]any{"backendNodeId": id}, &obj); err != nil {
		return err
	}
	var res struct {
		Result struct {
			Value bool `json:"value"`
		} `json:"result"`
	}
	err = p.call(ctx, "Runtime.callFunctionOn", map[string]any{
		"objectId":      obj.Object.ObjectID,
		"returnByValue": true,
		"arguments":     []map[string]any{{"value": option}},
		"functionDeclaration": `function(want) {
			if (!this.options) return false;
			for (const o of this.options) {
				if (o.text.trim() === want || o.value === want) {
					this.value = o.value;
					this.dispatchEvent(new Event('input', {bubbles: true}));
					this.dispatchEvent(new Event('change', {bubbles: true}));
					return true;
				}
			}
			return false;
		}`,
	}, &res)
	if err != nil {
		return err
	}
	if !res.Result.Value {
		return errors.New("that element has no option " + fmt.Sprintf("%q", option))
	}
	return nil
}
