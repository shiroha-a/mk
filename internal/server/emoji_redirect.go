package server

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// 静的アサーション: repository.EmojiRepository が emojiLookup を満たすこと
// を build 時に検証する (avatar.go の同パターンと揃える)。配線時の型
// 不一致 (interface 拡張 / signature 変更) を早期に検出。
var _ emojiLookup = (repository.EmojiRepository)(nil)

// emojiRedirectFallback is the path the frontend treats as the
// "emoji not found" placeholder. Misskey TS upstream redirects to
// the bundled static asset when the request includes a `fallback`
// query string.
const emojiRedirectFallback = "/static-assets/emoji-unknown.png"

// emojiPathRegexp validates the URL component the frontend uses to
// reference custom emoji (`<name>[@<host>].webp`). Mirrors upstream's
// regex (`/^[a-zA-Z0-9\-_@\.]+?\.webp$/`) so requests for unrelated
// or attacker-crafted paths are rejected before hitting the DB.
var emojiPathRegexp = regexp.MustCompile(`^[a-zA-Z0-9\-_@\.]+?\.webp$`)

// emojiLookup is the minimal interface used by the redirect handler.
// Keeping the surface narrow lets handler tests stub without standing
// up the full EmojiRepository.
type emojiLookup interface {
	FindByNameAndHost(name string, host *string) (*model.Emoji, error)
}

// emojiRedirectHandler implements `GET /emoji/:path(.*)` (#468). The
// frontend `MkCustomEmoji` falls back to fetching this endpoint when
// no explicit `emojiUrl` prop is passed — notably the notification
// `MkReactionIcon` for reaction notifications, which would otherwise
// render the fallback image. We mirror upstream Misskey's
// `ServerService.ts:153` semantics:
//
//   - path = "<name>.webp" → local emoji (host == nil)
//   - path = "<name>@.<>.webp" → local emoji (`@.` is the canonical
//     local-suffix sentinel emitted by ReactionService)
//   - path = "<name>@<host>.webp" → remote emoji
//   - row found → 302 redirect to publicUrl (fallback to originalUrl)
//   - row missing → 404, or 302 to /static-assets/emoji-unknown.png
//     when the `fallback` query string is present
//
// Cache-Control header mirrors upstream so the browser stops
// re-fetching reaction images on every notification render.
func emojiRedirectHandler(repo emojiLookup) echo.HandlerFunc {
	return func(c echo.Context) error {
		path := c.Param("path")

		c.Response().Header().Set(echo.HeaderCacheControl, "public, max-age=86400")

		if !emojiPathRegexp.MatchString(path) {
			return c.NoContent(http.StatusNotFound)
		}

		// `.webp` 拡張子を剥がし、`@` で name / host に分解する。
		emojiPath := strings.TrimSuffix(path, ".webp")
		chunks := strings.Split(emojiPath, "@")
		if len(chunks) > 2 {
			return c.NoContent(http.StatusBadRequest)
		}

		name := chunks[0]
		var hostPtr *string
		if len(chunks) == 2 {
			h := chunks[1]
			// `@.` は ReactionService.normalizeReaction が永続化する
			// canonical local-suffix。host_NULL と等価に扱う。
			if h != "" && h != "." {
				hostPtr = &h
			}
		}

		emoji, err := repo.FindByNameAndHost(name, hostPtr)
		if err != nil || emoji == nil {
			if _, hasFallback := c.QueryParams()["fallback"]; hasFallback {
				return c.Redirect(http.StatusFound, emojiRedirectFallback)
			}
			return c.NoContent(http.StatusNotFound)
		}

		// publicUrl が空の場合 originalUrl にフォールバック (upstream の
		// `emoji.publicUrl || emoji.originalUrl` と同じ後方互換)。
		target := emoji.PublicURL
		if target == "" {
			target = emoji.OriginalURL
		}
		if target == "" {
			return c.NoContent(http.StatusNotFound)
		}
		// upstream (`ServerService.ts` の `/emoji/:path`) はここで
		// `default-src 'none'; style-src 'unsafe-inline'` を付けるので header を
		// 揃える。ただし **この応答は redirect なので CSP は実際には効かない**
		// (ブラウザは最終的な応答の header を適用する)。upstream も redirect の
		// 前に付けており同じ状態。security 上の効果を期待して置くものではなく、
		// header 集合を一致させるためのもの。
		c.Response().Header().Set("Content-Security-Policy", assetCSP)
		// upstream は 301 redirect だが、emoji の publicUrl が変動しうる
		// (リモート instance の URL 更新等) ので 302 で永続キャッシュを
		// 避ける。Cache-Control: max-age=86400 でブラウザ側 1 日キャッシュ
		// と組み合わせる。
		// リモート emoji はメディアプロキシ経由に書き換える (#2425)。upstream も
		// この endpoint では `${mediaProxy}/emoji.webp` へ飛ばしており、生の URL を
		// 返していた mk-go の方が乖離していた。包まないとリアクション 1 つ表示する
		// だけで閲覧者の IP が相手インスタンスへ渡る。
		//
		// API 応答の `emojis` field を server-proxy してはいけない (frontend が
		// client 側で包むので二重になる) のとは別経路。こちらは frontend が包まない。
		return c.Redirect(http.StatusFound, entity.ProxyEmojiURLString(target))
	}
}
