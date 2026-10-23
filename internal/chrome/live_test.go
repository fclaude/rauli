package chrome

import (
	"bytes"
	"context"
	"image/jpeg"
	"strings"
	"testing"
	"time"
)

func TestScreencastDeliversFramesAndStopsCleanly(t *testing.T) {
	p, base, ctx := page(t)
	frames, stop, err := p.Screencast(ctx, 50, 640, 400)
	if err != nil {
		t.Fatal(err)
	}
	// The page changes after the screencast starts, which is what makes the browser send a frame.
	if err := p.Navigate(ctx, base+"/"); err != nil {
		t.Fatal(err)
	}
	select {
	case f, ok := <-frames:
		if !ok {
			t.Fatal("the frame channel closed before any frame")
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(f.JPEG))
		if err != nil {
			t.Fatalf("a frame is not a JPEG: %v", err)
		}
		if cfg.Width > 640 || cfg.Height > 400 || cfg.Width == 0 {
			t.Errorf("frame is %dx%d, outside the 640x400 limit", cfg.Width, cfg.Height)
		}
		if f.Width != 1280 || f.Height != 800 {
			t.Errorf("the frame reports a %vx%v viewport, want 1280x800", f.Width, f.Height)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no frame arrived")
	}
	stop()
	stop() // stopping twice is harmless
	select {
	case _, ok := <-frames:
		for ok {
			_, ok = <-frames
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the channel stayed open after stop")
	}
}

func TestRawInputReachesThePage(t *testing.T) {
	p, base, ctx := page(t)
	if err := p.Navigate(ctx, base+"/"); err != nil {
		t.Fatal(err)
	}
	// A pointer press and release on the Alert button is a click at those coordinates.
	p.Eval(ctx, `document.body.style.margin='0'; const b=document.getElementById('log'); b.style.cssText='position:fixed;left:0;top:0;width:200px;height:100px'`)
	for _, typ := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		if err := p.Pointer(ctx, typ, 100, 50, "left", 1, 0); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c := p.Console(); len(c) > 0 && strings.Contains(c[0], "clicked") {
			goto typed
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the pointer events did not click the button")

typed:
	p.Eval(ctx, "document.getElementById('q').focus()")
	if err := p.RawKey(ctx, "keyDown", "a", "KeyA", 65, "a", 0); err != nil {
		t.Fatal(err)
	}
	p.RawKey(ctx, "keyUp", "a", "KeyA", 65, "", 0)
	if err := p.InsertText(ctx, "bc"); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.Eval(ctx, "document.getElementById('q').value"); string(got) != `"abc"` {
		t.Fatalf("the field holds %s", got)
	}
	if err := p.Wheel(ctx, 100, 50, 0, 800); err != nil {
		t.Fatal(err)
	}
	if err := p.Pointer(ctx, "mouseTeleported", 1, 1, "", 0, 0); err == nil {
		t.Fatal("an unknown mouse event should be refused")
	}
	if err := p.RawKey(ctx, "typo", "a", "", 0, "", 0); err == nil {
		t.Fatal("an unknown key event should be refused")
	}
	w, h, err := p.ViewportSize(ctx)
	if err != nil || w != 1280 || h != 800 {
		t.Fatalf("viewport %v x %v, %v", w, h, err)
	}
}

func TestABrowserSaysWhenAndHowItEnded(t *testing.T) {
	if _, err := Find(); err != nil {
		t.Skip(err)
	}
	b, err := Launch(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.Done():
		t.Fatal("Done is closed while the browser runs")
	default:
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done stayed open after Close")
	}
	// A browser asked to close exits cleanly; one that had to be killed says so.
	if err := b.ExitError(); err != nil && !strings.Contains(err.Error(), "killed") {
		t.Fatalf("unexpected exit: %v", err)
	}
}
