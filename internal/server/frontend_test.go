package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// frontendHTML が upstream `_splash.tsx` 互換の splash markup を返すこと。
// upstream 互換要素 (#xxx):
//   - id="splash" wrapper
//   - id="splashIcon" な img 要素
//   - id="splashSpinner" + 2 spinner SVG (bg / fg)
//   - default img src は /static-assets/splash.png (meta.iconUrl 未設定時)
//   - meta.iconUrl が設定されていればそちらを使う
//   - ai.png mascot は splash には使わない
func TestFrontendHTML_SplashStructure(t *testing.T) {
	cfg := &config.Config{URL: "https://example.test", Version: "0.0.1-test"}

	t.Run("default fallback to /static-assets/splash.png", func(t *testing.T) {
		repo := testutil.NewMockMetaRepository()
		handler := frontendHTML(cfg, repo, nil)

		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		require.NoError(t, handler(c))

		body := rec.Body.String()
		assert.Contains(t, body, `<div id="splash">`)
		assert.Contains(t, body, `<img id="splashIcon"`)
		assert.Contains(t, body, `src="/static-assets/splash.png"`,
			"default splash icon should be /static-assets/splash.png (Misskey logo)")
		assert.Contains(t, body, `<div id="splashSpinner">`)
		assert.Contains(t, body, `class="spinner bg"`)
		assert.Contains(t, body, `class="spinner fg"`)
		// ai.png は mascot 用、splash には出ない
		assert.NotContains(t, body, `src="/assets/ai.png"`,
			"splash should not use ai.png (mascot)")
		// 旧 placeholder の "Loading..." text も無いこと
		assert.NotContains(t, body, "<p>Loading...</p>")
	})

	t.Run("uses configured meta.iconUrl when set", func(t *testing.T) {
		repo := testutil.NewMockMetaRepository()
		customIcon := "https://example.test/files/server-icon.png"
		repo.Meta = &model.Meta{ID: "x", IconURL: &customIcon}

		handler := frontendHTML(cfg, repo, nil)
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		require.NoError(t, handler(c))

		body := rec.Body.String()
		// splash img src には server icon が反映される
		assert.True(t, strings.Contains(body, `<img id="splashIcon" src="`+customIcon+`"`),
			"splash icon should use configured meta.iconUrl")
	})

	t.Run("mascot field is not used for splash", func(t *testing.T) {
		// 旧実装は mascotImageUrl を splash に使っていたので regression guard。
		repo := testutil.NewMockMetaRepository()
		mascot := "https://example.test/custom-mascot.png"
		repo.Meta = &model.Meta{ID: "x", MascotImageURL: &mascot}

		handler := frontendHTML(cfg, repo, nil)
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		require.NoError(t, handler(c))

		body := rec.Body.String()
		// splash img には反映されないこと (mascotImageUrl は /api/meta で
		// frontend が別用途で使う field、splash のアイコンではない)。
		// strings.Index は見つからない場合 -1 を返すので、有効な offset は
		// >= 0 で check する (relative offset なので 0 もあり得る)。
		splashStart := strings.Index(body, `<img id="splashIcon"`)
		require.GreaterOrEqual(t, splashStart, 0, "splashIcon img not found in body")
		splashEnd := strings.Index(body[splashStart:], `</div>`)
		require.GreaterOrEqual(t, splashEnd, 0, "closing </div> not found after splashIcon")
		splashSection := body[splashStart : splashStart+splashEnd]
		assert.NotContains(t, splashSection, mascot,
			"mascot URL should not appear in splash img")
		// default splash.png が使われる
		assert.Contains(t, splashSection, `src="/static-assets/splash.png"`)
	})
}
