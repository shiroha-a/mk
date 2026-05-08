package users

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	"github.com/shiroha-a/mk/internal/core/notesfilter"
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
	emojiRepo            repository.EmojiRepository
	bufReader            entity.BufferedReactionsReader
	userListFavoriteRepo UserListFavoriteRepository
	userListRepo         repository.UserListRepository
	clipRepo             repository.ClipRepository
	flashRepo            repository.FlashRepository
	galleryRepo          repository.GalleryRepository
	pageRepo             repository.PageRepository
	piningRepo           repository.UserNotePiningRepository
	fieldRes             *entity.NoteFieldResolver
	// userRepo は users/notes / users/search-by-username-and-host 経由で
	// 表示する note list の hardMutedWords filter (#787) に使う。
	userRepo repository.UserRepository
	// noteReactionRepo は users/reactions endpoint で reactor 視点の reaction
	// list を取得するために使う (#821 PR-D)。
	noteReactionRepo repository.NoteReactionRepository
	// remoteStatsFetcher は remote user の users/show で notesCount /
	// followersCount / followingCount を origin instance から取得して上書き
	// 表示するための fetcher (#943)。nil なら local 観測値を fallback。
	remoteStatsFetcher RemoteStatsFetcher
}

// RemoteStatsFetcher abstracts the federation.RemoteStatsFetcher so wiring is
// nil-tolerant (= test handler doesn't need a real federation package).
type RemoteStatsFetcher interface {
	Fetch(ctx context.Context, host, username string) *RemoteUserStatsView
}

// RemoteUserStatsView mirrors federation.RemoteUserStats. 同型を package に
// import せず interface 経由で渡せるようにするための view 構造体。
type RemoteUserStatsView struct {
	NotesCount     int
	FollowersCount int
	FollowingCount int
}

// SetRemoteStatsFetcher wires the remote stats fetcher (#943).
func (h *Handler) SetRemoteStatsFetcher(f RemoteStatsFetcher) {
	h.remoteStatsFetcher = f
}

// SetUserRepo wires a UserRepository so users/notes filters out notes that
// match the viewer's hardMutedWords (#787) and so users/reactions can run
// the IS_REMOTE_USER / REACTIONS_NOT_PUBLIC access control (#821 PR-D).
// 本番では必ず wire される (router.go で setupRoutes 時に呼ばれる)。
// unwired の場合 users/reactions は access control を skip して
// noteReactionRepo の有無に応じて空配列か list を返す test 互換 path に
// 落ちる (= production 影響なし、test 用 fall-through)。
func (h *Handler) SetUserRepo(r repository.UserRepository) {
	h.userRepo = r
}

// SetNoteReactionRepo wires the NoteReactionRepository for users/reactions
// (= reactor 視点の reaction list、#821 PR-D)。
func (h *Handler) SetNoteReactionRepo(r repository.NoteReactionRepository) {
	h.noteReactionRepo = r
}

// SetNoteFieldResolver wires the shared resolver that fills Files /
// MyReaction / Channel on packed notes including embedded Renote / Reply
// (#426)。
func (h *Handler) SetNoteFieldResolver(r *entity.NoteFieldResolver) {
	h.fieldRes = r
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

// SetEmojiRepo attaches an EmojiRepository for custom emoji resolution.
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

// instanceLookup adapts instanceRepo to entity.InstanceLookup. Returns nil
// when no repo is wired (entity.PackNotes treats nil as "skip Instance").
func (h *Handler) instanceLookup() entity.InstanceLookup {
	if h.instanceRepo == nil {
		return nil
	}
	return h.instanceRepo
}

// emojiLookup adapts emojiRepo to entity.EmojiLookup. Returns nil
// when no repo is wired (entity.PackNotes treats nil as "skip Emoji").
func (h *Handler) emojiLookup() entity.EmojiLookup {
	if h.emojiRepo == nil {
		return nil
	}
	return h.emojiRepo
}

// populateUserEmojis resolves custom emoji names in user.Emojis to URLs and
// sets lite.Emojis. PackNotes経由でなくUserLite/UserDetailedを直接返す
// パス (users/show等) で使用する。
func (h *Handler) populateUserEmojis(u *model.User, lite *entity.UserLite) {
	if h.emojiRepo == nil || u == nil || lite == nil || len(u.Emojis) == 0 {
		return
	}
	emojis, err := h.emojiRepo.FindManyByNamesAndHost(u.Emojis, u.Host)
	if err != nil || len(emojis) == 0 {
		return
	}
	m := make(map[string]string, len(emojis))
	for _, e := range emojis {
		url := e.PublicURL
		if url == "" {
			url = e.OriginalURL
		}
		m[e.Name] = url
	}
	lite.Emojis = m
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
		// 旧実装は ShowByID をループで呼んで最大 200 round-trip の N+1 を
		// 出していた (#503)。ShowManyByIDs は 2 batch query で済む。
		// instance は #277 と同じく 1 回の batch で resolve する。
		bundles, err := h.userService.ShowManyByIDs(req.UserIDs)
		if err != nil {
			return apierr.JSONInternalError(c)
		}
		users := make([]*model.User, 0, len(bundles))
		for _, b := range bundles {
			users = append(users, b.User)
		}
		resolver := entity.NewInstanceResolver(h.instanceLookup(), users...)
		out := make([]entity.UserLite, 0, len(bundles))
		for _, b := range bundles {
			lite := entity.PackUserLite(b.User)
			resolver.FillUserLite(&lite)
			h.populateUserEmojis(b.User, &lite)
			out = append(out, lite)
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

	// 認証済み viewer は pinned note の myReaction (#426) と viewer 依存
	// フィールド取得 (下方ブロック) で再利用するため一度だけ取り出す。匿名なら nil。
	viewer := middleware.GetUser(c)

	// チャート集計はベストエフォート。匿名訪問者は visitor key として
	// リモートホスト名を使う (簡易実装; 認証済みなら viewer id を渡す)。
	if h.chartHook != nil {
		viewerID := ""
		visitorKey := ""
		if viewer != nil {
			viewerID = viewer.ID
		} else {
			visitorKey = c.Request().RemoteAddr
		}
		h.chartHook.OnUserShow(bundle.User.ID, viewerID, visitorKey)
	}

	detailed := entity.PackUserDetailed(bundle.User, bundle.Profile, h.idGen)

	// remote user の場合は origin instance の /api/users/show から実際の counts
	// を取得して上書きする (#943)。Misskey TS は自インスタンス観測値のみ集計する
	// 仕様で remote user の数値が実体より小さく出るが、本拡張で「リモートサーバー
	// 上の実値」を表示する。失敗時は local 観測値 (PackUserDetailed の値) を
	// fallback として残す。
	if h.remoteStatsFetcher != nil && bundle.User.Host != nil && *bundle.User.Host != "" {
		if stats := h.remoteStatsFetcher.Fetch(c.Request().Context(), *bundle.User.Host, bundle.User.Username); stats != nil {
			detailed.NotesCount = stats.NotesCount
			detailed.FollowersCount = stats.FollowersCount
			detailed.FollowingCount = stats.FollowingCount
		}
	}

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

	// ユーザーdisplayNameのカスタム絵文字を解決 (#330)
	h.populateUserEmojis(bundle.User, &detailed.UserLite)

	// Phase 7-3 (#245): ピン止めnote / ピン止めpage を埋める。viewer は
	// pinned note の myReaction を埋めるのに使う (#426)。
	h.fillPinned(c.Request().Context(), viewer, bundle.User, bundle.Profile, &detailed)

	// viewerがログインしている場合、viewer依存フィールドを並列取得する。
	// 各リポジトリへのクエリは完全に独立しているためgoroutineで並列実行し、
	// 全結果が揃ってからdetailedに反映する。
	if viewer != nil && viewer.ID != bundle.User.ID {
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
	Query string `json:"query"`
	Limit int    `json:"limit"`
	// Origin は upstream Misskey TS と同じ enum: "local" / "remote" /
	// "combined" (default)。空 / 不明値は "combined" 扱い (#763)。
	Origin string `json:"origin"`
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
	// origin の enum 検証 (#763)。upstream Misskey TS は paramDef の
	// JSON schema で 不正値を 400 reject する。mk-go も同等にする。
	// 空文字列は default (combined) として許容。
	switch req.Origin {
	case "":
		req.Origin = repository.SearchOriginCombined
	case repository.SearchOriginLocal, repository.SearchOriginRemote, repository.SearchOriginCombined:
		// OK
	default:
		return apierr.JSONInvalidParam(c)
	}

	users, err := h.userService.Search(req.Query, req.Limit, req.Offset, req.Origin)
	if err != nil {
		return apierr.JSONInternalError(c)
	}

	// users/search が検索結果 N 件ぶん per-row GetProfile を呼んでいた N+1 を
	// 1 batch query に置換する (#517)。Profile が見つからない user は
	// PackUserDetailed が nil profile を許容するのでそのまま渡る。
	ids := make([]string, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	profiles := h.userService.GetProfilesByUserIDs(ids)

	resolver := entity.NewInstanceResolver(h.instanceLookup(), users...)
	out := make([]entity.UserDetailed, 0, len(users))
	for _, u := range users {
		d := entity.PackUserDetailed(u, profiles[u.ID], h.idGen)
		resolver.FillUserLite(&d.UserLite)
		h.populateUserEmojis(u, &d.UserLite)
		out = append(out, d)
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

	viewer := middleware.GetUser(c)
	notes = notesfilter.ApplyHardMute(h.userRepo, viewer, notes)
	out := entity.PackNotes(c.Request().Context(), notes, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
	h.fieldRes.Apply(out, viewer)
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
	return h.packRelationItems(rows, req, true), nil
}

func (h *Handler) collectFollowing(req FollowersRequest) ([]relationItem, error) {
	rows, err := h.followingService.ListSentFollowing(req.UserID, req.Limit, req.Offset)
	if err != nil {
		return nil, err
	}
	return h.packRelationItems(rows, req, false), nil
}

// packRelationItems builds the response slice for users/followers and
// users/following. followers=true means embed the follower side, false means
// embed the followee side. cursor (sinceId/untilId) で filter したあとに
// ShowManyByIDs (#503) で 1 batch query にまとめ、map で O(1) 解決して
// 旧 ShowByID per-row N+1 を解消する (#300 2-3)。instance は引き続き
// batch 1 回で resolve する (#277)。
func (h *Handler) packRelationItems(
	rows []*model.Following,
	req FollowersRequest,
	followers bool,
) []relationItem {
	filtered := make([]*model.Following, 0, len(rows))
	idSet := make(map[string]struct{}, len(rows))
	for _, f := range rows {
		if req.SinceID != "" && f.ID <= req.SinceID {
			continue
		}
		if req.UntilID != "" && f.ID >= req.UntilID {
			continue
		}
		filtered = append(filtered, f)
		var target string
		if followers {
			target = f.FollowerID
		} else {
			target = f.FolloweeID
		}
		if target != "" {
			idSet[target] = struct{}{}
		}
	}

	bundleByID := make(map[string]*user.UserWithProfile, len(idSet))
	if len(idSet) > 0 {
		ids := make([]string, 0, len(idSet))
		for id := range idSet {
			ids = append(ids, id)
		}
		if bundles, err := h.userService.ShowManyByIDs(ids); err == nil {
			for _, b := range bundles {
				bundleByID[b.User.ID] = b
			}
		}
	}

	remoteUsers := make([]*model.User, 0, len(bundleByID))
	for _, b := range bundleByID {
		remoteUsers = append(remoteUsers, b.User)
	}
	resolver := entity.NewInstanceResolver(h.instanceLookup(), remoteUsers...)

	out := make([]relationItem, 0, len(filtered))
	for _, f := range filtered {
		item := relationItem{ID: f.ID, FollowerID: f.FollowerID, FolloweeID: f.FolloweeID}
		var target string
		if followers {
			target = f.FollowerID
		} else {
			target = f.FolloweeID
		}
		if b, ok := bundleByID[target]; ok {
			d := entity.PackUserDetailed(b.User, b.Profile, h.idGen)
			resolver.FillUserLite(&d.UserLite)
			h.populateUserEmojis(b.User, &d.UserLite)
			if followers {
				item.Follower = &d
			} else {
				item.Followee = &d
			}
		}
		out = append(out, item)
	}
	return out
}

// fillPinned populates PinnedNoteIDs / PinnedNotes / PinnedPageID / PinnedPage
// on the passed UserDetailed from the user's user_note_pining rows and
// user_profile.pinnedPageId. Missing repos fall back to default empty/nil.
//
// viewer は users/show を叩いている認証ユーザー (匿名なら nil)。pinned note
// の myReaction を埋めるために fieldRes.Apply に流す (#426)。
func (h *Handler) fillPinned(ctx context.Context, viewer *model.User, u *model.User, profile *model.UserProfile, detailed *entity.UserDetailed) {
	if h.piningRepo != nil {
		if pinings, err := h.piningRepo.ListByUser(u.ID); err == nil && len(pinings) > 0 {
			ids := make([]string, 0, len(pinings))
			for _, p := range pinings {
				ids = append(ids, p.NoteID)
			}
			detailed.PinnedNoteIDs = ids
			if h.noteRepo != nil {
				if notes, err := h.noteRepo.FindManyByIDsWithUser(ids); err == nil {
					entities := entity.PackNotes(ctx, notes, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
					h.fieldRes.Apply(entities, viewer)
					packed := make([]any, 0, len(entities))
					for _, pn := range entities {
						packed = append(packed, pn)
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
