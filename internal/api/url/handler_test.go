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

// fetcher が error を返したとき、handler は HTTP 200 + 全 field null の degrade
// body を返す (= upstream Misskey /api/url の「取得失敗でも 200 で空 card」挙動)。
// degrade body の shape (key 一式 + null 初期化 + player object + url echo) を
// 固定する。
func TestPreview_FetcherError_DegradeBody(t *testing.T) {
	// disabled fetcher は Fetch で ErrDisabled を返すので degrade 経路に入る。
	h := apiurl.NewHandler(urlpreview.NewFetcher(urlpreview.Config{Enabled: false}, nil, "", nil))
	rawURL := "https://example.com/article"
	rec := doPreview(h, rawURL)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	// メタ系は null、url は要求 URL を echo、sensitive は false。
	for _, k := range []string{"title", "description", "thumbnail", "icon", "sitename", "activityPub"} {
		assert.Nil(t, body[k], "degrade body の %q は null であること", k)
	}
	assert.Equal(t, rawURL, body["url"])
	assert.Equal(t, false, body["sensitive"])

	player, ok := body["player"].(map[string]any)
	require.True(t, ok, "player は object であること")
	assert.Nil(t, player["url"])
	assert.Nil(t, player["width"])
	assert.Nil(t, player["height"])
	allow, ok := player["allow"].([]any)
	require.True(t, ok, "player.allow は array であること")
	assert.Empty(t, allow)
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
	entity.SetMediaProxy(func(rawURL, mode string) string {
		return "https://mk.test/proxy/image.webp?url=" + rawURL + "&sig=SIG"
	}, func() bool { return true }, false)
	defer entity.SetMediaProxy(nil, nil, false)

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
	if !strings.HasPrefix(thumb, "https://mk.test/proxy/image.webp?url=") {
		t.Fatalf("url-preview thumbnail not proxied: %q", thumb)
	}
}
