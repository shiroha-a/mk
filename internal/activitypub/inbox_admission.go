package activitypub

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
)

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
//     base64(sha256(body)), compared in constant time.
//
// parsed is the parsed Signature header (parsed.Headers is the signed header
// set), hostHeader is the request Host, digestHeader is the raw Digest header,
// and body is the raw request body. A non-nil error means the request must be
// rejected with 401.
func VerifyInboxAdmission(parsed *ParsedSignature, hostHeader, expectedHost, digestHeader string, body []byte) error {
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
