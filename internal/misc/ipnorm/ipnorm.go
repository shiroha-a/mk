// Package ipnorm canonicalizes remote IP strings before they are stored or
// searched (#3103).
//
// **記録の時点で畳む。** 検索のときだけ正規化すると、既に保存されている表記揺れの
// 行に完全一致で当たらない。同じ端末からの観測が別々の行に分かれると、関連
// アカウントの候補としても数えられなくなる (#3066)。
package ipnorm

import (
	"net/netip"
	"strings"
)

// Normalize returns the canonical text form of raw.
//
// 畳むのは 3 つ:
//
//   - **IPv4-mapped IPv6** (`::ffff:192.0.2.1`) は対応する IPv4 へ。#3066 が
//     「IPv4-mapped IPv6は、対応するIPv4と別のIPとして重複集計しない」と要求している
//   - IPv6 の大文字・ゼロ圧縮の揺れ (`2001:DB8::0001` → `2001:db8::1`)
//   - **zone (`fe80::1%eth0` の `%eth0`)**。zone はこのホストのインタフェース名で、
//     アカウントを突き合わせる材料にならない。残すと同じ端末が zone 名違いで
//     別の行に分かれる
//
// パースできない値は ("", false) を返す。**保存しない** — どの検索にも一致しえない
// 値を入れても、関連候補の材料にならないまま IP として記録だけが残る。
func Normalize(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	// **これは防御であって、唯一の歯止めではない。** 空文字は下の
	// `ParseAddr` / `ParseAddrPort` が両方失敗するので、この分岐を外しても
	// 振る舞いは同じ (テストでも差が出ない)。読む側に「空は弾く」と伝えるために残す。
	if s == "" {
		return "", false
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		// **`host:port` も受ける。** `echo.Context.RealIP()` は `RemoteAddr` を
		// 分解するので通常は port を外して返すが、**`X-Real-IP` に port 付きを
		// 載せた値はそのまま返る** (`X-Forwarded-For` 無しの UDS 構成で実測)。
		// ここで拾わないと丸ごと捨てることになる。
		ap, perr := netip.ParseAddrPort(s)
		if perr != nil {
			return "", false
		}
		addr = ap.Addr()
	}
	// Unmap が IPv4-mapped を IPv4 へ畳む。WithZone("") で zone を落とす。
	return addr.Unmap().WithZone("").String(), true
}
