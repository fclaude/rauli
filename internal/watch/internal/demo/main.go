// Command demo starts a browser session on a small fixture site and serves the
// live view for it, so the view can be tried by hand and audited. It is not
// part of Raulí itself.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/fclaude/rauli/internal/tools"
	"github.com/fclaude/rauli/internal/watch"
)

const home = `<!doctype html><html lang="en"><title>Garden notes</title>
<body style="font:20px system-ui;margin:0;background:#f4f0e6;color:#23301f">
<h1 style="margin:24px">Garden notes</h1>
<p style="margin:0 24px 16px">Pick a plant to read about it.</p>
<p style="margin:0 24px"><a href="/raul">Raulí</a> · <a href="/lenga">Lenga</a></p>
<p style="margin:24px"><label>Your name <input id="name" style="font:inherit"></label></p>
<p style="margin:24px"><button id="b" style="font:inherit;padding:16px 32px;background:#2f6b3a;color:white;border:0"
 onclick="this.textContent='Clicked '+(window.n=(window.n||0)+1)+' times'">Click me</button></p>
</body></html>`

// queue is a small approval queue, enough to show the panel.
type queue struct {
	mu   sync.Mutex
	list []watch.Pending
	subs []chan struct{}
}

func (q *queue) Pending() []watch.Pending {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]watch.Pending(nil), q.list...)
}

func (q *queue) Resolve(id string, approve bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, p := range q.list {
		if p.ID == id {
			q.list = append(q.list[:i], q.list[i+1:]...)
			log.Printf("request %s %s", id, map[bool]string{true: "approved", false: "refused"}[approve])
			for _, s := range q.subs {
				select {
				case s <- struct{}{}:
				default:
				}
			}
			return nil
		}
	}
	return fmt.Errorf("no request %q is waiting", id)
}

func (q *queue) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	q.mu.Lock()
	q.subs = append(q.subs, ch)
	q.mu.Unlock()
	return ch, func() {}
}

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "address to serve the view on")
	headed := flag.Bool("headed", false, "show a real browser window as well")
	pending := flag.Bool("approvals", true, "include a pending request to show the approvals panel")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, home)
	})
	for _, p := range []string{"raul", "lenga"} {
		p := p
		mux.HandleFunc("/"+p, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!doctype html><title>%s</title><body style="font:20px system-ui;margin:24px"><h1>%s</h1><p>A southern beech.</p><a href="/">Back</a>`, p, p)
		})
	}
	site := httptest.NewServer(mux)
	defer site.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	opts := tools.Options{}
	opts.Browser.Headed = *headed
	s, err := tools.New(ctx, opts)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	call := func(name string, args any) {
		raw, _ := json.Marshal(args)
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := s.Call(cctx, name, raw); err != nil {
			log.Printf("%s: %v", name, err)
		}
	}
	q := &queue{}
	if *pending {
		q.list = []watch.Pending{{ID: "1", Tool: "click", Element: `button "Pay now"`, Detail: "", Reason: "payments need approval", Since: time.Now()}}
	}
	url, err := watch.Serve(ctx, s, watch.Options{Addr: *addr, Approvals: q})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(url)
	// Browse after the view is listening, so the action log has something in it.
	call("navigate", map[string]any{"url": site.URL})
	call("screenshot", map[string]any{})
	<-ctx.Done()
}
