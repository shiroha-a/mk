package middleware

import (
	"strings"

	"github.com/labstack/echo/v4"
)

// frameGuardSkipPrefixes lists path prefixes that must stay embeddable.
//
//   - /embed/ は iframe に埋め込まれること自体が目的。upstream も
//     ClientServerService で一律 DENY を付けたうえで、embed route だけ
//     `reply.removeHeader('X-Frame-Options')` して外している
//   - /files/ と /proxy/ は画像・動画・PDF などを配る。upstream の
//     FileServerService は X-Frame-Options を付けておらず、PDF を iframe で
//     開く利用を壊さないためにも合わせる
//
// mk-go には現時点で /embed/ の配線が無いが、先に除外を書いておく。後から
// embed を足す人がここに気付かないと、埋め込みが動かない理由を探すことになる。
var frameGuardSkipPrefixes = []string{
	"/embed/",
	"/files/",
	"/proxy/",
}

// FrameGuard returns a GLOBAL middleware that sets `X-Frame-Options: DENY`
// so the UI cannot be framed by a third-party page.
//
// upstream は ClientServerService の onRequest hook で同じ header を付けている
// (「クリックジャッキング防止のためiFrameの中に入れられないようにする」)。
// mk-go にはこれが無く、**upstream にあって mk-go に無い**状態だった。
// 単なる hardening ではなく互換性の欠落でもある。
//
// 特に守りたいのは OAuth の同意画面。認可プロンプトを透明な iframe で重ねて
// クリックを盗む攻撃はクリックジャッキングの典型で、SPA shell と同じ経路で
// 配信されている以上ここで一括して塞ぐのが確実。
//
// JSON を返す /api/* にも付くが、JSON document を frame する用途は無いので
// 実害はない。「どこに付けるか」を細かく列挙するより、外すべき経路
// (frameGuardSkipPrefixes) を明示する方が、route が増えたときに守り漏れない。
func FrameGuard() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !frameGuardSkipped(c.Request().URL.Path) {
				c.Response().Header().Set("X-Frame-Options", "DENY")
			}
			return next(c)
		}
	}
}

// frameGuardSkipped reports whether p is exempt from the frame guard.
func frameGuardSkipped(p string) bool {
	for _, prefix := range frameGuardSkipPrefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}
