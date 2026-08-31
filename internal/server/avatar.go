package server

import (
	"github.com/shiroha-a/mk/internal/entity"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	apiusers "github.com/shiroha-a/mk/internal/api/users"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// avatarStaticFallback is the path the frontend treats as the
// "user not found" placeholder. Lives under /static-assets which the
// router maps to the bundled frontend static directory.
const avatarStaticFallback = "/static-assets/user-unknown.png"

// avatarUserLookup is the minimal interface needed to resolve an acct
// string to a model.User row. Implemented by repository.UserRepository,
// abstracted here so handler tests can stub without standing up the
// full repository.
type avatarUserLookup interface {
	FindByUsernameLower(username string, host *string) (*model.User, error)
}

// avatarHandler implements `GET /avatar/@:acct` (#462). The frontend
// `MkMention.vue` constructs `<img src="/avatar/@user@host">` directly
// rather than fetching API metadata, so without this endpoint mention
// chips render with no avatar image.
//
// Behaviour mirrors Misskey TS upstream `ServerService.ts:211`:
//   - acct = "username" → local user (host == nil filter)
//   - acct = "username@host" → remote user; if the host matches the
//     running instance's host it is treated as local (host == nil)
//     since upstream stores local users with host=NULL.
//   - User found → 302 redirect to user.avatarUrl, or to the
//     identicon endpoint (/identicon/:userID) when avatarUrl is unset.
//   - User missing or suspended → 302 redirect to
//     /static-assets/user-unknown.png so the existence of an account
//     is not leaked via mention probes.
//
// Sets Cache-Control: public, max-age=86400 so browsers do not run
// the DB lookup for every mention chip (matches upstream).
func avatarHandler(userRepo avatarUserLookup, localHost string) echo.HandlerFunc {
	localHost = strings.ToLower(localHost)
	return func(c echo.Context) error {
		// 304 / cache-hit でも返したい header なので Redirect 前に書く。
		c.Response().Header().Set(echo.HeaderCacheControl, "public, max-age=86400")

		acct := c.Param("acct")
		// **実装は users.ParseAcct に一本化してある** (#2791)。
		// pinned-users も同じ形で acct を解く。
		username, host := apiusers.ParseAcct(acct, localHost)
		if username == "" {
			return c.Redirect(http.StatusFound, avatarStaticFallback)
		}

		user, err := userRepo.FindByUsernameLower(strings.ToLower(username), host)
		if err != nil || user == nil {
			return c.Redirect(http.StatusFound, avatarStaticFallback)
		}
		if user.IsSuspended {
			// upstream は suspended な user も `where: { isSuspended: false }`
			// で除外する。一致させて mention 経由で生存確認できないようにする。
			return c.Redirect(http.StatusFound, avatarStaticFallback)
		}

		target := user.AvatarURL
		if target == nil || *target == "" {
			// identicon fallback。`user.id` をシード化文字列として使う。
			target = strPtr("/identicon/" + user.ID)
		}
		// リモートの avatar はメディアプロキシ経由に書き換える (#2425)。
		//
		// **ここだけ生の URL を Location に入れていた。** API 応答側は
		// `entity.ProxyAvatarURL` で既に leak-safe にしてあり (UserLite /
		// chat packUser / stream)、その doc comment も「every avatar-bearing
		// response」と言っているのに、この endpoint は素通しだった。結果、
		// mention chip を 1 つ表示するだけで閲覧者の IP とリファラが相手
		// インスタンスへ渡っていた。
		//
		// 同一オリジン・相対 URL (identicon fallback を含む) は wrap されない。
		return c.Redirect(http.StatusFound, entity.ProxyAvatarURLString(*target))
	}
}

func strPtr(s string) *string { return &s }

// 静的アサーション: repository.UserRepository は avatarUserLookup を
// 満たす (handler 配線時の型不一致を build-time で検出する)。
var _ avatarUserLookup = (repository.UserRepository)(nil)
