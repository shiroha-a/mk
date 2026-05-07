package notes

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
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
			return c.JSON(http.StatusNotFound, apierr.NoSuchNote())
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
	NoteID  string `json:"noteId"`
	Limit   int    `json:"limit"`
	SinceID string `json:"sinceId"`
	UntilID string `json:"untilId"`
}

func (r *listRequest) normalize() {
	if r.Limit <= 0 {
		r.Limit = 10
	}
	if r.Limit > 100 {
		r.Limit = 100
	}
}

// Renotes handles POST /api/notes/renotes.
func (h *Handler) Renotes(c echo.Context) error {
	return h.serveList(c, func(viewer *model.User, req listRequest) ([]*model.Note, error) {
		return h.queryService.ListRenotes(viewer, req.NoteID, req.UntilID, req.SinceID, req.Limit)
	})
}

// Replies handles POST /api/notes/replies.
func (h *Handler) Replies(c echo.Context) error {
	return h.serveList(c, func(viewer *model.User, req listRequest) ([]*model.Note, error) {
		return h.queryService.ListReplies(viewer, req.NoteID, req.UntilID, req.SinceID, req.Limit)
	})
}

// Children handles POST /api/notes/children.
func (h *Handler) Children(c echo.Context) error {
	return h.serveList(c, func(viewer *model.User, req listRequest) ([]*model.Note, error) {
		return h.queryService.ListChildren(viewer, req.NoteID, req.UntilID, req.SinceID, req.Limit)
	})
}

func (h *Handler) serveList(c echo.Context, fn func(*model.User, listRequest) ([]*model.Note, error)) error {
	var req listRequest
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return apierr.JSONInvalidParam(c)
	}
	req.normalize()

	viewer := middleware.GetUser(c)
	notes, err := fn(viewer, req)
	if err != nil {
		if errors.Is(err, note.ErrNoteNotFound) {
			return apierr.JSONNoSuchNote(c)
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
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 100 {
		req.Limit = 100
	}

	untilID := req.UntilID
	if untilID == "" && req.UntilDate != nil && h.idGen != nil {
		untilID = h.idGen.Generate(time.UnixMilli(*req.UntilDate))
	}
	sinceID := req.SinceID
	if sinceID == "" && req.SinceDate != nil && h.idGen != nil {
		sinceID = h.idGen.Generate(time.UnixMilli(*req.SinceDate))
	}

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
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 100 {
		req.Limit = 100
	}

	viewer := middleware.GetUser(c)
	notes, err := h.queryService.Conversation(viewer, req.NoteID, req.Limit)
	if err != nil {
		// 現状QueryService.ConversationはErrNoteNotFound以外を返さない
		return apierr.JSONNoSuchNote(c)
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
