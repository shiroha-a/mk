package notes

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/pagination"
	"github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/core/notesfilter"
	"github.com/shiroha-a/mk/internal/core/poll"
	"github.com/shiroha-a/mk/internal/core/reaction"
	"github.com/shiroha-a/mk/internal/core/search"
	"github.com/shiroha-a/mk/internal/core/timeline"
	"github.com/shiroha-a/mk/internal/core/translate"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Handler handles note-related API endpoints.
type Handler struct {
	noteRepo          repository.NoteRepository
	createService     *note.CreateService
	deleteService     *note.DeleteService
	queryService      *note.QueryService
	timelineService   *timeline.Service
	reactionService   *reaction.Service
	pollService       *poll.Service
	searchService     *search.Service
	idGen             id.Generator
	favoriteRepo      repository.NoteFavoriteRepository
	driveFileRepo     repository.DriveFileRepository
	draftRepo         repository.NoteDraftRepository
	noteReactionRepo  repository.NoteReactionRepository
	channelRepo       repository.ChannelRepository
	channelMutingRepo repository.ChannelMutingRepository
	mutingRepo        repository.MutingRepository
	renoteMutingRepo  repository.RenoteMutingRepository
	instanceRepo      repository.InstanceRepository
	emojiRepo         repository.EmojiRepository
	driveFolderRepo   repository.DriveFolderRepository
	userRepo          repository.UserRepository
	userListRepo      repository.UserListRepository
	bufReader         entity.BufferedReactionsReader
	// ugcVisibility controls what unauthenticated visitors can see.
	// "all" (default), "local", "none"
	ugcVisibility string
	translator    *translate.DeepLClient
	// policyProvider は LocalTimeline / GlobalTimeline / HybridTimeline で
	// ltlAvailable / gtlAvailable policy を gate するために使う (#1026)。
	// 匿名 viewer (userID="") に対しても base policies を返す upstream 互換
	// semantics で、middleware ではなく handler 内で gate する。
	policyProvider TimelinePolicyProvider
	// scheduledNoteEnqueuer は drafts/create で `isActuallyScheduled=true`
	// の draft を delayed queue に enqueue するための narrow interface (#1040)。
	// nil 時は enqueue を skip する (= test fixture / queue 未配線パス互換)。
	scheduledNoteEnqueuer ScheduledNoteEnqueuer
	// timelineCache は experiment (MK_TIMELINE_JSON_CACHE) 有効時のみ non-nil。
	// first-page timeline 応答を per-viewer で短期キャッシュする。
	timelineCache *timelineJSONCache
}

// EnableTimelineJSONCache turns on the first-page timeline response cache with
// the given TTL. 実験フラグ MK_TIMELINE_JSON_CACHE 経由で server 配線から呼ぶ。
func (h *Handler) EnableTimelineJSONCache(ttl time.Duration) {
	h.timelineCache = newTimelineJSONCache(ttl)
}

// ScheduledNoteEnqueuer abstracts the delayed enqueue path used by
// drafts/create. Implemented by *queue.Client (= EnqueuePostScheduledNote)。
// SupportsScheduledNote returns whether the underlying driver provides
// reliable schedule cancel (= mkq yes, asynq no) so handler can degrade
// to "feature disabled" on asynq (#1045 Phase 2-C)。
type ScheduledNoteEnqueuer interface {
	EnqueuePostScheduledNote(payload queue.PostScheduledNotePayload, opts ...driver.EnqueueOption) error
	ClearScheduledNote(draftID string) error
	SupportsScheduledNote() bool
}

// SetScheduledNoteEnqueuer wires a delayed queue client used by drafts/create
// when the request asks for a scheduled publish (#1040). nil disables the
// enqueue path (= test fixture / unwired)。
func (h *Handler) SetScheduledNoteEnqueuer(e ScheduledNoteEnqueuer) {
	h.scheduledNoteEnqueuer = e
}

// TimelinePolicyProvider abstracts the role-policy lookup used by timeline
// gating. core/role.Service が実装する。匿名 viewer (userID="") に対しては
// base policies (DefaultPolicies + meta.policies) を返す upstream 互換挙動
// を要求する (#1026)。
type TimelinePolicyProvider interface {
	GetUserPolicies(userID string) map[string]any
}

// SetPolicyProvider wires a TimelinePolicyProvider so timeline endpoints
// gate access by ltlAvailable / gtlAvailable role policy (#1026). nil 時は
// gate を skip する (= test 経路 / 旧挙動互換)。
func (h *Handler) SetPolicyProvider(p TimelinePolicyProvider) {
	h.policyProvider = p
}

// SetChannelMutingRepo attaches a ChannelMutingRepository so timeline handlers
// can exclude notes posted to channels the viewer has muted.
func (h *Handler) SetChannelMutingRepo(r repository.ChannelMutingRepository) {
	h.channelMutingRepo = r
}

// SetMutingRepo attaches a MutingRepository so timeline handlers can exclude
// notes whose author / renote-source the viewer has muted (= upstream Misskey
// TS の muting JOIN と同 semantics、#874)。
func (h *Handler) SetMutingRepo(r repository.MutingRepository) {
	h.mutingRepo = r
}

// SetRenoteMutingRepo attaches a RenoteMutingRepository so timeline handlers
// can exclude pure renotes by users the viewer has renote-muted (#903)。
// upstream Misskey TS の generateMutedUserRelatedRenotesQuery と同 semantics。
func (h *Handler) SetRenoteMutingRepo(r repository.RenoteMutingRepository) {
	h.renoteMutingRepo = r
}

// SetTranslator attaches a DeepL translator for /api/notes/translate.
func (h *Handler) SetTranslator(t *translate.DeepLClient) {
	h.translator = t
}

// SetUGCVisibility sets the visitor content visibility policy from meta.
func (h *Handler) SetUGCVisibility(v string) {
	h.ugcVisibility = v
}

// SetDriveFileRepo attaches a DriveFileRepository for file resolution.
func (h *Handler) SetDriveFileRepo(r repository.DriveFileRepository) {
	h.driveFileRepo = r
}

// SetNoteReactionRepo attaches a NoteReactionRepository for myReaction resolution.
func (h *Handler) SetNoteReactionRepo(r repository.NoteReactionRepository) {
	h.noteReactionRepo = r
}

// SetChannelRepo attaches a ChannelRepository for channel resolution.
func (h *Handler) SetChannelRepo(r repository.ChannelRepository) {
	h.channelRepo = r
}

// SetInstanceRepo attaches an InstanceRepository so remote user embeds in
// note responses get their `instance` field populated (#277).
func (h *Handler) SetInstanceRepo(r repository.InstanceRepository) {
	h.instanceRepo = r
}

// SetEmojiRepo attaches an EmojiRepository so custom emoji shortcodes in
// note text and user displayNames get resolved to URLs (#330).
func (h *Handler) SetEmojiRepo(r repository.EmojiRepository) {
	h.emojiRepo = r
}

// SetReactionReader wires a BufferedReactionsReader so PackNote / PackNotes
// can merge in-flight buffered reaction deltas (#647)。enableReactionsBuffering
// が無効なら nil でも構わない。
func (h *Handler) SetReactionReader(r entity.BufferedReactionsReader) {
	h.bufReader = r
}

func (h *Handler) reactionReader() entity.BufferedReactionsReader {
	return h.bufReader
}

// SetDriveFolderRepo attaches a DriveFolderRepository so attached DriveFiles
// in note responses can embed the owning folder (#317).
func (h *Handler) SetDriveFolderRepo(r repository.DriveFolderRepository) {
	h.driveFolderRepo = r
}

// SetUserRepo attaches a UserRepository so attached DriveFiles in note
// responses can embed the owning user (#317).
func (h *Handler) SetUserRepo(r repository.UserRepository) {
	h.userRepo = r
}

// SetUserListRepo attaches a UserListRepository for user-list-timeline.
func (h *Handler) SetUserListRepo(r repository.UserListRepository) {
	h.userListRepo = r
}

// instanceLookup returns the repository as an entity.InstanceLookup, or nil
// when no repo has been wired. Narrowing to the entity interface keeps the
// packer independent from repository details.
func (h *Handler) instanceLookup() entity.InstanceLookup {
	if h.instanceRepo == nil {
		return nil
	}
	return h.instanceRepo
}

// emojiLookup returns the repository as an entity.EmojiLookup, or nil when
// no repo has been wired.
func (h *Handler) emojiLookup() entity.EmojiLookup {
	if h.emojiRepo == nil {
		return nil
	}
	return h.emojiRepo
}

// NewHandler creates a new notes Handler.
// queryService/timelineService/reactionService/pollService/searchService が
// nil の場合、それぞれの依存に対応するエンドポイントは利用不可となる
// (テストで一部だけ初期化する用途を許容する)。
func NewHandler(
	noteRepo repository.NoteRepository,
	createService *note.CreateService,
	deleteService *note.DeleteService,
	queryService *note.QueryService,
	timelineService *timeline.Service,
	reactionService *reaction.Service,
	pollService *poll.Service,
	searchService *search.Service,
	idGen id.Generator,
) *Handler {
	return &Handler{
		noteRepo:        noteRepo,
		createService:   createService,
		deleteService:   deleteService,
		queryService:    queryService,
		timelineService: timelineService,
		reactionService: reactionService,
		pollService:     pollService,
		searchService:   searchService,
		idGen:           idGen,
	}
}

// CreateRequest is the request body for notes/create.
type CreateRequest struct {
	Visibility         string       `json:"visibility"`
	VisibleUserIDs     []string     `json:"visibleUserIds"`
	CW                 *string      `json:"cw"`
	Text               *string      `json:"text"`
	LocalOnly          bool         `json:"localOnly"`
	ReactionAcceptance *string      `json:"reactionAcceptance"`
	FileIDs            []string     `json:"fileIds"`
	ReplyID            *string      `json:"replyId"`
	RenoteID           *string      `json:"renoteId"`
	ChannelID          *string      `json:"channelId"`
	Poll               *PollRequest `json:"poll"`
}

// PollRequest is the poll part of a create note request.
type PollRequest struct {
	Choices      []string `json:"choices"`
	Multiple     bool     `json:"multiple"`
	ExpiresAt    *int64   `json:"expiresAt"`
	ExpiredAfter *int64   `json:"expiredAfter"`
}

// Create handles POST /api/notes/create.
func (h *Handler) Create(c echo.Context) error {
	user := middleware.GetUser(c)

	var req CreateRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}

	in := note.CreateInput{
		User:               user,
		Text:               req.Text,
		CW:                 req.CW,
		Visibility:         model.NoteVisibility(req.Visibility),
		VisibleUserIDs:     req.VisibleUserIDs,
		LocalOnly:          req.LocalOnly,
		ReactionAcceptance: req.ReactionAcceptance,
		FileIDs:            req.FileIDs,
		ReplyID:            req.ReplyID,
		RenoteID:           req.RenoteID,
		ChannelID:          req.ChannelID,
	}

	if req.Poll != nil {
		in.Poll = &note.PollInput{
			Choices:  req.Poll.Choices,
			Multiple: req.Poll.Multiple,
		}
		// Misskey TS API 互換: expiresAt (絶対 unix ms) と expiredAfter (相対 ms)
		// のどちらか / 両方が送られる。frontend の form は「期限なし / 日時指定 /
		// X 後」の 3 択で、相対指定時は expiredAfter のみが入る。両方来た時は
		// expiresAt を優先する (TS PollService と同じ挙動、#690)。
		switch {
		case req.Poll.ExpiresAt != nil:
			t := time.UnixMilli(*req.Poll.ExpiresAt)
			in.Poll.ExpiresAt = &t
		case req.Poll.ExpiredAfter != nil:
			t := time.Now().Add(time.Duration(*req.Poll.ExpiredAfter) * time.Millisecond)
			in.Poll.ExpiresAt = &t
		}
	}

	created, err := h.createService.Create(in)
	if err != nil {
		switch {
		case errors.Is(err, note.ErrNoteContentRequired):
			return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Text, fileIds, or renoteId is required.", apierr.UUIDInvalidParam))
		case errors.Is(err, note.ErrReplyTargetNotFound):
			return apierr.JSONNoSuchReplyTarget(c)
		case errors.Is(err, note.ErrRenoteTargetNotFound):
			return apierr.JSONNoSuchRenoteTarget(c)
		case errors.Is(err, note.ErrCannotReplyToInvisibleNote):
			return apierr.JSONCannotReplyToAnInvisibleNote(c)
		case errors.Is(err, note.ErrCannotRenoteInvisibleNote):
			return apierr.JSONCannotRenoteDueToVisibility(c)
		case errors.Is(err, note.ErrChannelNotFound):
			return apierr.JSONNoSuchChannel(c)
		case errors.Is(err, note.ErrCannotRenoteToAPureRenote):
			return apierr.JSONCannotRenoteToAPureRenote(c)
		case errors.Is(err, note.ErrCannotReplyToAPureRenote):
			return apierr.JSONCannotReplyToAPureRenote(c)
		case errors.Is(err, note.ErrCannotReplyToSpecifiedVisibility):
			return apierr.JSONCannotReplyToSpecifiedVisibilityNoteWithExtendedVisibility(c)
		case errors.Is(err, note.ErrYouHaveBeenBlocked):
			return apierr.JSONYouHaveBeenBlocked(c)
		case errors.Is(err, note.ErrCannotCreateAlreadyExpiredPoll):
			return apierr.JSONCannotCreateAlreadyExpiredPoll(c)
		case errors.Is(err, note.ErrNoSuchFile):
			return apierr.JSONNoSuchFile(c)
		case errors.Is(err, note.ErrCannotRenoteOutsideOfChannel):
			return apierr.JSONCannotRenoteOutsideOfChannel(c)
		case errors.Is(err, note.ErrContainsProhibitedWords):
			return apierr.JSONContainsProhibitedWords(c)
		case errors.Is(err, note.ErrContainsTooManyMentions):
			return apierr.JSONContainsTooManyMentions(c)
		}
		return apierr.JSONInternalError(c)
	}

	packed := entity.PackNoteWithInstance(c.Request().Context(), created, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
	s := []entity.NoteEntity{packed}
	h.fieldResolver().Apply(s, user)
	return c.JSON(http.StatusOK, map[string]any{
		"createdNote": s[0],
	})
}

// ShowRequest is the request body for notes/show.
type ShowRequest struct {
	NoteID string `json:"noteId"`
}

// Show handles POST /api/notes/show.
func (h *Handler) Show(c echo.Context) error {
	var req ShowRequest
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return apierr.JSONInvalidParam(c)
	}

	viewer := middleware.GetUser(c)
	n, err := h.lookupForShow(req.NoteID)
	if err != nil {
		return apierr.JSONNoSuchNote(c)
	}

	packed := entity.PackNoteWithInstance(c.Request().Context(), n, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
	s := []entity.NoteEntity{packed}
	h.fieldResolver().Apply(s, viewer)
	return c.JSON(http.StatusOK, s[0])
}

// lookupForShow fetches the note for the /api/notes/show endpoint.
//
// upstream Misskey TS は ID 指定の notes/show では visibility 違反でも
// note を返す (= ID を既に知っている viewer には公開する設計、#799)。
// drop-in 互換のため mk-go も follow-only / specified note を 200 で
// 返す。timeline / replies / renotes 等の二次経路では引き続き
// requireVisible (CanSeeNote) で filter するので、visibility leak は
// "直接 ID を知っている人にだけ" に留まる。
//
// QueryService が wire されていない場合は ErrNoteNotFound を返して安全
// 側に倒す (= production では router.go で常に wire される)。
func (h *Handler) lookupForShow(noteID string) (*model.Note, error) {
	if h.queryService == nil {
		return nil, note.ErrNoteNotFound
	}
	return h.queryService.ShowForAPI(noteID)
}

// DeleteRequest is the request body for notes/delete.
type DeleteRequest struct {
	NoteID string `json:"noteId"`
}

// Delete handles POST /api/notes/delete.
func (h *Handler) Delete(c echo.Context) error {
	user := middleware.GetUser(c)

	var req DeleteRequest
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return apierr.JSONInvalidParam(c)
	}

	if err := h.deleteService.Delete(user, req.NoteID); err != nil {
		switch {
		case errors.Is(err, note.ErrNoteNotFound):
			// notes/delete は汎用 UUIDNoSuchNote ではなく endpoint 固有 id を使う
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "490be23f-8c1f-4796-819f-94cb4f9d1630"))
		case errors.Is(err, note.ErrNoteAccessDenied):
			return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "You are not the author of this note.", "fe8d7103-0ea8-4ec3-814d-f8b401dc69e9"))
		default:
			return apierr.JSONInternalError(c)
		}
	}

	return c.NoContent(http.StatusNoContent)
}

// listRequest is the common pagination request shared by renotes/replies/children.
type listRequest struct {
	NoteID    string `json:"noteId"`
	Limit     int    `json:"limit"`
	SinceID   string `json:"sinceId"`
	UntilID   string `json:"untilId"`
	SinceDate *int64 `json:"sinceDate"`
	UntilDate *int64 `json:"untilDate"`
}

func (r *listRequest) normalize() {
	if r.Limit <= 0 {
		r.Limit = 10
	}
	if r.Limit > 100 {
		r.Limit = 100
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。serveList から
	// 呼ばれて Renotes / Replies / Children の 3 handler で一括適用される。
	r.SinceID, r.UntilID = id.NormalizeCursor(r.SinceID, r.UntilID, r.SinceDate, r.UntilDate)
}

// Renotes handles POST /api/notes/renotes.
func (h *Handler) Renotes(c echo.Context) error {
	return h.serveList(c, "12908022-2e21-46cd-ba6a-3edaf6093f46", func(viewer *model.User, req listRequest) ([]*model.Note, error) {
		req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
		return h.queryService.ListRenotes(viewer, req.NoteID, req.UntilID, req.SinceID, req.Limit)
	})
}

// Replies handles POST /api/notes/replies.
func (h *Handler) Replies(c echo.Context) error {
	return h.serveList(c, apierr.UUIDNoSuchNote, func(viewer *model.User, req listRequest) ([]*model.Note, error) {
		req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
		return h.queryService.ListReplies(viewer, req.NoteID, req.UntilID, req.SinceID, req.Limit)
	})
}

// Children handles POST /api/notes/children.
func (h *Handler) Children(c echo.Context) error {
	return h.serveList(c, apierr.UUIDNoSuchNote, func(viewer *model.User, req listRequest) ([]*model.Note, error) {
		req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
		return h.queryService.ListChildren(viewer, req.NoteID, req.UntilID, req.SinceID, req.Limit)
	})
}

func (h *Handler) serveList(c echo.Context, noSuchNoteID string, fn func(*model.User, listRequest) ([]*model.Note, error)) error {
	var req listRequest
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return apierr.JSONInvalidParam(c)
	}
	req.normalize()

	viewer := middleware.GetUser(c)
	notes, err := fn(viewer, req)
	if err != nil {
		if errors.Is(err, note.ErrNoteNotFound) {
			// upstream は notes/renotes 等で NO_SUCH_NOTE に endpoint 固有 id を割り当てる
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", noSuchNoteID))
		}
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.packMany(c.Request().Context(), notes, viewer))
}

// SearchRequest is the request body for notes/search.
//
// Misskey 本家と互換のフィールド構成。`sinceDate` / `untilDate` (unix milli)
// が指定されたときは ID generator で対応する note ID に変換し、`sinceId` /
// `untilId` のフォールバックとして使う。`host == "."` はローカル限定検索。
type SearchRequest struct {
	Query     string `json:"query"`
	Limit     int    `json:"limit"`
	SinceID   string `json:"sinceId"`
	UntilID   string `json:"untilId"`
	SinceDate *int64 `json:"sinceDate"`
	UntilDate *int64 `json:"untilDate"`
	UserID    string `json:"userId"`
	ChannelID string `json:"channelId"`
	Host      string `json:"host"`
}

// Search handles POST /api/notes/search.
func (h *Handler) Search(c echo.Context) error {
	var req SearchRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	if h.searchService == nil {
		return apierr.JSONInternalError(c)
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)

	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)

	viewer := middleware.GetUser(c)
	notes, err := h.searchService.SearchNote(
		viewer,
		req.Query,
		search.SearchOpts{
			UserID:    req.UserID,
			ChannelID: req.ChannelID,
			Host:      req.Host,
		},
		search.Pagination{
			UntilID: untilID,
			SinceID: sinceID,
			Limit:   req.Limit,
		},
	)
	if err != nil {
		// 空クエリは invalidParam として返す。
		// それ以外のエラー (DB障害など) はinternalErrorで返す。
		if errors.Is(err, search.ErrEmptyQuery) {
			return apierr.JSONInvalidParam(c)
		}
		// fulltextSearch.provider="none" 時は ErrUnavailable を 400
		// UNAVAILABLE に翻訳する (= upstream Misskey TS と同 shape、#877)。
		if errors.Is(err, search.ErrUnavailable) {
			return c.JSON(http.StatusBadRequest, apierr.Error("UNAVAILABLE", "Search of notes unavailable.", "0b44998d-77aa-4427-80d0-d2c9b8523011"))
		}
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.packMany(c.Request().Context(), notes, viewer))
}

// State handles POST /api/notes/state.
func (h *Handler) State(c echo.Context) error {
	var req ShowRequest
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return apierr.JSONInvalidParam(c)
	}
	viewer := middleware.GetUser(c)
	st, err := h.queryService.State(viewer, req.NoteID)
	if err != nil {
		// 現状QueryService.StateはErrNoteNotFound以外を返さない
		return apierr.JSONNoSuchNote(c)
	}
	return c.JSON(http.StatusOK, st)
}

// ConversationRequest is the request body for notes/conversation.
type ConversationRequest struct {
	NoteID string `json:"noteId"`
	Limit  int    `json:"limit"`
}

// Conversation handles POST /api/notes/conversation.
func (h *Handler) Conversation(c echo.Context) error {
	var req ConversationRequest
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return apierr.JSONInvalidParam(c)
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)

	viewer := middleware.GetUser(c)
	notes, err := h.queryService.Conversation(viewer, req.NoteID, req.Limit)
	if err != nil {
		// 現状QueryService.ConversationはErrNoteNotFound以外を返さない
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "e1035875-9551-45ec-afa8-1ded1fcb53c8"))
	}
	return c.JSON(http.StatusOK, h.packMany(c.Request().Context(), notes, viewer))
}

// BulkShow handles POST /api/notes — bulk note lookup by noteIds.
// visibility チェックを通して非公開ノートの漏洩を防ぐ。
func (h *Handler) BulkShow(c echo.Context) error {
	var req struct {
		NoteIDs []string `json:"noteIds"`
	}
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	if len(req.NoteIDs) == 0 {
		return c.JSON(http.StatusOK, []any{})
	}
	if len(req.NoteIDs) > 100 {
		req.NoteIDs = req.NoteIDs[:100]
	}
	notes, err := h.noteRepo.FindManyByIDsWithUser(req.NoteIDs)
	if err != nil {
		return c.JSON(http.StatusOK, []any{})
	}
	viewer := middleware.GetUser(c)
	// queryService 未配線時は visibility filter を経ずに全 note を返してしまう
	// と、followers / specified visibility のノートが任意の閲覧者に漏洩する。
	// production の router.go では常に wire されるが、wiring が壊れた場合の
	// fail-closed として空配列で返す (#509、#445 / PR #505 と同じ defensive)。
	if h.queryService == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	notes = h.queryService.FilterVisible(viewer, notes)
	return c.JSON(http.StatusOK, h.packMany(c.Request().Context(), notes, viewer))
}

// packMany serializes a list of notes into NoteEntity objects.
// driveFileRepoが設定されている場合、ファイル情報を解決してFilesに含める。
// viewerがnon-nilの場合、myReactionなどのviewer依存フィールドも解決する。
//
// Files / MyReaction / Channel の解決は entity.NoteFieldResolver に切り出して
// あり、他の PackNotes 利用 handler (antennas / users / pinned 等) と共通化
// している (#426)。
//
// 全 list endpoint (TL / Search / Conversation / Mentions / UserList /
// SearchByTag) が本関数を経由するので、hardMutedWords の filter (#787) は
// PackNotes に渡す前に一括で適用する。viewer == nil / userRepo 未配線 /
// 規則無し のいずれも no-op に倒す best-effort 設計。
//
// 注意: filter を意図的に bypass したい admin 系 path (例: モデレーション目的で
// 隠さず全件を見せたい / pinned notes 等) は本関数を経由せず entity.PackNotes
// を直接呼ぶ慣習。pinned (i/show, users/show) と i/favorites は upstream に
// 揃えて既に packMany を経由していない。
func (h *Handler) packMany(ctx context.Context, notes []*model.Note, viewer *model.User) []entity.NoteEntity {
	notes = notesfilter.ApplyHardMute(h.userRepo, viewer, notes)
	out := entity.PackNotes(ctx, notes, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
	h.fieldResolver().Apply(out, viewer)
	return out
}

// fieldResolver assembles the per-handler NoteFieldResolver from already-wired
// repositories. Returns nil-safe (Apply is no-op when none of the repos are
// configured)。
func (h *Handler) fieldResolver() *entity.NoteFieldResolver {
	return entity.NewNoteFieldResolver(
		nilOrDriveFile(h.driveFileRepo),
		nilOrDriveFolder(h.driveFolderRepo),
		nilOrUser(h.userRepo),
		nilOrNoteReaction(h.noteReactionRepo),
		nilOrChannel(h.channelRepo),
		h.idGen,
	)
}

// nilOr* helpers preserve the「未配線時は interface も nil で渡す」契約。
// 直接代入だと typed-nil が non-nil interface になり resolver の lookup nil
// チェックを擦り抜けてしまうため。
func nilOrDriveFile(r repository.DriveFileRepository) entity.DriveFileLookup {
	if r == nil {
		return nil
	}
	return r
}
func nilOrDriveFolder(r repository.DriveFolderRepository) entity.DriveFolderLookup {
	if r == nil {
		return nil
	}
	return r
}
func nilOrUser(r repository.UserRepository) entity.FileOwnerLookup {
	if r == nil {
		return nil
	}
	return r
}
func nilOrNoteReaction(r repository.NoteReactionRepository) entity.NoteReactionLookup {
	if r == nil {
		return nil
	}
	return r
}
func nilOrChannel(r repository.ChannelRepository) entity.ChannelLookup {
	if r == nil {
		return nil
	}
	return r
}
