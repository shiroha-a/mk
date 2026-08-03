package server

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
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
		handler := frontendHTML(cfg, repo, nil, nil)

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

		handler := frontendHTML(cfg, repo, nil, nil)
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

		handler := frontendHTML(cfg, repo, nil, nil)
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

// SSR 埋め込み meta (`#misskey_meta`) の policies が
// `{...DEFAULT_POLICIES, ...meta.policies}` になること。
//
// upstream HtmlTemplateService は metaEntityService.packDetailed(meta) を
// そのまま埋め込むため instance.policies の上書きが反映される。mk-go は
// 以前 role.DefaultPolicies() 固定を埋めていたため、
// admin/roles/update-default-policies や update-meta で policy を変えても
// client 側の instance.policies に一切反映されなかった。frontend の
// instance.ts は data-generated-at が localStorage の instanceCachedAt より
// 新しいと SSR 値で cache を上書きし以後 1 時間 /api/meta を叩かないので、
// 誤った policies が恒久的に居座る。
func TestFrontendHTML_EmbeddedMetaPolicies(t *testing.T) {
	cfg := &config.Config{URL: "https://example.test", Version: "0.0.1-test"}

	extractMeta := func(t *testing.T, body string) map[string]any {
		t.Helper()
		const openTag = `id="misskey_meta"`
		idx := strings.Index(body, openTag)
		require.NotEqual(t, -1, idx, "misskey_meta script should be embedded")
		start := strings.Index(body[idx:], ">")
		require.NotEqual(t, -1, start)
		start += idx + 1
		end := strings.Index(body[start:], "</script>")
		require.NotEqual(t, -1, end)

		var parsed map[string]any
		require.NoError(t, json.Unmarshal([]byte(body[start:start+end]), &parsed))
		return parsed
	}

	render := func(t *testing.T, m *model.Meta) map[string]any {
		t.Helper()
		repo := testutil.NewMockMetaRepository()
		repo.Meta = m
		handler := frontendHTML(cfg, repo, nil, nil)

		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		require.NoError(t, handler(c))
		return extractMeta(t, rec.Body.String())
	}

	t.Run("no override falls back to default policies", func(t *testing.T) {
		parsed := render(t, &model.Meta{ID: "x"})

		policies, ok := parsed["policies"].(map[string]any)
		require.True(t, ok, "policies should be an object")
		assert.Equal(t, false, policies["canSearchNotes"])
		assert.Equal(t, true, policies["ltlAvailable"])

		features, ok := parsed["features"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, true, features["localTimeline"])
		assert.Equal(t, true, features["globalTimeline"])
	})

	// SSR 埋め込み meta も /api/meta と同じく version = 互換 Misskey 版、
	// mkGoVersion = mk-go の実装版で出すこと (#2274)。about 系ページが
	// fetchInstance を待たずに実装版を表示できるようにするため。
	t.Run("exposes mkGoVersion alongside the compatible Misskey version", func(t *testing.T) {
		parsed := render(t, &model.Meta{ID: "x"})

		assert.Equal(t, cfg.Version, parsed["version"], "version は互換 Misskey 版")
		assert.Equal(t, config.MkGoVersion, parsed["mkGoVersion"], "mkGoVersion は mk-go の実装版")
	})

	t.Run("meta.policies override is reflected", func(t *testing.T) {
		parsed := render(t, &model.Meta{
			ID:       "x",
			Policies: []byte(`{"canSearchNotes":true,"ltlAvailable":false,"gtlAvailable":false}`),
		})

		policies, ok := parsed["policies"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, true, policies["canSearchNotes"], "admin override should win")
		assert.Equal(t, false, policies["ltlAvailable"])
		// 上書きしていない key は default のまま残る
		assert.Equal(t, true, policies["canPublicNote"])

		features, ok := parsed["features"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, false, features["localTimeline"])
		assert.Equal(t, false, features["globalTimeline"])
	})
}

// #2313 / #2314: SSR 埋め込み meta にも分割アップロードの能力告知を載せる。
// frontend の instance.ts は SSR 埋め込みを localStorage cache より優先し、以後
// 1 時間 /api/meta を再取得しない。ここに載せ忘れると、admin が有効にしても
// client は最大 1 時間 従来の単発アップロードに倒れ続け、100MB 超が
// リバースプロキシで弾かれる。
func TestFrontendHTML_EmbedsChunkedUploadCapability(t *testing.T) {
	cfg := &config.Config{URL: "https://example.com", Version: "2026.7.0"}
	repo := testutil.NewMockMetaRepository()
	repo.Meta = &model.Meta{ID: "x"}

	extract := func(t *testing.T, handler echo.HandlerFunc) map[string]any {
		t.Helper()
		e := echo.New()
		rec := httptest.NewRecorder()
		require.NoError(t, handler(e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)))
		m := regexp.MustCompile(`(?s)id="misskey_meta"[^>]*>(.*?)</script>`).FindStringSubmatch(rec.Body.String())
		require.Len(t, m, 2, "misskey_meta element must be present")
		var out map[string]any
		require.NoError(t, json.Unmarshal([]byte(html.UnescapeString(m[1])), &out))
		return out
	}

	// 未対応構成では field ごと出さない (純正 Misskey と同じ undefined)。
	got := extract(t, frontendHTML(cfg, repo, nil, func() (int64, bool) { return 0, false }))
	_, present := got["chunkedUpload"]
	assert.False(t, present)

	// 未配線でも落ちない。
	got = extract(t, frontendHTML(cfg, repo, nil, nil))
	_, present = got["chunkedUpload"]
	assert.False(t, present)

	// 利用可能なら /api/meta と同じ shape で出す。
	got = extract(t, frontendHTML(cfg, repo, nil, func() (int64, bool) { return 10 * 1024 * 1024, true }))
	require.NotNil(t, got["chunkedUpload"])
	assert.Equal(t, float64(10*1024*1024), got["chunkedUpload"].(map[string]any)["chunkSize"])
}
