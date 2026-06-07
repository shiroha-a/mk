package note

import (
	"slices"

	"github.com/shiroha-a/mk/internal/model"
)

// EmbedFacts carries the minimal, source-agnostic facts HideEmbedDecision needs
// to decide whether an embedded renote/reply must be blanked for a viewer.
//
// Callers build it from their own representation (REST from the packed
// entity.NoteEntity, streaming from the decoded payload) so core/note depends on
// neither the entity nor the stream layer.
//
// AuthorPrefsKnown reports whether the embed author's preference fields
// (RequireSigninToViewContents / MakeNotes*Before) were available at pack time.
// When false (= the embed author row was not preloaded), the author-preference
// gates are skipped — the note-intrinsic visibility gates (specified / followers)
// still apply, so the core embed-leak is closed regardless; only the opt-in
// author-pref refinements (which most authors never set) are not evaluated.
type EmbedFacts struct {
	AuthorID                     string
	Visibility                   string
	VisibleUserIDs               []string
	ReplyTargetAuthorID          string
	Mentions                     []string
	RequireSigninToViewContents  bool
	MakeNotesHiddenBefore        *int
	MakeNotesFollowersOnlyBefore *int
	CreatedAtMs                  int64
	AuthorPrefsKnown             bool
}

// HideEmbedDecision reports whether an embedded note described by facts must be
// hidden (its content blanked) from viewer. It is a faithful port of upstream
// Misskey NoteEntityService.treatVisibility followed by shouldHideNote (in that
// order). It is pure: the caller supplies `follows` (does viewer follow
// authorID) from its own batched set / per-connection snapshot, and `nowMs`
// (current unix milliseconds) for the time-window gates. viewer may be nil
// (anonymous), in which case followers/specified embeds are hidden.
//
// The same decision drives both the REST batch path and the streaming
// per-connection path; only the `follows` source differs.
func HideEmbedDecision(viewer *model.User, f EmbedFacts, follows func(authorID string) bool, nowMs int64) bool {
	vis := f.Visibility

	// treatVisibility: 投稿者の makeNotesFollowersOnlyBefore ウィンドウを過ぎた
	// public/home note は、フォロワー判定の前に followers へ降格する。著者設定
	// 由来なので prefs が判明している時のみ。
	if vis == string(model.NoteVisibilityPublic) || vis == string(model.NoteVisibilityHome) {
		if f.AuthorPrefsKnown && shouldHideNoteByTime(f.MakeNotesFollowersOnlyBefore, f.CreatedAtMs, nowMs) {
			vis = string(model.NoteVisibilityFollowers)
		}
	}

	var viewerID string
	if viewer != nil {
		viewerID = viewer.ID
	}

	// 自分の投稿は常に見える (upstream: meId === userId)。
	if viewer != nil && viewerID == f.AuthorID {
		return false
	}
	// requireSigninToViewContents: 未ログインには隠す。
	if f.AuthorPrefsKnown && f.RequireSigninToViewContents && viewer == nil {
		return true
	}
	// makeNotesHiddenBefore: 期限切れの古い note を隠す。
	if f.AuthorPrefsKnown && shouldHideNoteByTime(f.MakeNotesHiddenBefore, f.CreatedAtMs, nowMs) {
		return true
	}
	// specified: 指定ユーザー以外には隠す (note-intrinsic, prefs 不要)。
	if vis == string(model.NoteVisibilitySpecified) {
		if viewer == nil {
			return true
		}
		return !slices.Contains(f.VisibleUserIDs, viewerID)
	}
	// followers: 投稿者のフォロワー / reply 先本人 / mention 対象以外には隠す。
	if vis == string(model.NoteVisibilityFollowers) {
		if viewer == nil {
			return true
		}
		if f.ReplyTargetAuthorID != "" && viewerID == f.ReplyTargetAuthorID {
			return false
		}
		if slices.Contains(f.Mentions, viewerID) {
			return false
		}
		if follows != nil && follows(f.AuthorID) {
			return false
		}
		return true
	}

	return false
}

// shouldHideNoteByTime ports upstream misc/should-hide-note-by-time.ts.
//
// hiddenBefore は秒単位。nil は無効 (隠さない)。<= 0 は「作成からの経過秒」での相対
// 判定で、経過 >= |hiddenBefore| 秒なら隠す。> 0 は絶対 epoch 秒での判定で、
// createdAt(秒) <= hiddenBefore なら隠す。createdAtMs / nowMs はミリ秒。
func shouldHideNoteByTime(hiddenBefore *int, createdAtMs, nowMs int64) bool {
	if hiddenBefore == nil {
		return false
	}
	v := *hiddenBefore
	if v <= 0 {
		elapsedSeconds := float64(nowMs-createdAtMs) / 1000.0
		return elapsedSeconds >= float64(-v)
	}
	createdAtSeconds := float64(createdAtMs) / 1000.0
	return createdAtSeconds <= float64(v)
}
