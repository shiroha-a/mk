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
	// **配送先は可視性から決める。** Update(Question) は RenderQuestionUpdate が
	// RenderNote をそのまま包むので content / cw / attachment が丸ごと入る。
	// specified (DM) なアンケートに票が入るたびに DeliverToFollowers へ流すと、
	// 宛先でもないリモートフォロワー全員に DM 本文が配送される
	// (upstream PollService.deliverQuestionUpdate も同型だが実害は残る)。
	// recipient 集合の作り方は reaction_delivery_hook.go に揃える:
	// specified は DirectRecipe のみ、それ以外は follower fanout。
	if target.Visibility == model.NoteVisibilitySpecified {
		inboxes := remoteInboxesForUserIDs(h.userRepo, target.VisibleUserIDs, target.UserID)
		if len(inboxes) == 0 {
			return
		}
		if err := h.deliver.DeliverActivity(target.UserID, body, inboxes); err != nil {
			slog.Warn("poll delivery: enqueue question update (specified) failed",
				"noteId", target.ID, "err", err)
		}
		return
	}
	if err := h.deliver.DeliverToFollowers(target.UserID, body); err != nil {
		slog.Warn("poll delivery: enqueue question update failed",
			"noteId", target.ID, "err", err)
	}
}

// remoteInboxesForUserIDs resolves the preferred inbox of every REMOTE user in
// ids, skipping local users, blank/duplicate ids, skipID and users without an
// inbox. 戻り値の inbox URL も重複排除する (sharedInbox を共有する相手が
// 複数居ても 1 回しか送らない)。
//
// note の宛先 (visibleUserIds / mentions) から DirectRecipe の inbox を組み立てる
// 用途で、PollDeliveryHook と NoteDeleteDeliveryHook の両方が使う。
func remoteInboxesForUserIDs(userRepo repository.UserRepository, ids []string, skipID string) []string {
	if userRepo == nil || len(ids) == 0 {
		return nil
	}
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, uid := range ids {
		if uid == "" || uid == skipID {
			continue
		}
		if _, dup := seen[uid]; dup {
			continue
		}
		seen[uid] = struct{}{}
		unique = append(unique, uid)
	}
	if len(unique) == 0 {
		return nil
	}
	users, err := userRepo.FindManyByIDs(unique)
	if err != nil {
		// 引けなければ配送先を組み立てられない。**フォロワーへの fallback は
		// しない** (宛先限定のノートを無関係な follower へ流すことになる)。
		slog.Warn("federation: direct recipient lookup failed",
			"requested", len(unique), "err", err)
		return nil
	}
	inboxes := make([]string, 0, len(users))
	seenInbox := make(map[string]struct{}, len(users))
	for _, u := range users {
		if u == nil || u.IsLocal() {
			continue
		}
		inbox := preferredInbox(u)
		if inbox == "" {
			continue
		}
		if _, dup := seenInbox[inbox]; dup {
			continue
		}
		seenInbox[inbox] = struct{}{}
		inboxes = append(inboxes, inbox)
	}
	return inboxes
}
