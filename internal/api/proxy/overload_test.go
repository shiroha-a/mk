package proxy

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/core/mediaproxy"
)

// 過負荷で落とした応答の wire 上の形を固定する (#3032)。
//
// **`Cache-Control` が本体。** 通常の fallback は `max-age=300` だが、
// 一時的な輻輳をそれで返すと CDN に 5 分載り、輻輳が去っても画像が壊れた
// ままになる。#2913 が同型 (403 が 1 日キャッシュされてアイコンが 1 日
// 壊れた) なので、ここは必ず `no-store` にする。

// stubProxy returns a fixed error (or a PNG) from Fetch.
type stubProxy struct {
	fetchErr error
}

func (s stubProxy) Authorize(context.Context, string, string) error { return nil }

func (s stubProxy) Fetch(context.Context, string, mediaproxy.ProxyMode, mediaproxy.OutputFormat, bool) (*mediaproxy.ProxyResult, error) {
	if s.fetchErr != nil {
		return nil, s.fetchErr
	}
	return mediaproxy.DummyPNG(), nil
}

func stubHandler(t *testing.T, fetchErr error) (*Handler, *echo.Echo) {
	t.Helper()
	cfg := &config.Config{
		URL:              "https://example.com",
		MediaProxy:       "https://example.com/proxy",
		MediaProxySecret: []byte("test-secret"),
		UserAgent:        "Misskey/2026.5.4 (https://example.com)",
	}
	return NewHandler(stubProxy{fetchErr: fetchErr}, cfg), echo.New()
}

func TestHandle_OverloadedReturns503WithoutCaching(t *testing.T) {
	h, e := stubHandler(t, mediaproxy.ErrOverloaded)

	rec := doRequest(e, h, http.MethodGet,
		"/proxy/image.webp?avatar=1&url=https%3A%2F%2Fremote.example%2Fa.png",
		map[string]string{"User-Agent": "Mozilla/5.0"})

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"),
		"過負荷の応答をキャッシュさせてはいけない")
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
}

func TestHandle_OverloadedWithFallbackServesUncachedDummy(t *testing.T) {
	h, e := stubHandler(t, mediaproxy.ErrOverloaded)

	rec := doRequest(e, h, http.MethodGet,
		"/proxy/image.webp?avatar=1&fallback=1&url=https%3A%2F%2Fremote.example%2Fa.png",
		map[string]string{"User-Agent": "Mozilla/5.0"})

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"),
		"fallback でも過負荷の応答はキャッシュさせない")
	assert.NotEmpty(t, rec.Body.Bytes())
	// RFC 9110 が `Retry-After` を定義しているのは 503 と 3xx。200 に
	// 乗せると中間装置の挙動が未定義になる。
	assert.Empty(t, rec.Header().Get("Retry-After"),
		"200 の応答に Retry-After を乗せない")
}

// 恒久的な失敗まで no-store にしてしまっていないこと。
//
// **片方だけ見ると回帰に気付けない。** `serveFallback` を一律 `no-store` に
// 変える修正は、過負荷のテストだけなら緑のまま通る。
func TestHandle_NotFoundFallbackKeepsShortCache(t *testing.T) {
	h, e := stubHandler(t, mediaproxy.ErrNotFound)

	rec := doRequest(e, h, http.MethodGet,
		"/proxy/image.webp?avatar=1&fallback=1&url=https%3A%2F%2Fremote.example%2Fa.png",
		map[string]string{"User-Agent": "Mozilla/5.0"})

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "max-age=300", rec.Header().Get("Cache-Control"))
}

// 利用者の離脱を 500 に数えない (#3032)。
//
// **監視で本物の障害が埋もれる。** モバイル回線ではスクロールのたびに
// 未完了の画像要求が切られるので、ここを 500 にすると 5xx が常時立つ。
func TestHandle_ClientCancelIsNot500(t *testing.T) {
	h, e := stubHandler(t, context.Canceled)

	rec := doRequest(e, h, http.MethodGet,
		"/proxy/image.webp?avatar=1&url=https%3A%2F%2Fremote.example%2Fa.png",
		map[string]string{"User-Agent": "Mozilla/5.0"})

	assert.Equal(t, statusClientClosedRequest, rec.Code)
}

// **`context.DeadlineExceeded` は 499 にしない (#3032 レビュー)。**
//
// `/proxy` のリクエスト context に deadline を付ける middleware は無いので、
// ここへ届く `DeadlineExceeded` は **mk-go 自身の 30 秒
// `httpClient.Timeout`** から来る。Go 1.26 では `Client.Timeout` 由来の
// エラーが `errors.Is(err, context.DeadlineExceeded)` を満たし、
// `fetchRemote` が `%w` でラップしているので、**リモート origin が body を
// 引き延ばしただけでここに来る**。499 に倒すと、そのサーバー側障害が 4xx
// にも 5xx にも出なくなり、しかも `?fallback` が無視される。
func TestHandle_ServerSideTimeoutIsNotTreatedAsClientCancel(t *testing.T) {
	h, e := stubHandler(t, context.DeadlineExceeded)

	rec := doRequest(e, h, http.MethodGet,
		"/proxy/image.webp?avatar=1&url=https%3A%2F%2Fremote.example%2Fa.png",
		map[string]string{"User-Agent": "Mozilla/5.0"})

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotEqual(t, statusClientClosedRequest, rec.Code)

	// fallback が効くことも見る (499 だと本文ゼロで返っていた)。
	rec = doRequest(e, h, http.MethodGet,
		"/proxy/image.webp?avatar=1&fallback=1&url=https%3A%2F%2Fremote.example%2Fa.png",
		map[string]string{"User-Agent": "Mozilla/5.0"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
}

// fallback 付きでも利用者の離脱は 499 のまま。
//
// **ダミー画像を 200 で返すと、切れた接続に書こうとするうえ、実装として
// 「切断」と「処理失敗」の区別が消える。**
func TestHandle_ClientCancelIgnoresFallback(t *testing.T) {
	h, e := stubHandler(t, context.Canceled)

	rec := doRequest(e, h, http.MethodGet,
		"/proxy/image.webp?avatar=1&fallback=1&url=https%3A%2F%2Fremote.example%2Fa.png",
		map[string]string{"User-Agent": "Mozilla/5.0"})

	assert.Equal(t, statusClientClosedRequest, rec.Code)
}

// リモート取得の失敗は 404 ではなく 502 で、長期キャッシュさせない (#3034)。
//
// **404 + `max-age=86400` で返していたのが元のバグ。** リモートの一時障害が
// CDN に「この画像は存在しない」として 1 日焼き付き、復旧しても壊れたままに
// なっていた (#2913 と同型)。
func TestHandle_UpstreamUnavailableIsBadGatewayWithShortCache(t *testing.T) {
	h, e := stubHandler(t, fmt.Errorf("%w: dial tcp: connection refused", mediaproxy.ErrUpstreamUnavailable))

	rec := doRequest(e, h, http.MethodGet,
		"/proxy/image.webp?avatar=1&url=https%3A%2F%2Fremote.example%2Fa.png",
		map[string]string{"User-Agent": "Mozilla/5.0"})

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Equal(t, "max-age=300", rec.Header().Get("Cache-Control"))
	assert.NotEqual(t, "max-age=86400", rec.Header().Get("Cache-Control"),
		"リモートの一時障害を 1 日キャッシュさせている")
}

// **このテストが唯一検出するのは「502 分岐の中の `?fallback` だけを消す」変異。**
// 応答は generic 経路と byte 単位で同じ (200 + dummy PNG + `max-age=300`) なので、
// 分岐を丸ごと消す変異はここでは落ちない — そちらは
// TestHandle_UpstreamUnavailableIsBadGatewayWithShortCache と
// TestHandle_UpstreamTimeoutIsGatewayTimeout が落とす (実測)。
// 消すと `?fallback=1` で 502 が返るようになるので、残すこと。
func TestHandle_UpstreamUnavailableWithFallbackIsShortCached(t *testing.T) {
	h, e := stubHandler(t, fmt.Errorf("%w: dial tcp: connection refused", mediaproxy.ErrUpstreamUnavailable))

	rec := doRequest(e, h, http.MethodGet,
		"/proxy/image.webp?avatar=1&fallback=1&url=https%3A%2F%2Fremote.example%2Fa.png",
		map[string]string{"User-Agent": "Mozilla/5.0"})

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assert.Equal(t, "max-age=300", rec.Header().Get("Cache-Control"))
}

// 30 秒の httpClient.Timeout に当たったものは 504、それ以外が 502 (#3034)。
//
// **混ぜると監視で「繋がらない」と「遅い」が分離できない。**
func TestHandle_UpstreamTimeoutIsGatewayTimeout(t *testing.T) {
	h, e := stubHandler(t, fmt.Errorf("%w: %w", mediaproxy.ErrUpstreamUnavailable, context.DeadlineExceeded))

	rec := doRequest(e, h, http.MethodGet,
		"/proxy/image.webp?avatar=1&url=https%3A%2F%2Fremote.example%2Fa.png",
		map[string]string{"User-Agent": "Mozilla/5.0"})

	assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
	assert.NotEqual(t, http.StatusBadGateway, rec.Code)
	assert.Equal(t, "max-age=300", rec.Header().Get("Cache-Control"))
}

// 取りに行けない URL は 400 + 長期キャッシュ (#3034)。
//
// **恒久的なので短期キャッシュにしない。** 5 分ごとに引き直しても結果は
// 変わらないし、監視で「相手が落ちている」と読まれてもいけない。
func TestHandle_UnfetchableURLIsBadRequestWithLongCache(t *testing.T) {
	h, e := stubHandler(t, mediaproxy.ErrBadRequest)

	rec := doRequest(e, h, http.MethodGet,
		"/proxy/image.webp?avatar=1&url=%2Fidenticon%2Falice",
		map[string]string{"User-Agent": "Mozilla/5.0"})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "max-age=86400", rec.Header().Get("Cache-Control"))
	assert.NotEqual(t, http.StatusBadGateway, rec.Code)
}

// 本当に「無い」ときは従来どおり 404 + 1 日キャッシュのまま。
//
// **片側だけ見ると回帰に気付けない。** 「全部 502 にする」実装は上の 2 本なら
// 緑で通るが、消えた画像まで 5 分ごとに取りに行くことになる。
func TestHandle_NotFoundKeepsLongCache(t *testing.T) {
	h, e := stubHandler(t, mediaproxy.ErrNotFound)

	rec := doRequest(e, h, http.MethodGet,
		"/proxy/image.webp?avatar=1&url=https%3A%2F%2Fremote.example%2Fa.png",
		map[string]string{"User-Agent": "Mozilla/5.0"})

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "max-age=86400", rec.Header().Get("Cache-Control"))
}

// 取得中の離脱は 502 ではなく 499。
//
// **`%w` を 2 つにしたので、離脱したエラーは `ErrUpstreamUnavailable` と
// `context.Canceled` の両方を満たす。** handler の判定順が入れ替わると、
// 利用者が自分で切っただけのものが 502 として監視に積み上がる。
func TestHandle_CancelDuringFetchWinsOverUpstreamUnavailable(t *testing.T) {
	h, e := stubHandler(t, fmt.Errorf("%w: %w", mediaproxy.ErrUpstreamUnavailable, context.Canceled))

	rec := doRequest(e, h, http.MethodGet,
		"/proxy/image.webp?avatar=1&url=https%3A%2F%2Fremote.example%2Fa.png",
		map[string]string{"User-Agent": "Mozilla/5.0"})

	assert.Equal(t, statusClientClosedRequest, rec.Code)
	assert.NotEqual(t, http.StatusBadGateway, rec.Code)
}

// Service が MediaProxy を満たしていること。
//
// **インターフェースを足したので、production の型がずれても handler の
// テストだけは緑で通る。** router.go の配線と同じ代入をここで固定する。
func TestServiceSatisfiesMediaProxy(t *testing.T) {
	svc := mediaproxy.NewService(
		"https://example.com", "Misskey/2026.5.4 (https://example.com)",
		&mockStorage{files: map[string][]byte{}},
		&mockAllowlist{allowed: map[string]bool{}},
		[]byte("test-secret"), testAllowedCIDRs,
	)
	var _ MediaProxy = svc
	require.NotNil(t, NewHandler(svc, &config.Config{}))
}
