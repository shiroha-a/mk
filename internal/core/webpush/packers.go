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
// notification.NotePacker interface, re-using entity.PackNote for the note shape.
//
// **REST /api/i/notifications と同一の内容にはならない。** REST と streaming は
// files に加えて channel / myReaction / poll の isVoted も埋めるが (#2735)、push は
// files だけを埋める。残りは push payload では使われず、上限 (約 4 KB) を圧迫する
// だけのため。
//
// **files は要約に要る** (#2737)。`notesummary.Get` が `note["files"]` の件数を見て
// `(📎N)` を足すので、埋めないと本文が無く画像だけの通知が空文字になる。
type NoteRepoPacker struct {
	repo          repository.NoteRepository
	idGen         id.Generator
	followingRepo repository.FollowingRepository
	files         *entity.NoteFieldResolver
}

// NewNoteRepoPacker constructs a NoteRepoPacker. followingRepo is used to gate
// the embedded note by the push recipient's visibility (#1572); a nil repo
// fails closed (followers notes hidden from non-author recipients).
// driveFile は要約の `(📎N)` を出すための batch lookup。nil なら files は空配列
// のままになる (#2737)。
func NewNoteRepoPacker(repo repository.NoteRepository, idGen id.Generator, followingRepo repository.FollowingRepository, driveFile entity.DriveFileLookup) *NoteRepoPacker {
	p := &NoteRepoPacker{repo: repo, idGen: idGen, followingRepo: followingRepo}
	if driveFile != nil {
		// folder / owner の lookup は渡さない。push payload では使われず、
		// 1 ファイルあたり 312-367 B 増えるだけで上限に近づく (#2737)。
		p.files = entity.NewNoteFieldResolver(driveFile, nil, nil, nil, nil, idGen)
	}
	return p
}

// PackNoteByID implements notification.NotePacker. viewerID is the push
// recipient (notifiee); the note is gated by their visibility before packing
// (CanSeeNote)。見えない場合は (nil, false) を返し、呼び出し元が note detail を
// 省く (通知自体は noteId を持ったまま届く)。Web Push previously packed the note
// with no gate at all (#1572 IDOR). followingRepo unwired / blank viewerID fails
// closed for followers / specified notes; public / home always pass.
//
// **REST i/notifications とは shape が違う。** あちらは #1953 以降 note-required
// 通知を行ごと落とすが、push で通知自体を落とすと届かなくなるので揃えない。
// stream 通知 (noteVisibleToNotifiee、#1471) と同じ「行は残して detail を落とす」形。
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
	// hide の後に解決するのは**クエリを減らすため**。HideNoteEntity は
	// FileIDs を空にするので、隠した embed のファイル行は SELECT にも載らない。
	//
	// **順序は安全性の要ではない。** HideNoteEntity は Files も空にするので、
	// 逆順にしても hide が上書きして漏れない。ここを安全性の境界だと思って
	// HideNoteEntity から Files のクリアを外すと、そのとき初めて漏れる。
	if p.files != nil {
		// **スライス経由で受け取り直すこと。** resolver は要素を書き換えるので、
		// リテラルを渡すとコピーだけが埋まり top-level の files が空のままになる
		// (embed は pointer なので気付きにくい)。
		notes := []entity.NoteEntity{packed}
		p.files.ResolveFilesShallow(notes)
		packed = notes[0]
	}
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
