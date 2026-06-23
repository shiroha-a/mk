package middleware

import (
	"strings"

	"github.com/labstack/echo/v4"
	emiddleware "github.com/labstack/echo/v4/middleware"
)

// BodyLimitByPath returns a GLOBAL middleware that caps the request body size by
// path, matching upstream's per-route bodyLimit:
//
//   - /api/*  → 1MiB (upstream ApiServerService の bodyLimit: 1024*1024)
//   - /inbox, /users/:id/inbox → 64KiB (upstream ActivityPubServerService の bodyLimit: 1024*64)
//   - その他 → 制限なし
//
// これは **auth.Authenticate より前** (global pre-auth) に登録する必要がある。
// auth.Authenticate は token 抽出のため body を io.ReadAll する (extractToken) が、
// echo の middleware 順は global → group → route なので、route/group に BodyLimit を
// 置いても auth の read が先に走って無制限に body を消費し bypass される (#1958 / #2075)。
// 本 middleware を auth より前に置くことで auth・JSONBodyParse・handler の全 read が
// limitedReader 経由になり cap される。
//
// 超過時は upstream fastify と同じ 413 (echo BodyLimit が ContentLength 即時 reject /
// chunked は limitedReader が 413 error を返す。後者は JSONBodyParse / inbox handler が
// echo.HTTPError を伝播する)。
//
// upload endpoint (drive/files/create) **のみ** 1MiB 制限から除外する。これは唯一の
// multipart upload route で RequireAuth + write:drive 保護され、file サイズは drive handler の
// maxFileSize が別途 bound する。content-type が multipart というだけで /api 全体を除外すると、
// 非 upload endpoint (例: 未認証到達可能な /api/meta) に multipart を投げて 1MiB を bypass し
// ParseMultipartForm に disk spill させる DoS 穴になる。upstream も requireFile endpoint だけ
// multipart parser を起動し、他の endpoint は bodyLimit:1024*1024 が raw body に効く。
func BodyLimitByPath() echo.MiddlewareFunc {
	apiBL := emiddleware.BodyLimit("1MiB")    // = 1024*1024 = 1048576
	inboxBL := emiddleware.BodyLimit("64KiB") // = 1024*64   = 65536
	const driveUploadPath = "/api/drive/files/create"
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		// limitedReader pool は per-route で 1 度だけ構築する (BodyLimit の
		// MiddlewareFunc を next で 1 回 wrap)。本 middleware は global なので
		// next は router dispatch、pool 構築も 1 度きり。
		apiNext := apiBL(next)
		inboxNext := inboxBL(next)
		return func(c echo.Context) error {
			p := c.Request().URL.Path
			switch {
			case p == driveUploadPath:
				return next(c) // 唯一の multipart upload endpoint のみ除外 (auth + maxFileSize 保護)
			case p == "/api" || strings.HasPrefix(p, "/api/"):
				return apiNext(c)
			case isInboxPath(p):
				return inboxNext(c)
			}
			return next(c)
		}
	}
}

// isInboxPath reports whether p is an ActivityPub inbox route (/inbox or
// /users/:id/inbox)。
func isInboxPath(p string) bool {
	return p == "/inbox" || (strings.HasPrefix(p, "/users/") && strings.HasSuffix(p, "/inbox"))
}
