package url_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	apiurl "github.com/shiroha-a/mk/internal/api/url"
	"github.com/shiroha-a/mk/internal/core/urlpreview"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
)

// doPreview builds an echo context for GET /api/url?url=<raw> and invokes the
// handler, returning the recorder for assertions.
func doPreview(h *apiurl.Handler, rawURL string) *httptest.ResponseRecorder {
	e := echo.New()
	target := "/api/url"
	if rawURL != "" {
		target += "?url=" + rawURL
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_ = h.Preview(c)
	return rec
}

// url query param が空のとき 400 INVALID_PARAM を返す。
func TestPreview_EmptyURL(t *testing.T) {
	// fetcher は呼ばれない経路なので disabled で十分。
	h := apiurl.NewHandler(urlpreview.NewFetcher(urlpreview.Config{Enabled: false}, nil, "", nil))
	rec := doPreview(h, "")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "INVALID_PARAM", errObj["code"])
	assert.Equal(t, apierr.UUIDInvalidParam, errObj["id"])
}

// #2106 N18: url preview が disabled のとき、handler は 403 + URL_PREVIEW_DISABLED
// を返す (以前は 200 + null degrade body で frontend の !res.ok 分岐が機能しなかった)。
func TestPreview_Disabled_Returns403(t *testing.T) {
	h := apiurl.NewHandler(urlpreview.NewFetcher(urlpreview.Config{Enabled: false}, nil, "", nil))
	rec := doPreview(h, "https://example.com/article")

	assert.Equal(t, http.StatusForbidden, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	e, ok := body["error"].(map[string]any)
	require.True(t, ok, "error object であること")
	assert.Equal(t, "URL_PREVIEW_DISABLED", e["code"])
	assert.Equal(t, "58b36e13-d2f5-0323-b0c6-76aa9dabefb8", e["id"])
}

// #2106 N18: enabled fetcher が取得失敗すると 422 + URL_PREVIEW_FAILED を返す。
func TestPreview_FetchFailed_Returns422(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	failURL := srv.URL
	srv.Close() // close して connection refused にし fetch を失敗させる

	f := urlpreview.NewFetcher(urlpreview.Config{
		Enabled: true, TimeoutMs: 2000, MaxContentLength: 1 << 20,
	}, nil, "", nil)
	f.SetHTTPClient(&http.Client{})
	h := apiurl.NewHandler(f)

	rec := doPreview(h, failURL)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	e, ok := body["error"].(map[string]any)
	require.True(t, ok, "error object であること")
	assert.Equal(t, "URL_PREVIEW_FAILED", e["code"])
	assert.Equal(t, "09d01cb5-53b9-4856-82e5-38a50c290a3b", e["id"])
	assert.Equal(t, "max-age=86400, immutable", rec.Header().Get("Cache-Control"))
}

// fetcher が成功したとき、parse された Result をそのまま 200 で返す。
func TestPreview_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head>
			<meta property="og:title" content="Hello">
			<meta property="og:description" content="World">
		</head><body></body></html>`))
	}))
	defer srv.Close()

	f := urlpreview.NewFetcher(urlpreview.Config{
		Enabled:          true,
		AllowRedirect:    true,
		TimeoutMs:        5000,
		MaxContentLength: 1 << 20,
	}, nil, "", nil)
	// SetHTTPClient で SSRF-safe transport を上書きし、httptest (127.0.0.1) へ
	// 到達できるようにする (= urlpreview/fetcher_test.go の既存パターン)。
	f.SetHTTPClient(&http.Client{})
	h := apiurl.NewHandler(f)

	rec := doPreview(h, srv.URL)
	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "Hello", body["title"])
	assert.Equal(t, "World", body["description"])

	// success 経路でも shape を固定する: og:url 無しなので url は要求 URL を
	// echo、sensitive は false、player は object (plain HTML なので埋め込み
	// player URL は無し)。degrade 経路との shape 一貫性を保証する。
	assert.Equal(t, srv.URL, body["url"])
	assert.Equal(t, false, body["sensitive"])
	player, ok := body["player"].(map[string]any)
	require.True(t, ok, "player は object であること")
	assert.Nil(t, player["url"], "plain HTML では player.url は null")
}

// リモートの og:image (thumbnail) / favicon (icon) は proxy 経由に書き換えられ、
// 閲覧者 IP が外部サイトへ漏洩しない (issue #1529)。
func TestPreview_RemoteThumbnailProxied(t *testing.T) {
	entity.SetMediaURLContext(entity.NewMediaURLContext(
		"https://mk.test", "https://mk.test/proxy", []byte("url-preview-secret"), false, true))
	defer entity.SetMediaURLContext(nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head>
			<meta property="og:title" content="Hello">
			<meta property="og:image" content="https://cdn.example.test/img.png">
		</head><body></body></html>`))
	}))
	defer srv.Close()

	f := urlpreview.NewFetcher(urlpreview.Config{
		Enabled: true, AllowRedirect: true, TimeoutMs: 5000, MaxContentLength: 1 << 20,
	}, nil, "", nil)
	f.SetHTTPClient(&http.Client{})
	h := apiurl.NewHandler(f)

	rec := doPreview(h, srv.URL)
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	thumb, _ := body["thumbnail"].(string)
	// q.Encode() がキーをソートするため ?sig=...&url=... の順になる。直接の
	// 外部ホストではなく内部 proxy 経由であること + 元 URL が url= に埋め込まれて
	// いることを確認する。
	if !strings.HasPrefix(thumb, "https://mk.test/proxy/image.webp?") {
		t.Fatalf("url-preview thumbnail not proxied: %q", thumb)
	}
	if strings.HasPrefix(thumb, "https://cdn.example.test") || !strings.Contains(thumb, "cdn.example.test") {
		t.Fatalf("proxied thumbnail must embed remote target in url= param, not expose it directly: %q", thumb)
	}
}
