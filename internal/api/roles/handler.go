package roles

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/notehide"
	"github.com/shiroha-a/mk/internal/api/userrelation"
	"github.com/shiroha-a/mk/internal/core/notesfilter"
	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// RoleNotesQuery provides a way to fetch notes by role.
type RoleNotesQuery interface {
	ListByRole(roleID string, limit int, sinceID, untilID string) ([]*model.Note, error)
}

// Handler handles public role API endpoints.
type Handler struct {
	roleService  *role.Service
	notesQuery   RoleNotesQuery
	noteRepo     repository.NoteRepository
	idGen        id.Generator
	instanceRepo repository.InstanceRepository
	emojiRepo    repository.EmojiRepository
	bufReader    entity.BufferedReactionsReader
	fieldRes     *entity.NoteFieldResolver
	// userRepo は roles/notes の hardMutedWords filter (#787)。
	userRepo repository.UserRepository
	// mutingRepo / blockingRepo / channelMutingRepo は roles/notes で viewer が
	// mute/block した user の note と mute した channel の note を除外する (#1544)。
	// upstream notes.ts の generateBaseNoteFilteringQuery + channelMuting に対応。
	// 未配線時は該当 dimension の filter skip。
	mutingRepo        repository.MutingRepository
	blockingRepo      repository.BlockingRepository
	channelMutingRepo repository.ChannelMutingRepository
	// relation は roles/users の embed user に viewer 視点の relation block を
	// 付与する (upstream packMany(users, me))。未配線 / 匿名では no-op (#1973)。
	relation userrelation.Repos
}

// SetRelationRepos wires the repositories used to populate viewer-relative
// relation fields on roles/users embedded users (#1973). Unset = relations omitted.
func (h *Handler) SetRelationRepos(r userrelation.Repos) {
	h.relation = r
}

// SetUserRepo wires a UserRepository so roles/notes filters out notes that
// match the viewer's hardMutedWords (#787).
func (h *Handler) SetUserRepo(r repository.UserRepository) {
	h.userRepo = r
}

// SetMuteBlockRepos wires the muting / blocking / channel-muting repositories so
// roles/notes excludes notes from users the viewer muted or who blocked the
// viewer, plus notes in channels the viewer muted (#1544). Unwired repos simply
// disable that filter dimension.
func (h *Handler) SetMuteBlockRepos(
	muting repository.MutingRepository,
	blocking repository.BlockingRepository,
	channelMuting repository.ChannelMutingRepository,
) {
	h.mutingRepo = muting
	h.blockingRepo = blocking
	h.channelMutingRepo = channelMuting
}

// SetNoteFieldResolver wires the shared resolver that fills Files /
// MyReaction / Channel on packed notes (#426)。
func (h *Handler) SetNoteFieldResolver(r *entity.NoteFieldResolver) {
	h.fieldRes = r
}

// NewHandler creates a new roles Handler. idGen is required for note packing
// under /api/roles/notes.
func NewHandler(roleService *role.Service, idGen id.Generator) *Handler {
	return &Handler{roleService: roleService, idGen: idGen}
}

// SetNotesQuery attaches a RoleNotesQuery for the roles/notes endpoint.
func (h *Handler) SetNotesQuery(q RoleNotesQuery) {
	h.notesQuery = q
}

// SetNoteRepo wires a NoteRepository used by the roles/notes renote-nested
// mute/block check (#1630).
func (h *Handler) SetNoteRepo(r repository.NoteRepository) {
	h.noteRepo = r
}

// SetInstanceRepo attaches an InstanceRepository so roles/notes populates
// UserLite.Instance for remote users (#277).
func (h *Handler) SetInstanceRepo(r repository.InstanceRepository) {
	h.instanceRepo = r
}

func (h *Handler) instanceLookup() entity.InstanceLookup {
	if h.instanceRepo == nil {
		return nil
	}
	return h.instanceRepo
}

// SetEmojiRepo attaches an EmojiRepository so custom emoji shortcodes in
// note text and user displayNames get resolved to URLs.
func (h *Handler) SetEmojiRepo(r repository.EmojiRepository) {
	h.emojiRepo = r
}

// SetReactionReader wires a BufferedReactionsReader so PackNote / PackNotes
// can merge in-flight buffered reaction deltas (#647)。
func (h *Handler) SetReactionReader(r entity.BufferedReactionsReader) {
	h.bufReader = r
}

func (h *Handler) reactionReader() entity.BufferedReactionsReader {
	return h.bufReader
}

func (h *Handler) emojiLookup() entity.EmojiLookup {
	if h.emojiRepo == nil {
		return nil
	}
	return h.emojiRepo
}

// List handles POST /api/roles/list.
func (h *Handler) List(c echo.Context) error {
	roles, err := h.roleService.List()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	var result []any
	for _, r := range roles {
		// upstream roles/list は isPublic AND isExplorable の 2 条件で絞る。
		if r.IsPublic && r.IsExplorable {
			result = append(result, h.packRole(r))
		}
	}
	if result == nil {
		result = []any{}
	}
	return c.JSON(http.StatusOK, result)
}

// Show handles POST /api/roles/show.
func (h *Handler) Show(c echo.Context) error {
	var req struct {
		RoleID string `json:"roleId"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	r, err := h.roleService.Show(req.RoleID)
	if err != nil || !r.IsPublic {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "de5502bf-009a-4639-86c1-fec349e46dcb"))
	}
	return c.JSON(http.StatusOK, h.packRole(r))
}

// Users handles POST /api/roles/users.
//
// upstream users.ts: isPublic かつ isExplorable な role の roleAssignments を
// 期限有効分だけ取得し id cursor でページングして [{id, user:UserDetailed}] を
// 返す。!isPublic / !isExplorable は NO_SUCH_ROLE 扱い (#1544)。
func (h *Handler) Users(c echo.Context) error {
	var req struct {
		RoleID    string `json:"roleId"`
		Limit     int    `json:"limit"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	r, err := h.roleService.Show(req.RoleID)
	if err != nil || !r.IsPublic || !r.IsExplorable {
		// upstream は findOneBy({id, isPublic:true, isExplorable:true}) が null
		// なら NO_SUCH_ROLE。!isPublic / !isExplorable も同じ扱い。
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "30aaaee3-4792-48dc-ab0d-cf501a575ac5"))
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	// sinceDate / untilDate を aidx prefix に正規化 (Notes と同 pattern、#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	assigns, err := h.roleService.ListByRole(req.RoleID, untilID, sinceID, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// reporter/assignee と同様に user_profile を 1 batch で解決して UserDetailed を
	// pack する (N+1 回避)。userRepo 未配線時は profile なしで pack する。
	viewerID := ""
	if v := middleware.GetUser(c); v != nil {
		viewerID = v.ID
	}
	profByID := h.assignmentProfiles(assigns)
	out := make([]any, 0, len(assigns))
	for _, a := range assigns {
		if a.User == nil {
			// preload 失敗 (user 削除等) は upstream では userId fallback だが、
			// UserDetailed を組めないため skip する (defensive)。
			continue
		}
		d := entity.PackUserDetailed(a.User, profByID[a.UserID], h.idGen)
		// 認証 viewer には viewer->user の relation block を付与 (匿名/self は no-op、#1973)。
		h.relation.Apply(&d, viewerID, a.User, profByID[a.UserID])
		out = append(out, map[string]any{
			"id":   a.ID,
			"user": d,
		})
	}
	return c.JSON(http.StatusOK, out)
}

// assignmentProfiles batch-fetches the user_profile rows of the assignees so
// roles/users can pack UserDetailed without N+1 (#1544)。userRepo 未配線時は nil。
func (h *Handler) assignmentProfiles(assigns []*model.RoleAssignment) map[string]*model.UserProfile {
	if h.userRepo == nil {
		return nil
	}
	ids := make([]string, 0, len(assigns))
	seen := map[string]struct{}{}
	for _, a := range assigns {
		if a.User == nil {
			continue
		}
		if _, ok := seen[a.UserID]; ok {
			continue
		}
		seen[a.UserID] = struct{}{}
		ids = append(ids, a.UserID)
	}
	if len(ids) == 0 {
		return nil
	}
	profiles, err := h.userRepo.FindProfilesByUserIDs(ids)
	if err != nil {
		return nil
	}
	byID := make(map[string]*model.UserProfile, len(profiles))
	for _, p := range profiles {
		byID[p.UserID] = p
	}
	return byID
}

// Notes handles POST /api/roles/notes.
func (h *Handler) Notes(c echo.Context) error {
	var req struct {
		RoleID    string `json:"roleId"`
		Limit     int    `json:"limit"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	r, err := h.roleService.Show(req.RoleID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "eb70323a-df61-4dd4-ad90-89c83c7cf26e"))
	}
	if !r.IsPublic {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ROLE", "No such role.", "eb70323a-df61-4dd4-ad90-89c83c7cf26e"))
	}
	// upstream notes.ts: role が public でも !isExplorable なら空配列を返す
	// (role の note を explore させない policy)。このガードが無いと非 explorable
	// role の note 一覧が漏れる (#1544)。
	if !r.IsExplorable {
		return c.JSON(http.StatusOK, []any{})
	}

	if h.notesQuery == nil {
		return c.JSON(http.StatusOK, []any{})
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	// sinceDate / untilDate を aidx prefix に正規化 (#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	notes, err := h.notesQuery.ListByRole(req.RoleID, limit, sinceID, untilID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	viewer := middleware.GetUser(c)
	// mute/block/channel-mute/instance-mute filter (#1544 / #1630): upstream
	// notes.ts の generateBaseNoteFilteringQuery + channelMuting に相当。set の
	// ロードに失敗したら fail-closed で 500 を返す (security 項目なので
	// silently leak しない)。
	mbSets, err := notesfilter.LoadMuteBlockSets(viewer, h.mutingRepo, h.blockingRepo, h.channelMutingRepo, h.userRepo)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	notes, err = notesfilter.ApplyMuteBlockChannel(notes, mbSets, h.noteRepo)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	notes = notesfilter.ApplyHardMute(h.userRepo, viewer, notes)
	entities := entity.PackNotes(c.Request().Context(), notes, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
	h.fieldRes.Apply(entities, viewer)
	notehide.HideEmbeds(viewer, entities)
	out := make([]any, 0, len(entities))
	for _, pn := range entities {
		out = append(out, pn)
	}
	return c.JSON(http.StatusOK, out)
}

// packRole builds the misskey_dart RolesListResponse 互換 shape。createdAt /
// updatedAt (非null String) / canEditMembersByModerator (非null bool) /
// usersCount (非null num) を含めないと misskey_dart の RolesListResponse.fromJson
// が cast で落ちる (#1249)。usersCount は mk-go が role member 数を集計していない
// ため 0 固定 (best-effort、非crashing)。
// packRole renders a public role in the upstream-compatible shape. upstream の
// roles/show・list は admin と同じ RoleEntityService.pack を使うため共通 packer
// (entity.PackRole) に統一する。旧実装は target / condFormula / policies /
// preserveAssignmentOnMoveAccount を欠き usersCount を 0 固定していた。
// usersCount は active assignment 数 (role は少数なので per-role count)。
func (h *Handler) packRole(r *model.Role) map[string]any {
	return entity.PackRole(r, h.roleService.CountAssignedUsers(r.ID), h.idGen, role.DefaultPolicies())
}
