// Package ap provides ActivityPub resource endpoints for users and notes.
package ap

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/api/apierr"
	corenote "github.com/shiroha-a/mk/internal/core/note"
	coreuser "github.com/shiroha-a/mk/internal/core/user"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

// RemoteFetcher fetches remote ActivityPub objects.
type RemoteFetcher interface {
	FetchObject(uri string) ([]byte, error)
}

// RemoteResolver resolves remote actors and notes.
type RemoteResolver interface {
	ResolveActor(uri string) (*model.User, error)
	ResolveNote(uri string) (*model.Note, error)
}

// Handler handles ActivityPub resource endpoints.
type Handler struct {
	renderer         *activitypub.Renderer
	userService      *coreuser.Service
	queryService     *corenote.QueryService
	keypairRepo      repository.UserKeypairRepository
	keypairExtraRepo repository.UserKeypairExtraRepository
	idGen            id.Generator
	remoteFetcher    RemoteFetcher
	remoteResolver   RemoteResolver
	nonAPFallback    echo.HandlerFunc
	// backfillGroup collapses parallel lazy-backfill 経路の Generate +
	// InsertIfAbsent を同 user ID で 1 回に集約する。多 goroutine が
	// 同時に backfill しても entropy / CPU を浪費しない (#1081 review #1)。
	backfillGroup singleflight.Group
}

// SetRemote attaches remote AP fetcher and resolver.
func (h *Handler) SetRemote(fetcher RemoteFetcher, resolver RemoteResolver) {
	h.remoteFetcher = fetcher
	h.remoteResolver = resolver
}

// SetNonAPFallback registers a handler to serve when the incoming Accept
// header does not request an ActivityPub document. Typically the SPA HTML
// frontend is registered here so that browser reloads on actor/note URLs
// render the frontend instead of returning a bare 404 JSON. Echo does not
// re-dispatch errored handlers to the catch-all route, so this delegation
// has to happen inside the AP handler itself.
func (h *Handler) SetNonAPFallback(fn echo.HandlerFunc) {
	h.nonAPFallback = fn
}

// serveNonAP is called from AP resource handlers when the client did not
// request application/activity+json. When a fallback is wired (runtime)
// it renders the frontend HTML; otherwise (tests) it returns ErrNotFound
// so the behavior degrades gracefully when the handler is used in
// isolation.
func (h *Handler) serveNonAP(c echo.Context) error {
	if h.nonAPFallback != nil {
		return h.nonAPFallback(c)
	}
	return echo.ErrNotFound
}

// NewHandler constructs a Handler.
func NewHandler(
	renderer *activitypub.Renderer,
	userService *coreuser.Service,
	queryService *corenote.QueryService,
	keypairRepo repository.UserKeypairRepository,
	idGen id.Generator,
) *Handler {
	return &Handler{
		renderer:     renderer,
		userService:  userService,
		queryService: queryService,
		keypairRepo:  keypairRepo,
		idGen:        idGen,
	}
}

// SetKeypairExtraRepo wires the Ed25519 keypair repository so that actor JSON
// responses can include `assertionMethod[]` Multikey entries for users that
// own an Ed25519 keypair (#1067 / #1069). Optional: 未配線でも RSA only で
// 動き、upstream Misskey TS と同一の actor JSON shape を維持する。
func (h *Handler) SetKeypairExtraRepo(r repository.UserKeypairExtraRepository) {
	h.keypairExtraRepo = r
}

// lookupEd25519PublicKey returns the Ed25519 public key for the local user
// when keypairExtraRepo is wired. P1 マイグレーション前から存在する旧 user
// には user_keypair_extra に行が無いため、ErrRecordNotFound のときに lazy
// backfill (鍵生成 + InsertIfAbsent + 再 lookup) を行って actor JSON に
// assertionMethod を expose する (#1072)。生成失敗 / PEM parse 失敗のいずれも
// nil を返して RSA only にフォールバックする。
//
// 並列 actor JSON 生成で同 user に対して複数 goroutine が backfill を
// 開始しても、InsertIfAbsent (ON CONFLICT DO NOTHING) で 最初に書かれた
// 行が残り、後続は再 lookup で既存行を取得する → race-safe。
//
// DB error (= ErrRecordNotFound 以外) と PEM parse error は診断のため
// slog.Warn を出す。
func (h *Handler) lookupEd25519PublicKey(userID string) ed25519.PublicKey {
	if h.keypairExtraRepo == nil {
		return nil
	}
	row, err := h.keypairExtraRepo.FindByUserID(userID)
	if err == nil {
		return h.parseEd25519PublicKeyOrLog(userID, row.Ed25519PublicKey)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		slog.Warn("ed25519 keypair lookup failed",
			"userID", userID, "error", err)
		return nil
	}
	// 行なし → P5 lazy backfill 経路
	return h.backfillEd25519PublicKey(userID)
}

// backfillEd25519PublicKey generates a fresh Ed25519 keypair for a local user
// that pre-dates the P1 migration, persists it via InsertIfAbsent (race-safe),
// and returns the public key for the renderer. Any failure path returns nil
// → RSA only fallback。
//
// 同 userID への並行 backfill は singleflight で 1 回に集約され、Generate +
// InsertIfAbsent + 再 lookup を多 goroutine が個別に走らせる無駄を抑える
// (#1081 review #1)。InsertIfAbsent の戻り値 inserted で「自分が書いた」
// 「他が先に書いた」を区別し、inserted=true なら自身が生成した PEM を
// そのまま返して再 FindByUserID を skip (= 1 query 削減 #1081 review #2)。
func (h *Handler) backfillEd25519PublicKey(userID string) ed25519.PublicKey {
	val, _, _ := h.backfillGroup.Do(userID, func() (any, error) {
		// 失敗時に typed-nil の ed25519.PublicKey を返すと caller 側で
		// `val == nil` が false になる (interface comparison の罠)。明示的に
		// untyped nil を return して check が正しく short-circuit するように
		// する。
		if pub := h.runEd25519Backfill(userID); pub != nil {
			return pub, nil
		}
		return nil, nil
	})
	if val == nil {
		return nil
	}
	return val.(ed25519.PublicKey)
}

func (h *Handler) runEd25519Backfill(userID string) ed25519.PublicKey {
	privPEM, pubPEM, err := activitypub.GenerateEd25519Keypair()
	if err != nil {
		slog.Warn("ed25519 backfill keypair generate failed",
			"userID", userID, "error", err)
		return nil
	}
	inserted, err := h.keypairExtraRepo.InsertIfAbsent(&model.UserKeypairExtra{
		UserID:            userID,
		Ed25519PublicKey:  pubPEM,
		Ed25519PrivateKey: privPEM,
	})
	if err != nil {
		slog.Warn("ed25519 backfill insert failed",
			"userID", userID, "error", err)
		return nil
	}
	if inserted {
		// 自身が書いた行 → 生成済 PEM をそのまま返す (1 query 削減)
		return h.parseEd25519PublicKeyOrLog(userID, pubPEM)
	}
	// 並列 race (= singleflight 期間外の重複 backfill) で別 goroutine が
	// 先に書いた → 再 lookup で永続化された別公開鍵を取得
	row, err := h.keypairExtraRepo.FindByUserID(userID)
	if err != nil {
		slog.Warn("ed25519 backfill re-lookup failed",
			"userID", userID, "error", err)
		return nil
	}
	return h.parseEd25519PublicKeyOrLog(userID, row.Ed25519PublicKey)
}

// parseEd25519PublicKeyOrLog parses an Ed25519 PEM and logs on failure.
// Shared by lookup / backfill paths.
func (h *Handler) parseEd25519PublicKeyOrLog(userID, pemStr string) ed25519.PublicKey {
	pub, err := activitypub.ParseEd25519PublicKeyPEM(pemStr)
	if err != nil {
		slog.Warn("ed25519 PEM parse failed",
			"userID", userID, "error", err)
		return nil
	}
	return pub
}

// User handles GET /users/:id with ActivityPub content negotiation.
// AP clients (Accept: application/activity+json) receive the Person
// document; browser callers are 302-redirected to the canonical
// `/@<username>` permalink so the SPA's userPage route can resolve.
//
// Misskey TS の ClientServerService.ts と同じ挙動: 共有 frontend は
// `/users/:id` の SPA ルートを持たず `/@:acct` のみなので、QR code (frontend
// が自分の id で組み立てる) や AP federation 由来のリンクから来た訪問者を
// 確実に SPA で開けるよう backend で redirect する (#691)。リモート user
// の場合は SPA 側でも canonical な remote URI を開かせるため Host 付きで
// `/@username@host` 形式に redirect する。
func (h *Handler) User(c echo.Context) error {
	// Vary: Accept は AP / browser の両分岐を持つすべての response に必要。
	// content-negotiation 実行前に header を立てておけば、redirect / Person /
	// 404 のいずれが返っても intermediate cache が誤配信しない (#691 review)。
	c.Response().Header().Set("Vary", "Accept")

	id := c.Param("id")
	bundle, err := h.userService.ShowByID(id)
	if err != nil {
		// 既存仕様: AP client は 404、browser も 404 (ID 不正)
		return c.NoContent(http.StatusNotFound)
	}

	if !wantsActivityJSON(c.Request().Header.Get("Accept")) {
		// suspended local user は upstream Misskey TS と同じく 404。SPA の
		// `/@<username>` route に流すと「アカウントが見つかりません」UI が
		// 出るが、upstream 仕様 (`isSuspended: false` filter) と合わせて
		// backend 側で明示的に止める (#691 review)。
		if (bundle.User.Host == nil || *bundle.User.Host == "") && bundle.User.IsSuspended {
			return c.NoContent(http.StatusNotFound)
		}
		// browser は username 付き permalink に redirect。
		acct := "/@" + bundle.User.Username
		if bundle.User.Host != nil && *bundle.User.Host != "" {
			acct += "@" + *bundle.User.Host
		}
		return c.Redirect(http.StatusFound, acct)
	}

	// リモートユーザーへのリダイレクト相当は将来対応
	if bundle.User.Host != nil {
		return c.NoContent(http.StatusNotFound)
	}
	keypair, err := h.keypairRepo.FindByUserID(bundle.User.ID)
	if err != nil {
		return c.NoContent(http.StatusInternalServerError)
	}
	person := h.renderer.RenderPerson(bundle.User, bundle.Profile, keypair.PublicKey, h.lookupEd25519PublicKey(bundle.User.ID))
	return writeActivityJSON(c, person)
}

// UserByAcct handles GET /@:acct with ActivityPub content negotiation.
// When the Accept header prefers application/activity+json (which is how
// other AP implementations resolve actors that they discovered via
// WebFinger or a raw link), return the Person document. Otherwise
// delegate to the non-AP fallback so browser reloads render the SPA
// frontend instead of a bare 404.
func (h *Handler) UserByAcct(c echo.Context) error {
	if !wantsActivityJSON(c.Request().Header.Get("Accept")) {
		return h.serveNonAP(c)
	}
	acct := c.Param("acct")
	// /@alice or /@alice@host 形式。ローカルのみ扱う。
	username := acct
	if idx := strings.Index(acct, "@"); idx >= 0 {
		username = acct[:idx]
	}
	bundle, err := h.userService.ShowByUsername(username, nil)
	if err != nil {
		return c.NoContent(http.StatusNotFound)
	}
	if bundle.User.Host != nil {
		return c.NoContent(http.StatusNotFound)
	}
	keypair, err := h.keypairRepo.FindByUserID(bundle.User.ID)
	if err != nil {
		return c.NoContent(http.StatusInternalServerError)
	}
	person := h.renderer.RenderPerson(bundle.User, bundle.Profile, keypair.PublicKey, h.lookupEd25519PublicKey(bundle.User.ID))
	return writeActivityJSON(c, person)
}

// writeActivityJSON serializes v and writes it with the ActivityPub
// content type. Remote implementations (Misskey, Mastodon, ...) check
// Content-Type before treating a response as an AP document, so a plain
// application/json would cause them to reject it.
func writeActivityJSON(c echo.Context, v any) error {
	return c.Blob(http.StatusOK, `application/activity+json; charset=utf-8`, mustMarshal(v))
}

// wantsActivityJSON reports whether the caller prefers an AP document.
// Any occurrence of application/activity+json or application/ld+json in
// the Accept header is treated as a positive signal.
func wantsActivityJSON(accept string) bool {
	return strings.Contains(accept, "application/activity+json") ||
		strings.Contains(accept, "application/ld+json")
}

// APIGet handles POST /api/ap/get — Admin専用。URIからActivityPubオブジェクトを取得。
func (h *Handler) APIGet(c echo.Context) error {
	var req struct {
		URI string `json:"uri"`
	}
	if err := c.Bind(&req); err != nil || req.URI == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "uri is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	// ローカルURIからオブジェクトを解決
	obj, err := h.resolveLocal(req.URI)
	if err == nil {
		return c.JSON(http.StatusOK, obj)
	}

	// リモートフェッチ
	if h.remoteFetcher != nil {
		data, err := h.remoteFetcher.FetchObject(req.URI)
		if err == nil {
			var parsed map[string]any
			if json.Unmarshal(data, &parsed) == nil {
				return c.JSON(http.StatusOK, parsed)
			}
		}
	}

	return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_OBJECT", "No such object.", "dc94d745-1262-4e63-a17d-fecaa57efc82"))
}

// APIShow handles POST /api/ap/show — URIからUser/Noteを解決して返す。
func (h *Handler) APIShow(c echo.Context) error {
	var req struct {
		URI string `json:"uri"`
	}
	if err := c.Bind(&req); err != nil || req.URI == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "uri is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	// ローカルのノートURIかチェック (/notes/ を含む)
	if noteID := extractLocalID(req.URI, "/notes/"); noteID != "" {
		n, err := h.queryService.Show(nil, noteID)
		if err == nil && n.UserHost == nil {
			return c.JSON(http.StatusOK, map[string]any{
				"type":   "Note",
				"object": packNoteForAPI(n),
			})
		}
	}

	// ローカルのユーザーURIかチェック (/users/ を含む)
	if userID := extractLocalID(req.URI, "/users/"); userID != "" {
		bundle, err := h.userService.ShowByID(userID)
		if err == nil && bundle.User.Host == nil {
			return c.JSON(http.StatusOK, map[string]any{
				"type":   "User",
				"object": packUserForAPI(bundle.User),
			})
		}
	}

	// リモートオブジェクトをフェッチしてType判定
	// Note の場合は ResolveNote でローカルDBに取り込み、local ID を返す。
	// これにより後続の notes/reactions/create などが local note を見つけられる。
	if h.remoteFetcher != nil {
		if data, err := h.remoteFetcher.FetchObject(req.URI); err == nil {
			var parsed map[string]any
			if json.Unmarshal(data, &parsed) == nil {
				t, _ := parsed["type"].(string)
				switch t {
				case "Note", "Article", "Question":
					if h.remoteResolver != nil {
						if remoteNote, err := h.remoteResolver.ResolveNote(req.URI); err == nil {
							return c.JSON(http.StatusOK, map[string]any{
								"type":   "Note",
								"object": packNoteForAPI(remoteNote),
							})
						}
					}
					// Resolver 無し or ResolveNote 失敗時は raw AP JSON を返す
					return c.JSON(http.StatusOK, map[string]any{
						"type":   "Note",
						"object": parsed,
					})
				case "Person", "Service", "Application", "Organization", "Group":
					if h.remoteResolver != nil {
						if remoteUser, err := h.remoteResolver.ResolveActor(req.URI); err == nil {
							return c.JSON(http.StatusOK, map[string]any{
								"type":   "User",
								"object": packUserForAPI(remoteUser),
							})
						}
					}
				}
			}
		}
	}

	// フェッチ失敗 or Type 不明の場合は ResolveActor を試す (webfinger 経由の
	// /@user URL に対する fetch が失敗するケースがあるため)。
	if h.remoteResolver != nil {
		if remoteUser, err := h.remoteResolver.ResolveActor(req.URI); err == nil {
			return c.JSON(http.StatusOK, map[string]any{
				"type":   "User",
				"object": packUserForAPI(remoteUser),
			})
		}
	}

	return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_OBJECT", "No such object.", "dc94d745-1262-4e63-a17d-fecaa57efc82"))
}

// resolveLocal attempts to resolve a local URI to an AP object.
func (h *Handler) resolveLocal(uri string) (any, error) {
	if noteID := extractLocalID(uri, "/notes/"); noteID != "" {
		n, err := h.queryService.Show(nil, noteID)
		if err != nil {
			return nil, err
		}
		return h.renderer.RenderNote(n, h.idGen), nil
	}
	if userID := extractLocalID(uri, "/users/"); userID != "" {
		bundle, err := h.userService.ShowByID(userID)
		if err != nil {
			return nil, err
		}
		keypair, err := h.keypairRepo.FindByUserID(bundle.User.ID)
		if err != nil {
			return nil, err
		}
		return h.renderer.RenderPerson(bundle.User, bundle.Profile, keypair.PublicKey, h.lookupEd25519PublicKey(bundle.User.ID)), nil
	}
	return nil, http.ErrNotSupported
}

func extractLocalID(uri, pathPrefix string) string {
	// URI末尾の /notes/{id} や /users/{id} からIDを抽出
	idx := len(uri) - 1
	for idx >= 0 && uri[idx] != '/' {
		idx--
	}
	if idx < 0 {
		return ""
	}
	// pathPrefixが含まれているか確認
	prefixIdx := -1
	for i := 0; i+len(pathPrefix) <= len(uri); i++ {
		if uri[i:i+len(pathPrefix)] == pathPrefix {
			prefixIdx = i
			break
		}
	}
	if prefixIdx < 0 {
		return ""
	}
	id := uri[prefixIdx+len(pathPrefix):]
	// フラグメント (#main-key 等) を除去
	if i := strings.IndexByte(id, '#'); i >= 0 {
		id = id[:i]
	}
	return id
}

func packNoteForAPI(n *model.Note) map[string]any {
	result := map[string]any{
		"id":         n.ID,
		"text":       n.Text,
		"userId":     n.UserID,
		"visibility": n.Visibility,
	}
	if n.User != nil {
		result["user"] = packUserForAPI(n.User)
	}
	return result
}

func packUserForAPI(u *model.User) map[string]any {
	if u == nil {
		return map[string]any{}
	}
	return map[string]any{
		"id":       u.ID,
		"username": u.Username,
		"name":     u.Name,
		"host":     u.Host,
	}
}

// Note handles GET /notes/:id with ActivityPub content negotiation.
// AP clients receive the AS Note object; browser reloads are handed
// off to the frontend fallback so the SPA renders the note permalink.
func (h *Handler) Note(c echo.Context) error {
	if !wantsActivityJSON(c.Request().Header.Get("Accept")) {
		return h.serveNonAP(c)
	}
	noteID := c.Param("id")
	// 公開ノートのみAPでフェッチ可能 (非ログインから取得されるため viewer=nil)
	n, err := h.queryService.Show(nil, noteID)
	if err != nil {
		return c.NoContent(http.StatusNotFound)
	}
	// リモートノートはホスト元へリダイレクトすべきだが現状は404
	if n.UserHost != nil {
		return c.NoContent(http.StatusNotFound)
	}
	note := h.renderer.RenderNote(n, h.idGen)
	return writeActivityJSON(c, note)
}
