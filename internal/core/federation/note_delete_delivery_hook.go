package federation

import (
	"encoding/json"
	"log/slog"

	"github.com/shiroha-a/mk/internal/activitypub"
	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// NoteDeleteDeliveryHook implements core/note.DeleteFederationHook by emitting
// a Delete activity to the followers of the local author.
//
// 配信ルール:
//   - author がリモート → 配信不要 (リモート側で発火する)
//   - localOnly note → 配信不要
//   - public / home → 既知のリモートインスタンス全体 (sharedInbox 単位)。
//     ap/show などで pull 済みのインスタンスにも反映させるため。
//   - followers → フォロワー + メンション先 (フォロワーとは限らない)
//   - specified (DM) → 宛先 (visibleUserIds) とメンション先だけ。**フォロワーには
//     送らない** (宛先でない相手に DM の URI を見せない)
//
// 純粋リノート (ブースト) の削除だけは Delete ではなく Undo(Announce) を出す。
// 理由は activitypub.RenderUndoAnnounceForNote の doc を参照。
type NoteDeleteDeliveryHook struct {
	deliver  *DeliverService
	renderer *activitypub.Renderer
	urls     *activitypub.URLBuilder
	userRepo repository.UserRepository
}

// NewNoteDeleteDeliveryHook constructs a NoteDeleteDeliveryHook.
func NewNoteDeleteDeliveryHook(
	deliver *DeliverService,
	renderer *activitypub.Renderer,
	urls *activitypub.URLBuilder,
) *NoteDeleteDeliveryHook {
	return &NoteDeleteDeliveryHook{deliver: deliver, renderer: renderer, urls: urls}
}

// SetUserRepo wires a user repository used by the hook to look up remote
// inboxes for broadcast delivery. nil 渡しはフォロワーのみへの配信に戻る。
func (h *NoteDeleteDeliveryHook) SetUserRepo(r repository.UserRepository) {
	h.userRepo = r
}

// HasUserRepo reports whether the user repository was wired.
//
// **未配線だと DM の Delete が宛先へ届かない。** 宛先の inbox を引けないので
// `directInboxes` が常に空になり、specified の Delete は誰にも配送されず、
// フォロワー限定でも「フォロワーでない宛先」に届かない。失敗しても例外は
// 出ないので、載せておかないと気付けない。
func (h *NoteDeleteDeliveryHook) HasUserRepo() bool { return h.userRepo != nil }

// OnNoteDeleted is invoked by NoteDeleteService once a note has been removed.
func (h *NoteDeleteDeliveryHook) OnNoteDeleted(author *model.User, note *model.Note) {
	if author == nil || note == nil {
		return
	}
	if !author.IsLocal() {
		return
	}
	if note.LocalOnly {
		return
	}

	body, ok := h.renderBody(author, note)
	if !ok {
		return
	}

	// **宛先限定 (DM) はフォロワーへ流さない。** 宛先でない相手に、消したはずの
	// DM の URI を参照する activity を配る理由が無い。recipient 集合の作り方は
	// reaction_delivery_hook.go に揃える (specified は DirectRecipe のみ)。
	// upstream は specified でも deliverToFollowers を呼ぶが、mk-go は意図的に
	// 絞る (宛先側には直接届くので、届く相手が減るわけではない)。
	if note.Visibility == model.NoteVisibilitySpecified {
		h.deliverDirect(author, note, body, h.directInboxes(note))
		return
	}

	// Public / Home は既知のリモートインスタンス全体へ送る。フォロワー以外が
	// ap/show 経由で note を取り込んでいるケースを補償する。
	//
	// **フォロワー配送と重ねない (#2575)。** 既知リモートはフォロワーの上位集合
	// (どちらも COALESCE(NULLIF(sharedInbox,''), inbox) で解決する) なので、
	// 両方走らせると全フォロワーが同じ Delete を同じ URL に 2 回受け取る。
	// 既知リモート全体はメンション先を内包するので direct も不要 (ここは
	// specified ではないので DM 宛先は元から関係しない)。
	if h.broadcastDelete(author, note, body) {
		return
	}

	// フォロワーとは限らない直接宛先 (メンション先) へ送る。
	// followers 可視性のノートでメンションされたリモート user は Create を
	// 直接受け取っている (note_delivery_hook の deliverToDirectRecipients) ので、
	// Delete も直接届けないと相手側に残り続ける。
	direct := h.directInboxes(note)
	h.deliverDirect(author, note, body, direct)
	exclude := make(map[string]bool, len(direct))
	for _, inbox := range direct {
		exclude[inbox] = true
	}
	if err := h.deliver.DeliverToFollowersExcluding(author.ID, body, exclude); err != nil {
		slog.Warn("note delete delivery failed",
			"noteId", note.ID, "err", err)
	}
}

// renderBody renders the activity announcing the deletion and reports whether
// anything should be delivered at all.
//
// 純粋リノートは Delete(Tombstone) では取り消せない。受信側は Announce を
// `uri = <noteURI>/activity` で保存するのに対し Tombstone の id は `/activity`
// の付かない note URI で、両者は一致しない (mk-go 自身も processor.go が
// `renote.URI = act.ID` で保存する)。upstream NoteDeleteService も pure renote
// のときだけ renderUndo(renderAnnounce(...)) に切り替える。
func (h *NoteDeleteDeliveryHook) renderBody(author *model.User, note *model.Note) ([]byte, bool) {
	if corenote.IsPureRenote(note) {
		// specified な pure renote は Announce 自体を送っていない (#1886 で
		// 連合を skip している) ので、取り消す対象も無い。
		if note.Visibility == model.NoteVisibilitySpecified {
			return nil, false
		}
		// idGen は hook が持っていないので nil を渡す。inner Announce の
		// published が省かれるだけで (omitempty)、受信側が対象を引くのに使う
		// id / object は変わらない。
		undo := h.renderer.RenderUndoAnnounceForNote(author, note, nil)
		if undo == nil {
			return nil, false
		}
		body, err := json.Marshal(undo)
		if err != nil {
			slog.Warn("note delete delivery: marshal undo announce failed",
				"noteId", note.ID, "err", err)
			return nil, false
		}
		return body, true
	}

	noteURI := h.urls.NoteURI(note.ID)
	if note.URI != nil && *note.URI != "" {
		noteURI = *note.URI
	}
	del := h.renderer.RenderDelete(author, noteURI)
	// renderer 由来の Delete は string field だけで構成されるので Marshal は
	// 失敗しない。
	body, _ := json.Marshal(del)
	return body, true
}

// directInboxes returns the inboxes of remote users that must be reached even
// when they do not follow the author: the explicit recipients of a DM
// (visibleUserIds) and the mentioned users.
//
// upstream の getMentionedRemoteUsers (mentionedRemoteUsers + DM 宛先) に対応する。
// getRenotedOrRepliedRemoteUsers 相当 (この note を renote / reply した
// リモート user) は note repository が要るため未対応。
func (h *NoteDeleteDeliveryHook) directInboxes(note *model.Note) []string {
	ids := make([]string, 0, len(note.VisibleUserIDs)+len(note.Mentions))
	if note.Visibility == model.NoteVisibilitySpecified {
		ids = append(ids, note.VisibleUserIDs...)
	}
	ids = append(ids, note.Mentions...)
	return remoteInboxesForUserIDs(h.userRepo, ids, note.UserID)
}

// deliverDirect enqueues body to an explicit inbox list, logging failures.
func (h *NoteDeleteDeliveryHook) deliverDirect(author *model.User, note *model.Note, body []byte, inboxes []string) {
	if len(inboxes) == 0 {
		return
	}
	if err := h.deliver.DeliverActivity(author.ID, body, inboxes); err != nil {
		slog.Warn("note delete delivery: direct failed",
			"noteId", note.ID, "err", err)
	}
}

// broadcastDelete sends the Delete to every known remote inbox, reporting
// whether it took over delivery.
//
// **false を返したらフォロワーには送る。** 一覧が引けないときに何も送らないのは、
// フォロワーにすら届かなくなるぶん今より悪い。
func (h *NoteDeleteDeliveryHook) broadcastDelete(author *model.User, note *model.Note, body []byte) bool {
	if h.userRepo == nil {
		return false
	}
	switch note.Visibility {
	case model.NoteVisibilityPublic, model.NoteVisibilityHome:
	default:
		return false
	}
	inboxes, err := h.userRepo.ListRemoteInboxes()
	if err != nil {
		slog.Warn("note delete delivery: list remote inboxes failed",
			"noteId", note.ID, "err", err)
		return false
	}
	if len(inboxes) == 0 {
		// **0 件を「送るものが無い」と解釈しない。** 既知リモートがフォロワーの
		// 上位集合であることに寄りかかると、片方のクエリだけが変わったときに
		// 黙って配送が消える。空ならフォロワー配送に任せる。
		return false
	}
	if err := h.deliver.DeliverToInboxes(author.ID, body, inboxes); err != nil {
		slog.Warn("note delete delivery: broadcast failed",
			"noteId", note.ID, "err", err)
	}
	return true
}
