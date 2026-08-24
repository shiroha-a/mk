// Package idnhost normalizes host names for comparison against stored values.
package idnhost

import (
	"strings"

	"golang.org/x/net/idna"
)

// Puny normalizes a host for comparison the way upstream
// UtilityService.toPuny does (idna.ToASCII(lowercase), UTS#46)。
//
// **保存されている host は punycode。** リモートの `user.host` も `instance.host`
// も actor URI の host から作られるので ASCII に閉じている。一方、比較に使う側は
// Unicode IDN で来ることがある (フロントの mention リンクは `toUnicode(host)` で
// URL を組むし、投稿本文の mention は書き手が打ったままの形になる)。両辺に
// 掛けて `パイ.example` と `xn--eckve.example` を同一視する。
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
