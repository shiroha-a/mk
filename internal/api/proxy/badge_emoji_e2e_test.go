package proxy

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/core/mediaproxy"
)

// makeColorPNG builds a 200x120 image with a saturated red pixel, so that the
// badge pipeline's resize (96x96) and grayscale conversion are both observable.
func makeColorPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 200, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// #2909: `/emoji/<name>.webp?badge=1` が組み立てる URL の形が、ハンドラ経由で
// 実際に badge (96x96 グレースケール PNG) になること。
//
// **`emoji=1` を付けないのが要点。** parseMode は emoji を先に見るので、
// `emoji=1&badge=1` だと ModeEmoji に落ちて badge が黙って無視される。
// entity.BadgeEmojiProxyURL は upstream に合わせて badge だけを付けるが、
// その判断が実際に効いているかはハンドラを叩かないと分からない。
func TestHandle_BadgeEmojiIsGrayscale96(t *testing.T) {
	pngData := makeColorPNG(t)
	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngData)
	}))
	defer imgServer.Close()

	cfg := &config.Config{
		URL: "https://example.com", MediaProxy: "https://example.com/proxy",
		MediaProxySecret: []byte("test-secret"),
		UserAgent:        "Misskey/2026.5.4 (https://example.com)",
	}
	svc := mediaproxy.NewService(cfg.URL, cfg.UserAgent,
		&mockStorage{files: map[string][]byte{}},
		&mockAllowlist{allowed: map[string]bool{imgServer.URL + "/a.png": true}},
		cfg.MediaProxySecret, testAllowedCIDRs)
	h := NewHandler(svc, cfg)
	e := echo.New()

	// entity.BadgeEmojiProxyURL が出す形 (emoji.png + badge のみ)。
	rec := doRequest(e, h, http.MethodGet, "/proxy/emoji.png?url="+imgServer.URL+"/a.png&badge=1",
		map[string]string{"User-Agent": "TestBrowser/1.0"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"),
		"badge は PNG で返る")

	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	require.NoError(t, err)
	assert.Equal(t, 96, img.Bounds().Dx(), "badge は 96x96")
	assert.Equal(t, 96, img.Bounds().Dy(), "badge は 96x96")
	r, g, b, _ := img.At(48, 48).RGBA()
	assert.True(t, r == g && g == b,
		"badge はグレースケールでなければならない (got r=%d g=%d b=%d)", r, g, b)

	// **emoji=1 を足すと badge が奪われる。** BadgeEmojiProxyURL が
	// emoji=1 を付けてはいけない理由をここで固定する。
	rec = doRequest(e, h, http.MethodGet, "/proxy/emoji.png?url="+imgServer.URL+"/a.png&emoji=1&badge=1",
		map[string]string{"User-Agent": "TestBrowser/1.0"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotEqual(t, "image/png", rec.Header().Get("Content-Type"),
		"emoji=1 が同居すると parseMode が ModeEmoji を返し、badge にならない")
}
