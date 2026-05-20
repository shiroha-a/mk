package server

import (
	urlpkg "net/url"
	"path/filepath"

	"github.com/labstack/echo/v4"
)

// staticAssetCacheControl is the Cache-Control header value used for
// long-lived browser-cached static asset routes (/twemoji, /fluent-emoji).
// 30 days matches upstream Misskey TS `ClientServerService.ts` `ms('30 days')`.
// `immutable` enables aggressive cache: the browser SKIPS revalidation
// entirely until the entry expires, eliminating repeat HTTP requests for
// already-fetched assets. Safe because the assets are content-addressed by
// hex codepoint and never mutate in place (a new emoji set ships with new
// filenames).
const staticAssetCacheControl = "public, max-age=2592000, immutable"

// serveStaticAssetDir mirrors echo.Echo.Static(prefix, root) but attaches
// the long-lived `Cache-Control` header. Used for /twemoji and
// /fluent-emoji to align with upstream Misskey TS browser cache policy.
//
// Echo's built-in `Static` does not accept middleware or response-header
// hooks, so this helper duplicates Echo's tiny `prefix+"/*"` + `c.File`
// pattern (path traversal is rejected via `filepath.Clean` per Echo's
// own implementation) and adds the header before serving.
func serveStaticAssetDir(e *echo.Echo, prefix, root string) {
	e.GET(prefix+"/*", func(c echo.Context) error {
		p, err := urlpkg.PathUnescape(c.Param("*"))
		if err != nil {
			return err
		}
		c.Response().Header().Set("Cache-Control", staticAssetCacheControl)
		return c.File(filepath.Join(root, filepath.Clean("/"+p)))
	})
}
