package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/server/middleware"
)

// corsProbe runs the global CORS middleware over a stub handler and returns the
// recorded response. DB も Redis も要らない (gzip_test.go と同じ形)。
func corsProbe(t *testing.T, method, path, origin string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	e.Use(corsMiddleware())
	e.Any("/*", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	req := httptest.NewRequest(method, path, nil)
	if origin != "" {
		req.Header.Set(echo.HeaderOrigin, origin)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// TestCORS_RelationListsRejectCrossOriginBrowsers asserts that the public
// relation lists do not advertise CORS (#2953).
//
// **越境のブラウザからフォロー一覧を一括で抜くのを止めるのが目的。** 対象は
// POST + JSON なのでプリフライトが必須で、`Access-Control-Allow-Origin` が
// 無ければブラウザは実リクエストに到達しない。
func TestCORS_RelationListsRejectCrossOriginBrowsers(t *testing.T) {
	for path := range noCORSPaths {
		t.Run(path, func(t *testing.T) {
			pre := corsProbe(t, http.MethodOptions, path, "https://example.com")
			require.Emptyf(t, pre.Header().Get(echo.HeaderAccessControlAllowOrigin),
				"%s のプリフライトが CORS を許可している。越境のブラウザから一括取得できる", path)
			// **拒否はしない (204)。** Origin を付けてくる非ブラウザの
			// クライアントをステータスで壊さないため。
			require.Equal(t, http.StatusNoContent, pre.Code,
				"プリフライトは 204 で打ち切る。通すと catchall が 200+{} を返して警告ログを吐く")

			post := corsProbe(t, http.MethodPost, path, "https://example.com")
			require.Emptyf(t, post.Header().Get(echo.HeaderAccessControlAllowOrigin),
				"%s の実リクエストが CORS を許可している", path)
			require.Equal(t, http.StatusOK, post.Code,
				"リクエスト自体は拒否しない。ヘッダを出さないだけ")
		})
	}
}

// **同一オリジンと非ブラウザは無影響。**
//
// ブラウザは同一オリジンの応答に CORS 検査を適用しないので、ヘッダが無くても
// 同梱のフロントエンドは動く。Origin を持たないクライアント (ネイティブアプリ /
// CLI / 連合) は応答ヘッダを見ない。
func TestCORS_RelationListsStillServeRequests(t *testing.T) {
	for path := range noCORSPaths {
		for _, origin := range []string{"", "https://example.com"} {
			rec := corsProbe(t, http.MethodPost, path, origin)
			require.Equalf(t, http.StatusOK, rec.Code,
				"%s (origin=%q) が拒否された。ヘッダを出さないだけにすること", path, origin)
		}
	}
}

// **`Vary: Origin` を落とさない。** echo の CORS は Skipper より前に無条件で
// 付けているので、迂回した経路では自分で戻す必要がある。落ちると、CORS ヘッダの
// 有無が Origin ごとに違う応答が共有キャッシュで混ざりうる。
func TestCORS_RelationListsKeepVaryOrigin(t *testing.T) {
	for path := range noCORSPaths {
		rec := corsProbe(t, http.MethodPost, path, "https://example.com")
		require.Containsf(t, rec.Header().Values(echo.HeaderVary), echo.HeaderOrigin,
			"%s の応答から Vary: Origin が落ちている", path)
	}
}

// **他の API は従来どおり。** 除外を広げすぎると、サードパーティの Web
// クライアントを想定外に壊す。
func TestCORS_OtherEndpointsUnchanged(t *testing.T) {
	rec := corsProbe(t, http.MethodPost, "/api/notes/create", "https://example.com")
	require.Equal(t, "*", rec.Header().Get(echo.HeaderAccessControlAllowOrigin),
		"除外が広がりすぎている。対象は noCORSPaths の 2 つだけ")
}

// **CORS の除外とレート制限は同じ endpoint を対象にする (#2953)。**
//
// 片方だけ足すと、「越境は止めたが速度は無制限」または「速度は絞ったが越境の
// ブラウザから取り放題」という中途半端な状態になる。対になっていることを固定
// しておく。
func TestCORS_NoCORSPathsMatchRateLimitedEndpoints(t *testing.T) {
	for path := range noCORSPaths {
		key := path[len("/api/"):]
		_, ok := middleware.DefaultEndpointLimits[key]
		require.Truef(t, ok,
			"%s は CORS を絞っているのにレート制限が無い。ratelimit_defs.go にも足すこと", path)
	}
}
