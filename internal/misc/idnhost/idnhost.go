// Package idnhost normalizes host names for comparison against stored values.
package idnhost

import (
	"strings"

	"golang.org/x/net/idna"
)

// Puny normalizes a host for comparison the way upstream
// UtilityService.toPuny does (idna.ToASCII(lowercase), UTS#46)。
//
// **比較専用。** 比較に使う側は正規化されていない形で来ることがある
// (フロントの mention リンクは `toUnicode(host)` で URL を組むし、投稿本文の
// mention は書き手が打ったまま)。両辺に掛けて `パイ.example` と
// `xn--eckve.example`、`Remote.Example` と `remote.example` を同一視する。
//
// **保存側は正規化していない。** `hostFromURI` は `url.Parse(uri).Host` を
// そのまま入れるので、`user.host` / `instance.host` には非正規化の行がありうる。
// これを揃えるには既存行の backfill migration が要るので別スコープ
// (`internal/repository` の `hostMatch` は、そのため両方の形に当てている)。
//
// idna が失敗する不正入力のみ小文字化で返す (Go default の lenient UTS#46
// profile では port 付き host も成功し ASCII tail はそのまま残るため、fallback は
// 実質ほぼ発生しない)。
//
// なお Go の idna は ideographic/fullwidth dot (U+3002 等) を `.` に畳まない
// (Node の domainToASCII と異なるが、別 authority を同一視しない安全側)。
func Puny(host string) string {
	lower := strings.ToLower(host)
	if ascii, err := idna.ToASCII(lower); err == nil {
		return ascii
	}
	return lower
}

// PunyPtr is Puny for optional hosts. nil はローカル指定なのでそのまま返す。
func PunyPtr(host *string) *string {
	if host == nil {
		return nil
	}
	p := Puny(*host)
	return &p
}
