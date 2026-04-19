package users

import (
	"errors"
	"net/http"
	"sync"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	"github.com/shiroha-a/mk/internal/core/user"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// ChartHook is invoked after a user/show request resolves so the
// chart subsystem can record the profile pageview and the
// activeUsers Read event. パッケージ間の循環依存を避けるため
// interface で受け取る (実装は core/chart/charthook)。
type ChartHook interface {
	OnUserShow(ownerID, viewerID, visitorKey string)
}

// Handler handles user-related API endpoints.
type Handler struct {
	userService          *user.Service
	followingService     *corefollowing.Service
	noteRepo             repository.NoteRepository
	idGen                id.Generator
	chartHook            ChartHook
	abuseRepo            repository.AbuseReportRepository
	followingRepo        repository.FollowingRepository
	memoRepo             repository.UserMemoRepository
	blockingRepo         repository.BlockingRepository
	mutingRepo           repository.MutingRepository
	renoteMutingRepo     repository.RenoteMutingRepository
	followRequestRepo    repository.FollowRequestRepository
	instanceRepo         repository.InstanceRepository
	userListFavoriteRepo UserListFavoriteRepository
	userListRepo         repository.UserListRepository
	clipRepo             repository.ClipRepository
	flashRepo            repository.FlashRepository
	galleryRepo          repository.GalleryRepository
	pageRepo             repository.PageRepository
	piningRepo           repository.UserNotePiningRepository
}

// SetPiningRepo wires the user_note_pining repository used to fill
// pinnedNoteIds / pinnedNotes on /api/users/show.
func (h *Handler) SetPiningRepo(r repository.UserNotePiningRepository) {
	h.piningRepo = r
}

// SetClipRepo attaches a ClipRepository for users/clips.
func (h *Handler) SetClipRepo(r repository.ClipRepository) { h.clipRepo = r }

// SetFlashRepo attaches a FlashRepository for users/flashs.
func (h *Handler) SetFlashRepo(r repository.FlashRepository) { h.flashRepo = r }

// SetGalleryRepo attaches a GalleryRepository for users/gallery/posts.
func (h *Handler) SetGalleryRepo(r repository.GalleryRepository) { h.galleryRepo = r }

// SetPageRepo attaches a PageRepository for users/pages.
func (h *Handler) SetPageRepo(r repository.PageRepository) { h.pageRepo = r }

// SetMemoRepo attaches a UserMemoRepository for users/update-memo.
func (h *Handler) SetMemoRepo(r repository.UserMemoRepository) {
	h.memoRepo = r
}

// SetFollowingRepo attaches a FollowingRepository for follow relation queries.
func (h *Handler) SetFollowingRepo(r repository.FollowingRepository) {
	h.followingRepo = r
}

// SetBlockingRepo attaches a BlockingRepository for block status queries.
func (h *Handler) SetBlockingRepo(r repository.BlockingRepository) {
	h.blockingRepo = r
}

// SetMutingRepo attaches a MutingRepository for mute status queries.
func (h *Handler) SetMutingRepo(r repository.MutingRepository) {
	h.mutingRepo = r
}

// SetRenoteMutingRepo attaches a RenoteMutingRepository for renote mute status queries.
func (h *Handler) SetRenoteMutingRepo(r repository.RenoteMutingRepository) {
	h.renoteMutingRepo = r
}

// SetFollowRequestRepo attaches a FollowRequestRepository for pending request queries.
func (h *Handler) SetFollowRequestRepo(r repository.FollowRequestRepository) {
	h.followRequestRepo = r
}

// SetInstanceRepo attaches an InstanceRepository for remote user instance info.
func (h *Handler) SetInstanceRepo(r repository.InstanceRepository) {
	h.instanceRepo = r
}

// NewHandler creates a new users Handler.
// followingService, noteRepo, idGen are optional for the bare /show endpoint.
func NewHandler(
	userService *user.Service,
	followingService *corefollowing.Service,
	noteRepo repository.NoteRepository,
	idGen id.Generator,
) *Handler {
	return &Handler{
		userService:      userService,
		followingService: followingService,
		noteRepo:         noteRepo,
		idGen:            idGen,
	}
}

// SetChartHook attaches a ChartHook invoked after Show successfully
// resolves a profile.
func (h *Handler) SetChartHook(c ChartHook) {
	h.chartHook = c
}

// ShowRequest is the request body for users/show.
type ShowRequest struct {
	UserID   *string  `json:"userId"`
	UserIDs  []string `json:"userIds"`
	Username *string  `json:"username"`
	Host     *string  `json:"host"`
}

// Show handles POST /api/users/show.
// TS互換: userIds (配列) が渡された場合は UserLite の配列を返す。
// userId / username が渡された場合は単体 UserDetailed を返す。
func (h *Handler) Show(c echo.Context) error {
	var req ShowRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}

	// userIds が指定されている場合はバルクモード (UsersBulkと同等)。
	// JSON "userIds":[] は空スライス (len==0) として届く。nil はフィールド未指定。
	if req.UserIDs != nil {
		if len(req.UserIDs) > 100 {
			req.UserIDs = req.UserIDs[:100]
		}
		out := make([]entity.UserLite, 0, len(req.UserIDs))
		for _, uid := range req.UserIDs {
			if bundle, err := h.userService.ShowByID(uid); err == nil {
				out = append(out, entity.PackUserLite(bundle.User))
			}
		}
		return c.JSON(http.StatusOK, out)
	}

	if req.UserID == nil && req.Username == nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId or username is required.", apierr.UUIDInvalidParam))
	}

	var (
		bundle *user.UserWithProfile
		err    error
	)
	if req.UserID != nil {
		bundle, err = h.userService.ShowByID(*req.UserID)
	} else {
		bundle, err = h.userService.ShowByUsername(*req.Username, req.Host)
	}

	if err != nil {
		if errors.Is(err, user.ErrFailedToResolveRemoteUser) {
			return apierr.JSONFailedToResolveRemoteUser(c)
		}
		return apierr.JSONNoSuchUser(c)
	}

	// チャート集計はベストエフォート。匿名訪問者は visitor key として
	// リモートホスト名を使う (簡易実装; 認証済みなら viewer id を渡す)。
	if h.chartHook != nil {
		viewerID := ""
		visitorKey := ""
		if viewer := middleware.GetUser(c); viewer != nil {
			viewerID = viewer.ID
		} else {
			visitorKey = c.Request().RemoteAddr
		}
		h.chartHook.OnUserShow(bundle.User.ID, viewerID, visitorKey)
	}

	detailed := entity.PackUserDetailed(bundle.User, bundle.Profile, h.idGen)

	// リモートユーザーの場合、Instance情報を付与
	if bundle.User.Host != nil && h.instanceRepo != nil {
		if inst, err := h.instanceRepo.FindByHost(*bundle.User.Host); err == nil {
			detailed.Instance = &entity.InstanceLite{
				Name:            inst.Name,
				SoftwareName:    inst.SoftwareName,
				SoftwareVersion: inst.SoftwareVersion,
				IconURL:         inst.IconURL,
				FaviconURL:      inst.FaviconURL,
				ThemeColor:      inst.ThemeColor,
			}
		}
	}

	// Phase 7-3 (#245): ピン止めnote / ピン止めpage を埋める。
	h.fillPinned(bundle.User, bundle.Profile, &detailed)

	// viewerがログインしている場合、viewer依存フィールドを並列取得する。
	// 各リポジトリへのクエリは完全に独立しているためgoroutineで並列実行し、
	// 全結果が揃ってからdetailedに反映する。
	if viewer := middleware.GetUser(c); viewer != nil && viewer.ID != bundle.User.ID {
		var (
			isFollowing, isFollowed      bool
			isBlocking, isBlocked        bool
			isMuted, isRenoteMuted       bool
			hasPendingFrom, hasPendingTo bool
			followRec                    *model.Following
			memo                         *model.UserMemo
			wg                           sync.WaitGroup
		)

		if h.followingRepo != nil {
			wg.Add(3)
			go func() { defer wg.Done(); isFollowing, _ = h.followingRepo.Exists(viewer.ID, bundle.User.ID) }()
			go func() { defer wg.Done(); isFollowed, _ = h.followingRepo.Exists(bundle.User.ID, viewer.ID) }()
			go func() { defer wg.Done(); followRec, _ = h.followingRepo.FindByPair(viewer.ID, bundle.User.ID) }()
		}
		if h.blockingRepo != nil {
			wg.Add(2)
			go func() { defer wg.Done(); isBlocking, _ = h.blockingRepo.Exists(viewer.ID, bundle.User.ID) }()
			go func() { defer wg.Done(); isBlocked, _ = h.blockingRepo.Exists(bundle.User.ID, viewer.ID) }()
		}
		if h.mutingRepo != nil {
			wg.Add(1)
			go func() { defer wg.Done(); isMuted, _ = h.mutingRepo.Exists(viewer.ID, bundle.User.ID) }()
		}
		if h.renoteMutingRepo != nil {
			wg.Add(1)
			go func() { defer wg.Done(); isRenoteMuted, _ = h.renoteMutingRepo.Exists(viewer.ID, bundle.User.ID) }()
		}
		if h.followRequestRepo != nil {
			wg.Add(2)
			go func() { defer wg.Done(); hasPendingFrom, _ = h.followRequestRepo.Exists(viewer.ID, bundle.User.ID) }()
			go func() { defer wg.Done(); hasPendingTo, _ = h.followRequestRepo.Exists(bundle.User.ID, viewer.ID) }()
		}
		if h.memoRepo != nil {
			wg.Add(1)
			go func() { defer wg.Done(); memo, _ = h.memoRepo.FindByPair(viewer.ID, bundle.User.ID) }()
		}

		wg.Wait()

		if h.followingRepo != nil {
			detailed.IsFollowing = &isFollowing
			detailed.IsFollowed = &isFollowed
			if followRec != nil {
				detailed.Notify = followRec.Notify
				wr := followRec.WithReplies
				detailed.WithReplies = &wr
			}
		}
		if h.blockingRepo != nil {
			detailed.IsBlocking = &isBlocking
			detailed.IsBlocked = &isBlocked
		}
		if h.mutingRepo != nil {
			detailed.IsMuted = &isMuted
		}
		if h.renoteMutingRepo != nil {
			detailed.IsRenoteMuted = &isRenoteMuted
		}
		if h.followRequestRepo != nil {
			detailed.HasPendingFollowRequestFromYou = &hasPendingFrom
			detailed.HasPendingFollowRequestToYou = &hasPendingTo
		}
		if h.memoRepo != nil && memo != nil {
			detailed.Memo = &memo.Memo
		}
	}

	return c.JSON(http.StatusOK, detailed)
}

// SearchRequest is the request body for users/search.
type SearchRequest struct {
	Query  string `json:"query"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// Search handles POST /api/users/search.
func (h *Handler) Search(c echo.Context) error {
	var req SearchRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}

	users, err := h.userService.Search(req.Query, req.Limit, req.Offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}

	out := make([]entity.UserDetailed, 0, len(users))
	for _, u := range users {
		profile := h.userService.GetProfile(u.ID)
		out = append(out, entity.PackUserDetailed(u, profile))
	}
	return c.JSON(http.StatusOK, out)
}

// NotesRequest is the request body for users/notes.
type NotesRequest struct {
	UserID  string `json:"userId"`
	Limit   int    `json:"limit"`
	SinceID string `json:"sinceId"`
	UntilID string `json:"untilId"`
}

// Notes handles POST /api/users/notes.
func (h *Handler) Notes(c echo.Context) error {
	var req NotesRequest
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 100 {
		req.Limit = 100
	}

	if _, err := h.userService.ShowByID(req.UserID); err != nil {
		return apierr.JSONNoSuchUser(c)
	}

	notes, err := h.noteRepo.ListByUserID(req.UserID, req.UntilID, req.SinceID, req.Limit)
	if err != nil {
		return apierr.JSONInternalError(c)
	}

	out := make([]entity.NoteEntity, 0, len(notes))
	for _, n := range notes {
		out = append(out, entity.PackNote(n, h.idGen))
	}
	return c.JSON(http.StatusOK, out)
}

// FollowersRequest is the request body for users/followers and users/following.
type FollowersRequest struct {
	UserID  string `json:"userId"`
	Limit   int    `json:"limit"`
	Offset  int    `json:"offset"`
	SinceID string `json:"sinceId"`
	UntilID string `json:"untilId"`
}

// Followers handles POST /api/users/followers.
func (h *Handler) Followers(c echo.Context) error {
	return h.listRelations(c, true)
}

// Following handles POST /api/users/following.
func (h *Handler) Following(c echo.Context) error {
	return h.listRelations(c, false)
}

func (h *Handler) listRelations(c echo.Context, followers bool) error {
	var req FollowersRequest
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 100 {
		req.Limit = 100
	}

	if _, err := h.userService.ShowByID(req.UserID); err != nil {
		return apierr.JSONNoSuchUser(c)
	}

	var (
		rows []relationItem
		err  error
	)
	if followers {
		rows, err = h.collectFollowers(req)
	} else {
		rows, err = h.collectFollowing(req)
	}
	if err != nil {
		return apierr.JSONInternalError(c)
	}

	return c.JSON(http.StatusOK, rows)
}

// relationItem represents a single entry in followers/following lists.
type relationItem struct {
	ID         string               `json:"id"`
	FollowerID string               `json:"followerId"`
	FolloweeID string               `json:"followeeId"`
	Follower   *entity.UserDetailed `json:"follower,omitempty"`
	Followee   *entity.UserDetailed `json:"followee,omitempty"`
}

func (h *Handler) collectFollowers(req FollowersRequest) ([]relationItem, error) {
	rows, err := h.followingService.ListReceivedFollowing(req.UserID, req.Limit, req.Offset)
	if err != nil {
		return nil, err
	}
	out := make([]relationItem, 0, len(rows))
	for _, f := range rows {
		// カーソルベースページネーション
		if req.SinceID != "" && f.ID <= req.SinceID {
			continue
		}
		if req.UntilID != "" && f.ID >= req.UntilID {
			continue
		}
		item := relationItem{ID: f.ID, FollowerID: f.FollowerID, FolloweeID: f.FolloweeID}
		if b, err := h.userService.ShowByID(f.FollowerID); err == nil {
			d := entity.PackUserDetailed(b.User, b.Profile)
			item.Follower = &d
		}
		out = append(out, item)
	}
	return out, nil
}

func (h *Handler) collectFollowing(req FollowersRequest) ([]relationItem, error) {
	rows, err := h.followingService.ListSentFollowing(req.UserID, req.Limit, req.Offset)
	if err != nil {
		return nil, err
	}
	out := make([]relationItem, 0, len(rows))
	for _, f := range rows {
		if req.SinceID != "" && f.ID <= req.SinceID {
			continue
		}
		if req.UntilID != "" && f.ID >= req.UntilID {
			continue
		}
		item := relationItem{ID: f.ID, FollowerID: f.FollowerID, FolloweeID: f.FolloweeID}
		if b, err := h.userService.ShowByID(f.FolloweeID); err == nil {
			d := entity.PackUserDetailed(b.User, b.Profile)
			item.Followee = &d
		}
		out = append(out, item)
	}
	return out, nil
}

// fillPinned populates PinnedNoteIDs / PinnedNotes / PinnedPageID / PinnedPage
// on the passed UserDetailed from the user's user_note_pining rows and
// user_profile.pinnedPageId. Missing repos fall back to default empty/nil.
func (h *Handler) fillPinned(u *model.User, profile *model.UserProfile, detailed *entity.UserDetailed) {
	if h.piningRepo != nil {
		if pinings, err := h.piningRepo.ListByUser(u.ID); err == nil && len(pinings) > 0 {
			ids := make([]string, 0, len(pinings))
			for _, p := range pinings {
				ids = append(ids, p.NoteID)
			}
			detailed.PinnedNoteIDs = ids
			if h.noteRepo != nil {
				if notes, err := h.noteRepo.FindManyByIDsWithUser(ids); err == nil {
					packed := make([]any, 0, len(notes))
					for _, n := range notes {
						packed = append(packed, entity.PackNote(n, h.idGen))
					}
					detailed.PinnedNotes = packed
				}
			}
		}
	}

	if profile != nil && profile.PinnedPageID != nil && *profile.PinnedPageID != "" {
		detailed.PinnedPageID = profile.PinnedPageID
		if h.pageRepo != nil {
			if p, err := h.pageRepo.FindByID(*profile.PinnedPageID); err == nil {
				detailed.PinnedPage = entity.PackPage(p, h.idGen)
			}
		}
	}
}
