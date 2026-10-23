// Package watch is the live view: a web page that shows the tab an agent is
// driving, lists what the agent did, and lets a person take the browser over.
//
// The view is a window into a browser that may be logged in to real accounts,
// so it is built to be hard to reach by accident or by a hostile web page. It
// listens on a loopback address only. Every request needs a random token, given
// once in the address that is printed and kept afterwards in an HttpOnly,
// SameSite=Strict cookie. The Host header must name the machine itself, which
// stops a page on another site from reaching the view through DNS rebinding.
// The WebSocket must come from the view's own page. The page loads nothing from
// anywhere else, and the policy that says so is sent with every response.
package watch

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/fclaude/rauli/internal/tools"
)

//go:embed assets
var assetFiles embed.FS

// Pending is a request waiting for a person to approve or refuse it.
type Pending struct {
	ID      string    `json:"id"`
	Tool    string    `json:"tool"`
	Element string    `json:"element,omitempty"`
	Detail  string    `json:"detail,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Since   time.Time `json:"since"`
}

// Approvals is the queue of requests that need a person. The view shows them
// and resolves them; whatever fills the queue is somewhere else.
type Approvals interface {
	// Pending lists what is waiting, oldest first.
	Pending() []Pending
	// Resolve approves or refuses one request. An unknown id is an error.
	Resolve(id string, approve bool) error
	// Subscribe is notified whenever the list changes. The view reads Pending
	// again each time, so a notification carries nothing.
	Subscribe() (<-chan struct{}, func())
}

// Options say how the view is served.
type Options struct {
	// Addr is the address to listen on. The default is 127.0.0.1 with a random
	// port. Only loopback addresses are accepted.
	Addr string
	// Token is the secret that opens the view. Empty means a random one.
	Token string
	// Approvals is shown in the view when it is set.
	Approvals Approvals
	// AllowedHosts are Host header values accepted besides the machine's own
	// loopback names, for a view reached through a tunnel the person set up.
	AllowedHosts []string
	// Quality is the JPEG quality of the picture, 1 to 100. Zero means 60.
	Quality int
}

type server struct {
	s     *tools.Session
	o     Options
	token string
	hub   *hub
	files fs.FS

	mu      sync.Mutex
	cookies map[string]bool
}

const cookieName = "rauli_watch"

func newServer(s *tools.Session, o Options) *server {
	if o.Token == "" {
		o.Token = randomToken()
	}
	sub, _ := fs.Sub(assetFiles, "assets")
	sv := &server{s: s, o: o, token: o.Token, files: sub, cookies: map[string]bool{}}
	sv.hub = newHub(s, o)
	return sv
}

// Handler returns the view as an http.Handler. It starts work in the
// background that ends when the session's browser exits; Serve also stops it
// when its context ends.
func Handler(s *tools.Session, o Options) http.Handler { return newServer(s, o) }

// Serve listens and serves the view until ctx ends. It returns the address to
// open, which carries the token.
func Serve(ctx context.Context, s *tools.Session, o Options) (string, error) {
	addr := o.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("listen address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); (ip == nil || !ip.IsLoopback()) && !strings.EqualFold(host, "localhost") {
		return "", fmt.Errorf("the live view listens on loopback addresses only, not %q", host)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	sv := newServer(s, o)
	hs := &http.Server{Handler: sv, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = hs.Shutdown(shut)
		sv.Close()
	}()
	return fmt.Sprintf("http://%s/?token=%s", ln.Addr().String(), sv.token), nil
}

// Close stops the background work.
func (sv *server) Close() { sv.hub.close() }

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// A view that cannot get randomness must not start with a guessable token.
		panic("watch: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// hostOK accepts the machine's own names, which are the only ones a person
// types, and any host the options add. A page on another site reaches this
// server through its own name, which is none of those.
func (sv *server) hostOK(host string) bool {
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	name = strings.Trim(name, "[]")
	if ip := net.ParseIP(name); ip != nil {
		return ip.IsLoopback()
	}
	if strings.EqualFold(name, "localhost") {
		return true
	}
	for _, a := range sv.o.AllowedHosts {
		if strings.EqualFold(a, host) {
			return true
		}
	}
	return false
}

// originOK requires the request to come from the view's own page. Browsers
// always send an Origin with a WebSocket handshake, so a missing one means a
// script outside a browser, which has no business here without trying harder.
func originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (sv *server) headers(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	// The page needs its own scripts and styles, pictures made in memory, and
	// the one WebSocket back to this server. Nothing else, from anywhere.
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; "+
		"connect-src 'self' ws://"+r.Host+"; font-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cache-Control", "no-store")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}

func (sv *server) authorised(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	sv.mu.Lock()
	defer sv.mu.Unlock()
	return sv.cookies[c.Value]
}

func (sv *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sv.headers(w, r)

	if !sv.hostOK(r.Host) {
		http.Error(w, "This address is not one the live view answers to.", http.StatusForbidden)
		return
	}
	// The view changes nothing over plain HTTP, so only reads are accepted.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Only GET is accepted.", http.StatusMethodNotAllowed)
		return
	}
	// A request that names another origin is a page on another site reaching in.
	if o := r.Header.Get("Origin"); o != "" && r.URL.Path != "/ws" && !originOK(r) {
		http.Error(w, "Cross-origin requests are refused.", http.StatusForbidden)
		return
	}

	if r.URL.Path == "/" && r.URL.Query().Has("token") {
		if !equal(r.URL.Query().Get("token"), sv.token) {
			http.Error(w, "That token is wrong.", http.StatusForbidden)
			return
		}
		value := randomToken()
		sv.mu.Lock()
		if len(sv.cookies) > 64 {
			sv.cookies = map[string]bool{}
		}
		sv.cookies[value] = true
		sv.mu.Unlock()
		http.SetCookie(w, &http.Cookie{
			Name: cookieName, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		})
		// Redirect to a clean address, so the token does not stay in the history.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if !sv.authorised(r) {
		http.Error(w, "Open the address that Raulí printed, which carries the token.", http.StatusUnauthorized)
		return
	}

	switch r.URL.Path {
	case "/ws":
		sv.socket(w, r)
	case "/":
		sv.asset(w, r, "index.html", "text/html; charset=utf-8")
	case "/app.js":
		sv.asset(w, r, "app.js", "text/javascript; charset=utf-8")
	case "/app.css":
		sv.asset(w, r, "app.css", "text/css; charset=utf-8")
	default:
		http.NotFound(w, r)
	}
}

func (sv *server) asset(w http.ResponseWriter, r *http.Request, name, mime string) {
	data, err := fs.ReadFile(sv.files, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	_, _ = w.Write(data)
}

func (sv *server) socket(w http.ResponseWriter, r *http.Request) {
	if !originOK(r) {
		http.Error(w, "The live view's WebSocket opens only from its own page.", http.StatusForbidden)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(64 << 10)
	sv.hub.serve(r.Context(), conn)
}

var errNoPage = errors.New("there is no tab open yet")
