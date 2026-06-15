package federation

import (
	"encoding/json"
	"log/slog"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// UserModerationDeliveryHook emits AP Delete / Undo(Delete) activities for a
// local user's actor when the account is suspended, deleted, or unsuspended
// (#1759). Mirrors upstream UserSuspendService / DeleteAccountService, which
// deliver Delete(actor) to every known sharedInbox on suspend/delete and
// Undo(Delete) on unsuspend.
//
// 配信ルール (NoteDeleteDeliveryHook と同方針):
//   - user がリモート → 配信不要 (当該インスタンスが発火元)
//   - それ以外 → フォロワー (リモート) + 既知のリモートインスタンス全体
//     (sharedInbox 単位) に配信する。フォロー関係に無い instance も ap/show
//     で actor を pull 済みのことがあるためブロードキャストで補償する。
type UserModerationDeliveryHook struct {
	deliver  *DeliverService
	renderer *activitypub.Renderer
	userRepo repository.UserRepository
}

// NewUserModerationDeliveryHook constructs a UserModerationDeliveryHook.
func NewUserModerationDeliveryHook(
	deliver *DeliverService,
	renderer *activitypub.Renderer,
	userRepo repository.UserRepository,
) *UserModerationDeliveryHook {
	return &UserModerationDeliveryHook{deliver: deliver, renderer: renderer, userRepo: userRepo}
}

// OnUserDeleted delivers a Delete(actor) for a suspended or deleted local user.
// delete-account 経路では keypair が cascade で消える前に呼ぶこと (署名に必要)。
func (h *UserModerationDeliveryHook) OnUserDeleted(user *model.User) {
	if user == nil || h.renderer == nil {
		return
	}
	h.broadcast(user, h.renderer.RenderDeleteActor(user))
}

// OnUserRestored delivers an Undo(Delete) for an unsuspended local user.
func (h *UserModerationDeliveryHook) OnUserRestored(user *model.User) {
	if user == nil || h.renderer == nil {
		return
	}
	h.broadcast(user, h.renderer.RenderUndoDeleteActor(user))
}

// broadcast signs the activity with the user's key and delivers it to the
// user's remote followers plus every known remote sharedInbox. Errors are
// best-effort logged (moderation actions must not fail on delivery hiccups).
func (h *UserModerationDeliveryHook) broadcast(user *model.User, activity any) {
	if h.deliver == nil || !user.IsLocal() {
		return
	}
	body, err := json.Marshal(activity)
	if err != nil {
		slog.Warn("user moderation delivery: marshal failed", "userId", user.ID, "err", err)
		return
	}
	if err := h.deliver.DeliverToFollowers(user.ID, body); err != nil {
		slog.Warn("user moderation delivery: followers deliver failed", "userId", user.ID, "err", err)
	}
	if h.userRepo == nil {
		return
	}
	inboxes, err := h.userRepo.ListRemoteInboxes()
	if err != nil {
		slog.Warn("user moderation delivery: list remote inboxes failed", "userId", user.ID, "err", err)
		return
	}
	if len(inboxes) == 0 {
		return
	}
	if err := h.deliver.DeliverActivity(user.ID, body, inboxes); err != nil {
		slog.Warn("user moderation delivery: broadcast failed", "userId", user.ID, "err", err)
	}
}
