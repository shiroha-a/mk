package mediaproxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// expiringSigSeparator splits the deadline from the digest in an expiring
// signature (`<unix seconds>.<hex digest>`).
//
// **`sig` の中に入れる。** 別のクエリ引数にすると、その値が署名の対象から
// 外れていないかを呼び出し側ごとに確かめる必要が出る。1 つの文字列にして
// おけば、**片方だけ書き換えると必ず検証が落ちる**。
const expiringSigSeparator = "."

// SignURLUntil produces a signature that is only valid until `until`.
//
// **無期限の署名を配らないための形 (#3037)。** `SignURL` の署名は URL だけを
// 覆うので、一度どこかで発行されると**永久に有効**になる。`/url` (URL
// プレビュー) は**未認証で任意の URL を渡せて**、その OGP 画像 —
// つまり攻撃者が自分のページに書いた任意の URL — に対して署名を発行する
// ので、そこから無期限の proxy URL を好きなだけ作れた。media proxy の
// allowlist は「DB のどれかの列に実在する URL だけ」を通す mk-go 独自の
// 硬化だが、この経路はそれを丸ごと迂回する。
//
// 期限を入れても**発行そのもの**は止まらない (攻撃者はいつでも叩き直せる)。
// 止まるのは「発行した URL を貼って配る」形 — ノートや外部サイトに埋めた
// proxy URL が期限で死ぬので、恒久的な増幅リンクにはならない。
func SignURLUntil(secret []byte, rawURL string, until time.Time) string {
	exp := strconv.FormatInt(until.Unix(), 10)
	return exp + expiringSigSeparator + hex.EncodeToString(expiringDigest(secret, rawURL, exp))
}

// VerifyExpiringHMAC checks an expiring signature against rawURL at time now.
//
// 期限の形をしていない署名には false を返す (呼び出し側が `VerifyHMAC` を
// 先に試す)。
func VerifyExpiringHMAC(secret []byte, rawURL, sig string, now time.Time) bool {
	exp, digest, ok := strings.Cut(sig, expiringSigSeparator)
	if !ok {
		return false
	}
	// **先に HMAC を見る。** 期限だけを見て早期に返すと、署名が正しいかどうかと
	// 無関係に「期限切れか」が観測できてしまう。
	want := hex.EncodeToString(expiringDigest(secret, rawURL, exp))
	if !hmac.Equal([]byte(want), []byte(digest)) {
		return false
	}
	sec, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return false
	}
	return now.Unix() <= sec
}

// expiringDigest is HMAC-SHA256 over `<url>\n<exp>`.
//
// **区切りを入れる。** 単純に連結すると `url="…a" exp="12"` と
// `url="…a1" exp="2"` が同じ digest になり、期限を伸ばせる。URL に改行は
// 入りえない (`colfit.Storable` を通す前でも、proxy が受けるのは
// url-decode 済みのクエリ値で、改行入りの URL はそもそも取得に失敗する)。
func expiringDigest(secret []byte, rawURL, exp string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(rawURL))
	mac.Write([]byte("\n"))
	mac.Write([]byte(exp))
	return mac.Sum(nil)
}
