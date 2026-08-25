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
// **保存側も #2706 で同じ正規化を掛ける。** `hostFromURI` が `Puny` に通すので、
// 新しく入る行は正規化形しか持たない。`internal/repository` の `hostMatch` が今も
// 両方の形に当てているのは、**backfill 前に非正規化で保存された行**を引くため
// (`cmd/backfill-remote-host` を流し終えた環境では外せる)。
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
