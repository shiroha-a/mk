package users

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/notehide"
	"github.com/shiroha-a/mk/internal/api/pagination"
	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	"github.com/shiroha-a/mk/internal/core/notesfilter"
	corerole "github.com/shiroha-a/mk/internal/core/role"
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
	// moderatorChecker は users/reactions で viewer が moderator/admin かを
	// 判定し、remote user / 非 public reactions の閲覧制限を bypass するため
	// に使う (upstream の iAmModerator 経路)。
	moderatorChecker ModeratorChecker
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

// ModeratorChecker reports whether a user holds moderator / administrator
// privileges. core/role.Service implements it. Narrowing to this interface
// avoids importing core/role into the handler.
type ModeratorChecker interface {
	IsModerator(userID string) bool
	IsAdministrator(userID string) bool
}

// SetModeratorChecker wires the moderator check used by users/reactions so a
// moderator/admin viewer can see any user's reactions (incl. remote), matching
// upstream's `if (!iAmModerator) { ...isRemoteUser/publicReactions checks }`.
// nil keeps the gate strict (= test fixture / unwired)。
func (h *Handler) SetModeratorChecker(m ModeratorChecker) {
	h.moderatorChecker = m
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

// resolveUserIDByURI resolves an ActivityPub actor URI to a local user ID via
// a local DB lookup only (no remote fetch). Used by UserDetailed.ResolveMoveTargets
// to fill movedTo / alsoKnownAs. Returns ("", false) when unwired or unknown.
func (h *Handler) resolveUserIDByURI(uri string) (string, bool) {
	if h.userRepo == nil {
		return "", false
	}
	u, err := h.userRepo.FindByURI(uri)
	if err != nil || u == nil {
		return "", false
	}
	return u.ID, true
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

	// movedTo / alsoKnownAs を URI→ローカルID 解決して埋める (#1255)。単一
	// ユーザー path なので FindByURI は数回で済む。list path (followers 等) は
	// N+1 を避けるため解決せず null のまま (move banner は profile でのみ表示)。
	detailed.ResolveMoveTargets(bundle.User, h.resolveUserIDByURI)

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

	// viewer===target の self-view では upstream `pack(user, me)` 互換で
	// MeDetailed 拡張 field (isExplorable / noCrawle 等 11 個 + #985 で
	// 追加した notification 3 field) を merge して返す (#970)。これにより
	// frontend が `/api/users/show?username=me` 経由で自分のプロフィールを
	// 開いたとき、`/api/i` 経由の $i と field set を一致させられる。
	if viewer != nil && viewer.ID == bundle.User.ID {
		me := entity.AsMeDetailed(detailed, bundle.User, bundle.Profile)
		// policies は role 依存で entity 層では計算できないため handler で埋める。
		// users/show では権威ソースでないので default policies を best-effort で
		// 入れる (misskey_dart の MeDetailed.fromJson は policies を非null Map と
		// して要求する、#1240)。権威値は /api/i 経由で取得される。
		me.Policies = corerole.DefaultPolicies()
		return c.JSON(http.StatusOK, me)
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
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
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
// withFiles / withReplies / withRenotes / withChannelNotes は upstream の
// paramDef と同じ optional boolean。pointer で受けることで JSON 上の
// "未指定" を upstream paramDef のデフォルトに置き換える経路を確保する
// (#1021)。
type NotesRequest struct {
	UserID           string `json:"userId"`
	Limit            int    `json:"limit"`
	SinceID          string `json:"sinceId"`
	UntilID          string `json:"untilId"`
	SinceDate        *int64 `json:"sinceDate"`
	UntilDate        *int64 `json:"untilDate"`
	WithFiles        *bool  `json:"withFiles"`
	WithReplies      *bool  `json:"withReplies"`
	WithRenotes      *bool  `json:"withRenotes"`
	WithChannelNotes *bool  `json:"withChannelNotes"`
}

// Notes handles POST /api/users/notes.
func (h *Handler) Notes(c echo.Context) error {
	var req NotesRequest
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return apierr.JSONInvalidParam(c)
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)

	if _, err := h.userService.ShowByID(req.UserID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "27e494ba-2ac2-48e8-893b-10d4d8c2387b"))
	}

	// upstream paramDef のデフォルトに合わせる (= withFiles=false /
	// withReplies=true / withRenotes=true / withChannelNotes=false)。
	withFiles := false
	if req.WithFiles != nil {
		withFiles = *req.WithFiles
	}
	withReplies := true
	if req.WithReplies != nil {
		withReplies = *req.WithReplies
	}
	withRenotes := true
	if req.WithRenotes != nil {
		withRenotes = *req.WithRenotes
	}
	withChannelNotes := false
	if req.WithChannelNotes != nil {
		withChannelNotes = *req.WithChannelNotes
	}

	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	viewer := middleware.GetUser(c)
	viewerID := ""
	if viewer != nil {
		viewerID = viewer.ID
	}
	// visibility は repository 側で LIMIT 前に push down する (#1418 review)。
	// post-fetch filter だとページ過少充填 + followers 判定の N+1 になるため。
	notes, err := h.noteRepo.ListByUserIDFiltered(req.UserID, viewerID, untilID, sinceID, req.Limit, withFiles, withReplies, withRenotes, withChannelNotes)
	if err != nil {
		return apierr.JSONInternalError(c)
	}

	notes = notesfilter.ApplyHardMute(h.userRepo, viewer, notes)
	out := entity.PackNotes(c.Request().Context(), notes, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
	h.fieldRes.Apply(out, viewer)
	notehide.HideEmbeds(viewer, out)
	return c.JSON(http.StatusOK, out)
}

// FollowersRequest is the request body for users/followers and users/following.
type FollowersRequest struct {
	UserID    string `json:"userId"`
	Limit     int    `json:"limit"`
	Offset    int    `json:"offset"`
	SinceID   string `json:"sinceId"`
	UntilID   string `json:"untilId"`
	SinceDate *int64 `json:"sinceDate"`
	UntilDate *int64 `json:"untilDate"`
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
		// upstream は users/followers と users/following で NO_SUCH_USER に別 id を割り当てる
		nsuID := "63e4aba4-4156-4e53-be25-c9559e42d71b" // users/following
		if followers {
			nsuID = "27fa5435-88ab-43de-9360-387de88727cd" // users/followers
		}
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", nsuID))
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	req.SinceID, req.UntilID = id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)

	viewer := middleware.GetUser(c)

	// followersVisibility / followingVisibility gate (#1461)。upstream
	// `users/followers.ts` / `users/following.ts` と同等に、target profile の
	// 設定で viewer を gate する。moderator は users/reactions 側と同じく
	// bypass 経路に乗せる (upstream: `!await roleService.isModerator(me)`)。
	// profile 行が無い (= 古いデータ / remote 未同期) 場合は upstream の
	// default 値 'public' を採用して素通りさせる。
	if denied, err := h.gateRelationVisibility(c, req.UserID, viewer, followers); denied {
		return err
	}

	var (
		rows []relationItem
		err  error
	)
	if followers {
		rows, err = h.collectFollowers(c.Request().Context(), req, viewer)
	} else {
		rows, err = h.collectFollowing(c.Request().Context(), req, viewer)
	}
	if err != nil {
		return apierr.JSONInternalError(c)
	}

	return c.JSON(http.StatusOK, rows)
}

// gateRelationVisibility enforces the target user's followersVisibility /
// followingVisibility setting on users/followers and users/following (#1461).
// Returns (denied=true, response error) when the viewer must be blocked, or
// (denied=false, nil) when the request should proceed. followers=true gates
// the "followers" list (uses FollowersVisibility), followers=false gates the
// "following" list (uses FollowingVisibility).
func (h *Handler) gateRelationVisibility(c echo.Context, targetID string, viewer *model.User, followers bool) (bool, error) {
	// upstream forbidden error UUID は endpoint ごとに別 (drop-in 互換維持)。
	forbiddenID := "f6cdb0df-c19f-ec5c-7dbb-0ba84a1f92ba" // users/following
	if followers {
		forbiddenID = "3c6a84db-d619-26af-ca14-06232a21df8a" // users/followers
	}
	deny := func() (bool, error) {
		return true, c.JSON(http.StatusForbidden, apierr.Error("FORBIDDEN", "Forbidden.", forbiddenID))
	}

	vis := model.FollowingVisibilityPublic
	if profile := h.userService.GetProfile(targetID); profile != nil {
		if followers {
			vis = profile.FollowersVisibility
		} else {
			vis = profile.FollowingVisibility
		}
		if vis == "" {
			vis = model.FollowingVisibilityPublic
		}
	}
	if vis == model.FollowingVisibilityPublic {
		return false, nil
	}

	// self / moderator は常に bypass。upstream の
	// `me.id === user.id` / `roleService.isModerator(me)` 経路に対応。
	isSelf := viewer != nil && viewer.ID == targetID
	isMod := viewer != nil && h.moderatorChecker != nil && h.moderatorChecker.IsModerator(viewer.ID)
	if isSelf || isMod {
		return false, nil
	}

	switch vis {
	case model.FollowingVisibilityPrivate:
		return deny()
	case model.FollowingVisibilityFollowers:
		if viewer == nil || h.followingRepo == nil {
			return deny()
		}
		ok, err := h.followingRepo.Exists(viewer.ID, targetID)
		if err != nil {
			return true, apierr.JSONInternalError(c)
		}
		if !ok {
			return deny()
		}
	}
	return false, nil
}

// relationItem represents a single entry in followers/following lists.
type relationItem struct {
	ID string `json:"id"`
	// CreatedAt は misskey_dart の Following.fromJson が非null String として
	// cast するため必須 (#1243)。following row の ID (aidx) から復元する。
	CreatedAt  string               `json:"createdAt"`
	FollowerID string               `json:"followerId"`
	FolloweeID string               `json:"followeeId"`
	Follower   *entity.UserDetailed `json:"follower,omitempty"`
	Followee   *entity.UserDetailed `json:"followee,omitempty"`
}

func (h *Handler) collectFollowers(ctx context.Context, req FollowersRequest, viewer *model.User) ([]relationItem, error) {
	rows, err := h.followingService.ListReceivedFollowing(req.UserID, req.Limit, req.Offset)
	if err != nil {
		return nil, err
	}
	return h.packRelationItems(ctx, rows, req, true, viewer), nil
}

func (h *Handler) collectFollowing(ctx context.Context, req FollowersRequest, viewer *model.User) ([]relationItem, error) {
	rows, err := h.followingService.ListSentFollowing(req.UserID, req.Limit, req.Offset)
	if err != nil {
		return nil, err
	}
	return h.packRelationItems(ctx, rows, req, false, viewer), nil
}

// packRelationItems builds the response slice for users/followers and
// users/following. followers=true means embed the follower side, false means
// embed the followee side. cursor (sinceId/untilId) で filter したあとに
// ShowManyByIDs (#503) で 1 batch query にまとめ、map で O(1) 解決して
// 旧 ShowByID per-row N+1 を解消する (#300 2-3)。instance は引き続き
// batch 1 回で resolve する (#277)。
//
// viewer が non-nil なら follow relation flag (isFollowing / isFollowed) を
// `FilterFollowing` / `FilterFollowedBy` の 2 batch query で埋める (#1144、
// frontend MkUserInfo の `followsYou` ラベル + MkFollowButton の初期 state
// が正しく描画されるのに必要)。viewer が自分自身を含む list 経路でも viewer
// と list 内 user の id 一致時は relation lookup を skip する (= self-flag
// は意味なし)。
func (h *Handler) packRelationItems(
	ctx context.Context,
	rows []*model.Following,
	req FollowersRequest,
	followers bool,
	viewer *model.User,
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

	// viewer 視点の follow relation を 2 batch query で先に解決しておく
	// (#1144)。viewer が nil (= unauthenticated) なら lookup skip して
	// 全 user の flag を nil 維持 (upstream も me が nil の経路では
	// UserDetailedNotMe の relation field を omit する)。
	followingMap, followedMap := h.batchFollowRelations(viewer, bundleByID)
	// pending follow request も同じく 2 batch query で解決 (#1144 #2)。
	// MkFollowButton が `hasPendingFollowRequestFromYou` で表示分岐するため
	// `isFollowing` だけだと「pending 中なのに Follow ボタン」が出る regression
	// になる。upstream UserDetailedNotMe schema と整合させる。
	pendingFromMap, pendingToMap := h.batchPendingRequestRelations(viewer, bundleByID)

	// remote user の notes/followers/following count を origin instance の
	// /api/users/show から fetch して上書き (#1146)。Show 経路 (handler.go:329)
	// と同 logic だが、本 list 経路では N 件並列 fetch で N round-trip を回避
	// する (singleflight が同 key dedup、cache 1h で次 scroll は HTTP 0)。
	remoteStatsMap := h.batchRemoteStatsOverride(ctx, bundleByID)

	out := make([]relationItem, 0, len(filtered))
	for _, f := range filtered {
		item := relationItem{ID: f.ID, FollowerID: f.FollowerID, FolloweeID: f.FolloweeID}
		if t, err := h.idGen.ParseTime(f.ID); err == nil {
			item.CreatedAt = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
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
			if viewer != nil && viewer.ID != b.User.ID {
				isFollowing := followingMap[b.User.ID]
				isFollowed := followedMap[b.User.ID]
				d.IsFollowing = &isFollowing
				d.IsFollowed = &isFollowed
				pendingFrom := pendingFromMap[b.User.ID]
				pendingTo := pendingToMap[b.User.ID]
				d.HasPendingFollowRequestFromYou = &pendingFrom
				d.HasPendingFollowRequestToYou = &pendingTo
				// misskey_dart の UserDetailed union は isFollowing が present だと
				// UserDetailedNotMeWithRelations を選び、isBlocking/isBlocked/
				// isMuted/isRenoteMuted も非null bool として cast する (#1249)。
				// follow/follower list は block/mute の権威ソースではない (#1144 の
				// batch を壊さないため per-user lookup は避ける) ので best-effort
				// false で埋める。正確な値は users/show 単体で取得される。
				d.EnsureRelationFlags()
			}
			if stats := remoteStatsMap[b.User.ID]; stats != nil {
				d.NotesCount = stats.NotesCount
				d.FollowersCount = stats.FollowersCount
				d.FollowingCount = stats.FollowingCount
			}
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

// batchFollowRelations resolves viewer's follow relations against the
// candidate user set in 2 batch queries (followingRepo.FilterFollowings
// FromAnchor / ToAnchor). Returns nil maps if viewer or repo is nil so
// callers can skip safely.
func (h *Handler) batchFollowRelations(viewer *model.User, bundleByID map[string]*user.UserWithProfile) (map[string]bool, map[string]bool) {
	if viewer == nil || h.followingRepo == nil || len(bundleByID) == 0 {
		return nil, nil
	}
	candidates := buildRelationCandidates(viewer, bundleByID)
	if len(candidates) == 0 {
		return nil, nil
	}
	followingMap := make(map[string]bool, len(candidates))
	followedMap := make(map[string]bool, len(candidates))
	if ids, err := h.followingRepo.FilterFollowingsFromAnchor(viewer.ID, candidates); err == nil {
		for _, id := range ids {
			followingMap[id] = true
		}
	}
	if ids, err := h.followingRepo.FilterFollowingsToAnchor(viewer.ID, candidates); err == nil {
		for _, id := range ids {
			followedMap[id] = true
		}
	}
	return followingMap, followedMap
}

// batchPendingRequestRelations resolves viewer's pending follow_request
// relations across the candidate user set in 2 batch queries
// (followRequestRepo.FilterPendingFromAnchor / ToAnchor). Returns nil maps
// if viewer or repo is nil so callers can skip safely.
//
// `hasPendingFollowRequestFromYou` / `hasPendingFollowRequestToYou` を
// list 経路で埋めないと frontend MkFollowButton が「pending 中なのに
// `Follow` ボタン」を表示する drift になる (#1144 #2)。
func (h *Handler) batchPendingRequestRelations(viewer *model.User, bundleByID map[string]*user.UserWithProfile) (map[string]bool, map[string]bool) {
	if viewer == nil || h.followRequestRepo == nil || len(bundleByID) == 0 {
		return nil, nil
	}
	candidates := buildRelationCandidates(viewer, bundleByID)
	if len(candidates) == 0 {
		return nil, nil
	}
	fromMap := make(map[string]bool, len(candidates))
	toMap := make(map[string]bool, len(candidates))
	if ids, err := h.followRequestRepo.FilterPendingFromAnchor(viewer.ID, candidates); err == nil {
		for _, id := range ids {
			fromMap[id] = true
		}
	}
	if ids, err := h.followRequestRepo.FilterPendingToAnchor(viewer.ID, candidates); err == nil {
		for _, id := range ids {
			toMap[id] = true
		}
	}
	return fromMap, toMap
}

// remoteStatsBatchTimeout caps the wall-clock time the list response will
// wait for remote `/api/users/show` Fetches. fetcher が独自の HTTP timeout
// (= safehttp 経由で 10-30s) を持つが、複数 remote host のうち 1 host が
// 遅い場合に list 全体の tail latency が悪化する。本 timeout でその上限を
// 5s に縮め、slow host があれば silent fallback (local count) を維持する
// (#1146 #1)。値は典型 list (limit=20) の cold cache 想定で「20 host の
// HTTPS RTT が並列で 5s 以内に揃う」前提。
const remoteStatsBatchTimeout = 5 * time.Second

// batchRemoteStatsOverride fans out RemoteStatsFetcher.Fetch goroutines for
// every remote user in bundleByID and returns the resulting stats keyed by
// user ID. Local users (host == nil) are skipped. fetcher が未配線 / list
// に remote user 0 件なら nil を返して caller が override loop を skip できる
// (#1146)。
//
// 並列化の妥当性: 各 Fetch は HTTP I/O bound (~500ms RTT) で CPU は遊んで
// いるので、20-100 件並列でも runtime コストは無視可能。同じ (host, username)
// に対する concurrent call は fetcher 内部の singleflight.Group で 1 HTTP に
// dedup される。
//
// ctx propagation: 上位 request ctx に `remoteStatsBatchTimeout` の deadline
// を被せた派生 ctx を全 goroutine に渡し、deadline 超過 / client abort
// どちらでも fetcher 内部の HTTP client が ctx cancel を受けて即時 return
// する (= 取りこぼした user は silent fallback で local count 維持)。
func (h *Handler) batchRemoteStatsOverride(ctx context.Context, bundleByID map[string]*user.UserWithProfile) map[string]*RemoteUserStatsView {
	if h.remoteStatsFetcher == nil || len(bundleByID) == 0 {
		return nil
	}
	type job struct {
		userID, host, username string
	}
	jobs := make([]job, 0, len(bundleByID))
	for id, b := range bundleByID {
		if b.User.Host == nil || *b.User.Host == "" {
			continue
		}
		jobs = append(jobs, job{userID: id, host: *b.User.Host, username: b.User.Username})
	}
	if len(jobs) == 0 {
		return nil
	}
	fetchCtx, cancel := context.WithTimeout(ctx, remoteStatsBatchTimeout)
	defer cancel()
	out := make(map[string]*RemoteUserStatsView, len(jobs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(jobs))
	for _, j := range jobs {
		go func(j job) {
			defer wg.Done()
			stats := h.remoteStatsFetcher.Fetch(fetchCtx, j.host, j.username)
			if stats == nil {
				return
			}
			mu.Lock()
			out[j.userID] = stats
			mu.Unlock()
		}(j)
	}
	wg.Wait()
	return out
}

// buildRelationCandidates returns the candidate user IDs for viewer-relative
// batch relation lookups, excluding viewer's own id (= self-flag has no
// meaning, matches upstream `UserDetailedNotMe` semantics).
func buildRelationCandidates(viewer *model.User, bundleByID map[string]*user.UserWithProfile) []string {
	candidates := make([]string, 0, len(bundleByID))
	for id := range bundleByID {
		if id == viewer.ID {
			continue
		}
		candidates = append(candidates, id)
	}
	return candidates
}

// fillPinned populates PinnedNoteIDs / PinnedNotes / PinnedPageID / PinnedPage
// on the passed UserDetailed from the user's user_note_pining rows and
// user_profile.pinnedPageId. Missing repos fall back to default empty/nil.
//
// viewer は users/show を叩いている認証ユーザー (匿名なら nil)。pinned note
// の myReaction を埋めるために fieldRes.Apply に流す (#426)。
//
// 設計メモ — PinnedNoteIDs を visibility filter 前の生 ID 配列で返す理由 (#1489):
//
//   - pin = author の意図的な self-disclosure 行為。「followers にだけ見せる
//     note を profile に固定する」と author が選んだ時点で、pinnedNoteIds が
//     viewer に露出することは upstream Misskey TS でも同 shape (drop-in 互換)。
//   - notes/show ShowForAPI doctrine の境界線上だが、author 自身が pin を選んで
//     いる以上、ID-known な viewer に content が返るのは「意図された情報開示」
//     の範疇とする (#1489 で議論)。
//   - PinnedNotes 本体 (= 中身の埋め込み) は FilterVisible で絞るため、profile
//     表示上は viewer から見えない pin はカード化されない。
//   - pinning は per-user 上限が厳しい (= 数件) ので mass enumeration リスクは
//     低い。
//
// この設計は Option A (現状維持 = upstream-aligned) を採用した結果 (#1489
// wontfix)。Option B (IDs も filter) は frontend drop-in 互換と「author 意図」
// に逆行するため不採用。
func (h *Handler) fillPinned(ctx context.Context, viewer *model.User, u *model.User, profile *model.UserProfile, detailed *entity.UserDetailed) {
	if h.piningRepo != nil {
		if pinings, err := h.piningRepo.ListByUser(u.ID); err == nil && len(pinings) > 0 {
			ids := make([]string, 0, len(pinings))
			for _, p := range pinings {
				ids = append(ids, p.NoteID)
			}
			// PinnedNoteIDs は意図的に filter 前の生 IDs (上記設計メモ参照)。
			detailed.PinnedNoteIDs = ids
			if h.noteRepo != nil {
				if notes, err := h.noteRepo.FindManyByIDsWithUser(ids); err == nil {
					notes = notesfilter.FilterVisible(viewer, notes, h.followingRepo)
					entities := entity.PackNotes(ctx, notes, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
					h.fieldRes.Apply(entities, viewer)
					notehide.HideEmbeds(viewer, entities)
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
				// golden Page は user 必須。pinnedPage は profile user 自身の page
				// なので owner=u を渡して user (UserLite) を埋める (#1266 follow-up)。
				detailed.PinnedPage = entity.PackPageWithContext(p, entity.PackPageContext{IDGen: h.idGen, Owner: u})
			}
		}
	}
}
