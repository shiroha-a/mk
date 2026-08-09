// Package move implements i/move (account migration) business logic:
// destination URI resolution via the federation Resolver, alsoKnownAs
// verification, local user row updates (movedToUri/movedAt/alsoKnownAs),
// and Move activity delivery to follower inboxes.
package move

import (
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/repository"
)

// Errors returned by Service.Move.
var (
	// ErrNoSuchUser is returned when the destination URI cannot be resolved.
	ErrNoSuchUser = errors.New("no such user")
	// ErrAlreadyMoved is returned when the source user already has movedToUri set.
	ErrAlreadyMoved = errors.New("already moved")
	// ErrDestinationForbids is returned when the destination does not declare
	// the source URI in its alsoKnownAs, or the destination itself has already
	// moved elsewhere.
	ErrDestinationForbids = errors.New("destination account forbids")
	// ErrURINull is returned when the source or destination user lacks a URI.
	ErrURINull = errors.New("user URI is null")
	// ErrRemoteSourceForbidden is returned when the caller is not a local user.
	// AP 配送のため秘密鍵が必要で、リモートユーザーを move 元にはできない。
	ErrRemoteSourceForbidden = errors.New("source must be a local user")
)

// Resolver fetches a remote actor by URI and returns the persisted local
// representation. Implemented by federation.Resolver; kept as a local
// interface so Service doesn't import federation (avoids circular deps during
// wiring).
type Resolver interface {
	ResolveActor(uri string) (*model.User, error)
}

// Deliverer delivers a pre-rendered activity body to the follower inboxes of
// signerUserID. Implemented by federation.DeliverService.
type Deliverer interface {
	DeliverToFollowers(signerUserID string, body []byte) error
}

// FollowEnqueuer schedules follow jobs. Implemented by queue.Client.
//
// アカウント移行のフォロワー引き継ぎは 1 回で大量の follow を生むため、
// 必ず queue 経由にする。#2403 で relationship queue に分離済みなので、
// ここが AP 配送 (deliver queue) の worker を奪うことはない。
type FollowEnqueuer interface {
	EnqueueFollowBulk(payloads []queue.FollowPayload) error
}

// ProxyAccountIDResolver returns the local proxy account's user id.
// The second return value is false when no proxy account is configured.
type ProxyAccountIDResolver func() (string, bool)

// Service owns the account-move workflow.
type Service struct {
	userRepo      repository.UserRepository
	followingRepo repository.FollowingRepository
	urls          *activitypub.URLBuilder
	renderer      *activitypub.Renderer
	resolver      Resolver
	deliverer     Deliverer

	// followQueue / proxyAccount は post-move 処理 (#2418) 用。既存の
	// NewService 呼び出しを壊さないよう setter で後付けする。未設定なら
	// フォロワー引き継ぎは skip する (移行そのものは成立させる)。
	followQueue  FollowEnqueuer
	proxyAccount ProxyAccountIDResolver
}

// SetFollowQueue wires the queue used to schedule follower migration jobs.
func (s *Service) SetFollowQueue(q FollowEnqueuer) { s.followQueue = q }

// SetProxyAccountResolver wires the lookup used to exclude the proxy account
// from follower migration.
func (s *Service) SetProxyAccountResolver(f ProxyAccountIDResolver) { s.proxyAccount = f }

// NewService constructs a Service. resolver / deliverer may be nil in tests;
// Move returns ErrNoSuchUser early if resolver is missing, and skips delivery
// if deliverer is missing.
func NewService(
	userRepo repository.UserRepository,
	followingRepo repository.FollowingRepository,
	urls *activitypub.URLBuilder,
	renderer *activitypub.Renderer,
	resolver Resolver,
	deliverer Deliverer,
) *Service {
	return &Service{
		userRepo:      userRepo,
		followingRepo: followingRepo,
		urls:          urls,
		renderer:      renderer,
		resolver:      resolver,
		deliverer:     deliverer,
	}
}

// Move migrates src to the user identified by dstURI.
//
// Preconditions (mirror upstream AccountMoveService.moveFromLocal):
//   - src is a local user with a URI
//   - src.movedToUri is empty
//   - resolver.ResolveActor(dstURI) returns a user whose alsoKnownAs contains
//     src's canonical URI and whose movedToUri is empty
//
// Side effects on success:
//   - user row updated: movedToUri, movedAt, alsoKnownAs (ensures dstURI is
//     in the csv list so remote instances treat the migration as bidirectional)
//   - Move activity rendered and enqueued to every remote follower inbox
func (s *Service) Move(src *model.User, dstURI string) error {
	if src == nil {
		return ErrNoSuchUser
	}
	if !src.IsLocal() {
		return ErrRemoteSourceForbidden
	}
	if src.MovedToURI != nil && *src.MovedToURI != "" {
		return ErrAlreadyMoved
	}
	dstURI = strings.TrimSpace(dstURI)
	if dstURI == "" {
		return ErrNoSuchUser
	}
	srcURI := s.urls.UserURI(src.ID)

	if s.resolver == nil {
		return ErrNoSuchUser
	}
	dst, err := s.resolver.ResolveActor(dstURI)
	if err != nil || dst == nil {
		return ErrNoSuchUser
	}
	// 解決後の dst URI は resolver が正規化した形 (actor.id) を使う。
	// これが空なら遷移先が AP 的に不正。
	dstCanonical := ""
	if dst.URI != nil {
		dstCanonical = *dst.URI
	}
	if dstCanonical == "" {
		dstCanonical = dstURI
	}
	if !alsoKnownAsIncludes(dst.AlsoKnownAs, srcURI) {
		return ErrDestinationForbids
	}
	if dst.MovedToURI != nil && *dst.MovedToURI != "" {
		return ErrDestinationForbids
	}

	now := time.Now()
	newAlsoKnownAs := appendIfMissing(src.AlsoKnownAs, dstCanonical)
	fields := map[string]any{
		"movedToUri":  dstCanonical,
		"movedAt":     now,
		"alsoKnownAs": newAlsoKnownAs,
	}
	if err := s.userRepo.UpdateUser(src.ID, fields); err != nil {
		return err
	}
	// 呼び出し側が返り値で最新状態を参照できるよう、local struct も in-place 更新する。
	src.MovedToURI = &dstCanonical
	src.MovedAt = &now
	src.AlsoKnownAs = strPtr(newAlsoKnownAs)

	// フォロワーの引き継ぎ。DB は既にコミット済みなので best-effort
	// (エラーを返して再試行されると次回 ErrAlreadyMoved で詰む)。
	// deliverer 未配線でも引き継ぎは行いたいので、下の early return より前で呼ぶ。
	s.PostMoveProcess(src, dst)

	if s.deliverer == nil {
		return nil
	}
	move := s.renderer.RenderMove(src, dstCanonical)
	body, err := json.Marshal(move)
	if err != nil {
		return err
	}
	// DB は既にコミット済みなので、配送失敗で handler がエラーを返して再試行
	// されると次回 ErrAlreadyMoved で詰む。follower 通知は best-effort にし、
	// 失敗は log に落として握り潰す (note_delivery_hook と同じパターン)。
	if err := s.deliverer.DeliverToFollowers(src.ID, body); err != nil {
		slog.Warn("move: deliver to followers failed (DB already committed)",
			"srcID", src.ID, "dstURI", dstCanonical, "err", err)
	}
	return nil
}

// alsoKnownAsIncludes returns true if the csv alsoKnownAs field contains uri.
// 本家の alsoKnownAs は配列だが Go 側は text csv として格納している点に注意。
// Move() の呼び出し側が uri != "" を保証しているため、ここでは csv 側だけ
// 空チェックする。
func alsoKnownAsIncludes(csvPtr *string, uri string) bool {
	if csvPtr == nil || *csvPtr == "" {
		return false
	}
	for _, part := range strings.Split(*csvPtr, ",") {
		if strings.TrimSpace(part) == uri {
			return true
		}
	}
	return false
}

// appendIfMissing returns the csv with uri appended if not already present.
// 既存要素は順序を保ったまま残す (remote の expectation を壊さないため)。
// 呼び出し元 Move() が uri != "" を保証しているので空文字のハンドリングはしない。
func appendIfMissing(csvPtr *string, uri string) string {
	if csvPtr == nil || *csvPtr == "" {
		return uri
	}
	for _, part := range strings.Split(*csvPtr, ",") {
		if strings.TrimSpace(part) == uri {
			return *csvPtr
		}
	}
	return *csvPtr + "," + uri
}

func strPtr(s string) *string { return &s }

// PostMoveProcess migrates local followers of src onto dst.
//
// upstream AccountMoveService.postMoveProcess の後半に対応する。前半
// (ブロック / ミュート / ロール / リスト / アンテナの引き継ぎ) は #2419。
//
// move-out (Move) と move-in (#2414 の processRemoteMove) の両方から呼ばれる
// 想定なので exported にしてある。
//
// **best-effort。** 呼び出し時点で DB の movedToUri は既にコミット済みで、
// ここでエラーを返して呼び出し側が再試行すると ErrAlreadyMoved で詰む。
// 失敗は log に落として握り潰す (Move の配送失敗と同じ扱い)。
func (s *Service) PostMoveProcess(src, dst *model.User) {
	if src == nil || dst == nil || s.followingRepo == nil {
		return
	}
	followerIDs, err := s.followingRepo.ListLocalFollowerIDs(src.ID)
	if err != nil {
		slog.Warn("move: list local followers failed",
			"srcID", src.ID, "dstID", dst.ID, "err", err)
		return
	}
	// proxy account はリストの購読用に機械的にフォローしているだけなので、
	// 移行先へ follow させない (upstream も followerId: Not(proxy.id) で除外)。
	if s.proxyAccount != nil {
		if proxyID, ok := s.proxyAccount(); ok {
			followerIDs = slices.DeleteFunc(followerIDs, func(id string) bool {
				return id == proxyID
			})
		}
	}
	if len(followerIDs) == 0 {
		return
	}

	// カウント調整を先に行う。follow job は非同期なので、先に enqueue すると
	// worker が新しい follow を作った後で古いカウントを引くことになり、
	// 二重に減る余地がある。
	s.adjustFollowingCounts(followerIDs, src)

	if s.followQueue == nil {
		slog.Warn("move: follow queue not wired; followers were not migrated",
			"srcID", src.ID, "dstID", dst.ID, "followers", len(followerIDs))
		return
	}
	payloads := make([]queue.FollowPayload, 0, len(followerIDs))
	for _, followerID := range followerIDs {
		payloads = append(payloads, queue.FollowPayload{
			FollowerID: followerID,
			FolloweeID: dst.ID,
		})
	}
	if err := s.followQueue.EnqueueFollowBulk(payloads); err != nil {
		slog.Warn("move: enqueue follower migration failed",
			"srcID", src.ID, "dstID", dst.ID, "followers", len(payloads), "err", err)
	}
}

// adjustFollowingCounts zeroes the old account's counters and decrements the
// counters of everyone who was in a follow relationship with it.
//
// **旧アカウントを unfollow しない**のが要点。upstream のコメントどおり
// "Decrease following count instead of unfollowing" で、フォロー行は残したまま
// カウントだけ落とす。旧アカウントが移行後もまだ機能しうるため関係を切らない、
// という設計。素直に unfollow に置き換えると挙動が変わる。
//
// 結果として DB は「フォロー行はあるがカウントに出ない」状態になる。これは
// upstream と同じ状態で、意図的なもの。
func (s *Service) adjustFollowingCounts(localFollowerIDs []string, src *model.User) {
	if len(localFollowerIDs) == 0 || s.userRepo == nil {
		return
	}
	// 旧アカウント自身のカウントを 0 にする。
	if err := s.userRepo.UpdateUser(src.ID, map[string]any{
		"followersCount": 0,
		"followingCount": 0,
	}); err != nil {
		slog.Warn("move: reset old account counters failed", "srcID", src.ID, "err", err)
	}
	// ローカルフォロワーの followingCount を 1 ずつ減らす。
	for _, followerID := range localFollowerIDs {
		if err := s.userRepo.IncrementFollowingCount(followerID, -1); err != nil {
			slog.Warn("move: decrement follower's followingCount failed",
				"followerID", followerID, "err", err)
		}
	}
	// 旧アカウントがフォローしていた相手の followersCount を 1 ずつ減らす。
	followeeIDs, err := s.followingRepo.ListFolloweeIDs(src.ID)
	if err != nil {
		slog.Warn("move: list followees of old account failed", "srcID", src.ID, "err", err)
		return
	}
	for _, followeeID := range followeeIDs {
		if err := s.userRepo.IncrementFollowersCount(followeeID, -1); err != nil {
			slog.Warn("move: decrement followee's followersCount failed",
				"followeeID", followeeID, "err", err)
		}
	}
}
