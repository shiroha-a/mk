package federation

import (
	"encoding/json"
	"log/slog"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// NoteDeleteDeliveryHook implements core/note.DeleteFederationHook by emitting
// a Delete activity to the followers of the local author.
//
// 配信ルール:
//   - author がリモート → 配信不要 (リモート側で発火する)
//   - localOnly note → 配信不要
//   - それ以外 → フォロワー (リモート) + 既知のリモートインスタンス全体
//     (sharedInbox 単位) に Delete を送る。ap/show などで pull 済みの
//     インスタンスにも反映させるためフォロワーに加えブロードキャストする。
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

	noteURI := h.urls.NoteURI(note.ID)
	if note.URI != nil && *note.URI != "" {
		noteURI = *note.URI
	}
	del := h.renderer.RenderDelete(author, noteURI)
	body, _ := json.Marshal(del)
	// Public / Home は既知のリモートインスタンス全体へ送る。フォロワー以外が
	// ap/show 経由で note を取り込んでいるケースを補償する。
	//
	// **フォロワー配送と重ねない (#2575)。** 既知リモートはフォロワーの上位集合
	// (どちらも COALESCE(NULLIF(sharedInbox,''), inbox) で解決する) なので、
	// 両方走らせると全フォロワーが同じ Delete を同じ URL に 2 回受け取る。
	if h.broadcastDelete(author, note, body) {
		return
	}
	if err := h.deliver.DeliverToFollowers(author.ID, body); err != nil {
		slog.Warn("note delete delivery failed",
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
