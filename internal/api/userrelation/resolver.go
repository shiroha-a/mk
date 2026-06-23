// Package userrelation computes the viewer->target relation block shared by
// /api/users/show and /api/blocking/{create,delete}, mirroring upstream
// UserEntityService's pack(target, me) relation fields (isFollowing, isFollowed,
// isBlocking, isBlocked, isMuted, isRenoteMuted, hasPendingFollowRequest*,
// notify, withReplies, followedMessage, memo). Centralizing the computation
// keeps the two call sites from drifting (#1802).
package userrelation

import (
	"sync"

	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// Repos bundles the repositories needed to compute a viewer->target relation
// block. Any nil repo skips its corresponding flags (= test fixtures / partial
// wiring); production wires all of them so the full relation block is emitted.
type Repos struct {
	Following     repository.FollowingRepository
	Blocking      repository.BlockingRepository
	Muting        repository.MutingRepository
	RenoteMuting  repository.RenoteMutingRepository
	FollowRequest repository.FollowRequestRepository
	Memo          repository.UserMemoRepository
}

// Apply computes the viewer->target relation flags and writes them onto
// detailed, matching upstream UserEntityService relation block. It is a no-op
// when detailed/target is nil, or when viewerID is empty (anonymous) or equals
// target.ID (self view) — the same conditions under which upstream omits the
// block. profile may be nil (followedMessage is then skipped). Returns whether
// the viewer follows the target; callers gate follower/following count
// visibility on it (#1558).
//
// 各リポジトリへのクエリは独立なので goroutine で並列実行し、全結果が揃ってから
// detailed に反映する (元々 users/show にインラインだった実装を共有化、#1802)。
func (r Repos) Apply(detailed *entity.UserDetailed, viewerID string, target *model.User, profile *model.UserProfile) bool {
	if detailed == nil || target == nil || viewerID == "" || viewerID == target.ID {
		return false
	}

	var (
		isFollowing, isFollowed      bool
		isBlocking, isBlocked        bool
		isMuted, isRenoteMuted       bool
		hasPendingFrom, hasPendingTo bool
		followRec                    *model.Following
		memo                         *model.UserMemo
		wg                           sync.WaitGroup
	)

	if r.Following != nil {
		wg.Add(3)
		go func() { defer wg.Done(); isFollowing, _ = r.Following.Exists(viewerID, target.ID) }()
		go func() { defer wg.Done(); isFollowed, _ = r.Following.Exists(target.ID, viewerID) }()
		go func() { defer wg.Done(); followRec, _ = r.Following.FindByPair(viewerID, target.ID) }()
	}
	if r.Blocking != nil {
		wg.Add(2)
		go func() { defer wg.Done(); isBlocking, _ = r.Blocking.Exists(viewerID, target.ID) }()
		go func() { defer wg.Done(); isBlocked, _ = r.Blocking.Exists(target.ID, viewerID) }()
	}
	if r.Muting != nil {
		wg.Add(1)
		go func() { defer wg.Done(); isMuted, _ = r.Muting.Exists(viewerID, target.ID) }()
	}
	if r.RenoteMuting != nil {
		wg.Add(1)
		go func() { defer wg.Done(); isRenoteMuted, _ = r.RenoteMuting.Exists(viewerID, target.ID) }()
	}
	if r.FollowRequest != nil {
		wg.Add(2)
		go func() { defer wg.Done(); hasPendingFrom, _ = r.FollowRequest.Exists(viewerID, target.ID) }()
		go func() { defer wg.Done(); hasPendingTo, _ = r.FollowRequest.Exists(target.ID, viewerID) }()
	}
	if r.Memo != nil {
		wg.Add(1)
		go func() { defer wg.Done(); memo, _ = r.Memo.FindByPair(viewerID, target.ID) }()
	}

	wg.Wait()

	viewerIsFollowing := false
	if r.Following != nil {
		detailed.IsFollowing = &isFollowing
		detailed.IsFollowed = &isFollowed
		viewerIsFollowing = isFollowing
		// notify / withReplies は relation ブロックが存在する限り常に emit する。
		// upstream は `notify: relation.following?.notify ?? 'none'` /
		// `withReplies: relation.following?.withReplies ?? false` で、follow row が
		// 無い / notify 未設定でも default を出す (#1558)。
		none := "none"
		withReplies := false
		if followRec != nil {
			if followRec.Notify != nil {
				detailed.Notify = followRec.Notify
			} else {
				detailed.Notify = &none
			}
			withReplies = followRec.WithReplies
		} else {
			detailed.Notify = &none
		}
		detailed.WithReplies = &withReplies
		// followedMessage は follower にだけ見せる。upstream は未設定でも null を
		// emit する (`relation.isFollowing ? (profile.followedMessage ?? null) :
		// undefined`、#1558 / #2097)。
		if isFollowing && profile != nil {
			entity.SetFollowedMessageForFollower(detailed, profile.FollowedMessage)
		}
	}
	if r.Blocking != nil {
		detailed.IsBlocking = &isBlocking
		detailed.IsBlocked = &isBlocked
	}
	if r.Muting != nil {
		detailed.IsMuted = &isMuted
	}
	if r.RenoteMuting != nil {
		detailed.IsRenoteMuted = &isRenoteMuted
	}
	if r.FollowRequest != nil {
		detailed.HasPendingFollowRequestFromYou = &hasPendingFrom
		detailed.HasPendingFollowRequestToYou = &hasPendingTo
	}
	if r.Memo != nil && memo != nil {
		detailed.Memo = &memo.Memo
	}
	return viewerIsFollowing
}
