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

// `<head>` の icon link 群が upstream `views/base.tsx` と同じ値を返すこと。
//
// 特に `<link rel="apple-touch-icon">` が無いと、iOS Safari は manifest の
// icons にフォールバックする。192/512 は `purpose: "maskable"` なので候補から
// 外れ、透明背景の splash.png がホーム画面アイコンに選ばれてしまい、純正
// Misskey と見た目が変わる (#2527)。
func TestFrontendHTML_IconLinks(t *testing.T) {
	cfg := &config.Config{URL: "https://example.test", Version: "0.0.1-test"}

	render := func(t *testing.T, m *model.Meta) string {
		t.Helper()
		repo := testutil.NewMockMetaRepository()
		if m != nil {
			repo.Meta = m
		}
		handler := frontendHTML(cfg, repo, nil, nil)
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		require.NoError(t, handler(c))
		return rec.Body.String()
	}

	t.Run("defaults match upstream base.tsx", func(t *testing.T) {
		body := render(t, nil)

		assert.Contains(t, body, `<link rel="icon" href="/favicon.ico">`,
			"upstream の fallback は /favicon.ico")
		assert.Contains(t, body, `<link rel="apple-touch-icon" href="/apple-touch-icon.png">`,
			"apple-touch-icon が無いと iOS が manifest 側の splash.png を拾う")
	})

	t.Run("uses meta.iconUrl for favicon", func(t *testing.T) {
		icon := "https://example.test/files/server-icon.png"
		body := render(t, &model.Meta{ID: "x", IconURL: &icon})

		assert.Contains(t, body, `<link rel="icon" href="`+icon+`">`)
		// app512IconUrl 未設定なら apple-touch-icon は default のまま
		// (upstream も iconUrl は apple-touch-icon に流用しない)。
		assert.Contains(t, body, `<link rel="apple-touch-icon" href="/apple-touch-icon.png">`)
	})

	t.Run("uses meta.app512IconUrl for apple-touch-icon", func(t *testing.T) {
		appIcon := "https://example.test/files/app-512.png"
		body := render(t, &model.Meta{ID: "x", App512IconURL: &appIcon})

		assert.Contains(t, body, `<link rel="apple-touch-icon" href="`+appIcon+`">`)
		// favicon 側は iconUrl 由来なので巻き込まれない
		assert.Contains(t, body, `<link rel="icon" href="/favicon.ico">`)
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

// 外部 media proxy 構成では、その origin を CSP に足す (#2501)。リモート画像も
// カスタム絵文字も proxy の origin から配信されるため、無いと enforce で全滅する。
// internal proxy ('self') 構成では何も足さない。
func TestFrontendHTML_ExternalMediaProxyOriginInCSP(t *testing.T) {
	serve := func(cfg *config.Config) string {
		t.Helper()
		handler := frontendHTML(cfg, testutil.NewMockMetaRepository(), nil, nil)
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		require.NoError(t, handler(c))
		return rec.Header().Get("Content-Security-Policy")
	}

	t.Run("external proxy origin appears in media directives", func(t *testing.T) {
		csp := serve(&config.Config{
			URL: "https://example.test", Version: "0.0.1-test",
			FrontendContentSecurityPolicy: CSPModeEnforce,
			ExternalMediaProxyEnabled:     true,
			MediaProxy:                    "https://proxy.example/proxy",
		})
		require.NotEmpty(t, csp)
		for _, d := range strings.Split(csp, "; ") {
			name, _, _ := strings.Cut(d, " ")
			switch name {
			case "img-src", "media-src", "connect-src":
				assert.Containsf(t, d, "https://proxy.example", "%s に proxy origin が要る", name)
			case "script-src":
				// 画像を配るだけの host にスクリプト実行の権限を与えない。
				assert.NotContains(t, d, "https://proxy.example")
			}
		}
	})

	t.Run("internal proxy adds nothing", func(t *testing.T) {
		csp := serve(&config.Config{
			URL: "https://example.test", Version: "0.0.1-test",
			FrontendContentSecurityPolicy: CSPModeEnforce,
			MediaProxy:                    "https://example.test/proxy",
		})
		require.NotEmpty(t, csp)
		assert.NotContains(t, csp, "proxy.example")
	})
}

// 有効な captcha 業者の origin が CSP に載る (#2502)。無いと enforce で
// captcha の script が読めずサインアップが壊れる。無効の業者は載せない。
func TestFrontendHTML_CaptchaOriginsInCSP(t *testing.T) {
	cfg := &config.Config{
		URL: "https://example.test", Version: "0.0.1-test",
		FrontendContentSecurityPolicy: CSPModeEnforce,
	}
	serve := func(meta *model.Meta) string {
		t.Helper()
		repo := testutil.NewMockMetaRepository()
		if meta != nil {
			repo.Meta = meta
		}
		handler := frontendHTML(cfg, repo, nil, nil)
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		require.NoError(t, handler(c))
		return rec.Header().Get("Content-Security-Policy")
	}

	t.Run("turnstile 有効で origin が載る", func(t *testing.T) {
		csp := serve(&model.Meta{ID: "x", EnableTurnstile: true})
		require.NotEmpty(t, csp)
		for _, d := range strings.Split(csp, "; ") {
			name, _, _ := strings.Cut(d, " ")
			switch name {
			case "script-src":
				assert.Contains(t, d, "https://challenges.cloudflare.com")
			case "connect-src":
				// 公式の要求は script / frame のみ。
				assert.NotContains(t, d, "cloudflare")
			}
		}
		assert.NotContains(t, csp, "hcaptcha", "無効の業者は載せない")
	})

	// hcaptcha 単独のケースも見る。captchaCSPExtras の引数は positional な
	// bool 3 連なので、取り違えると「有効にした業者と違う origin が載る」。
	// turnstile だけの検証では第 1・第 2 引数の入れ替えを検出できない。
	t.Run("hcaptcha 有効でその origin だけが載る", func(t *testing.T) {
		csp := serve(&model.Meta{ID: "x", EnableHcaptcha: true})
		require.NotEmpty(t, csp)
		assert.Contains(t, csp, "https://*.hcaptcha.com")
		assert.NotContains(t, csp, "recaptcha")
		assert.NotContains(t, csp, "cloudflare")
	})

	t.Run("全て無効なら載らない", func(t *testing.T) {
		csp := serve(&model.Meta{ID: "x"})
		require.NotEmpty(t, csp)
		assert.NotContains(t, csp, "cloudflare")
		assert.NotContains(t, csp, "hcaptcha")
		assert.NotContains(t, csp, "recaptcha")
	})
}

// PWA manifest が upstream `ClientServerService.manifestHandler` と同じ形を
// 返すこと。icons は PWA のホーム画面アイコンそのものなので、192/512 の
// maskable と splash の any の組み合わせが崩れると純正と見た目が変わる。
func TestManifestJSON(t *testing.T) {
	cfg := &config.Config{URL: "https://example.test", Version: "0.0.1-test"}

	serve := func(t *testing.T, m *model.Meta) map[string]any {
		t.Helper()
		repo := testutil.NewMockMetaRepository()
		if m != nil {
			repo.Meta = m
		}
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/manifest.json", nil), rec)
		require.NoError(t, manifestJSON(cfg, repo)(c))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "max-age=300", rec.Header().Get("Cache-Control"))

		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}

	iconsOf := func(t *testing.T, manifest map[string]any) []map[string]any {
		t.Helper()
		raw, ok := manifest["icons"].([]any)
		require.True(t, ok, "icons should be an array")
		out := make([]map[string]any, 0, len(raw))
		for _, i := range raw {
			icon, ok := i.(map[string]any)
			require.True(t, ok)
			out = append(out, icon)
		}
		return out
	}

	t.Run("defaults match upstream", func(t *testing.T) {
		manifest := serve(t, nil)

		assert.Equal(t, "/", manifest["start_url"])
		assert.Equal(t, "standalone", manifest["display"])
		assert.Equal(t, "#313a42", manifest["background_color"])
		assert.Equal(t, "#86b300", manifest["theme_color"])

		icons := iconsOf(t, manifest)
		require.Len(t, icons, 3)
		assert.Equal(t, "/static-assets/icons/192.png", icons[0]["src"])
		assert.Equal(t, "192x192", icons[0]["sizes"])
		assert.Equal(t, "maskable", icons[0]["purpose"])
		assert.Equal(t, "/static-assets/icons/512.png", icons[1]["src"])
		assert.Equal(t, "512x512", icons[1]["sizes"])
		assert.Equal(t, "maskable", icons[1]["purpose"])
		assert.Equal(t, "/static-assets/splash.png", icons[2]["src"])
		assert.Equal(t, "300x300", icons[2]["sizes"])
		// maskable しか無いと iOS が候補を見つけられないので any が要る。
		assert.Equal(t, "any", icons[2]["purpose"])

		shortcuts, ok := manifest["shortcuts"].([]any)
		require.True(t, ok, "upstream は safemode ショートカットを持つ")
		require.Len(t, shortcuts, 1)
		assert.Equal(t, map[string]any{"name": "Safemode", "url": "/?safemode=true"}, shortcuts[0])

		shareTarget, ok := manifest["share_target"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "/share/", shareTarget["action"])
		assert.Equal(t, "GET", shareTarget["method"])
	})

	t.Run("reflects meta app icon urls", func(t *testing.T) {
		icon192 := "https://example.test/files/app-192.png"
		icon512 := "https://example.test/files/app-512.png"
		manifest := serve(t, &model.Meta{ID: "x", App192IconURL: &icon192, App512IconURL: &icon512})

		icons := iconsOf(t, manifest)
		require.Len(t, icons, 3)
		assert.Equal(t, icon192, icons[0]["src"])
		assert.Equal(t, icon512, icons[1]["src"])
		// splash は meta で差し替えられない (upstream も固定)。
		assert.Equal(t, "/static-assets/splash.png", icons[2]["src"])
	})

	t.Run("reflects name / shortName / themeColor", func(t *testing.T) {
		name := "テストサーバー"
		short := "テスト"
		color := "#00eb91"
		manifest := serve(t, &model.Meta{ID: "x", Name: &name, ShortName: &short, ThemeColor: &color})

		assert.Equal(t, name, manifest["name"])
		assert.Equal(t, short, manifest["short_name"])
		assert.Equal(t, color, manifest["theme_color"])
	})

	t.Run("shortName 未設定なら name にフォールバック", func(t *testing.T) {
		name := "テストサーバー"
		manifest := serve(t, &model.Meta{ID: "x", Name: &name})

		assert.Equal(t, name, manifest["short_name"])
	})

	t.Run("manifestJsonOverride が最後に重なる", func(t *testing.T) {
		manifest := serve(t, &model.Meta{
			ID:                   "x",
			ManifestJSONOverride: `{"name":"上書き","icons":[{"src":"/custom.png","sizes":"192x192"}]}`,
		})

		assert.Equal(t, "上書き", manifest["name"])
		icons := iconsOf(t, manifest)
		require.Len(t, icons, 1, "配列は merge ではなく置換")
		assert.Equal(t, "/custom.png", icons[0]["src"])
	})

	t.Run("不正な override は無視する", func(t *testing.T) {
		manifest := serve(t, &model.Meta{ID: "x", ManifestJSONOverride: `{ not json`})

		// default が保たれる (override を落としても manifest 自体は壊さない)
		assert.Len(t, iconsOf(t, manifest), 3)
	})
}
