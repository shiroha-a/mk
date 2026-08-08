package server

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/meta"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/frontendutil"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// embedCacheControl mirrors upstream's `public, max-age=3600` on embed pages.
const embedCacheControl = "public, max-age=3600"

// embedContext is the payload serialized into #misskey_embedCtx. The embed
// bundle reads it instead of calling the API, so the field names must match
// upstream (`note` / `user` / `clip`).
type embedContext struct {
	Note any `json:"note,omitempty"`
	User any `json:"user,omitempty"`
	Clip any `json:"clip,omitempty"`
}

// EmbedDeps carries the repositories and packers the embed pages need.
//
// embed は **認証なしで誰でも読める** 経路なので、pack は viewer なし
// (viewerID = "") で行う。viewer を渡してしまうと、埋め込み経由で viewer 依存の
// フィールド (isFollowing 等) が漏れる。
type EmbedDeps struct {
	NoteRepo repository.NoteRepository
	UserRepo repository.UserRepository
	ClipRepo repository.ClipRepository

	PackNote func(c echo.Context, note *model.Note) (any, error)
	PackUser func(c echo.Context, user *model.User) (any, error)
	PackClip func(c echo.Context, clip *model.Clip) (any, error)
}

// embedHandlers renders the embed shell for the four upstream embed routes.
type embedHandlers struct {
	cfg                  *config.Config
	metaRepo             repository.MetaRepository
	proxyAccountResolver meta.ProxyAccountResolver
	chunkedUpload        meta.ChunkedUploadCapability
	entry                frontendutil.ClientEntryInfo
	deps                 EmbedDeps
}

// newEmbedHandlers builds the embed route handlers.
//
// entry は起動時に 1 度だけ manifest から解決する (通常シェルと同じ方針)。
func newEmbedHandlers(
	cfg *config.Config,
	metaRepo repository.MetaRepository,
	proxyAccountResolver meta.ProxyAccountResolver,
	chunkedUpload meta.ChunkedUploadCapability,
	deps EmbedDeps,
) *embedHandlers {
	return &embedHandlers{
		cfg:                  cfg,
		metaRepo:             metaRepo,
		proxyAccountResolver: proxyAccountResolver,
		chunkedUpload:        chunkedUpload,
		entry:                frontendutil.DetectEmbedEntry(),
		deps:                 deps,
	}
}

// Note renders /embed/notes/:note.
//
// 可視性ゲートは upstream ClientServerService と同一にする。埋め込みは認証を
// 伴わないので、ここを緩めるとそのまま IDOR になる。
//
//   - ノートが存在しない        -> 文脈なしのシェル
//   - visibility が specified   -> 文脈なしのシェル (宛先限定)
//   - visibility が followers   -> 文脈なしのシェル (フォロワー限定)
//   - userHost != nil (リモート) -> 文脈なしのシェル
//
// upstream は該当時に何も返さず fastify の既定 (空応答) に落ちるが、mk-go は
// 常にシェルを返す。「ノートが無い」と「非公開」を応答の形で区別させないため。
func (h *embedHandlers) Note(c echo.Context) error {
	// upstream は `relations: { user: true, reply: true, renote: true }` で引く。
	// FindByID だと author が空のまま pack され、埋め込みに投稿者名が出ない。
	note, err := h.deps.NoteRepo.FindByIDWithRelations(c.Param("note"))
	if err != nil || note == nil || !embedNoteIsPublic(note) {
		return h.render(c, nil)
	}
	packed, err := h.deps.PackNote(c, note)
	if err != nil {
		return h.render(c, nil)
	}
	return h.render(c, &embedContext{Note: packed})
}

// embedNoteIsPublic reports whether a note may be embedded anonymously.
func embedNoteIsPublic(note *model.Note) bool {
	switch note.Visibility {
	case "specified", "followers":
		return false
	}
	// リモートノートは埋め込まない。upstream と同じ制限で、自分が権威で
	// ないコンテンツを自分のドメインで配らないという線引き。
	return note.UserHost == nil || *note.UserHost == ""
}

// User renders /embed/user-timeline/:user.
//
// リモートユーザーは埋め込まない (upstream と同じ)。
func (h *embedHandlers) User(c echo.Context) error {
	user, err := h.deps.UserRepo.FindByID(c.Param("user"))
	if err != nil || user == nil || (user.Host != nil && *user.Host != "") {
		return h.render(c, nil)
	}
	packed, err := h.deps.PackUser(c, user)
	if err != nil {
		return h.render(c, nil)
	}
	return h.render(c, &embedContext{User: packed})
}

// Clip renders /embed/clips/:clip.
func (h *embedHandlers) Clip(c echo.Context) error {
	clip, err := h.deps.ClipRepo.FindByID(c.Param("clip"))
	if err != nil || clip == nil {
		return h.render(c, nil)
	}
	// 非公開クリップは埋め込まない。upstream は clip の存在だけを見ているが、
	// mk-go では公開フラグも見る。埋め込みは無認証なので、本人だけが見える
	// はずのクリップを配らない方が正しい (divergence として意図的)。
	if !clip.IsPublic {
		return h.render(c, nil)
	}
	packed, err := h.deps.PackClip(c, clip)
	if err != nil {
		return h.render(c, nil)
	}
	return h.render(c, &embedContext{Clip: packed})
}

// Fallback renders /embed/* for paths without a dedicated handler.
func (h *embedHandlers) Fallback(c echo.Context) error {
	return h.render(c, nil)
}

// render writes the embed HTML shell with the optional context payload.
func (h *embedHandlers) render(c echo.Context, ctx *embedContext) error {
	instanceName := "Misskey"
	iconURL := "/static-assets/icons/192.png"
	themeColor := "#86b300"
	metaJSON := "{}"

	if m, err := h.metaRepo.Fetch(); err == nil && m != nil {
		if m.Name != nil && *m.Name != "" {
			instanceName = *m.Name
		}
		if m.IconURL != nil && *m.IconURL != "" {
			iconURL = *m.IconURL
		}
		if m.ThemeColor != nil && *m.ThemeColor != "" {
			themeColor = *m.ThemeColor
		}
		metaJSON = buildMetaJSON(h.cfg, m, h.proxyAccountResolver, h.chunkedUpload)
	}

	embedCtxJSON := ""
	if ctx != nil {
		if b, err := json.Marshal(ctx); err == nil {
			embedCtxJSON = string(b)
		}
	}

	c.Response().Header().Set(echo.HeaderCacheControl, embedCacheControl)
	return c.HTML(http.StatusOK, h.buildHTML(instanceName, iconURL, themeColor, metaJSON, embedCtxJSON))
}

// buildHTML assembles the embed shell, mirroring upstream views/base-embed.tsx.
func (h *embedHandlers) buildHTML(instanceName, iconURL, themeColor, metaJSON, embedCtxJSON string) string {
	var head strings.Builder
	// ビルド済み bundle が無い場合 (dev) は vite client を読ませる。upstream の
	// `frontendEmbedViteFiles == null` 分岐と同じ。
	if h.entry.Script == "" {
		head.WriteString(`<script type="module" src="/embed_vite/@vite/client"></script>` + "\n")
	}
	for _, css := range h.entry.CSS {
		head.WriteString(fmt.Sprintf(`<link rel="stylesheet" href="/embed_vite/%s">`, html.EscapeString(css)) + "\n")
	}

	clientEntry := "null"
	if h.entry.Script != "" {
		if b, err := json.Marshal("/embed_vite/" + h.entry.Script); err == nil {
			clientEntry = string(b)
		}
	}

	// JSON は <script type="application/json"> にそのまま入れる。閉じタグの
	// 早期終了だけを潰せばよく、HTML エスケープすると JSON.parse が壊れる。
	metaBlock := ""
	if metaJSON != "" {
		metaBlock = fmt.Sprintf(
			`<script type="application/json" id="misskey_meta">%s</script>`,
			escapeJSONForScript(metaJSON),
		)
	}
	ctxBlock := ""
	if embedCtxJSON != "" {
		ctxBlock = fmt.Sprintf(
			`<script type="application/json" id="misskey_embedCtx">%s</script>`,
			escapeJSONForScript(embedCtxJSON),
		)
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<meta name="application-name" content="Misskey">
<meta name="referer" content="origin">
<meta name="theme-color" content="%s">
<meta name="theme-color-orig" content="%s">
<meta property="og:site_name" content="%s">
<meta property="instance_url" content="%s">
<meta name="viewport" content="width=device-width, initial-scale=1, minimum-scale=1, maximum-scale=1, user-scalable=no, viewport-fit=cover">
<meta name="format-detection" content="telephone=no,date=no,address=no,email=no,url=no">
<meta name="robots" content="noindex">
<link rel="icon" href="%s">
<title>%s</title>
%s<link rel="stylesheet" href="/embed_vite/loader/style.css">
<script>
const VERSION = "%s";
const CLIENT_ENTRY = %s;
</script>
%s
%s
<script src="/embed_vite/loader/boot.js"></script>
</head>
<body>
<noscript><p>JavaScriptを有効にしてください<br>Please turn on your JavaScript</p></noscript>
</body></html>`,
		html.EscapeString(themeColor),
		html.EscapeString(themeColor),
		html.EscapeString(instanceName),
		html.EscapeString(h.cfg.URL),
		html.EscapeString(iconURL),
		html.EscapeString(instanceName),
		head.String(),
		html.EscapeString(h.cfg.Version),
		clientEntry,
		metaBlock,
		ctxBlock,
	)
}

// escapeJSONForScript neutralises sequences that would terminate the enclosing
// <script> element early.
//
// JSON 内に `</script>` を含む文字列があると、そこで script 要素が閉じて以降が
// HTML として解釈される (= XSS)。ノート本文もユーザー名も攻撃者が自由に
// 書けるので、これは実在の経路。
//
// \u003c 等は JSON としてデコードすれば元の文字に戻るので、JSON.parse の
// 結果は変わらない。
//
// 現状の呼び出し元はどちらも encoding/json 由来で、Go の json.Marshal は
// 既定で `<` `>` `&` を escape する。つまり二重の防御だが、JSON の生成方法が
// 変わったときに黙って穴が開かないよう、埋め込む側でも明示的に潰しておく。
func escapeJSONForScript(s string) string {
	return strings.NewReplacer(
		"<", `\u003c`,
		">", `\u003e`,
		"&", `\u0026`,
	).Replace(s)
}
