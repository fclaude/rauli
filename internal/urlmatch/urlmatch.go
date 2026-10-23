// Package urlmatch matches addresses against the small pattern language that
// the rules and the keychain share.
//
// A pattern looks like an address: scheme://host[:port][/path]. A * matches any
// run of characters, including none.
//
//   - The scheme may be http, https, ws, wss or *. A pattern for https also
//     covers wss, and one for http also covers ws, because a page and its
//     websocket belong to the same site. A * scheme covers all four. A pattern
//     with no scheme is taken as having *.
//   - The host is matched without regard to case. *.example.com matches any
//     subdomain of example.com at any depth and does not match example.com
//     itself; list both to allow both. The host localhost stands for the
//     machine itself and matches localhost, 127.0.0.1 and [::1].
//   - The port, if given, must match exactly, and * matches any port. Without
//     one, only the scheme's own port (80 or 443) matches, except for localhost,
//     where development servers use any port and the pattern matches all.
//   - The path is matched from the root, and * in it matches across slashes.
//     Without a path a pattern matches every path. The query and the fragment
//     are not part of the match.
package urlmatch

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Pattern is a parsed pattern.
type Pattern struct {
	Source string

	scheme  string // http, https, ws, wss or *
	host    string // lower case
	port    string // "" for the scheme's default, "*" for any
	path    string // "" for any
	origin  bool   // set when the pattern may not carry a path
	loopbck bool
}

// Parse reads a pattern. With originOnly set, a path is an error, which is what
// a place a credential may be used asks for.
func Parse(s string, originOnly bool) (*Pattern, error) {
	src := s
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("the pattern is empty")
	}
	p := &Pattern{Source: src, origin: originOnly}

	rest := s
	if i := strings.Index(s, "://"); i >= 0 {
		p.scheme = strings.ToLower(s[:i])
		rest = s[i+3:]
		switch p.scheme {
		case "http", "https", "ws", "wss", "*":
		default:
			return nil, fmt.Errorf("the scheme %q is not one of http, https, ws, wss or *", p.scheme)
		}
	} else {
		p.scheme = "*"
	}

	hostport, path := rest, ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		hostport, path = rest[:i], rest[i:]
	}
	if hostport == "" {
		return nil, fmt.Errorf("the pattern has no host")
	}
	if strings.ContainsAny(hostport, " \t@") {
		return nil, fmt.Errorf("the host %q has characters that cannot be part of one", hostport)
	}
	host, port := hostport, ""
	if strings.HasPrefix(hostport, "[") {
		end := strings.Index(hostport, "]")
		if end < 0 {
			return nil, fmt.Errorf("the host %q has an unclosed bracket", hostport)
		}
		host = hostport[:end+1]
		if tail := hostport[end+1:]; tail != "" {
			if !strings.HasPrefix(tail, ":") {
				return nil, fmt.Errorf("the host %q has text after the bracket", hostport)
			}
			port = tail[1:]
		}
	} else if i := strings.LastIndex(hostport, ":"); i >= 0 {
		host, port = hostport[:i], hostport[i+1:]
	}
	if host == "" {
		return nil, fmt.Errorf("the pattern has no host")
	}
	if port != "" && port != "*" {
		n := 0
		for _, r := range port {
			if r < '0' || r > '9' {
				return nil, fmt.Errorf("the port %q is not a number or *", port)
			}
			n = n*10 + int(r-'0')
			if n > 65535 {
				return nil, fmt.Errorf("the port %q is too large", port)
			}
		}
	}
	p.host = strings.ToLower(host)
	p.port = port
	p.loopbck = p.host == "localhost"

	if i := strings.IndexAny(path, "?#"); i >= 0 {
		return nil, fmt.Errorf("a pattern cannot match the query or the fragment: remove %q", path[i:])
	}
	if path != "" && path != "/*" {
		if originOnly {
			return nil, fmt.Errorf("a place a credential may be used is an origin and has no path: remove %q", path)
		}
		p.path = path
	}
	return p, nil
}

// Match reports whether the address matches. It is false for an address that
// does not parse, and for one with no host.
func (p *Pattern) Match(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return p.MatchURL(u)
}

// MatchURL is Match for an address that is already parsed.
func (p *Pattern) MatchURL(u *url.URL) bool {
	scheme := strings.ToLower(u.Scheme)
	if !p.schemeMatches(scheme) {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(u.Host, ":") && strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if !p.hostMatches(host) {
		return false
	}
	if !p.portMatches(scheme, u.Port(), host) {
		return false
	}
	if p.path != "" {
		path := u.EscapedPath()
		if path == "" {
			path = "/"
		}
		if !Glob(p.path, path, false) {
			return false
		}
	}
	return true
}

func (p *Pattern) schemeMatches(s string) bool {
	switch p.scheme {
	case "*":
		return s == "http" || s == "https" || s == "ws" || s == "wss"
	case "http":
		return s == "http" || s == "ws"
	case "https":
		return s == "https" || s == "wss"
	default:
		return s == p.scheme
	}
}

func (p *Pattern) hostMatches(host string) bool {
	if p.loopbck {
		return host == "localhost" || host == "127.0.0.1" || host == "[::1]" || strings.HasSuffix(host, ".localhost")
	}
	return Glob(p.host, host, true)
}

func (p *Pattern) portMatches(scheme, port, host string) bool {
	switch p.port {
	case "*":
		return true
	case "":
		if p.loopbck {
			return true
		}
		def := "80"
		if scheme == "https" || scheme == "wss" {
			def = "443"
		}
		return port == "" || port == def
	default:
		if port == "" {
			port = "80"
			if scheme == "https" || scheme == "wss" {
				port = "443"
			}
		}
		return port == p.port
	}
}

// Glob reports whether s matches pattern, where * matches any run of
// characters. With fold set the comparison ignores case.
func Glob(pattern, s string, fold bool) bool {
	if fold {
		pattern, s = strings.ToLower(pattern), strings.ToLower(s)
	}
	// Iterative matching with a single backtrack point: linear in practice,
	// and immune to the blow-up a recursive matcher has on patterns like a*a*a*b.
	px, sx := 0, 0
	star, mark := -1, 0
	for sx < len(s) {
		switch {
		case px < len(pattern) && pattern[px] == '*':
			star, mark = px, sx
			px++
		case px < len(pattern) && pattern[px] == s[sx]:
			px++
			sx++
		case star >= 0:
			mark++
			sx = mark
			px = star + 1
		default:
			return false
		}
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}

// IsLiteralIP reports whether a host is an IP address, with or without brackets.
func IsLiteralIP(host string) (net.IP, bool) {
	h := strings.Trim(host, "[]")
	ip := net.ParseIP(h)
	return ip, ip != nil
}
