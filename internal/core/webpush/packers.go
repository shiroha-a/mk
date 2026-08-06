package webpush

import (
	"encoding/json"
	"time"

	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// NoteRepoPacker adapts (NoteRepository + id.Generator) into the
// notification.NotePacker interface. The adapter re-uses entity.PackNote so
// that the Web Push payload matches the shape returned by /api/i/notifications.
type NoteRepoPacker struct {
	repo          repository.NoteRepository
	idGen         id.Generator
	followingRepo repository.FollowingRepository
}

// NewNoteRepoPacker constructs a NoteRepoPacker. followingRepo is used to gate
// the embedded note by the push recipient's visibility (#1572); a nil repo
// fails closed (followers notes hidden from non-author recipients).
func NewNoteRepoPacker(repo repository.NoteRepository, idGen id.Generator, followingRepo repository.FollowingRepository) *NoteRepoPacker {
	return &NoteRepoPacker{repo: repo, idGen: idGen, followingRepo: followingRepo}
}

// PackNoteByID implements notification.NotePacker. viewerID is the push
// recipient (notifiee); the note is gated by their visibility before packing,
// mirroring REST i/notifications (FilterVisible, #1444) and the stream
// notification path (noteVisibleToNotifiee, #1471): when the recipient cannot
// see the note, return (nil, false) so the caller omits the note detail (the
// notification keeps its noteId). Web Push previously packed the note with no
// gate at all (#1572 IDOR). followingRepo unwired / blank viewerID fails closed
// for followers / specified notes; public / home always pass.
func (p *NoteRepoPacker) PackNoteByID(noteID, viewerID string) (map[string]any, bool) {
	if p == nil || p.repo == nil {
		return nil, false
	}
	n, err := p.repo.FindByIDWithRelations(noteID)
	if err != nil {
		return nil, false
	}
	var viewer *model.User
	if viewerID != "" {
		viewer = &model.User{ID: viewerID}
	}
	if !corenote.CanSeeNote(viewer, n, p.followingRepo) {
		return nil, false
	}
	packed := entity.PackNote(n, p.idGen)
	// depth-2 embed hide: 通知 note の renote/reply embed (= depth-1 here) を
	// 受信者可視性で blank する。REST notehide.HideNotificationNotes (#1570) /
	// stream hideNotificationNote (#1570) と整合させる (#1575)。core/webpush は
	// api/notehide を import できないため corenote.HideEmbedDecision +
	// entity.HideNoteEntity を直接使う。
	p.hideEmbeds(viewer, &packed)
	return toMap(packed)
}

// hideEmbeds blanks the renote/reply embeds of `packed` that `viewer` (the push
// recipient / notifiee) is not allowed to see, using corenote.HideEmbedDecision.
// follows は両 embed 著者を ONE batched FilterFollowingsFromAnchor で解決する。
// fail-closed: followingRepo 未配線 / viewer nil の場合 follows は常に false を
// 返し、followers/specified embed は hide される。
func (p *NoteRepoPacker) hideEmbeds(viewer *model.User, packed *entity.NoteEntity) {
	if packed == nil {
		return
	}
	nowMs := time.Now().UnixMilli()
	follows := p.buildFollowSet(viewer, packed)
	hideEmbedIfNeeded(viewer, packed.Renote, follows, nowMs)
	hideEmbedIfNeeded(viewer, packed.Reply, follows, nowMs)
	// depth-2 embed (renote.renote / renote.reply): packer がこれらを出すため、
	// 非公開の note が push payload に leak しないよう gate する
	// (notehide / stream note_filter と整合)。
	if packed.Renote != nil {
		hideEmbedIfNeeded(viewer, packed.Renote.Renote, follows, nowMs)
		hideEmbedIfNeeded(viewer, packed.Renote.Reply, follows, nowMs)
	}
}

// buildFollowSet resolves, in ONE query, which embed authors `viewer` follows.
// It collects the distinct authors of the renote/reply embeds that may require a
// follow check (followers, plus public/home that could downgrade via the
// author's makeNotesFollowersOnlyBefore), then issues a single
// FilterFollowingsFromAnchor. viewer nil / followingRepo unwired / a failed
// query all collapse to "follows nobody" (fail-closed), mirroring
// notehide.buildFollowSet.
func (p *NoteRepoPacker) buildFollowSet(viewer *model.User, packed *entity.NoteEntity) func(string) bool {
	never := func(string) bool { return false }
	if viewer == nil || p.followingRepo == nil {
		return never
	}
	seen := make(map[string]struct{})
	collectEmbedAuthor(packed.Renote, viewer.ID, seen)
	collectEmbedAuthor(packed.Reply, viewer.ID, seen)
	// depth-2 embed (renote.renote / renote.reply) の著者も follow 判定対象に含める。
	if packed.Renote != nil {
		collectEmbedAuthor(packed.Renote.Renote, viewer.ID, seen)
		collectEmbedAuthor(packed.Renote.Reply, viewer.ID, seen)
	}
	if len(seen) == 0 {
		return never
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	followed, err := p.followingRepo.FilterFollowingsFromAnchor(viewer.ID, ids)
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

// collectEmbedAuthor adds an embed author to seen only when a follow check is
// actually needed: followers visibility, or public/home that could downgrade via
// the author's makeNotesFollowersOnlyBefore. specified/unknown need no follow
// query (specified is decided by visibleUserIds). Mirrors
// notehide.collectEmbedAuthor.
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
		// specified / 不明: follow 判定不要。
		return
	}
	seen[embed.UserID] = struct{}{}
}

// hideEmbedIfNeeded blanks an embed (in place) when corenote.HideEmbedDecision
// says viewer cannot see it. Mirrors notehide.hideEmbedIfNeeded.
func hideEmbedIfNeeded(viewer *model.User, embed *entity.NoteEntity, follows func(string) bool, nowMs int64) {
	if embed == nil {
		return
	}
	if corenote.HideEmbedDecision(viewer, embedFactsFromEntity(embed), follows, nowMs) {
		entity.HideNoteEntity(embed)
	}
}

// embedFactsFromEntity translates a packed embed NoteEntity into core/note
// EmbedFacts, reading the author preference fields off the embed's UserLite
// (populated only when the embed author was preloaded — AuthorPrefsKnown
// reflects that). ReplyTargetAuthorID は depth-1 embed では取得できない
// (embed 自身の reply target = depth 2 は pack されない) ため常に空のままにする。
// notehide.embedFactsFromEntity と同じ変換。
func embedFactsFromEntity(embed *entity.NoteEntity) corenote.EmbedFacts {
	f := corenote.EmbedFacts{
		AuthorID:       embed.UserID,
		Visibility:     embed.Visibility,
		VisibleUserIDs: embed.VisibleUserIDs,
		Mentions:       embed.Mentions,
		CreatedAtMs:    parseCreatedAtMs(embed.CreatedAt),
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

// parseCreatedAtMs parses the packed RFC3339-ms createdAt back to unix-ms. On any
// failure it returns 0, which fails CLOSED on the time-window gates (the embed is
// treated as created at epoch 0). Returning now() instead would make the absolute
// makeNotes*Before epoch gate fail-OPEN. notehide.parseCreatedAtMs と同じ。
func parseCreatedAtMs(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// UserRepoPacker adapts UserRepository into notification.UserPacker using
// entity.PackUserLite.
type UserRepoPacker struct {
	repo repository.UserRepository
}

// NewUserRepoPacker constructs a UserRepoPacker.
func NewUserRepoPacker(repo repository.UserRepository) *UserRepoPacker {
	return &UserRepoPacker{repo: repo}
}

// PackUserByID implements notification.UserPacker.
func (p *UserRepoPacker) PackUserByID(userID string) (map[string]any, bool) {
	if p == nil || p.repo == nil {
		return nil, false
	}
	u, err := p.repo.FindByID(userID)
	if err != nil {
		return nil, false
	}
	return toMap(entity.PackUserLite(u))
}

// toMap round-trips any JSON-serializable value into a map. Used because the
// notification hook expects a map[string]any and the entity helpers return
// concrete structs.
func toMap(v any) (map[string]any, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}
