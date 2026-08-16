package server

import (
	"encoding/json"
	"fmt"
	stdhtml "html"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
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

// defaultInstanceDescription mirrors upstream `views/_.ts` の defaultDescription.
// meta.description が空のときの og:description / description に使う。
const defaultInstanceDescription = "✨🌎✨ A interplanetary communication platform ✨🚀✨"

// sameOriginURL reports whether u is served from the instance's own origin.
//
// 相対パスと自 origin の絶対 URL だけを true にする。`//other.example/x` は
// scheme-relative で別 origin なので、`/` 始まりの判定より先に弾く。
func sameOriginURL(base, u string) bool {
	if strings.HasPrefix(u, "//") {
		return false
	}
	if strings.HasPrefix(u, "/") {
		return true
	}
	base = strings.TrimSuffix(base, "/")
	return u == base || strings.HasPrefix(u, base+"/")
}

// frontendHTML generates the HTML shell for the Misskey frontend. When a
// built asset bundle is present the HTML wires CLIENT_ENTRY for production
// mode; otherwise it falls back to the Vite dev-server path.
// proxyAccountResolver is used to populate the embedded meta JSON's
// proxyAccountName field; passing nil leaves the value as null (appropriate
// for pre-setup instances).
func frontendHTML(cfg *config.Config, metaRepo repository.MetaRepository, proxyAccountResolver meta.ProxyAccountResolver, chunkedUpload meta.ChunkedUploadCapability) echo.HandlerFunc {
	// ビルド済みアセットからCLIENT_ENTRYを取得 (dev モードでは常に dev server を見る)
	clientEntry := clientEntryFor(cfg)

	return func(c echo.Context) error {
		return renderFrontendShell(c, cfg, metaRepo, proxyAccountResolver, chunkedUpload, clientEntry, shellOverrides{})
	}
}

// shellOverrides carries the per-page values that upstream passes to base.tsx
// as props. permalink (note / user 等) はこれでインスタンス既定を差し替える。
//
// ゼロ値は「差し替え無し」= 従来の shell。
type shellOverrides struct {
	// Head is injected verbatim into <head> (upstream の metaSlot 相当)。
	// OAuth 同意画面の misskey:oauth:* もここを使う (#1899)。
	Head string
	// Title replaces the <title> text. 空ならインスタンス名。
	Title string
	// Description replaces <meta name="description"> / og:description.
	// nil は「差し替え無し」でインスタンスの description を使う。空文字は
	// upstream と同じく defaultDescription に落ちる (タグ自体は出る)。
	Description *string
	// OG is the OGP block (upstream の ogSlot 相当)。非空なら shell 側の
	// 既定 OGP を出さない。両方出すと og:title が 2 つ並び、パーサは先頭を
	// 採用するのでページ固有の値が無視される (#2527)。
	OG string
	// NoIndex emits `<meta name="robots" content="noindex">` (upstream base.tsx
	// の `props.noindex`)。検索結果に出す意味が無いページ (タグ一覧など) 用で、
	// ユーザーの noCrawle 由来のものは Head 側に入る。
	NoIndex bool
	// CacheControl overrides the shell の既定値 (`public, max-age=30`)。
	// upstream はページ種別ごとに違う値を出す (permalink 15s、reversi や
	// announcement は 3600s)。
	CacheControl string
	// RobotsTag emits `X-Robots-Tag` ヘッダー。upstream は preventAiLearning の
	// ユーザーのページで noimageai / noai を出す。HTML を解析しないクローラにも
	// 効くのが meta 版との違い。
	RobotsTag []string
}

// defaultShellCacheControl mirrors upstream の renderBase (`public, max-age=30`)。
const defaultShellCacheControl = "public, max-age=30"

// splashSpinnerSVG is the startup spinner shown while the client boots (#2549).
//
// 6 つの点が 1 つずつ外から集まってきて、揃ってから 1 回転し、また散る。
// Misskey から受け継いだ 1/4 円弧はどのサービスにもある形だったので、
// 起動時に何かを待っている状況に合う形に替えた。
//
// **回転する層 (.rig) と半径方向に動く点 (.pkt) を分けてある。** 1 つの要素で
// 両方やらせると transform が衝突して、集まる動きが回転に巻き取られる。
// 動きの定義は `packages/frontend/public/loader/style.css` 側にある。
//
// **包みの `translate` を使わず、円を viewBox 座標に直接置いている。**
// CSS の `transform-box` は既定が `view-box` なので、`transform-origin` は
// viewBox の原点から測られる。包みで平行移動すると要素のローカル座標と
// ずれ、回転軸が中心から外れて首を振る。
// 拡大率は upstream の splash と同じ viewBox 152 のままにしてある。
const splashSpinnerSVG = `<svg viewBox="0 0 152 152" xmlns="http://www.w3.org/2000/svg"><g class="rig">` +
	`<g transform="rotate(0 76 76)"><circle class="pkt p1" cx="76" cy="76" r="10"/></g>` +
	`<g transform="rotate(60 76 76)"><circle class="pkt p2" cx="76" cy="76" r="10"/></g>` +
	`<g transform="rotate(120 76 76)"><circle class="pkt p3" cx="76" cy="76" r="10"/></g>` +
	`<g transform="rotate(180 76 76)"><circle class="pkt p4" cx="76" cy="76" r="10"/></g>` +
	`<g transform="rotate(240 76 76)"><circle class="pkt p5" cx="76" cy="76" r="10"/></g>` +
	`<g transform="rotate(300 76 76)"><circle class="pkt p6" cx="76" cy="76" r="10"/></g>` +
	`</g></svg>`

// splashColorPattern matches the colors we are willing to inline into the
// splash `<style>` block.
var splashColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$`)

// splashColor returns the color to paint the startup spinner with, falling back
// to the default theme color when the configured one is not a plain hex color.
//
// **HTML escape は `<style>` の中身を守らない。** style 要素の内容はマークアップ
// として解釈されないので実体参照はそのまま残り、`}` ひとつで後続の規則に化ける。
// テーマカラーは管理者が自由に入れられる値なので、素性の分かる形だけを通す。
func splashColor(themeColor string) string {
	if splashColorPattern.MatchString(themeColor) {
		return themeColor
	}
	return "#86b300"
}

// renderFrontendShell renders the Misskey frontend SPA shell.
func renderFrontendShell(c echo.Context, cfg *config.Config, metaRepo repository.MetaRepository, proxyAccountResolver meta.ProxyAccountResolver, chunkedUpload meta.ChunkedUploadCapability, clientEntry frontendutil.ClientEntryInfo, ov shellOverrides) error {
	instanceName := "Misskey"
	// og:description の既定値は upstream views/_.ts の defaultDescription。
	// `<meta name="description">` の方は upstream と同じく meta.description が
	// null のときは**タグごと出さない**ので、有無を別に持つ。
	instanceDesc := defaultInstanceDescription
	hasInstanceDesc := false
	// og:image は upstream (ClientServerService.ts:441) と同じく banner を使う。
	// 未設定ならタグごと省略する。以前は icon を入れていたが、既定値が
	// `/static-assets/icons/192.png` という相対 URL で OGP としては解決できず、
	// そもそも upstream と別の画像になっていた。
	bannerURL := ""
	// `<link rel="icon">` と `<link rel="apple-touch-icon">` は upstream
	// base.tsx と同じ fallback を持つ (前者は /favicon.ico、後者は
	// /apple-touch-icon.png)。og:image とは別 field なので変数を分ける。
	faviconURL := "/favicon.ico"
	// iOS Safari は明示された apple-touch-icon を manifest の icons より
	// 優先する。この link が無いと manifest 側にフォールバックし、
	// purpose:maskable の 192/512 が候補から外れて splash.png (透明背景) が
	// ホーム画面アイコンに選ばれてしまう (#2527)。
	appleTouchIconURL := "/apple-touch-icon.png"
	themeColor := "#86b300"
	// splash 中央のアイコンは upstream `_splash.tsx` 互換で server
	// iconUrl を使う (= 管理者が設定したインスタンス画像)。未設定なら
	// `/static-assets/splash.png` (Misskey ロゴ) にフォールバック。
	// mascotImageUrl (Ai キャラ) は別 field で splash には使わない (#993)。
	splashIconURL := "/static-assets/splash.png"
	metaJSON := "{}"
	prefetchTags := ""
	// CSP に足す設定依存の origin (#2425 / #2501 / #2502)。drive のファイルは
	// object storage から直接配信されるので、`'self'` だけだと enforce 時に
	// 画像・動画・音声が丸ごと表示できなくなる。
	var cspExtra cspExtras
	if m, err := metaRepo.Fetch(); err == nil {
		if m.Name != nil && *m.Name != "" {
			instanceName = *m.Name
		}
		// upstream base.tsx は `props.desc != null` でタグの有無を、
		// `props.desc || defaultDescription` で中身を決める。空文字は
		// 「タグは出すが既定文言」という扱いになる。
		if m.Description != nil {
			hasInstanceDesc = true
			if *m.Description != "" {
				instanceDesc = *m.Description
			}
		}
		if m.BannerURL != nil && *m.BannerURL != "" {
			bannerURL = *m.BannerURL
		}
		if m.IconURL != nil && *m.IconURL != "" {
			splashIconURL = *m.IconURL
			faviconURL = *m.IconURL
		}
		// upstream HtmlTemplateService は appleTouchIcon に app512IconUrl を渡す。
		if m.App512IconURL != nil && *m.App512IconURL != "" {
			appleTouchIconURL = *m.App512IconURL
		}
		if m.ThemeColor != nil && *m.ThemeColor != "" {
			themeColor = *m.ThemeColor
		}
		// upstream base.tsx:52-54 の branding 画像 prefetch。upstream は meta 未設定時に
		// 外部の既定画像 (xn--931a.moe) を入れるが、mk-go は CSP を出すので外部 origin の
		// prefetch は default-src で弾かれ report を汚すだけになる。frontend 側も
		// `v-if="instance.serverErrorImageUrl"` で未設定なら描画しないため、
		// **自 origin に解決できる設定値だけ** prefetch する。
		for _, u := range []*string{m.ServerErrorImageURL, m.InfoImageURL, m.NotFoundImageURL} {
			if u == nil || *u == "" || !sameOriginURL(cfg.URL, *u) {
				continue
			}
			prefetchTags += fmt.Sprintf(`<link rel="prefetch" as="image" href="%s">`, stdhtml.EscapeString(*u)) + "\n"
		}
		metaJSON = buildMetaJSON(cfg, m, proxyAccountResolver, chunkedUpload)
		// useObjectStorage が false でも baseUrl が残っていることがあるので、
		// **両方が揃っているときだけ**許可する。使っていない host を CSP に
		// 載せる必要は無い。
		if m.UseObjectStorage && m.ObjectStorageBaseURL != nil {
			if origin := objectStorageOrigin(*m.ObjectStorageBaseURL); origin != "" {
				cspExtra.Media = append(cspExtra.Media, origin)
			}
		}
		// 有効な captcha 業者の origin (#2502)。無いと enforce で captcha の
		// script が読めずサインアップが壊れる。
		captchaEx := captchaCSPExtras(m.EnableHcaptcha, m.EnableRecaptcha, m.EnableTurnstile)
		cspExtra.Script = captchaEx.Script
		cspExtra.Connect = captchaEx.Connect
		cspExtra.Style = captchaEx.Style
	}
	// 外部 media proxy 構成では、リモート画像もカスタム絵文字も proxy の origin
	// から配信される (server-side pack とクライアント側の meta.mediaProxy の両方)。
	// internal proxy ('self') なら何も足さない (#2501)。
	if cfg.ExternalMediaProxyEnabled {
		if origin := objectStorageOrigin(cfg.MediaProxy); origin != "" {
			cspExtra.Media = append(cspExtra.Media, origin)
		}
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

	// permalink (note / user 等) は自前の OGP を持ち込む (ssr_meta.go)。
	// upstream も base.tsx の `ogSlot` をページ側の値ごと差し替える。
	suppressDefaultOG := ov.OG != ""

	// ページ側の description が渡されていればそれを使う。空文字は upstream と
	// 同じく defaultDescription に落ちる (タグ自体は出る)。
	if ov.Description != nil {
		hasInstanceDesc = true
		if *ov.Description != "" {
			instanceDesc = *ov.Description
		} else {
			instanceDesc = defaultInstanceDescription
		}
	} else if suppressDefaultOG {
		// ページ固有の description を持たない permalink では、インスタンスの
		// 説明を出すと「ノートの説明」として誤った内容が読まれる (#2527)。
		hasInstanceDesc = false
	}
	// <title> と opensearch の title だけがページ固有になる。og:site_name /
	// instance_url は upstream でもインスタンスの値のまま。
	pageTitle := instanceName
	if ov.Title != "" {
		pageTitle = ov.Title
	}

	// 条件付きで消えるタグは先に組み立てる。upstream は img / desc が null なら
	// タグ自体を出さないので、空値で属性だけ残すのは互換にならない。
	ogImageTag := ""
	if bannerURL != "" {
		ogImageTag = fmt.Sprintf(`<meta property="og:image" content="%s">`, stdhtml.EscapeString(bannerURL)) + "\n"
	}
	noindexTag := ""
	if ov.NoIndex {
		noindexTag = `<meta name="robots" content="noindex">` + "\n"
	}
	descriptionTag := ""
	if hasInstanceDesc {
		descriptionTag = fmt.Sprintf(`<meta name="description" content="%s">`, stdhtml.EscapeString(instanceDesc)) + "\n"
	}
	// instance 名・description は管理者が自由に入れられる。属性値に生で埋めると
	// `"` や改行で head が壊れる (upstream は kitajs/html が属性を自動 escape する)。
	instanceNameEsc := stdhtml.EscapeString(instanceName)
	pageTitleEsc := stdhtml.EscapeString(pageTitle)
	instanceDescEsc := stdhtml.EscapeString(instanceDesc)
	themeColorEsc := stdhtml.EscapeString(themeColor)
	baseURL := strings.TrimSuffix(cfg.URL, "/")
	baseURLEsc := stdhtml.EscapeString(baseURL)

	// upstream base.tsx の `ogSlot` に相当するブロック。両方出すと 1 ページに
	// og:title が 2 つ並び、**クローラは先頭を採用する**ため、ノートを共有しても
	// 著者名でなくインスタンス名が出ていた。og:site_name / instance_url は upstream
	// でも ogSlot の外なので常に出す。
	ogGroup := ""
	if !suppressDefaultOG {
		ogGroup = `<meta property="og:type" content="website">` + "\n" +
			fmt.Sprintf(`<meta property="og:title" content="%s">`, instanceNameEsc) + "\n" +
			fmt.Sprintf(`<meta property="og:description" content="%s">`, instanceDescEsc) + "\n" +
			ogImageTag +
			fmt.Sprintf(`<meta property="og:url" content="%s">`, baseURLEsc) + "\n" +
			// twitter:card は upstream が `property=` で出しているが、Twitter の
			// 仕様は `name=`。ここは仕様側に合わせたまま維持する。
			`<meta name="twitter:card" content="summary">` + "\n"
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html><head>
<meta charset="UTF-8">
<meta name="application-name" content="Misskey">
<meta name="referer" content="origin">
<meta property="og:site_name" content="%s">
%s%s<meta name="theme-color" content="%s">
<meta name="theme-color-orig" content="%s">
<meta property="instance_url" content="%s">
<meta name="viewport" content="width=device-width, initial-scale=1, minimum-scale=1, maximum-scale=1, user-scalable=no, viewport-fit=cover">
<meta name="format-detection" content="telephone=no,date=no,address=no,email=no,url=no">
<title>%s</title>
%s<link rel="icon" href="%s">
<link rel="apple-touch-icon" href="%s">
<link rel="manifest" href="/manifest.json">
<link rel="search" type="application/opensearchdescription+xml" title="%s" href="%s/opensearch.xml">
%s%s
%s
%s
<link rel="stylesheet" href="/vite/loader/style.css">
<style>:root{--splash-color:%s}</style>
<script>const VERSION = '%s'; const CLIENT_ENTRY = %s; const LANGS = ["ja-JP","en-US"];</script>
<script type="application/json" id="misskey_meta" data-generated-at="%d">%s</script>
<script src="/vite/loader/boot.js"></script>
</head><body>
<noscript><p>Please turn on your JavaScript</p></noscript>
<div id="splash">
<img id="splashIcon" src="%s" />
<div id="splashSpinner">%s</div>
</div>
</body></html>`,
		instanceNameEsc, ogGroup,
		descriptionTag, themeColorEsc, themeColorEsc, baseURLEsc,
		pageTitleEsc, noindexTag, stdhtml.EscapeString(faviconURL), stdhtml.EscapeString(appleTouchIconURL),
		pageTitleEsc, baseURLEsc,
		prefetchTags, ov.OG+ov.Head, viteClientTag, cssLinkTags,
		splashColor(themeColor), cfg.Version, clientEntryJS,
		time.Now().UnixMilli(), metaJSON, stdhtml.EscapeString(splashIconURL), splashSpinnerSVG)

	// SPA shell にだけ CSP を付ける (#2425)。shell を返す経路は catch-all と
	// AP の non-AP fallback の 2 つで、どちらもこの関数を通るので path 判定が
	// 要らない。API / アセットに誤って付くこともない。
	applyFrontendCSP(c, cfg.FrontendContentSecurityPolicy, cspExtra)

	cacheControl := ov.CacheControl
	if cacheControl == "" {
		cacheControl = defaultShellCacheControl
	}
	c.Response().Header().Set(echo.HeaderCacheControl, cacheControl)
	for _, v := range ov.RobotsTag {
		c.Response().Header().Add("X-Robots-Tag", v)
	}

	return c.HTML(http.StatusOK, html)
}

// frontendConsentHTML returns an oauth.ConsentRenderer that serves the frontend
// SPA shell with the misskey:oauth:* meta tags injected, so the frontend's
// OAuth component can render the authorization prompt (#1899). Values are
// HTML-escaped (client name/logo are attacker-suppliable via discovery).
func frontendConsentHTML(cfg *config.Config, metaRepo repository.MetaRepository, proxyAccountResolver meta.ProxyAccountResolver, chunkedUpload meta.ChunkedUploadCapability) oauth.ConsentRenderer {
	clientEntry := clientEntryFor(cfg)
	return func(c echo.Context, m oauth.ConsentMeta) error {
		var sb strings.Builder
		sb.WriteString(`<meta name="misskey:oauth:transaction-id" content="` + stdhtml.EscapeString(m.TransactionID) + `">` + "\n")
		sb.WriteString(`<meta name="misskey:oauth:client-name" content="` + stdhtml.EscapeString(m.ClientName) + `">` + "\n")
		if m.ClientLogo != "" {
			sb.WriteString(`<meta name="misskey:oauth:client-logo" content="` + stdhtml.EscapeString(m.ClientLogo) + `">` + "\n")
		}
		sb.WriteString(`<meta name="misskey:oauth:scope" content="` + stdhtml.EscapeString(m.Scope) + `">`)
		// **同意画面は共有キャッシュに載せない。** transaction id を含む HTML が
		// CDN に載ると別の利用者に配られる。upstream も OAuth 側で no-store を
		// 出している (OAuth2ProviderService.ts:322)。
		return renderFrontendShell(c, cfg, metaRepo, proxyAccountResolver, chunkedUpload, clientEntry, shellOverrides{
			Head:         sb.String(),
			CacheControl: "no-store",
		})
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
		"approvalRequiredForSignup":    m.ApprovalRequiredForSignup,
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
			"registration":              !m.DisableRegistration,
			"emailRequiredForSignup":    m.EmailRequiredForSignup,
			"approvalRequiredForSignup": m.ApprovalRequiredForSignup,
			"localTimeline":             meta.PolicyBool(mergedPolicies, "ltlAvailable"),
			"globalTimeline":            meta.PolicyBool(mergedPolicies, "gtlAvailable"),
			"hcaptcha":                  m.EnableHcaptcha,
			"recaptcha":                 m.EnableRecaptcha,
			"turnstile":                 m.EnableTurnstile,
			"objectStorage":             m.UseObjectStorage,
			"serviceWorker":             m.EnableServiceWorker,
			"miauth":                    true,
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
				// purpose 省略時のデフォルトも "any" だが、upstream と値レベルで
				// 揃えるため明示する (差分比較ハーネスで拾われるため)。
				{"src": "/static-assets/splash.png", "sizes": "300x300", "type": "image/png", "purpose": "any"},
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
			// Android のランチャー長押しショートカット。upstream と同じく
			// safemode 起動だけを持つ。
			"shortcuts": []map[string]any{
				{"name": "Safemode", "url": "/?safemode=true"},
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
