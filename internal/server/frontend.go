package server

import (
	"encoding/json"
	"fmt"
	stdhtml "html"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/meta"
	"github.com/shiroha-a/mk/internal/api/oauth"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/frontendutil"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// frontendHTML generates the HTML shell for the Misskey frontend. When a
// built asset bundle is present the HTML wires CLIENT_ENTRY for production
// mode; otherwise it falls back to the Vite dev-server path.
// proxyAccountResolver is used to populate the embedded meta JSON's
// proxyAccountName field; passing nil leaves the value as null (appropriate
// for pre-setup instances).
func frontendHTML(cfg *config.Config, metaRepo repository.MetaRepository, proxyAccountResolver meta.ProxyAccountResolver, chunkedUpload meta.ChunkedUploadCapability) echo.HandlerFunc {
	// ビルド済みアセットからCLIENT_ENTRYを取得
	clientEntry := frontendutil.DetectClientEntry()

	return func(c echo.Context) error {
		return renderFrontendShell(c, cfg, metaRepo, proxyAccountResolver, chunkedUpload, clientEntry, "")
	}
}

// renderFrontendShell renders the Misskey frontend SPA shell. extraHead is
// injected verbatim into <head> (used by the OAuth consent page to add the
// misskey:oauth:* meta tags, #1899); pass "" for the normal shell.
func renderFrontendShell(c echo.Context, cfg *config.Config, metaRepo repository.MetaRepository, proxyAccountResolver meta.ProxyAccountResolver, chunkedUpload meta.ChunkedUploadCapability, clientEntry frontendutil.ClientEntryInfo, extraHead string) error {
	instanceName := "Misskey"
	instanceDesc := ""
	iconURL := "/static-assets/icons/192.png"
	themeColor := "#86b300"
	// splash 中央のアイコンは upstream `_splash.tsx` 互換で server
	// iconUrl を使う (= 管理者が設定したインスタンス画像)。未設定なら
	// `/static-assets/splash.png` (Misskey ロゴ) にフォールバック。
	// mascotImageUrl (Ai キャラ) は別 field で splash には使わない (#993)。
	splashIconURL := "/static-assets/splash.png"
	metaJSON := "{}"
	if m, err := metaRepo.Fetch(); err == nil {
		if m.Name != nil && *m.Name != "" {
			instanceName = *m.Name
		}
		if m.Description != nil {
			instanceDesc = *m.Description
		}
		if m.IconURL != nil && *m.IconURL != "" {
			iconURL = *m.IconURL
			splashIconURL = *m.IconURL
		}
		if m.ThemeColor != nil && *m.ThemeColor != "" {
			themeColor = *m.ThemeColor
		}
		metaJSON = buildMetaJSON(cfg, m, proxyAccountResolver, chunkedUpload)
	}

	// CLIENT_ENTRYの設定
	clientEntryJS := "null"
	viteClientTag := `<script type="module" src="/vite/@vite/client"></script>`
	cssLinkTags := ""
	if clientEntry.Script != "" {
		clientEntryJS = fmt.Sprintf("'%s'", clientEntry.Script)
		viteClientTag = "" // production ではVite clientは不要
		// Vite manifest の CSS 依存を <link> タグとして挿入する。
		// これがないとエントリの CSS (100KB+) が読み込まれずスタイル崩れになる。
		for _, css := range clientEntry.CSS {
			cssLinkTags += fmt.Sprintf(`<link rel="stylesheet" href="/vite/%s">`, css) + "\n"
		}
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html><head>
<meta charset="UTF-8">
<meta name="application-name" content="Misskey">
<meta name="referer" content="origin">
<meta property="og:type" content="website">
<meta property="og:site_name" content="%s">
<meta property="og:title" content="%s">
<meta property="og:description" content="%s">
<meta property="og:image" content="%s">
<meta property="og:url" content="%s">
<meta name="twitter:card" content="summary">
<meta name="theme-color" content="%s">
<meta property="instance_url" content="%s">
<meta name="viewport" content="width=device-width, initial-scale=1, minimum-scale=1, maximum-scale=1, user-scalable=no, viewport-fit=cover">
<title>%s</title>
<link rel="icon" href="%s">
<link rel="manifest" href="/manifest.json">
%s
%s
%s
<link rel="stylesheet" href="/vite/loader/style.css">
<script>const VERSION = '%s'; const CLIENT_ENTRY = %s; const LANGS = ["ja-JP","en-US"];</script>
<script type="application/json" id="misskey_meta" data-generated-at="%d">%s</script>
<script src="/vite/loader/boot.js"></script>
</head><body>
<noscript><p>Please turn on your JavaScript</p></noscript>
<div id="splash">
<img id="splashIcon" src="%s" />
<div id="splashSpinner">
<svg class="spinner bg" viewBox="0 0 152 152" xmlns="http://www.w3.org/2000/svg"><g transform="matrix(1,0,0,1,12,12)"><circle cx="64" cy="64" r="64" style="fill:none;stroke:currentColor;stroke-width:24px;"/></g></svg>
<svg class="spinner fg" viewBox="0 0 152 152" xmlns="http://www.w3.org/2000/svg"><g transform="matrix(1,0,0,1,12,12)"><path d="M128,64C128,28.654 99.346,0 64,0C99.346,0 128,28.654 128,64Z" style="fill:none;stroke:currentColor;stroke-width:24px;"/></g></svg>
</div>
</div>
</body></html>`, instanceName, instanceName, instanceDesc, iconURL, cfg.URL,
		themeColor, cfg.URL, instanceName, iconURL, extraHead, viteClientTag, cssLinkTags,
		cfg.Version, clientEntryJS,
		time.Now().UnixMilli(), metaJSON, splashIconURL)

	// SPA shell にだけ CSP を付ける (#2425)。shell を返す経路は catch-all と
	// AP の non-AP fallback の 2 つで、どちらもこの関数を通るので path 判定が
	// 要らない。API / アセットに誤って付くこともない。
	applyFrontendCSP(c, cfg.FrontendContentSecurityPolicy)

	return c.HTML(http.StatusOK, html)
}

// frontendConsentHTML returns an oauth.ConsentRenderer that serves the frontend
// SPA shell with the misskey:oauth:* meta tags injected, so the frontend's
// OAuth component can render the authorization prompt (#1899). Values are
// HTML-escaped (client name/logo are attacker-suppliable via discovery).
func frontendConsentHTML(cfg *config.Config, metaRepo repository.MetaRepository, proxyAccountResolver meta.ProxyAccountResolver, chunkedUpload meta.ChunkedUploadCapability) oauth.ConsentRenderer {
	clientEntry := frontendutil.DetectClientEntry()
	return func(c echo.Context, m oauth.ConsentMeta) error {
		var sb strings.Builder
		sb.WriteString(`<meta name="misskey:oauth:transaction-id" content="` + stdhtml.EscapeString(m.TransactionID) + `">` + "\n")
		sb.WriteString(`<meta name="misskey:oauth:client-name" content="` + stdhtml.EscapeString(m.ClientName) + `">` + "\n")
		if m.ClientLogo != "" {
			sb.WriteString(`<meta name="misskey:oauth:client-logo" content="` + stdhtml.EscapeString(m.ClientLogo) + `">` + "\n")
		}
		sb.WriteString(`<meta name="misskey:oauth:scope" content="` + stdhtml.EscapeString(m.Scope) + `">`)
		return renderFrontendShell(c, cfg, metaRepo, proxyAccountResolver, chunkedUpload, clientEntry, sb.String())
	}
}

// buildMetaJSON constructs the /api/meta equivalent JSON for inline embedding.
// /api/meta ハンドラ (meta/handler.go) と完全に同じフィールドを返す。
// フロントエンドはこのJSONを先に読んで /api/meta の呼び出しを省略する。
func buildMetaJSON(cfg *config.Config, m *model.Meta, proxyAccountResolver meta.ProxyAccountResolver, chunkedUpload meta.ChunkedUploadCapability) string {
	// mascotImageUrl フォールバック
	mascot := "/assets/ai.png"
	if m.MascotImageURL != nil && *m.MascotImageURL != "" {
		mascot = *m.MascotImageURL
	}
	// translatorAvailable
	translatorAvailable := m.DeeplAuthKey != nil && *m.DeeplAuthKey != ""

	// SSR 埋め込み meta の policies は upstream HtmlTemplateService と同じく
	// packDetailed 相当 (= DEFAULT_POLICIES に instance.policies を上書き) に
	// する。DefaultPolicies() 固定だと admin/roles/update-default-policies や
	// update-meta の変更が client に一切反映されない。frontend の instance.ts は
	// data-generated-at が localStorage の instanceCachedAt より新しいと SSR 値で
	// cache を上書きし、以後 1 時間 /api/meta を再取得しないため、誤った policies
	// が恒久的に居座る。
	mergedPolicies := role.MergeMetaPolicies(m.Policies)

	resp := map[string]any{
		"maintainerName":  m.MaintainerName,
		"maintainerEmail": m.MaintainerEmail,
		"version":         cfg.Version,
		// /api/meta と同じく mk-go の実装バージョンを additive に出す (#2274)。
		// SSR 埋め込みにも載せることで、about 系ページが fetchInstance を
		// 待たずに表示できる。
		"mkGoVersion":                  config.MkGoVersion,
		"name":                         m.Name,
		"shortName":                    m.ShortName,
		"uri":                          cfg.URL,
		"description":                  m.Description,
		"langs":                        m.Langs,
		"disableRegistration":          m.DisableRegistration,
		"emailRequiredForSignup":       m.EmailRequiredForSignup,
		"enableHcaptcha":               m.EnableHcaptcha,
		"hcaptchaSiteKey":              m.HcaptchaSiteKey,
		"enableRecaptcha":              m.EnableRecaptcha,
		"recaptchaSiteKey":             m.RecaptchaSiteKey,
		"enableTurnstile":              m.EnableTurnstile,
		"turnstileSiteKey":             m.TurnstileSiteKey,
		"themeColor":                   m.ThemeColor,
		"bannerUrl":                    m.BannerURL,
		"backgroundImageUrl":           m.BackgroundImageURL,
		"logoImageUrl":                 m.LogoImageURL,
		"iconUrl":                      m.IconURL,
		"cacheRemoteFiles":             m.CacheRemoteFiles,
		"enableServiceWorker":          m.EnableServiceWorker,
		"swPublickey":                  m.SwPublicKey,
		"serverRules":                  m.ServerRules,
		"maxNoteTextLength":            3000,
		"tosUrl":                       m.TermsOfServiceURL,
		"repositoryUrl":                m.RepositoryURL,
		"feedbackUrl":                  m.FeedbackURL,
		"impressumUrl":                 m.ImpressumURL,
		"privacyPolicyUrl":             m.PrivacyPolicyURL,
		"inquiryUrl":                   m.InquiryURL,
		"federation":                   m.Federation,
		"defaultLightTheme":            m.DefaultLightTheme,
		"defaultDarkTheme":             m.DefaultDarkTheme,
		"serverErrorImageUrl":          m.ServerErrorImageURL,
		"notFoundImageUrl":             m.NotFoundImageURL,
		"infoImageUrl":                 m.InfoImageURL,
		"app192IconUrl":                m.App192IconURL,
		"app512IconUrl":                m.App512IconURL,
		"mascotImageUrl":               mascot,
		"translatorAvailable":          translatorAvailable,
		"enableEmail":                  m.EnableEmail,
		"enableUrlPreview":             m.URLPreviewEnabled,
		"ads":                          []any{},
		"notesPerOneAd":                m.NotesPerOneAd,
		"mediaProxy":                   cfg.MediaProxy,
		"cacheRemoteSensitiveFiles":    m.CacheRemoteSensitiveFiles,
		"requireSetup":                 m.RootUserID == nil,
		"singleUserMode":               m.SingleUserMode,
		"providesTarball":              cfg.PublishTarballInsteadOfProvideRepositoryUrl,
		"maxFileSize":                  cfg.MaxFileSize,
		"proxyAccountName":             resolveProxyAccountNameForSSR(proxyAccountResolver),
		"noteSearchableScope":          meta.NoteSearchableScope(cfg.FulltextSearch, cfg.Meilisearch),
		"enableMcaptcha":               m.EnableMcaptcha,
		"mcaptchaSiteKey":              m.McaptchaSiteKey,
		"mcaptchaInstanceUrl":          m.McaptchaInstanceURL,
		"enableTestcaptcha":            m.EnableTestcaptcha,
		"sentryForFrontend":            sentryForFrontendForSSR(cfg.SentryForFrontend),
		"googleAnalyticsMeasurementId": m.GoogleAnalyticsMeasurementID,
		"clientOptions":                clientOptionsJSON(m.ClientOptions),
		"policies":                     mergedPolicies,
		"features": map[string]any{
			"registration":           !m.DisableRegistration,
			"emailRequiredForSignup": m.EmailRequiredForSignup,
			"localTimeline":          meta.PolicyBool(mergedPolicies, "ltlAvailable"),
			"globalTimeline":         meta.PolicyBool(mergedPolicies, "gtlAvailable"),
			"hcaptcha":               m.EnableHcaptcha,
			"recaptcha":              m.EnableRecaptcha,
			"turnstile":              m.EnableTurnstile,
			"objectStorage":          m.UseObjectStorage,
			"serviceWorker":          m.EnableServiceWorker,
			"miauth":                 true,
		},
	}
	// 分割アップロードの能力告知 (#2313)。frontend の instance.ts は SSR 埋め込みを
	// localStorage cache より優先し、以後 1 時間 /api/meta を再取得しない。ここに
	// 載せ忘れると、admin が有効にしても client は最大 1 時間 従来の単発
	// アップロードに倒れ続ける (100MB 超はリバースプロキシで弾かれる)。
	// 判定関数は /api/meta と同じものを配線して値が食い違わないようにする。
	if chunkedUpload != nil {
		if chunkSize, ok := chunkedUpload(); ok {
			resp["chunkedUpload"] = map[string]any{"chunkSize": chunkSize}
		}
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// newProxyAccountResolver returns a ProxyAccountResolver that looks up the
// system_account with type='proxy' and resolves the corresponding username.
// Read-only: if the row does not exist yet (_, false) is returned.
// 本家TSのsystemAccountService.fetchは未存在時に自動作成するが、/api/metaの
// 副作用としてシステムアカウントが勝手に生成されるのを避けるため、Go側は
// 明示セットアップ経路 (admin系エンドポイント) に生成を寄せている。
func newProxyAccountResolver(saRepo repository.SystemAccountRepository, userRepo repository.UserRepository) meta.ProxyAccountResolver {
	return func() (string, bool) {
		sa, err := saRepo.FindByType("proxy")
		if err != nil || sa == nil {
			return "", false
		}
		user, err := userRepo.FindByID(sa.UserID)
		if err != nil || user == nil {
			return "", false
		}
		return user.Username, true
	}
}

// resolveProxyAccountNameForSSR resolves the proxy account name for the
// SSR-embedded meta JSON. Mirrors the behavior of resolveProxyAccountName
// in the meta package; returns nil when the resolver is absent or lookup
// fails so the JSON field serializes as null.
func resolveProxyAccountNameForSSR(r meta.ProxyAccountResolver) any {
	if r == nil {
		return nil
	}
	name, ok := r()
	if !ok {
		return nil
	}
	return name
}

// sentryForFrontendForSSR normalizes config.SentryForFrontend for SSR embed.
// Empty map == null (TS: `?? null`).
func sentryForFrontendForSSR(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	return m
}

// clientOptionsJSON normalizes a jsonb byte slice into map[string]any.
func clientOptionsJSON(raw []byte) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// manifestJSON generates a PWA manifest.json response.
// TSバックエンドの manifestHandler と同等。meta.app192IconUrl /
// app512IconUrl が設定されていれば PWA icons に反映し、
// meta.manifestJsonOverride が valid JSON object なら最後に deep merge する。
func manifestJSON(cfg *config.Config, metaRepo repository.MetaRepository) echo.HandlerFunc {
	return func(c echo.Context) error {
		m, _ := metaRepo.Fetch()
		name := cfg.URL
		shortName := cfg.URL
		themeColor := "#86b300"
		icon192 := "/static-assets/icons/192.png"
		icon512 := "/static-assets/icons/512.png"
		if m != nil {
			if m.Name != nil && *m.Name != "" {
				name = *m.Name
			}
			if m.ShortName != nil && *m.ShortName != "" {
				shortName = *m.ShortName
			} else if m.Name != nil && *m.Name != "" {
				shortName = *m.Name
			}
			if m.ThemeColor != nil && *m.ThemeColor != "" {
				themeColor = *m.ThemeColor
			}
			// meta の app icon URL が設定されていればそちらを優先
			if m.App192IconURL != nil && *m.App192IconURL != "" {
				icon192 = *m.App192IconURL
			}
			if m.App512IconURL != nil && *m.App512IconURL != "" {
				icon512 = *m.App512IconURL
			}
		}
		manifest := map[string]any{
			"short_name":       shortName,
			"name":             name,
			"start_url":        "/",
			"display":          "standalone",
			"background_color": "#313a42",
			"theme_color":      themeColor,
			"icons": []map[string]any{
				{"src": icon192, "sizes": "192x192", "type": "image/png", "purpose": "maskable"},
				{"src": icon512, "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
				{"src": "/static-assets/splash.png", "sizes": "300x300", "type": "image/png"},
			},
			"share_target": map[string]any{
				"action":  "/share/",
				"method":  "GET",
				"enctype": "application/x-www-form-urlencoded",
				"params": map[string]any{
					"title": "title",
					"text":  "text",
					"url":   "url",
				},
			},
		}
		// manifestJsonOverride: meta テーブルに保存された JSON を最後に重ね書き。
		// 不正な JSON は warn を出してスキップ ('{}' デフォルトはノーオペ)。
		if m != nil && m.ManifestJSONOverride != "" && m.ManifestJSONOverride != "{}" {
			var override map[string]any
			if err := json.Unmarshal([]byte(m.ManifestJSONOverride), &override); err != nil {
				slog.Warn("manifest.json override is not valid JSON, ignoring", "err", err)
			} else {
				deepMergeManifest(manifest, override)
			}
		}
		c.Response().Header().Set("Cache-Control", "max-age=300")
		return c.JSON(http.StatusOK, manifest)
	}
}

// deepMergeManifest merges src on top of dst in-place.
// マップは再帰的に merge し、それ以外 (配列・スカラー) は src で上書きする。
// PWA manifest はネスト浅いので深さ無制限の単純実装で十分。
func deepMergeManifest(dst, src map[string]any) {
	for k, v := range src {
		if existing, ok := dst[k]; ok {
			if dstMap, ok1 := existing.(map[string]any); ok1 {
				if srcMap, ok2 := v.(map[string]any); ok2 {
					deepMergeManifest(dstMap, srcMap)
					continue
				}
			}
		}
		dst[k] = v
	}
}

// newViteProxy creates a reverse proxy handler that forwards requests to
// the Vite dev server. フロントエンドの開発サーバーへのプロキシ。
func newViteProxy(target string) echo.HandlerFunc {
	remote, err := url.Parse(target)
	if err != nil {
		panic("invalid vite proxy target: " + target)
	}
	proxy := httputil.NewSingleHostReverseProxy(remote)
	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)
		req.Host = remote.Host
	}

	return func(c echo.Context) error {
		proxy.ServeHTTP(c.Response(), c.Request())
		return nil
	}
}
