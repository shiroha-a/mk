package note

import (
	"slices"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// CanSeeNote reports whether viewer is allowed to see the given note based on
// its visibility level. viewer may be nil for unauthenticated requests.
//
// followingChecker is invoked only when the visibility level requires a follow
// relationship check; pass nil to skip the check (in which case "followers"
// notes are treated as invisible to non-author viewers).
func CanSeeNote(viewer *model.User, n *model.Note, followingChecker repository.FollowingRepository) bool {
	if n == nil {
		return false
	}
	if n.Visibility == model.NoteVisibilityPublic || n.Visibility == model.NoteVisibilityHome {
		return true
	}
	// 以降 (followers / specified) は viewer 必須。
	if viewer == nil {
		return false
	}
	// 投稿者本人は常に閲覧可。
	if viewer.ID == n.UserID {
		return true
	}
	// #2106 N27: upstream generateVisibilityQuery / isVisibleForMe と同じく、visibility 横断で
	// mentions / visibleUserIds に含まれる viewer は閲覧可 (followers note の mention/visibleUser
	// 宛を read 経路で過剰に隠さない)。SQL push-down (repository.applyViewerVisibility) と整合。
	// 注意: main-stream realtime push の #1472 anti-leak gate はこの緩和を含めない
	// canSeeNoteForStream を使う (mentioned 非フォロワーへの本文 realtime push を防ぐ)。
	if slices.Contains(n.Mentions, viewer.ID) || slices.Contains(n.VisibleUserIDs, viewer.ID) {
		return true
	}
	switch n.Visibility {
	case model.NoteVisibilityFollowers:
		// #2106 N27: reply 先が viewer の followers note は閲覧可 (upstream followers 分岐の
		// replyUserId=meId)。
		if n.ReplyUserID != nil && *n.ReplyUserID == viewer.ID {
			return true
		}
		if followingChecker == nil {
			return false
		}
		ok, err := followingChecker.Exists(viewer.ID, n.UserID)
		if err != nil {
			return false
		}
		return ok
	case model.NoteVisibilitySpecified:
		// visibleUserIds は上で判定済。それ以外の specified は不可視。
		return false
	}
	return false
}

// canSeeNoteForStream is the strict main-stream realtime-push visibility gate
// (#1472). Unlike CanSeeNote (#2106 N27 で read 経路を upstream に緩和) it does NOT
// grant visibility via mentions / replyUserId, so a followers note's body/CW is
// never pushed in real time to a mentioned / replied-to non-follower (anti-leak /
// anti-harassment hardening を維持)。visibleUserIds は specified note の正規の宛先
// なので許可する (N13 で reply target も visibleUserIds に入るため specified reply は届く)。
func canSeeNoteForStream(viewer *model.User, n *model.Note, followingChecker repository.FollowingRepository) bool {
	if n == nil {
		return false
	}
	switch n.Visibility {
	case model.NoteVisibilityPublic, model.NoteVisibilityHome:
		return true
	case model.NoteVisibilityFollowers:
		if viewer == nil {
			return false
		}
		if viewer.ID == n.UserID {
			return true
		}
		if followingChecker == nil {
			return false
		}
		ok, err := followingChecker.Exists(viewer.ID, n.UserID)
		if err != nil {
			return false
		}
		return ok
	case model.NoteVisibilitySpecified:
		if viewer == nil {
			return false
		}
		if viewer.ID == n.UserID {
			return true
		}
		return slices.Contains(n.VisibleUserIDs, viewer.ID)
	}
	return false
}
