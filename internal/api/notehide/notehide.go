// Package notehide centralizes the per-viewer embedded renote/reply hiding
// (#1536) so EVERY REST endpoint that packs notes applies the same gate. The
// gate blanks an embedded note's content when the requesting viewer is not
// allowed to see it, mirroring upstream Misskey's NoteEntityService.hideNote
// applied to embeds.
//
// Only EMBEDDED Renote/Reply are gated; the top-level note keeps its existing
// per-surface gate (RequireVisible / FilterVisible / SQL push-down / notes-show
// ID-known doctrine). Streaming has its own per-connection gate in
// internal/stream/channels.
package notehide

import (
	"time"

	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// followingRepo is wired once at startup (router) so handlers can call
// HideEmbeds without each threading a FollowingRepository. A nil repo fails
// closed (followers embeds hidden), which is safe.
var followingRepo repository.FollowingRepository

// SetFollowingRepo wires the FollowingRepository used by HideEmbeds. Call once
// during server setup before requests are served.
func SetFollowingRepo(r repository.FollowingRepository) {
	followingRepo = r
}

// HideEmbeds blanks embedded renote/reply content that `viewer` is not allowed
// to see, across the packed batch, using a SINGLE batched follow query for the
// whole page (never a per-embed query). Safe to call on every note-returning
// REST response. viewer==nil (anonymous) hides followers/specified embeds with
// no query.
func HideEmbeds(viewer *model.User, packed []entity.NoteEntity) {
	hideEmbedsAt(viewer, packed, followingRepo, time.Now().UnixMilli())
}

func hideEmbedsAt(viewer *model.User, packed []entity.NoteEntity, repo repository.FollowingRepository, nowMs int64) {
	if len(packed) == 0 {
		return
	}
	follows := buildFollowSet(viewer, packed, repo)
	for i := range packed {
		hideEmbedIfNeeded(viewer, packed[i].Renote, follows, nowMs)
		hideEmbedIfNeeded(viewer, packed[i].Reply, follows, nowMs)
	}
}

func hideEmbedIfNeeded(viewer *model.User, embed *entity.NoteEntity, follows func(string) bool, nowMs int64) {
	if embed == nil {
		return
	}
	if corenote.HideEmbedDecision(viewer, embedFactsFromEntity(embed, nowMs), follows, nowMs) {
		entity.HideNoteEntity(embed)
	}
}

// buildFollowSet resolves, in ONE query, which embed authors `viewer` follows.
// It collects the distinct authors of embeds that may require a follow check
// (followers, plus public/home that could downgrade via the author's
// makeNotesFollowersOnlyBefore), then issues a single FilterFollowingsFromAnchor.
func buildFollowSet(viewer *model.User, packed []entity.NoteEntity, repo repository.FollowingRepository) func(string) bool {
	never := func(string) bool { return false }
	if viewer == nil || repo == nil {
		return never
	}
	seen := make(map[string]struct{})
	for i := range packed {
		collectEmbedAuthor(packed[i].Renote, viewer.ID, seen)
		collectEmbedAuthor(packed[i].Reply, viewer.ID, seen)
	}
	if len(seen) == 0 {
		return never
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	followed, err := repo.FilterFollowingsFromAnchor(viewer.ID, ids)
	if err != nil {
		// fail-closed: 失敗時は「誰もフォローしていない」扱いで followers embed を隠す。
		return never
	}
	set := make(map[string]struct{}, len(followed))
	for _, id := range followed {
		set[id] = struct{}{}
	}
	return func(id string) bool {
		_, ok := set[id]
		return ok
	}
}

func collectEmbedAuthor(embed *entity.NoteEntity, viewerID string, seen map[string]struct{}) {
	if embed == nil || embed.UserID == "" || embed.UserID == viewerID {
		return
	}
	switch embed.Visibility {
	case string(model.NoteVisibilityFollowers):
		// followers は必ず follow 判定が要る。
	case string(model.NoteVisibilityPublic), string(model.NoteVisibilityHome):
		// public/home は通常 follow 不要。著者が makeNotesFollowersOnlyBefore を
		// 設定している時だけ followers へ降格しうるので、その場合のみ収集する。
		if embed.User.MakeNotesFollowersOnlyBefore == nil {
			return
		}
	default:
		// specified / 不明: follow 判定不要 (specified は visibleUserIds で判定)。
		return
	}
	seen[embed.UserID] = struct{}{}
}

// embedFactsFromEntity translates a packed embed NoteEntity into core/note
// EmbedFacts, reading the author preference fields off the embed's UserLite
// (populated only when the embed author was preloaded — AuthorPrefsKnown
// reflects that). ReplyTargetAuthorID は depth-1 embed では取得できない
// (embed 自身の reply target = depth 2 は pack されない) ため常に空のままで、
// core 側の reply-target escape hatch は embed では発火しない (= conservative)。
func embedFactsFromEntity(embed *entity.NoteEntity, fallbackMs int64) corenote.EmbedFacts {
	f := corenote.EmbedFacts{
		AuthorID:       embed.UserID,
		Visibility:     embed.Visibility,
		VisibleUserIDs: embed.VisibleUserIDs,
		Mentions:       embed.Mentions,
		CreatedAtMs:    parseCreatedAtMs(embed.CreatedAt, fallbackMs),
	}
	if embed.User.ID != "" {
		f.AuthorPrefsKnown = true
		if embed.User.RequireSigninToViewContents != nil {
			f.RequireSigninToViewContents = *embed.User.RequireSigninToViewContents
		}
		f.MakeNotesHiddenBefore = embed.User.MakeNotesHiddenBefore
		f.MakeNotesFollowersOnlyBefore = embed.User.MakeNotesFollowersOnlyBefore
	}
	return f
}

// parseCreatedAtMs parses the packed RFC3339-ms createdAt back to unix-ms; any
// failure returns fallbackMs (= now), which fails OPEN on the time-window gates
// only (never spuriously hides a parse-failed timestamp).
func parseCreatedAtMs(s string, fallbackMs int64) int64 {
	if s == "" {
		return fallbackMs
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fallbackMs
	}
	return t.UnixMilli()
}
