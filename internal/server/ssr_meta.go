package server

import (
	stdhtml "html"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/shiroha-a/mk/internal/api/meta"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/frontendutil"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// ssrMetaHandler serves the SPA shell with entity-specific <meta> tags for the
// public permalink routes (`/@user`, `/notes/:id`, ...).
//
// upstream は ClientServerService がこれらのパスを JSX で SSR し、OGP と
// `misskey:*` の meta を埋める。クローラや Discord / Slack のリンク展開は
// JavaScript を実行しないので、SPA shell をそのまま返すとどのページも
// インスタンス名しか読めない。
//
// 対象が見つからないときは meta 無しの素の shell を 200 で返す。upstream も
// 同じで、404 にするのは frontend の役目 (ページ内で「見つかりません」を出す)。
type ssrMetaHandler struct {
	cfg           *config.Config
	metaRepo      repository.MetaRepository
	proxyResolver meta.ProxyAccountResolver
	chunkedUpload meta.ChunkedUploadCapability
	clientEntry   frontendutil.ClientEntryInfo

	userRepo    repository.UserRepository
	noteRepo    repository.NoteRepository
	pageRepo    repository.PageRepository
	clipRepo    repository.ClipRepository
	flashRepo   repository.FlashRepository
	galleryRepo repository.GalleryRepository
}

func newSSRMetaHandler(
	cfg *config.Config,
	metaRepo repository.MetaRepository,
	proxyResolver meta.ProxyAccountResolver,
	chunkedUpload meta.ChunkedUploadCapability,
	userRepo repository.UserRepository,
	noteRepo repository.NoteRepository,
	pageRepo repository.PageRepository,
	clipRepo repository.ClipRepository,
	flashRepo repository.FlashRepository,
	galleryRepo repository.GalleryRepository,
) *ssrMetaHandler {
	return &ssrMetaHandler{
		cfg:           cfg,
		metaRepo:      metaRepo,
		proxyResolver: proxyResolver,
		chunkedUpload: chunkedUpload,
		clientEntry:   frontendutil.DetectClientEntry(),
		userRepo:      userRepo,
		noteRepo:      noteRepo,
		pageRepo:      pageRepo,
		clipRepo:      clipRepo,
		flashRepo:     flashRepo,
		galleryRepo:   galleryRepo,
	}
}

// render serves the shell with the given head fragment.
func (h *ssrMetaHandler) render(c echo.Context, head string) error {
	return renderFrontendShell(c, h.cfg, h.metaRepo, h.proxyResolver, h.chunkedUpload, h.clientEntry, head)
}

// metaTag builds a `<meta name=... content=...>` line. content は必ず escape する
// (username / note text は攻撃者が持ち込める)。
func metaTag(name, content string) string {
	return `<meta name="` + name + `" content="` + stdhtml.EscapeString(content) + `">` + "\n"
}

func propertyTag(property, content string) string {
	return `<meta property="` + property + `" content="` + stdhtml.EscapeString(content) + `">` + "\n"
}

// userHead builds the meta block shared by every page that belongs to a user.
// upstream の各 view (user.tsx / note.tsx / page.tsx ...) が共通で出すもの。
func (h *ssrMetaHandler) userHead(u *model.User) string {
	if u == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(metaTag("misskey:user-username", u.Username))
	sb.WriteString(metaTag("misskey:user-id", u.ID))
	// remote user は自インスタンスの URL を正規とみなされないよう noindex。
	// upstream user.tsx の `props.user.host != null` 分岐と同じ。
	if u.Host != nil && *u.Host != "" {
		sb.WriteString(metaTag("robots", "noindex"))
	}
	return sb.String()
}

// displayName returns the OGP title fragment for a user (`Name (@handle)`).
func displayName(u *model.User) string {
	handle := "@" + u.Username
	if u.Host != nil && *u.Host != "" {
		handle += "@" + *u.Host
	}
	if u.Name != nil && *u.Name != "" {
		return *u.Name + " (" + handle + ")"
	}
	return handle
}

// lookupUserByAcct resolves `alice` / `alice@remote.example` to a user row.
func (h *ssrMetaHandler) lookupUserByAcct(acct string) *model.User {
	if h.userRepo == nil || acct == "" {
		return nil
	}
	name, host, _ := strings.Cut(strings.TrimPrefix(acct, "@"), "@")
	if name == "" {
		return nil
	}
	var hostPtr *string
	// 自ホスト宛ての acct は local 扱いにする (upstream の Acct.parse と同じ)。
	if host != "" && !strings.EqualFold(host, h.cfg.Host) {
		hostPtr = &host
	}
	u, err := h.userRepo.FindByUsernameLower(strings.ToLower(name), hostPtr)
	if err != nil {
		return nil
	}
	return u
}

// UserPage serves `/@:acct` and its sub paths (`/@:acct/notes` 等)。
func (h *ssrMetaHandler) UserPage(c echo.Context) error {
	u := h.lookupUserByAcct(c.Param("acct"))
	if u == nil {
		return h.render(c, "")
	}
	head := h.userHead(u)
	head += propertyTag("og:type", "blog")
	head += propertyTag("og:title", displayName(u))
	head += propertyTag("og:url", h.cfg.URL+"/@"+u.Username)
	return h.render(c, head)
}

// UserPagePage serves `/@:acct/pages/:page`.
func (h *ssrMetaHandler) UserPagePage(c echo.Context) error {
	u := h.lookupUserByAcct(c.Param("acct"))
	if u == nil || h.pageRepo == nil {
		return h.render(c, "")
	}
	head := h.userHead(u)
	page, err := h.pageRepo.FindByUserAndName(u.ID, c.Param("page"))
	if err != nil || page == nil {
		return h.render(c, head)
	}
	head += metaTag("misskey:page-id", page.ID)
	head += propertyTag("og:type", "article")
	head += propertyTag("og:title", page.Title)
	return h.render(c, head)
}

// NotePage serves `/notes/:id` for HTML clients. AP クライアント (Accept:
// application/activity+json) は先に AP handler が処理するので、ここには
// 来ない。
func (h *ssrMetaHandler) NotePage(c echo.Context) error {
	if h.noteRepo == nil {
		return h.render(c, "")
	}
	note, err := h.noteRepo.FindByIDWithUser(c.Param("id"))
	if err != nil || note == nil {
		return h.render(c, "")
	}
	// visibility が public 以外の note は meta を出さない。クローラや
	// リンク展開に非公開投稿の本文・著者を渡さないため (upstream も
	// public 以外は SSR しない)。
	if note.Visibility != model.NoteVisibilityPublic {
		return h.render(c, "")
	}
	head := h.userHead(note.User)
	head += metaTag("misskey:note-id", note.ID)
	head += propertyTag("og:type", "article")
	if note.User != nil {
		head += propertyTag("og:title", displayName(note.User))
	}
	if note.Text != nil && *note.Text != "" {
		head += propertyTag("og:description", *note.Text)
	}
	head += propertyTag("og:url", h.cfg.URL+"/notes/"+note.ID)
	return h.render(c, head)
}

// ClipPage serves `/clips/:clip`.
func (h *ssrMetaHandler) ClipPage(c echo.Context) error {
	if h.clipRepo == nil {
		return h.render(c, "")
	}
	clip, err := h.clipRepo.FindByID(c.Param("clip"))
	if err != nil || clip == nil || !clip.IsPublic {
		return h.render(c, "")
	}
	head := h.userHead(h.userByID(clip.UserID))
	head += metaTag("misskey:clip-id", clip.ID)
	head += propertyTag("og:type", "article")
	head += propertyTag("og:title", clip.Name)
	return h.render(c, head)
}

// FlashPage serves `/play/:id`.
func (h *ssrMetaHandler) FlashPage(c echo.Context) error {
	if h.flashRepo == nil {
		return h.render(c, "")
	}
	flash, err := h.flashRepo.FindByID(c.Param("id"))
	if err != nil || flash == nil {
		return h.render(c, "")
	}
	head := h.userHead(h.userByID(flash.UserID))
	head += metaTag("misskey:flash-id", flash.ID)
	head += propertyTag("og:type", "article")
	head += propertyTag("og:title", flash.Title)
	return h.render(c, head)
}

// GalleryPage serves `/gallery/:post`.
func (h *ssrMetaHandler) GalleryPage(c echo.Context) error {
	if h.galleryRepo == nil {
		return h.render(c, "")
	}
	posts, err := h.galleryRepo.FindPostsByIDs([]string{c.Param("post")})
	if err != nil || len(posts) == 0 {
		return h.render(c, "")
	}
	post := posts[0]
	author := post.User
	if author == nil {
		author = h.userByID(post.UserID)
	}
	head := h.userHead(author)
	head += propertyTag("og:type", "article")
	head += propertyTag("og:title", post.Title)
	return h.render(c, head)
}

func (h *ssrMetaHandler) userByID(id string) *model.User {
	if h.userRepo == nil || id == "" {
		return nil
	}
	u, err := h.userRepo.FindByID(id)
	if err != nil {
		return nil
	}
	return u
}

// prefersHTML reports whether the client asked for HTML rather than
// ActivityPub JSON. `/@:acct` と `/notes/:id` は AP actor / object の URL でも
// あるので、Accept を見て振り分ける。
func prefersHTML(c echo.Context) bool {
	accept := c.Request().Header.Get(echo.HeaderAccept)
	if accept == "" {
		return true
	}
	return !strings.Contains(accept, "application/activity+json") &&
		!strings.Contains(accept, "application/ld+json")
}
