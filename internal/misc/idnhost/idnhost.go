// Package idnhost normalizes host names for comparison against stored values.
package idnhost

import (
	"net/url"
	"strconv"
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
// 新しく入る行は正規化形しか持たない。`internal/repository` の `hostMatch` は
// #2996 で**引き当て側も正規形の完全一致だけ**にした (生の形にも当てる互換経路を
// 撤去した)。backfill 前の非正規化の行が残っている環境では、その行が引けなくなる。
//
// **既定ポートの扱いだけは違う。** `hostFromURI` は `https://h:443` を `h` として
// 保存するようになったが (連合ゲートの綴り回避を塞ぐため)、`Puny` はポートを
// 剥がさない。ポートを含む host を引き当てるときは、保存側と同じ形にしてから
// 渡すこと。
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

// HostPort normalizes a parsed URL's authority the way upstream's `punyHost`
// does: punycode host + non-default port.
//
// **既定ポートは剥がす。これが authority の正規形を作る唯一の規則。** upstream の
// `punyHost` / `extractDbHost` は WHATWG `new URL()` を使うので
// `new URL("https://x:443/").host` は `x` になる。Go の `net/url` は剥がさないので、
// 剥がさずに比較すると `blocked.example:443` という**同じ authority の別綴り**が
// suffix 一致をすり抜ける。
//
// 非既定ポート (`:8443`) は別 host のまま残す (upstream も同じ)。
func HostPort(u *url.URL) string {
	host := Puny(u.Hostname())
	// IPv6 リテラルは authority では `[::1]` の形で現れるが `Hostname()` が
	// bracket を外す。戻さないと `::1` + port が `::1:8443` になり、host 部に
	// コロンを含むだけの値と見分けが付かなくなる。
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && !isDefaultPort(u.Scheme, port) {
		return host + ":" + port
	}
	return host
}

// isDefaultPort reports whether port is scheme's default (and therefore has no
// place in the canonical authority).
func isDefaultPort(scheme, port string) bool {
	n, err := strconv.Atoi(port)
	if err != nil {
		return false
	}
	switch strings.ToLower(scheme) {
	case "https":
		return n == 443
	case "http":
		return n == 80
	}
	return false
}

// CanonicalURI rewrites raw with a normalized scheme and authority so the same
// resource always yields the same string. Returns "" when raw has no host.
//
// **保存する身元のキーに使う。** 比較側 (`sameDeliveryHost`) は punycode と既定
// ポートを畳むので、**同じ authority の別綴りが 2 つとも gate を通り、別々の
// identity になる**。`https://パイ.example/...` で Invite を受け、
// `https://xn--eckve.example/...` で本文が来る、という取り違えが実際に起こりうる
// (#1850 が同じ形を報告している)。
func CanonicalURI(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = HostPort(u)
	return u.String()
}
