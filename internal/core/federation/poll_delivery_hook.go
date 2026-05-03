package federation

import (
	"encoding/json"
	"log/slog"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// PollDeliveryHook handles two outbound AP paths for polls (#690):
//   - OnVote: local user voted on a REMOTE poll → AP Note (with name +
//     inReplyTo) to the author's inbox. Misskey TS notes/polls/vote.ts と
//     同じ wire format。
//   - OnLocalPollUpdated: local poll's vote count changed → AP
//     Update(Question) broadcast to remote followers so counts stay in
//     sync without polling. PollService.deliverQuestionUpdate と同等。
//
// 失敗は best-effort で skip + warn ログ。投票自体は既に成功しているので
// 連合配信失敗で DB 状態を巻き戻すことはしない。
type PollDeliveryHook struct {
	renderer *activitypub.Renderer
	deliver  *DeliverService
	userRepo repository.UserRepository
	urls     *activitypub.URLBuilder
	idGen    id.Generator
}

// NewPollDeliveryHook constructs a PollDeliveryHook.
func NewPollDeliveryHook(renderer *activitypub.Renderer, deliver *DeliverService, userRepo repository.UserRepository, urls *activitypub.URLBuilder, idGen id.Generator) *PollDeliveryHook {
	return &PollDeliveryHook{
		renderer: renderer,
		deliver:  deliver,
		userRepo: userRepo,
		urls:     urls,
		idGen:    idGen,
	}
}

// OnVote is invoked by core/poll.Service after a successful vote. Skip when
// the target poll is local (no remote delivery needed) or when essential AP
// metadata (target.URI, author.Inbox, author.URI) is missing.
func (h *PollDeliveryHook) OnVote(voter *model.User, target *model.Note, choice int, choiceName string) {
	if h == nil || voter == nil || target == nil {
		return
	}
	// local poll: 連合先に通知不要 (local user は同インスタンスの DB から
	// 直接 count を読む)。
	if target.UserHost == nil || *target.UserHost == "" {
		return
	}
	if target.URI == nil || *target.URI == "" {
		return
	}
	author, err := h.userRepo.FindByID(target.UserID)
	if err != nil || author == nil {
		return
	}
	if author.Inbox == nil || *author.Inbox == "" {
		return
	}
	authorURI := ""
	if author.URI != nil {
		authorURI = *author.URI
	}
	if authorURI == "" {
		return
	}

	activity := h.renderer.RenderVote(voter, target, *target.URI, authorURI, choiceName)
	body, err := json.Marshal(activity)
	if err != nil {
		slog.Warn("poll delivery: marshal vote activity failed",
			"voter", voter.ID, "target", target.ID, "err", err)
		return
	}
	if err := h.deliver.DeliverActivity(voter.ID, body, []string{*author.Inbox}); err != nil {
		slog.Warn("poll delivery: enqueue vote deliver failed",
			"voter", voter.ID, "target", target.ID, "inbox", *author.Inbox, "err", err)
	}
}

// OnLocalPollUpdated broadcasts an AP Update(Question) for a local poll to
// all remote followers of the poll author so they refresh their count
// display. local-only ノートや LocalOnly フラグ時は skip。target が
// remote (本来は OnVote の path) の場合も skip する。
func (h *PollDeliveryHook) OnLocalPollUpdated(target *model.Note) {
	if h == nil || target == nil {
		return
	}
	if target.UserHost != nil && *target.UserHost != "" {
		return
	}
	if target.LocalOnly {
		return
	}

	activity := h.renderer.RenderQuestionUpdate(target, h.idGen)
	body, err := json.Marshal(activity)
	if err != nil {
		slog.Warn("poll delivery: marshal question update failed",
			"noteId", target.ID, "err", err)
		return
	}
	// Update activity は note 作成と同じく followers / mentioned remote /
	// reply target の remote / quote target の remote へ届ける必要がある。
	// 投票による count 変動は count を見せている全 follower に届くべき
	// なので DeliverToFollowers で十分 (Misskey TS upstream も
	// deliverToFollowers のみ使う)。
	if err := h.deliver.DeliverToFollowers(target.UserID, body); err != nil {
		slog.Warn("poll delivery: enqueue question update failed",
			"noteId", target.ID, "err", err)
	}
}
