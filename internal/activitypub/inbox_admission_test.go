package activitypub

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// sig builds a ParsedSignature carrying the given signed header set.
func sig(headers ...string) *ParsedSignature {
	return &ParsedSignature{KeyID: "k", Signature: "s", Headers: headers}
}

func TestVerifyInboxAdmission(t *testing.T) {
	body := []byte(`{"type":"Follow"}`)
	goodDigest := SHA256Digest(body) // "sha-256=<base64>"
	const host = "example.com"

	// dateHeader を空にした行は skew 検査を通らない (upstream も Date が無ければ
	// 検査しない)。既存ケースはそのまま digest / host の検査だけを見る。
	tests := []struct {
		name         string
		parsed       *ParsedSignature
		hostHeader   string
		expectedHost string
		dateHeader   string
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
			err := VerifyInboxAdmission(tt.parsed, tt.hostHeader, tt.expectedHost, tt.dateHeader, tt.digestHeader, tt.body)
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

// 署名済みリクエストを永久に再投函できないこと。**window が無いと、捕まえた
// 署名が期限なしで使える。**
func TestVerifyInboxAdmission_DateSkew(t *testing.T) {
	body := []byte(`{"type":"Follow"}`)
	digest := SHA256Digest(body)
	const host = "example.com"
	// 固定時刻に対して相対で見る。実時刻に依存させると境界のテストが不安定になる。
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	restore := nowFuncForAdmission
	nowFuncForAdmission = func() time.Time { return base }
	t.Cleanup(func() { nowFuncForAdmission = restore })

	admit := func(date string) error {
		return VerifyInboxAdmission(sig("(request-target)", "date", "host", "digest"),
			host, host, date, digest, body)
	}
	at := func(d time.Duration) string { return base.Add(d).UTC().Format(http.TimeFormat) }

	t.Run("窓の中は通る", func(t *testing.T) {
		for name, d := range map[string]time.Duration{
			"現在":     0,
			"過去ぎりぎり": -InboxDateSkew,
			"未来ぎりぎり": InboxDateSkew,
			"少し過去":   -InboxDateSkew / 2,
		} {
			t.Run(name, func(t *testing.T) {
				if err := admit(at(d)); err != nil {
					t.Fatalf("通るはずが %v", err)
				}
			})
		}
	})

	t.Run("窓の外は弾く", func(t *testing.T) {
		for name, d := range map[string]time.Duration{
			"わずかに過去":  -InboxDateSkew - time.Second,
			"わずかに未来":  InboxDateSkew + time.Second,
			"1 日前":    -24 * time.Hour,
			"1 年前の再送": -365 * 24 * time.Hour,
		} {
			t.Run(name, func(t *testing.T) {
				if err := admit(at(d)); !errors.Is(err, ErrInboxDateSkew) {
					t.Fatalf("ErrInboxDateSkew のはずが %v", err)
				}
			})
		}
	})

	// **upstream に合わせて緩くしてある部分。** ここを厳しくすると、JS では
	// 読めて Go では読めない書式を送る peer からの配送を落とす。値を差し替えれば
	// 署名が壊れるので、緩くても再投函の穴にはならない。
	t.Run("読めない値と空は通す", func(t *testing.T) {
		for _, date := range []string{"", "   ", "not a date", "0"} {
			if err := admit(date); err != nil {
				t.Errorf("date=%q は通るはずが %v", date, err)
			}
		}
	})

	// RFC1123 以外の HTTP-date も読めること (http.ParseTime が受ける 3 形式)。
	t.Run("RFC850 形式も読む", func(t *testing.T) {
		old := base.Add(-24 * time.Hour).UTC().Format(time.RFC850)
		if err := admit(old); !errors.Is(err, ErrInboxDateSkew) {
			t.Fatalf("ErrInboxDateSkew のはずが %v", err)
		}
	})
}

// X-Date が Date より優先されること (upstream parser と同じ)。
func TestInboxDateHeader(t *testing.T) {
	tests := []struct {
		name  string
		date  string
		xDate string
		want  string
	}{
		{name: "Date のみ", date: "d", want: "d"},
		{name: "X-Date のみ", xDate: "x", want: "x"},
		{name: "両方あれば X-Date", date: "d", xDate: "x", want: "x"},
		{name: "どちらも無い", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.date != "" {
				h.Set("Date", tt.date)
			}
			if tt.xDate != "" {
				h.Set("X-Date", tt.xDate)
			}
			if got := InboxDateHeader(h); got != tt.want {
				t.Fatalf("InboxDateHeader() = %q, want %q", got, tt.want)
			}
		})
	}
}

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
