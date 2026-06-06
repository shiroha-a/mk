package meta

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/repository"
)

// ProxyAccountResolver returns the username of the "proxy" system account.
// 呼び出し側 (router) が SystemAccountRepository + UserRepository を使って
// type='proxy' の system_account から userId を辿り、username を返す。
// 解決できないときは "" と false を返す。
type ProxyAccountResolver func() (string, bool)

// Handler handles meta-related API endpoints.
type Handler struct {
	config           *config.Config
	metaRepo         repository.MetaRepository
	adRepo           repository.AdRepository
	proxyAccountName ProxyAccountResolver
}

// NewHandler creates a new meta Handler.
func NewHandler(cfg *config.Config, metaRepo repository.MetaRepository) *Handler {
	return &Handler{config: cfg, metaRepo: metaRepo}
}

// SetAdRepo wires an AdRepository for the `ads` field on the /api/meta
// response. When unset, ads is returned as an empty array so existing tests
// without ad wiring keep passing.
func (h *Handler) SetAdRepo(r repository.AdRepository) {
	h.adRepo = r
}

// SetProxyAccountResolver wires the resolver used to populate the
// `proxyAccountName` field on MetaDetailed responses. When unset (tests or
// pre-setup instances), the field is reported as null.
func (h *Handler) SetProxyAccountResolver(r ProxyAccountResolver) {
	h.proxyAccountName = r
}

// Meta returns server metadata.
// POST /api/meta
// Meta handles POST /api/meta.
// TS互換: detail (boolean, default true)。falseの場合は簡易レスポンスを返す。
func (h *Handler) Meta(c echo.Context) error {
	var params struct {
		Detail *bool `json:"detail"`
	}
	_ = c.Bind(&params)
	detail := params.Detail == nil || *params.Detail

	m, err := h.metaRepo.Fetch()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}

	// upstream MetaEntityService と同じく policies は { ...DEFAULT_POLICIES,
	// ...instance.policies } でマージする。admin が update-meta で driveCapacityMb
	// 等を変更しても以前は DefaultPolicies のみ返して反映されなかった。
	mergedPolicies := role.MergeMetaPolicies(m.Policies)

	resp := map[string]any{
		"maintainerName":         m.MaintainerName,
		"maintainerEmail":        m.MaintainerEmail,
		"version":                h.config.Version,
		"name":                   m.Name,
		"shortName":              m.ShortName,
		"uri":                    h.config.URL,
		"description":            m.Description,
		"langs":                  strArrayOrEmpty(m.Langs),
		"disableRegistration":    m.DisableRegistration,
		"emailRequiredForSignup": m.EmailRequiredForSignup,
		"enableHcaptcha":         m.EnableHcaptcha,
		"hcaptchaSiteKey":        m.HcaptchaSiteKey,
		"enableRecaptcha":        m.EnableRecaptcha,
		"recaptchaSiteKey":       m.RecaptchaSiteKey,
		"enableTurnstile":        m.EnableTurnstile,
		"turnstileSiteKey":       m.TurnstileSiteKey,
		"themeColor":             m.ThemeColor,
		"bannerUrl":              m.BannerURL,
		"backgroundImageUrl":     m.BackgroundImageURL,
		"logoImageUrl":           m.LogoImageURL,
		"iconUrl":                m.IconURL,
		"cacheRemoteFiles":       m.CacheRemoteFiles,
		"enableServiceWorker":    m.EnableServiceWorker,
		"swPublickey":            m.SwPublicKey,
		"serverRules":            strArrayOrEmpty(m.ServerRules),
		"maxNoteTextLength":      3000,

		// フロントエンド互換性フィールド (Phase 4.5c)
		"tosUrl":              m.TermsOfServiceURL,
		"repositoryUrl":       m.RepositoryURL,
		"feedbackUrl":         m.FeedbackURL,
		"impressumUrl":        m.ImpressumURL,
		"privacyPolicyUrl":    m.PrivacyPolicyURL,
		"inquiryUrl":          m.InquiryURL,
		"federation":          m.Federation,
		"defaultLightTheme":   m.DefaultLightTheme,
		"defaultDarkTheme":    m.DefaultDarkTheme,
		"serverErrorImageUrl": m.ServerErrorImageURL,
		"notFoundImageUrl":    m.NotFoundImageURL,
		"infoImageUrl":        m.InfoImageURL,
		"app192IconUrl":       m.App192IconURL,
		"app512IconUrl":       m.App512IconURL,
		// mascotImageUrl: meta 値があればそれを返す。空または nil なら従来の
		// /assets/ai.png にフォールバック (フロントエンドが no-image にならないため)。
		"mascotImageUrl":               mascotURL(m.MascotImageURL),
		"translatorAvailable":          m.DeeplAuthKey != nil && *m.DeeplAuthKey != "",
		"enableEmail":                  m.EnableEmail,
		"enableUrlPreview":             m.URLPreviewEnabled,
		"ads":                          h.serializeActiveAds(),
		"notesPerOneAd":                m.NotesPerOneAd,
		"mediaProxy":                   h.config.MediaProxy,
		"cacheRemoteSensitiveFiles":    m.CacheRemoteSensitiveFiles,
		"requireSetup":                 m.RootUserID == nil,
		"singleUserMode":               m.SingleUserMode,
		"providesTarball":              h.config.PublishTarballInsteadOfProvideRepositoryUrl,
		"maxFileSize":                  h.config.MaxFileSize,
		"proxyAccountName":             resolveProxyAccountName(h.proxyAccountName),
		"enableMcaptcha":               m.EnableMcaptcha,
		"mcaptchaSiteKey":              m.McaptchaSiteKey,
		"mcaptchaInstanceUrl":          m.McaptchaInstanceURL,
		"enableTestcaptcha":            m.EnableTestcaptcha,
		"sentryForFrontend":            sentryForFrontendJSON(h.config.SentryForFrontend),
		"googleAnalyticsMeasurementId": m.GoogleAnalyticsMeasurementID,
		// clientOptions は jsonb なのでそのまま返す。空 jsonb なら frontend が
		// デフォルト値を使う前提。
		"clientOptions": clientOptionsJSON(m.ClientOptions),

		"policies": mergedPolicies,

		"features": map[string]any{
			"registration":           !m.DisableRegistration,
			"emailRequiredForSignup": m.EmailRequiredForSignup,
			"localTimeline":          PolicyBool(mergedPolicies, "ltlAvailable"),
			"globalTimeline":         PolicyBool(mergedPolicies, "gtlAvailable"),
			"hcaptcha":               m.EnableHcaptcha,
			"recaptcha":              m.EnableRecaptcha,
			"turnstile":              m.EnableTurnstile,
			"objectStorage":          m.UseObjectStorage,
			"serviceWorker":          m.EnableServiceWorker,
			"miauth":                 true,
		},
	}

	// noteSearchableScope: 検索 backend (fulltextSearch.provider) と meilisearch.scope
	// に基づき "local"/"global" を返す (upstream #17341 / 2026.5.0)。詳細は
	// NoteSearchableScope 関数の doc 参照。
	resp["noteSearchableScope"] = NoteSearchableScope(h.config.FulltextSearch, h.config.Meilisearch)

	// detail=false: TS MetaLite 互換。omit には golden MetaLite に**無い** field
	// (= MetaDetailedOnly / mk-go 内部 field) のみを入れる。golden MetaLite は
	// policies / clientOptions / sentryForFrontend / noteSearchableScope /
	// providesTarball を必須とし、Misskey の MetaEntityService.pack() (lite) も
	// 返すため、lite から落とすと client が非 null cast で落ちる (#1306 で
	// 旧 omit がこれらを誤って除外していたのを修正)。features / cacheRemoteFiles /
	// cacheRemoteSensitiveFiles / requireSetup / proxyAccountName は
	// MetaDetailedOnly なので lite から除外する。
	if !detail {
		omit := map[string]struct{}{
			"features":                  {},
			"cacheRemoteFiles":          {},
			"cacheRemoteSensitiveFiles": {},
			"requireSetup":              {},
			"proxyAccountName":          {},
			// singleUserMode は golden の MetaLite/MetaDetailedOnly どちらにも無い
			// mk-go 内部 field。lite では従来どおり省く。
			"singleUserMode": {},
		}
		lite := make(map[string]any, len(resp))
		for k, v := range resp {
			if _, skip := omit[k]; !skip {
				lite[k] = v
			}
		}
		return c.JSON(http.StatusOK, lite)
	}

	return c.JSON(http.StatusOK, resp)
}

// Ping returns a simple pong response.
// POST /api/ping
func (h *Handler) Ping(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"pong": true,
	})
}

// strArrayOrEmpty coalesces a nil string slice to a non-nil empty slice so the
// JSON encoder emits `[]` instead of `null`. golden MetaLite の langs /
// serverRules は string[] 必須 (non-null) で、空でも null だと client が
// 非 null cast で落ちる (#1306。channels pinnedNoteIds #1283 と同種)。
func strArrayOrEmpty(a []string) []string {
	if a == nil {
		return []string{}
	}
	return a
}

// mascotURL applies the legacy fallback for the mascot. Misskey フロントエンドは
// このフィールドが空文字 / 未設定でも特定の no-image を出すだけだが、本家挙動と
// 互換にするため /assets/ai.png をフォールバックに使う。
func mascotURL(v *string) string {
	if v != nil && *v != "" {
		return *v
	}
	return "/assets/ai.png"
}

// clientOptionsJSON returns m.ClientOptions as a generic any. JSON encoder の
// raw bytes をそのまま返すと frontend が parse しないので、空の場合は空 map に
// 正規化する (本家フロントエンドは object を期待する)。
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

// resolveProxyAccountName looks up the `proxyAccountName` field via the
// optional resolver wired by the router. Returns nil when the resolver is
// absent (tests, pre-setup instances) or cannot find the system account.
// Returning nil keeps the JSON field as null, matching the TS behavior when
// no proxy account is configured.
func resolveProxyAccountName(r ProxyAccountResolver) any {
	if r == nil {
		return nil
	}
	name, ok := r()
	if !ok {
		return nil
	}
	return name
}

// sentryForFrontendJSON normalizes config.SentryForFrontend for JSON output.
// Empty (nil or zero-length) map is treated as absent and returns nil so
// the JSON response carries `null` (TS behavior: `?? null`).
func sentryForFrontendJSON(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	return m
}

// PolicyBool reads a boolean policy value from role.DefaultPolicies() output
// with a conservative default. Unknown or non-bool values fall back to false
// so the client never sees an undefined flag in the features block.
func PolicyBool(policies map[string]any, key string) bool {
	v, ok := policies[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	if !ok {
		return false
	}
	return b
}

// NoteSearchableScope derives the "local"/"global" search scope flag from
// the search provider configuration.
//
// upstream Misskey #17341 (= 2026.5.0 fix / triage #1008): "local" を返すのは
// fulltextSearch.provider が "meilisearch" かつ meilisearch.scope が "local"
// のときだけで、それ以外は全て "global"。旧 upstream 実装は meilisearch block
// の有無だけ見ていたため、provider が sqlLike / none でも meilisearch.scope
// 設定が誤って反映される bug があった。本関数は新 upstream semantics に合わせる。
//
// mk-go 視点: 旧実装は default "local" を返していたが、これは sqlLike fallback
// でも remote note を検索できる挙動と矛盾していた (UI が local 検索 only と
// 解釈してしまう)。upstream 修正と同じく default "global" にすることで
// sqlLike / none の実挙動とも整合する。
func NoteSearchableScope(fts *config.FulltextSearchOptions, m *config.MeilisearchOptions) string {
	if fts == nil || fts.Provider != "meilisearch" {
		return "global"
	}
	if m == nil || m.Scope != "local" {
		return "global"
	}
	return "local"
}

// serializeActiveAds fetches currently-active ads via adRepo and projects
// them into the Misskey-compatible shape. Returns an empty slice when the
// repo is unwired (tests) or fetch fails, keeping the response stable.
// フロントエンドが notesPerOneAd と組み合わせて timeline に差し込む前提。
func (h *Handler) serializeActiveAds() []map[string]any {
	if h.adRepo == nil {
		return []map[string]any{}
	}
	ads, err := h.adRepo.ListActive(time.Now())
	if err != nil || len(ads) == 0 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(ads))
	for _, a := range ads {
		out = append(out, map[string]any{
			"id":        a.ID,
			"url":       a.URL,
			"place":     a.Place,
			"ratio":     a.Ratio,
			"imageUrl":  a.ImageURL,
			"dayOfWeek": a.DayOfWeek,
		})
	}
	return out
}
