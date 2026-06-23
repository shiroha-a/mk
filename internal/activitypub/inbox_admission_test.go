package activitypub

import (
	"errors"
	"testing"
)

// sig builds a ParsedSignature carrying the given signed header set.
func sig(headers ...string) *ParsedSignature {
	return &ParsedSignature{KeyID: "k", Signature: "s", Headers: headers}
}

func TestVerifyInboxAdmission(t *testing.T) {
	body := []byte(`{"type":"Follow"}`)
	goodDigest := SHA256Digest(body) // "sha-256=<base64>"
	const host = "example.com"

	tests := []struct {
		name         string
		parsed       *ParsedSignature
		hostHeader   string
		expectedHost string
		digestHeader string
		body         []byte
		wantErr      error
	}{
		{
			name:         "valid with host and digest signed",
			parsed:       sig("(request-target)", "date", "host", "digest"),
			hostHeader:   host,
			expectedHost: host,
			digestHeader: goodDigest,
			body:         body,
			wantErr:      nil,
		},
		{
			name:         "tampered body fails digest value comparison",
			parsed:       sig("(request-target)", "date", "host", "digest"),
			hostHeader:   host,
			expectedHost: host,
			digestHeader: goodDigest,
			body:         []byte(`{"type":"Follow","tampered":true}`),
			wantErr:      ErrInboxDigestMismatch,
		},
		{
			name:         "digest not in signed headers",
			parsed:       sig("(request-target)", "date", "host"),
			hostHeader:   host,
			expectedHost: host,
			digestHeader: goodDigest,
			body:         body,
			wantErr:      ErrInboxDigestUnsigned,
		},
		{
			name:         "digest header missing",
			parsed:       sig("(request-target)", "date", "host", "digest"),
			hostHeader:   host,
			expectedHost: host,
			digestHeader: "",
			body:         body,
			wantErr:      ErrInboxDigestMissing,
		},
		{
			name:         "digest header malformed",
			parsed:       sig("(request-target)", "date", "host", "digest"),
			hostHeader:   host,
			expectedHost: host,
			digestHeader: "not-a-digest",
			body:         body,
			wantErr:      ErrInboxDigestMalformed,
		},
		{
			name:         "unsupported digest algorithm",
			parsed:       sig("(request-target)", "date", "host", "digest"),
			hostHeader:   host,
			expectedHost: host,
			digestHeader: "SHA-512=abc",
			body:         body,
			wantErr:      ErrInboxDigestAlgo,
		},
		{
			// #2087: (request-target) 未署名は upstream parseRequest 同様 reject。
			name:         "request-target not signed",
			parsed:       sig("date", "host", "digest"),
			hostHeader:   host,
			expectedHost: host,
			digestHeader: goodDigest,
			body:         body,
			wantErr:      ErrInboxRequestTargetUnsigned,
		},
		{
			// #2087: date 未署名も reject (expectedHost 未設定でも必須)。
			name:         "date not signed (even when expectedHost empty)",
			parsed:       sig("(request-target)", "host", "digest"),
			hostHeader:   host,
			expectedHost: "",
			digestHeader: goodDigest,
			body:         body,
			wantErr:      ErrInboxDateUnsigned,
		},
		{
			name:         "host not signed when expectedHost set",
			parsed:       sig("(request-target)", "date", "digest"),
			hostHeader:   host,
			expectedHost: host,
			digestHeader: goodDigest,
			body:         body,
			wantErr:      ErrInboxHostUnsigned,
		},
		{
			name:         "host mismatch when expectedHost set",
			parsed:       sig("(request-target)", "date", "host", "digest"),
			hostHeader:   "evil.example",
			expectedHost: host,
			digestHeader: goodDigest,
			body:         body,
			wantErr:      ErrInboxHostMismatch,
		},
		{
			name:         "host check skipped when expectedHost empty",
			parsed:       sig("(request-target)", "date", "digest"),
			hostHeader:   "anything",
			expectedHost: "",
			digestHeader: goodDigest,
			body:         body,
			wantErr:      nil,
		},
		{
			name:         "host comparison is case-insensitive",
			parsed:       sig("(request-target)", "date", "host", "digest"),
			hostHeader:   "Example.COM",
			expectedHost: host,
			digestHeader: goodDigest,
			body:         body,
			wantErr:      nil,
		},
		{
			name:         "digest algo is case-insensitive",
			parsed:       sig("(request-target)", "date", "host", "digest"),
			hostHeader:   host,
			expectedHost: host,
			digestHeader: "sha-256=" + goodDigest[len("sha-256="):],
			body:         body,
			wantErr:      nil,
		},
		{
			name:         "nil parsed signature",
			parsed:       nil,
			hostHeader:   host,
			expectedHost: host,
			digestHeader: goodDigest,
			body:         body,
			wantErr:      errNilParsed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyInboxAdmission(tt.parsed, tt.hostHeader, tt.expectedHost, tt.digestHeader, tt.body)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if tt.wantErr == errNilParsed {
				// nil parsed returns a generic error, not a sentinel.
				if err == nil {
					t.Fatalf("expected error for nil parsed, got nil")
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

// errNilParsed is a test marker; VerifyInboxAdmission returns a generic (non-sentinel)
// error for a nil parsed signature, so the table uses this to branch.
var errNilParsed = errors.New("nil parsed marker")

func TestSplitDigestHeader(t *testing.T) {
	tests := []struct {
		in    string
		algo  string
		value string
		ok    bool
	}{
		{"SHA-256=abc123", "SHA-256", "abc123", true},
		{"SHA-256=ab+c/12==", "SHA-256", "ab+c/12==", true}, // base64 padding survives
		{"  SHA-256=x  ", "SHA-256", "x", true},             // trimmed
		{"=value", "", "", false},                           // empty algo
		{"algo=", "", "", false},                            // empty value
		{"noequals", "", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		algo, value, ok := splitDigestHeader(tt.in)
		if ok != tt.ok || algo != tt.algo || value != tt.value {
			t.Errorf("splitDigestHeader(%q) = (%q,%q,%v), want (%q,%q,%v)", tt.in, algo, value, ok, tt.algo, tt.value, tt.ok)
		}
	}
}
