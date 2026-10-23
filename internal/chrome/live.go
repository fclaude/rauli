package chrome

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Frame is one picture of the page from a screencast.
type Frame struct {
	// JPEG is the encoded picture.
	JPEG []byte
	// Width and Height are the size of the page's viewport in CSS pixels. The
	// picture may be smaller; a person's pointer position is a fraction of the
	// picture, and a fraction of the viewport is the same fraction of this.
	Width, Height float64
}

// Screencast streams pictures of the tab as it changes, for a live view. The
// browser only sends a frame when something on screen changed, so a still page
// is silent; callers wanting a first picture ask for a Screenshot as well.
//
// The channel holds the latest unread frame. A reader that falls behind skips
// frames instead of delaying the browser, which is what a live view wants.
// Call the returned function to stop; it also closes the channel.
func (p *Page) Screencast(ctx context.Context, quality, maxWidth, maxHeight int) (<-chan Frame, func(), error) {
	if quality <= 0 || quality > 100 {
		quality = 60
	}
	events, cancel := p.conn.Subscribe(p.session, "Page.screencastFrame")
	err := p.call(ctx, "Page.startScreencast", map[string]any{
		"format": "jpeg", "quality": quality, "maxWidth": maxWidth, "maxHeight": maxHeight, "everyNthFrame": 1,
	}, nil)
	if err != nil {
		cancel()
		return nil, nil, err
	}

	out := make(chan Frame, 1)
	stop := make(chan struct{})
	var once sync.Once
	finish := func() {
		once.Do(func() {
			close(stop)
			bg, done := context.WithTimeout(context.Background(), 3*time.Second)
			defer done()
			_ = p.call(bg, "Page.stopScreencast", nil, nil)
			cancel()
		})
	}

	go func() {
		defer close(out)
		for {
			select {
			case <-stop:
				return
			case <-p.conn.Done():
				return
			case raw := <-events:
				var e struct {
					Data     string `json:"data"`
					Session  int    `json:"sessionId"`
					Metadata struct {
						DeviceWidth  float64 `json:"deviceWidth"`
						DeviceHeight float64 `json:"deviceHeight"`
					} `json:"metadata"`
				}
				if json.Unmarshal(raw, &e) != nil {
					continue
				}
				// Every frame must be acknowledged or the browser stops sending.
				ack, done := context.WithTimeout(context.Background(), 3*time.Second)
				_ = p.call(ack, "Page.screencastFrameAck", map[string]any{"sessionId": e.Session}, nil)
				done()
				data, err := base64.StdEncoding.DecodeString(e.Data)
				if err != nil {
					continue
				}
				f := Frame{JPEG: data, Width: e.Metadata.DeviceWidth, Height: e.Metadata.DeviceHeight}
				select {
				case out <- f:
				default:
					select {
					case <-out:
					default:
					}
					select {
					case out <- f:
					default:
					}
				}
			}
		}
	}()
	return out, finish, nil
}

// ViewportSize is the size of the tab's viewport in CSS pixels.
func (p *Page) ViewportSize(ctx context.Context) (width, height float64, err error) {
	var m metrics
	if err := p.call(ctx, "Page.getLayoutMetrics", nil, &m); err != nil {
		return 0, 0, err
	}
	return m.Visual.ClientWidth, m.Visual.ClientHeight, nil
}

// Pointer sends one raw mouse event at a point of the viewport. typ is
// "mouseMoved", "mousePressed" or "mouseReleased"; button is "left", "right",
// "middle" or "none"; mods is a bit set of Alt=1, Control=2, Meta=4, Shift=8.
// It is the input of a person driving a live view: the caller has already
// decided what the event means, so nothing waits for a page load afterwards.
func (p *Page) Pointer(ctx context.Context, typ string, x, y float64, button string, clicks, mods int) error {
	switch typ {
	case "mouseMoved", "mousePressed", "mouseReleased":
	default:
		return fmt.Errorf("unknown mouse event %q", typ)
	}
	extra := map[string]any{"modifiers": mods}
	if button != "" && button != "none" {
		extra["button"] = button
	}
	if clicks > 0 {
		extra["clickCount"] = clicks
	}
	return p.mouse(ctx, typ, x, y, extra)
}

// Wheel scrolls with a wheel event at a point of the viewport.
func (p *Page) Wheel(ctx context.Context, x, y, dx, dy float64) error {
	return p.mouse(ctx, "mouseWheel", x, y, map[string]any{"deltaX": dx, "deltaY": dy})
}

// RawKey sends one key event as the browser's keyboard would. typ is "keyDown",
// "keyUp" or "rawKeyDown"; text is the character a key press produces, if any.
func (p *Page) RawKey(ctx context.Context, typ, key, code string, vk int, text string, mods int) error {
	switch typ {
	case "keyDown", "keyUp", "rawKeyDown":
	default:
		return fmt.Errorf("unknown key event %q", typ)
	}
	params := map[string]any{"type": typ, "modifiers": mods, "key": key}
	if code != "" {
		params["code"] = code
	}
	if vk != 0 {
		params["windowsVirtualKeyCode"] = vk
	}
	if text != "" && typ == "keyDown" && mods&(modControl|modAlt|modMeta) == 0 {
		params["text"] = text
	}
	return p.call(ctx, "Input.dispatchKeyEvent", params, nil)
}

// InsertText puts text into the focused element in one step.
func (p *Page) InsertText(ctx context.Context, text string) error {
	return p.call(ctx, "Input.insertText", map[string]any{"text": text}, nil)
}
