package urlmatch

import "testing"

func TestPatterns(t *testing.T) {
	tests := []struct {
		pattern, url string
		origin       bool
		want         bool
	}{
		// Hosts.
		{"https://example.com", "https://example.com/a/b", false, true},
		{"https://example.com", "https://EXAMPLE.com/a", false, true},
		{"https://example.com", "https://www.example.com/", false, false},
		{"https://*.example.com", "https://www.example.com/", false, true},
		{"https://*.example.com", "https://a.b.example.com/", false, true},
		{"https://*.example.com", "https://example.com/", false, false},
		{"https://*.example.com", "https://evil-example.com/", false, false},
		{"https://*.example.com", "https://example.com.evil.net/", false, false},
		{"https://*", "https://anything.test/", false, true},
		// Schemes.
		{"https://example.com", "http://example.com/", false, false},
		{"https://example.com", "wss://example.com/socket", false, true},
		{"http://example.com", "ws://example.com/socket", false, true},
		{"http://example.com", "wss://example.com/socket", false, false},
		{"example.com", "http://example.com/", false, true},
		{"example.com", "https://example.com/", false, true},
		{"example.com", "file://example.com/x", false, false},
		// Ports.
		{"https://example.com", "https://example.com:443/", false, true},
		{"https://example.com", "https://example.com:8443/", false, false},
		{"http://example.com", "http://example.com:80/", false, true},
		{"https://example.com:8443", "https://example.com:8443/x", false, true},
		{"https://example.com:8443", "https://example.com/x", false, false},
		{"https://example.com:*", "https://example.com:9999/x", false, true},
		// Localhost stands for the machine, on any port unless one is given.
		{"http://localhost", "http://localhost:3000/", false, true},
		{"http://localhost", "http://127.0.0.1:8080/x", false, true},
		{"http://localhost", "http://[::1]:8080/x", false, true},
		{"http://localhost", "http://app.localhost:8080/x", false, true},
		{"http://localhost:3000", "http://localhost:3001/", false, false},
		{"http://localhost", "http://localhost.evil.net/", false, false},
		// Paths.
		{"https://example.com/docs/*", "https://example.com/docs/a/b", false, true},
		{"https://example.com/docs/*", "https://example.com/docs", false, false},
		{"https://example.com/docs/*", "https://example.com/other", false, false},
		{"https://example.com/*", "https://example.com/", false, true},
		{"https://example.com/a", "https://example.com/a?x=1#y", false, true},
		{"https://example.com/a", "https://example.com/a/b", false, false},
		// Origins keep no path.
		{"https://example.com", "https://example.com/anything", true, true},
		// Not addresses.
		{"https://example.com", "about:blank", false, false},
		{"https://example.com", "data:text/plain,hi", false, false},
	}
	for _, tt := range tests {
		p, err := Parse(tt.pattern, tt.origin)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.pattern, err)
			continue
		}
		if got := p.Match(tt.url); got != tt.want {
			t.Errorf("%q against %q: got %v, want %v", tt.pattern, tt.url, got, tt.want)
		}
	}
}

func TestBadPatterns(t *testing.T) {
	for _, tt := range []struct {
		pattern string
		origin  bool
		want    string
	}{
		{"", false, "empty"},
		{"ftp://example.com", false, "scheme"},
		{"https://", false, "no host"},
		{"https://:8080", false, "no host"},
		{"https://example.com:abc", false, "not a number"},
		{"https://example.com:99999", false, "too large"},
		{"https://user@example.com", false, "characters"},
		{"https://example.com/a?b=1", false, "query"},
		{"https://example.com/a#frag", false, "query"},
		{"https://example.com/docs/*", true, "origin"},
		{"https://[::1", false, "bracket"},
	} {
		_, err := Parse(tt.pattern, tt.origin)
		if err == nil || !contains(err.Error(), tt.want) {
			t.Errorf("Parse(%q) = %v, want an error mentioning %q", tt.pattern, err, tt.want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestGlob(t *testing.T) {
	for _, tt := range []struct {
		pattern, s string
		fold, want bool
	}{
		{"*", "", false, true},
		{"*pay*", `button "Pay now"`, true, true},
		{"*pay*", `button "Pay now"`, false, false},
		{`button "Save"`, `button "Save"`, false, true},
		{`button "Save"`, `button "Save as"`, false, false},
		{"a*b*c", "aXbYc", false, true},
		{"a*b*c", "aXbY", false, false},
		{"**x", "x", false, true},
	} {
		if got := Glob(tt.pattern, tt.s, tt.fold); got != tt.want {
			t.Errorf("Glob(%q, %q, %v) = %v, want %v", tt.pattern, tt.s, tt.fold, got, tt.want)
		}
	}
	// A pattern built to make a recursive matcher take exponential time.
	long := ""
	for i := 0; i < 40; i++ {
		long += "a"
	}
	if Glob("a*a*a*a*a*a*a*a*a*b", long, false) {
		t.Error("the pathological pattern should not match")
	}
}
