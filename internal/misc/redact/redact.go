// Package redact removes credentials from strings that leave the process,
// such as access log lines and error-reporting payloads.
//
// 秘密パラメータの一覧をここに集約しているのは、記録先ごとに別々の一覧を
// 持つと片方だけ直して片方が漏れ続ける事故が起きるため。新しく秘密を query
// で受ける endpoint を足したら SensitiveQueryParams に追加すれば、
// アクセスログと Sentry の両方が同時に守られる。
package redact

import (
	"net/url"
	"strings"
)

// Placeholder replaces the value of a sensitive parameter.
const Placeholder = "REDACTED"

// SensitiveQueryParams lists query parameter names whose values must never
// leave the process.
//
// `i` は Misskey の API token そのもの。同梱クライアントは WebSocket を
// `/streaming?i=<token>` で開くので、query を素通しすると有効な credential
// がアクセスログや Sentry イベントに平文で残る。ログ閲覧権限だけで
// アカウントを乗っ取れる状態になる。
//
// OAuth 系 (`code` / `*_token` / `client_secret`) も同じ理由で伏せる。
var SensitiveQueryParams = map[string]struct{}{
	"i":             {},
	"token":         {},
	"code":          {},
	"access_token":  {},
	"refresh_token": {},
	"client_secret": {},
	"secret":        {},
	"password":      {},
}

// Query returns rawQuery with the values of sensitive parameters replaced by
// Placeholder. ok reports whether the query could be parsed; when it is false
// the returned string is empty and callers must not fall back to the original
// (that would leak the very values this function exists to remove).
//
// 解析できないクエリを「そのまま出す」形の fallback にしないのは、失敗が
// 漏らす方向に倒れるため。呼び出し側は捨てるか path だけを残す。
func Query(rawQuery string) (redacted string, ok bool) {
	if rawQuery == "" {
		return "", true
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", false
	}
	changed := false
	for key, vals := range values {
		if _, sensitive := SensitiveQueryParams[strings.ToLower(key)]; !sensitive {
			continue
		}
		for i := range vals {
			vals[i] = Placeholder
		}
		changed = true
	}
	if !changed {
		return rawQuery, true
	}
	// Encode は key 順にソートするので元の並びは保たれないが、記録の用途
	// (どの endpoint に何が来たか) では問題にならない。
	return values.Encode(), true
}

// URI returns rawURI with the values of sensitive query parameters replaced.
// When the query cannot be parsed only the path is returned.
func URI(rawURI string) string {
	path, query, found := strings.Cut(rawURI, "?")
	if !found {
		return rawURI
	}
	redacted, ok := Query(query)
	if !ok {
		return path
	}
	return path + "?" + redacted
}
