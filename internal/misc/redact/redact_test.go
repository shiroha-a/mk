package redact

import (
	"net/url"
	"strings"
	"testing"
)

func TestURI(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{"no query is returned unchanged", "/api/notes/create", "/api/notes/create"},
		{
			"query without secrets is returned unchanged",
			"/api/notes/show?limit=10&withFiles=true",
			"/api/notes/show?limit=10&withFiles=true",
		},
		{"api token in query is redacted", "/api/i?i=abcdef0123456789", "/api/i?i=REDACTED"},
		{
			"streaming websocket token is redacted",
			"/streaming?i=abcdef0123456789",
			"/streaming?i=REDACTED",
		},
		{
			"oauth code is redacted",
			"/oauth/token?code=secret-code&client_id=app",
			"/oauth/token?client_id=app&code=REDACTED",
		},
		{
			"non-secret params are preserved alongside redaction",
			"/proxy/image.webp?url=https%3A%2F%2Fexample.test%2Fa.png&i=tok",
			"/proxy/image.webp?i=REDACTED&url=https%3A%2F%2Fexample.test%2Fa.png",
		},
		{"secret param name is matched case-insensitively", "/api/i?I=abcdef", "/api/i?I=REDACTED"},
		{
			"repeated secret param redacts every occurrence",
			"/api/i?i=one&i=two",
			"/api/i?i=REDACTED&i=REDACTED",
		},
		{"empty query is returned unchanged", "/api/i?", "/api/i?"},
		{"unparseable query falls back to the path only", "/api/i?i=%zz", "/api/i"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := URI(tt.uri); got != tt.want {
				t.Errorf("URI(%q) = %q, want %q", tt.uri, got, tt.want)
			}
		})
	}
}

func TestQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
		ok    bool
	}{
		{"empty query", "", "", true},
		{"no secrets", "limit=10", "limit=10", true},
		{"secret is redacted", "i=tok", "i=REDACTED", true},
		{"mixed", "i=tok&limit=10", "i=REDACTED&limit=10", true},
		{"unparseable", "i=%zz", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Query(tt.query)
			if ok != tt.ok {
				t.Fatalf("Query(%q) ok = %v, want %v", tt.query, ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("Query(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

// TestNeverLeaksSecretValues guards the property that matters: whatever the
// input shape, a sensitive parameter's value must not survive.
func TestNeverLeaksSecretValues(t *testing.T) {
	const secret = "super-secret-token-value"
	for param := range SensitiveQueryParams {
		t.Run(param, func(t *testing.T) {
			query := url.Values{param: {secret}}.Encode()
			if got, _ := Query(query); strings.Contains(got, secret) {
				t.Errorf("Query(%q) leaked the secret: %q", query, got)
			}
			uri := "/api/test?" + query
			if got := URI(uri); strings.Contains(got, secret) {
				t.Errorf("URI(%q) leaked the secret: %q", uri, got)
			}
		})
	}
}

// TestUnparseableQueryNeverReturnsInput pins the fail-safe direction: a query
// that cannot be parsed must not be echoed back, because that is exactly the
// path where an unrecognised encoding could carry a token through.
func TestUnparseableQueryNeverReturnsInput(t *testing.T) {
	const bad = "i=%zz&token=%zz"
	got, ok := Query(bad)
	if ok {
		t.Fatalf("Query(%q) unexpectedly reported success", bad)
	}
	if got != "" {
		t.Errorf("Query(%q) = %q, want empty on failure", bad, got)
	}
}
