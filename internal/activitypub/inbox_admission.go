package activitypub

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
)

// InboxDateSkew bounds how far the signed Date may be from our clock.
//
// **これが無いと、署名済みリクエストを永久に再投函できる。** host が署名対象
// かつ自ホスト一致なので他所へは転用できず、各ハンドラも冪等なので単発の再送は
// ほぼ無害だが、Undo(Follow) / Undo(Block) のような**古い活動を後から差し込む**
// 形で連合の状態を巻き戻せる。
//
// 300 秒は upstream (@peertube/http-signature の parseRequest 既定 clockSkew)
// と同値。ここを詰めると時刻がずれている peer からの配送を落とすので揃える。
const InboxDateSkew = 5 * time.Minute

// nowFuncForAdmission is time.Now, replaced in tests.
var nowFuncForAdmission = time.Now

// Inbox admission errors mirror the 401 conditions Misskey's
// ActivityPubServerService.inbox enforces before queueing an inbound activity.
var (
	// ErrInboxRequestTargetUnsigned is returned when `(request-target)` is not
	// part of the signed header set. upstream の parseRequest は
	// `headers: ['(request-target)', 'host', 'date']` を必須にするため、これが
	// 署名されていないと 401 になる。署名が HTTP method/path に束縛されない
	// 非準拠署名を弾く (#2087)。
	ErrInboxRequestTargetUnsigned = errors.New("inbox: (request-target) is not signed")
	// ErrInboxDateUnsigned is returned when `date` is not part of the signed
	// header set (upstream parseRequest の必須 header、#2087)。
	ErrInboxDateUnsigned = errors.New("inbox: date header is not signed")
	// ErrInboxHostUnsigned is returned when `host` is not part of the signed
	// header set. Upstream requires the Host header to be covered by the
	// signature so it cannot be swapped after signing.
	ErrInboxHostUnsigned = errors.New("inbox: host header is not signed")
	// ErrInboxHostMismatch is returned when the Host header does not equal the
	// configured host. This binds the signature to this server's own inbox.
	ErrInboxHostMismatch = errors.New("inbox: host header does not match configured host")
	// ErrInboxDigestUnsigned is returned when `digest` is not part of the
	// signed header set. Without it the Digest value is not authenticated and
	// the body could be replaced while the signature still verifies.
	ErrInboxDigestUnsigned = errors.New("inbox: digest header is not signed")
	// ErrInboxDigestMissing is returned when the request carries no Digest header.
	ErrInboxDigestMissing = errors.New("inbox: digest header is missing")
	// ErrInboxDigestMalformed is returned when the Digest header is not of the
	// form `algorithm=value`.
	ErrInboxDigestMalformed = errors.New("inbox: digest header is malformed")
	// ErrInboxDigestAlgo is returned when the Digest algorithm is not SHA-256.
	ErrInboxDigestAlgo = errors.New("inbox: unsupported digest algorithm")
	// ErrInboxDigestMismatch is returned when the SHA-256 of the body does not
	// match the Digest value (body integrity violation).
	ErrInboxDigestMismatch = errors.New("inbox: digest does not match request body")
	// ErrInboxDateSkew is returned when the signed Date is further from our
	// clock than InboxDateSkew allows (replay window).
	ErrInboxDateSkew = errors.New("inbox: date is outside the accepted clock skew")
)

// VerifyInboxAdmission performs the body-integrity and host-binding checks that
// Misskey applies to every inbound activity before it is queued (see
// ActivityPubServerService.inbox). It is the authoritative Digest verification
// for mk-go: the generic signature verifier only validates the Digest header
// *format*, while this function recomputes SHA-256(body) and compares it.
//
// The checks, mirroring upstream:
//   - host must be signed and equal expectedHost (skipped when expectedHost is
//     empty, e.g. unit tests / unconfigured callers that cannot compare);
//   - digest must be signed;
//   - the Digest header must be `SHA-256=<base64>` and its value must equal
//     base64(sha256(body)), compared in constant time;
//   - the Date must be within InboxDateSkew of our clock, so a captured request
//     cannot be re-posted indefinitely.
//
// parsed is the parsed Signature header (parsed.Headers is the signed header
// set), hostHeader is the request Host, dateHeader is the raw Date (use
// InboxDateHeader to resolve it), digestHeader is the raw Digest header, and
// body is the raw request body. A non-nil error means the request must be
// rejected with 401.
func VerifyInboxAdmission(parsed *ParsedSignature, hostHeader, expectedHost, dateHeader, digestHeader string, body []byte) error {
	if parsed == nil {
		return errors.New("inbox: missing parsed signature")
	}

	// (request-target) / date: 署名対象であること (upstream parseRequest の必須
	// header ['(request-target)', 'host', 'date'] 相当、#2087)。host と違い configured
	// host への依存が無いので expectedHost に関わらず必須。production の正規 peer
	// (Misskey/Mastodon 等) も mk-go 自身の deliver も 4 header を署名する。
	if !containsHeaderFold(parsed.Headers, "(request-target)") {
		return ErrInboxRequestTargetUnsigned
	}
	if !containsHeaderFold(parsed.Headers, "date") {
		return ErrInboxDateUnsigned
	}
	if err := verifyDateSkew(dateHeader); err != nil {
		return err
	}

	// host: 署名対象 + 自ホスト一致。expectedHost が未設定の呼び出し元
	// (config 非配線のテスト等) では比較対象が無いため host check 全体を skip する。
	// production では router が config.Host を必ず配線するので upstream と等価。
	if expectedHost != "" {
		if !containsHeaderFold(parsed.Headers, "host") {
			return ErrInboxHostUnsigned
		}
		if !strings.EqualFold(strings.TrimSpace(hostHeader), expectedHost) {
			return ErrInboxHostMismatch
		}
	}

	// digest: 署名対象であること。署名されていないと Digest 値が認証されず、
	// 署名を保ったまま body を差し替えられる。
	if !containsHeaderFold(parsed.Headers, "digest") {
		return ErrInboxDigestUnsigned
	}

	algo, value, ok := splitDigestHeader(digestHeader)
	if !ok {
		if strings.TrimSpace(digestHeader) == "" {
			return ErrInboxDigestMissing
		}
		return ErrInboxDigestMalformed
	}
	if !strings.EqualFold(algo, "SHA-256") {
		return ErrInboxDigestAlgo
	}

	sum := sha256.Sum256(body)
	want := base64.StdEncoding.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(want), []byte(value)) != 1 {
		return ErrInboxDigestMismatch
	}
	return nil
}

// InboxDateHeader resolves the date value upstream's parser uses: `X-Date`
// takes precedence over `Date` when both are present.
func InboxDateHeader(h http.Header) string {
	if v := h.Get("X-Date"); v != "" {
		return v
	}
	return h.Get("Date")
}

// verifyDateSkew rejects a Date too far from our clock.
//
// **読めない値と空は通す。** upstream も `new Date(...)` が Invalid Date に
// なると `Math.abs(NaN) > skew` が false になって素通りする。ここだけ厳しく
// すると、JS では解釈できて Go では解釈できない書式を送ってくる peer からの
// 配送を落とす。値を差し替えれば署名が壊れる (date は署名対象を必須にして
// ある) ので、緩くても再投函の穴にはならない。
func verifyDateSkew(dateHeader string) error {
	raw := strings.TrimSpace(dateHeader)
	if raw == "" {
		return nil
	}
	t, err := http.ParseTime(raw)
	if err != nil {
		return nil
	}
	skew := nowFuncForAdmission().Sub(t)
	if skew < 0 {
		skew = -skew
	}
	if skew > InboxDateSkew {
		return ErrInboxDateSkew
	}
	return nil
}

// containsHeaderFold reports whether name appears in the signed header set,
// case-insensitively. Signature headers are conventionally lowercase, but we
// compare case-insensitively for robustness.
func containsHeaderFold(headers []string, name string) bool {
	for _, h := range headers {
		if strings.EqualFold(h, name) {
			return true
		}
	}
	return false
}

// splitDigestHeader parses a `algorithm=value` Digest header, mirroring
// upstream's `^([a-zA-Z0-9\-]+)=(.+)$` regex by splitting on the first `=`
// (base64 values can themselves contain `=` padding, so only the first
// separator is significant). Returns ok=false when there is no `=` or either
// side is empty.
func splitDigestHeader(header string) (algo, value string, ok bool) {
	header = strings.TrimSpace(header)
	idx := strings.IndexByte(header, '=')
	if idx <= 0 || idx == len(header)-1 {
		return "", "", false
	}
	return header[:idx], header[idx+1:], true
}
