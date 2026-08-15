package server

import (
	"encoding/json"
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
		clientEntry:   clientEntryFor(cfg),
		userRepo:      userRepo,
		noteRepo:      noteRepo,
		pageRepo:      pageRepo,
		clipRepo:      clipRepo,
		flashRepo:     flashRepo,
		galleryRepo:   galleryRepo,
	}
}

// render serves the shell with the given per-page overrides.
func (h *ssrMetaHandler) render(c echo.Context, ov shellOverrides) error {
	return renderFrontendShell(c, h.cfg, h.metaRepo, h.proxyResolver, h.chunkedUpload, h.clientEntry, ov)
}

// renderPlain serves the shell without any page-specific override. 対象が
// 見つからないときに使う (upstream も 404 にせず素の shell を返す)。
func (h *ssrMetaHandler) renderPlain(c echo.Context) error {
	return h.render(c, shellOverrides{})
}

// metaTag builds a `<meta name=... content=...>` line. content は必ず escape する
// (username / note text は攻撃者が持ち込める)。
func metaTag(name, content string) string {
	return `<meta name="` + name + `" content="` + stdhtml.EscapeString(content) + `">` + "\n"
}

func propertyTag(property, content string) string {
	return `<meta property="` + property + `" content="` + stdhtml.EscapeString(content) + `">` + "\n"
}

func linkTag(rel, typ, href string) string {
	s := `<link rel="` + rel + `"`
	if typ != "" {
		s += ` type="` + typ + `"`
	}
	return s + ` href="` + stdhtml.EscapeString(href) + `">` + "\n"
}

// profileOf loads the profile row that carries the crawler preferences.
// 見つからなければ nil (upstream は profile 必須だが、mk-go では
// user だけ存在する経路があり得るので落とさない)。
func (h *ssrMetaHandler) profileOf(userID string) *model.UserProfile {
	if h.userRepo == nil || userID == "" {
		return nil
	}
	p, err := h.userRepo.FindProfileByUserID(userID)
	if err != nil {
		return nil
	}
	return p
}

// userHead builds the meta block shared by every page that belongs to a user.
// upstream の各 view (user.tsx / note.tsx / page.tsx ...) が共通で出すもの。
//
// forceNoindex はページ側の追加条件 (note.tsx の isRenote) を渡す。
func userHead(u *model.User, p *model.UserProfile, forceNoindex bool) string {
	if u == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(metaTag("misskey:user-username", u.Username))
	sb.WriteString(metaTag("misskey:user-id", u.ID))
	// remote user は自インスタンスの URL を正規とみなされないよう noindex。
	// upstream user.tsx の `props.user.host != null` 分岐と同じ。
	remote := u.Host != nil && *u.Host != ""
	if forceNoindex || remote || (p != nil && p.NoCrawle) {
		sb.WriteString(metaTag("robots", "noindex"))
	}
	// 学習利用の拒否。upstream と同じく noimageai と noai を並べる。
	// **モデルの既定は true** なので、明示的に許可した利用者以外は出る。
	if p != nil && p.PreventAiLearning {
		sb.WriteString(metaTag("robots", "noimageai"))
		sb.WriteString(metaTag("robots", "noai"))
	}
	return sb.String()
}

// federationEnabled mirrors upstream の `props.federationEnabled`.
// 連合していないインスタンスで AP の URI を広告しても意味が無いので、
// rel="alternate" の出し分けに使う。
func (h *ssrMetaHandler) federationEnabled() bool {
	m, err := h.metaRepo.Fetch()
	if err != nil || m == nil {
		return false
	}
	return m.Federation != "none"
}

// userAlternateLinks emits the AP / canonical links upstream user.tsx puts in
// its metaSlot. sub パス (`/@alice/following` 等) では出さない。
func (h *ssrMetaHandler) userAlternateLinks(u *model.User, p *model.UserProfile, isSub bool) string {
	if u == nil || isSub || !h.federationEnabled() {
		return ""
	}
	var sb strings.Builder
	if u.Host == nil || *u.Host == "" {
		sb.WriteString(linkTag("alternate", "application/activity+json", h.cfg.URL+"/users/"+u.ID))
	}
	if u.URI != nil && *u.URI != "" {
		sb.WriteString(linkTag("alternate", "application/activity+json", *u.URI))
	}
	if p != nil && p.URL != nil && *p.URL != "" {
		sb.WriteString(linkTag("alternate", "text/html", *p.URL))
	}
	return sb.String()
}

// noteAlternateLinks emits the AP links upstream note.tsx puts in its metaSlot.
// ローカルノートは自分の AP URI を、リモートノートは originating server の URI を
// 指す (どちらも「この HTML の AP 版はこれ」を示す)。
func (h *ssrMetaHandler) noteAlternateLinks(note *model.Note) string {
	if note == nil || !h.federationEnabled() {
		return ""
	}
	var sb strings.Builder
	if note.UserHost == nil || *note.UserHost == "" {
		sb.WriteString(linkTag("alternate", "application/activity+json", h.cfg.URL+"/notes/"+note.ID))
	}
	if note.URI != nil && *note.URI != "" {
		sb.WriteString(linkTag("alternate", "application/activity+json", *note.URI))
	}
	return sb.String()
}

// meLinks emits `<link rel="me">` for the profile fields that hold a URL.
// upstream user.tsx と同じく http(s) で始まる値だけを対象にする
// (rel=me は所有証明に使われるので、任意文字列を混ぜない)。
func meLinks(p *model.UserProfile) string {
	if p == nil || len(p.Fields) == 0 {
		return ""
	}
	var fields []struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(p.Fields, &fields); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, f := range fields {
		if strings.HasPrefix(f.Value, "http://") || strings.HasPrefix(f.Value, "https://") {
			sb.WriteString(linkTag("me", "", f.Value))
		}
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
		return h.renderPlain(c)
	}
	p := h.profileOf(u.ID)
	og := propertyTag("og:type", "blog") +
		propertyTag("og:title", displayName(u)) +
		propertyTag("og:url", h.cfg.URL+"/@"+u.Username)
	head := userHead(u, p, false) +
		h.userAlternateLinks(u, p, c.Param("sub") != "") +
		meLinks(p)
	return h.render(c, shellOverrides{Head: head, OG: og})
}

// UserPagePage serves `/@:acct/pages/:page`.
func (h *ssrMetaHandler) UserPagePage(c echo.Context) error {
	u := h.lookupUserByAcct(c.Param("acct"))
	if u == nil || h.pageRepo == nil {
		return h.renderPlain(c)
	}
	p := h.profileOf(u.ID)
	page, err := h.pageRepo.FindByUserAndName(u.ID, c.Param("page"))
	if err != nil || page == nil {
		return h.render(c, shellOverrides{Head: userHead(u, p, false)})
	}
	og := propertyTag("og:type", "article") +
		propertyTag("og:title", page.Title)
	return h.render(c, shellOverrides{
		Head: userHead(u, p, false) + metaTag("misskey:page-id", page.ID),
		OG:   og,
	})
}

// NotePage serves `/notes/:id` for HTML clients. AP クライアント (Accept:
// application/activity+json) は先に AP handler が処理するので、ここには
// 来ない。
func (h *ssrMetaHandler) NotePage(c echo.Context) error {
	if h.noteRepo == nil {
		return h.renderPlain(c)
	}
	note, err := h.noteRepo.FindByIDWithUser(c.Param("id"))
	if err != nil || note == nil {
		return h.renderPlain(c)
	}
	// visibility が public 以外の note は meta を出さない。クローラや
	// リンク展開に非公開投稿の本文・著者を渡さないため (upstream も
	// public 以外は SSR しない)。
	if note.Visibility != model.NoteVisibilityPublic {
		return h.renderPlain(c)
	}
	og := propertyTag("og:type", "article")
	if note.User != nil {
		og += propertyTag("og:title", displayName(note.User))
	}
	if note.Text != nil && *note.Text != "" {
		og += propertyTag("og:description", *note.Text)
	}
	og += propertyTag("og:url", h.cfg.URL+"/notes/"+note.ID)
	var profile *model.UserProfile
	if note.User != nil {
		profile = h.profileOf(note.User.ID)
	}
	// upstream note.tsx は isRenotePacked (= renoteId != null) で noindex。
	// 引用リノートも対象で、他人の投稿を写した URL を検索対象にしない。
	head := userHead(note.User, profile, note.RenoteID != nil) +
		metaTag("misskey:note-id", note.ID) +
		h.noteAlternateLinks(note)
	return h.render(c, shellOverrides{Head: head, OG: og})
}

// ClipPage serves `/clips/:clip`.
func (h *ssrMetaHandler) ClipPage(c echo.Context) error {
	if h.clipRepo == nil {
		return h.renderPlain(c)
	}
	clip, err := h.clipRepo.FindByID(c.Param("clip"))
	if err != nil || clip == nil || !clip.IsPublic {
		return h.renderPlain(c)
	}
	og := propertyTag("og:type", "article") +
		propertyTag("og:title", clip.Name)
	author := h.userByID(clip.UserID)
	return h.render(c, shellOverrides{
		Head: userHead(author, h.profileOf(clip.UserID), false) + metaTag("misskey:clip-id", clip.ID),
		OG:   og,
	})
}

// FlashPage serves `/play/:id`.
func (h *ssrMetaHandler) FlashPage(c echo.Context) error {
	if h.flashRepo == nil {
		return h.renderPlain(c)
	}
	flash, err := h.flashRepo.FindByID(c.Param("id"))
	if err != nil || flash == nil {
		return h.renderPlain(c)
	}
	og := propertyTag("og:type", "article") +
		propertyTag("og:title", flash.Title)
	author := h.userByID(flash.UserID)
	return h.render(c, shellOverrides{
		Head: userHead(author, h.profileOf(flash.UserID), false) + metaTag("misskey:flash-id", flash.ID),
		OG:   og,
	})
}

// GalleryPage serves `/gallery/:post`.
func (h *ssrMetaHandler) GalleryPage(c echo.Context) error {
	if h.galleryRepo == nil {
		return h.renderPlain(c)
	}
	posts, err := h.galleryRepo.FindPostsByIDs([]string{c.Param("post")})
	if err != nil || len(posts) == 0 {
		return h.renderPlain(c)
	}
	post := posts[0]
	author := post.User
	if author == nil {
		author = h.userByID(post.UserID)
	}
	og := propertyTag("og:type", "article") +
		propertyTag("og:title", post.Title)
	head := userHead(author, h.profileOf(post.UserID), false) +
		metaTag("misskey:gallery-post-id", post.ID)
	return h.render(c, shellOverrides{Head: head, OG: og})
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
