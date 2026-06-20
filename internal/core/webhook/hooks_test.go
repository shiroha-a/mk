package webhook_test

import (
	"testing"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/core/webhook"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService(t *testing.T) (*webhook.Service, *fakeEnqueuer, *fakeWebhookRepo, *fakeSystemWebhookRepo) {
	t.Helper()
	enq := &fakeEnqueuer{}
	userRepo := &fakeWebhookRepo{hooks: make(map[string]*model.Webhook)}
	sysRepo := &fakeSystemWebhookRepo{hooks: make(map[string]*model.SystemWebhook)}
	svc := webhook.NewService(enq, userRepo, sysRepo, "https://example.com")
	return svc, enq, userRepo, sysRepo
}

func TestNoteCreateHook_FiresNoteEventOnAuthor(t *testing.T) {
	svc, enq, userRepo, _ := newTestService(t)
	userRepo.hooks["h1"] = &model.Webhook{
		ID: "h1", UserID: "alice", Active: true, On: pq.StringArray{"note"},
	}
	idGen, _ := id.NewGenerator("aidx")
	h := webhook.NewNoteCreateHook(svc, idGen)

	text := "hello"
	h.OnNoteCreated(
		&model.Note{ID: "n1", UserID: "alice", Text: &text, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "alice", Username: "alice", UsernameLower: "alice"},
		nil, nil,
	)
	require.Len(t, enq.userCalls, 1)
	assert.Equal(t, "h1", enq.userCalls[0].WebhookID)
}

func TestNoteCreateHook_FiresReplyEventOnParentAuthor(t *testing.T) {
	svc, enq, userRepo, _ := newTestService(t)
	userRepo.hooks["h_reply"] = &model.Webhook{
		ID: "h_reply", UserID: "bob", Active: true, On: pq.StringArray{"reply"},
	}
	idGen, _ := id.NewGenerator("aidx")
	h := webhook.NewNoteCreateHook(svc, idGen)

	text := "reply body"
	h.OnNoteCreated(
		&model.Note{ID: "n2", UserID: "alice", Text: &text, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "alice", Username: "alice", UsernameLower: "alice"},
		&model.Note{ID: "np", UserID: "bob"},
		nil,
	)
	require.Len(t, enq.userCalls, 1)
	assert.Equal(t, "reply", enq.userCalls[0].EventType)
}

// #1965: reply 先がスレッドをミュートしていれば reply webhook を出さない。
func TestNoteCreateHook_ReplyThreadMutedSkipped(t *testing.T) {
	svc, enq, userRepo, _ := newTestService(t)
	userRepo.hooks["h_reply"] = &model.Webhook{ID: "h_reply", UserID: "bob", Active: true, On: pq.StringArray{"reply"}}
	idGen, _ := id.NewGenerator("aidx")
	h := webhook.NewNoteCreateHook(svc, idGen)
	muteRepo := testutil.NewMockNoteThreadMutingRepository()
	require.NoError(t, muteRepo.Create(&model.NoteThreadMuting{UserID: "bob", ThreadID: "np"}))
	h.SetThreadMutingRepo(muteRepo)

	text := "reply body"
	h.OnNoteCreated(
		&model.Note{ID: "n2", UserID: "alice", Text: &text, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "alice", Username: "alice", UsernameLower: "alice"},
		&model.Note{ID: "np", UserID: "bob"},
		nil,
	)
	assert.Empty(t, enq.userCalls, "thread-muted reply 先には reply webhook を出さない (#1965)")
}

// #1965: reply 先が threaded note (ThreadID 持ち) の場合、その threadId で判定する。
func TestNoteCreateHook_ReplyThreadMuteUsesThreadID(t *testing.T) {
	svc, enq, userRepo, _ := newTestService(t)
	userRepo.hooks["h_reply"] = &model.Webhook{ID: "h_reply", UserID: "bob", Active: true, On: pq.StringArray{"reply"}}
	idGen, _ := id.NewGenerator("aidx")
	h := webhook.NewNoteCreateHook(svc, idGen)
	muteRepo := testutil.NewMockNoteThreadMutingRepository()
	require.NoError(t, muteRepo.Create(&model.NoteThreadMuting{UserID: "bob", ThreadID: "troot"}))
	h.SetThreadMutingRepo(muteRepo)

	troot := "troot"
	text := "reply body"
	h.OnNoteCreated(
		&model.Note{ID: "n2", UserID: "alice", Text: &text, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "alice", Username: "alice", UsernameLower: "alice"},
		&model.Note{ID: "np", UserID: "bob", ThreadID: &troot},
		nil,
	)
	assert.Empty(t, enq.userCalls, "reply 先ノートの threadId でミュート判定する (#1965)")
}

// #1965: mention 先がスレッドをミュートしていれば mention webhook を出さない。
func TestNoteCreateHook_MentionThreadMutedSkipped(t *testing.T) {
	svc, enq, userRepo, _ := newTestService(t)
	userRepo.hooks["h_m"] = &model.Webhook{ID: "h_m", UserID: "bob", Active: true, On: pq.StringArray{"mention"}}
	idGen, _ := id.NewGenerator("aidx")
	h := webhook.NewNoteCreateHook(svc, idGen)
	muteRepo := testutil.NewMockNoteThreadMutingRepository()
	require.NoError(t, muteRepo.Create(&model.NoteThreadMuting{UserID: "bob", ThreadID: "n_m"}))
	h.SetThreadMutingRepo(muteRepo)

	text := "hi @bob"
	h.OnNoteCreated(
		&model.Note{ID: "n_m", UserID: "alice", Text: &text, Visibility: model.NoteVisibilityPublic, Mentions: pq.StringArray{"bob"}},
		&model.User{ID: "alice", Username: "alice", UsernameLower: "alice"},
		nil, nil,
	)
	assert.Empty(t, enq.userCalls, "thread-muted mention 先には mention webhook を出さない (#1965)")
}

// #1965: renote はスレッドミュートで gate されない (upstream も ungated)。
func TestNoteCreateHook_RenoteThreadMuteNotGated(t *testing.T) {
	svc, enq, userRepo, _ := newTestService(t)
	userRepo.hooks["h_rn"] = &model.Webhook{ID: "h_rn", UserID: "bob", Active: true, On: pq.StringArray{"renote"}}
	idGen, _ := id.NewGenerator("aidx")
	h := webhook.NewNoteCreateHook(svc, idGen)
	muteRepo := testutil.NewMockNoteThreadMutingRepository()
	require.NoError(t, muteRepo.Create(&model.NoteThreadMuting{UserID: "bob", ThreadID: "nr"}))
	h.SetThreadMutingRepo(muteRepo)

	h.OnNoteCreated(
		&model.Note{ID: "n_rn", UserID: "alice", Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "alice", Username: "alice", UsernameLower: "alice"},
		nil, &model.Note{ID: "nr", UserID: "bob"},
	)
	require.Len(t, enq.userCalls, 1, "renote はスレッドミュートで抑制されない (#1965)")
	assert.Equal(t, "renote", enq.userCalls[0].EventType)
}

func TestNoteCreateHook_FiresRenoteEventOnRenoteAuthor(t *testing.T) {
	svc, enq, userRepo, _ := newTestService(t)
	userRepo.hooks["h_renote"] = &model.Webhook{
		ID: "h_renote", UserID: "bob", Active: true, On: pq.StringArray{"renote"},
	}
	idGen, _ := id.NewGenerator("aidx")
	h := webhook.NewNoteCreateHook(svc, idGen)

	h.OnNoteCreated(
		&model.Note{ID: "n3", UserID: "alice", Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "alice", Username: "alice", UsernameLower: "alice"},
		nil,
		&model.Note{ID: "nt", UserID: "bob"},
	)
	require.Len(t, enq.userCalls, 1)
	assert.Equal(t, "renote", enq.userCalls[0].EventType)
}

func TestNoteCreateHook_FiresMentionEventOnMentionedUsers(t *testing.T) {
	svc, enq, userRepo, _ := newTestService(t)
	userRepo.hooks["h_mention"] = &model.Webhook{
		ID: "h_mention", UserID: "carol", Active: true, On: pq.StringArray{"mention"},
	}
	idGen, _ := id.NewGenerator("aidx")
	h := webhook.NewNoteCreateHook(svc, idGen)

	h.OnNoteCreated(
		&model.Note{ID: "n4", UserID: "alice", Mentions: pq.StringArray{"carol"}, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "alice", Username: "alice", UsernameLower: "alice"},
		nil, nil,
	)
	require.Len(t, enq.userCalls, 1)
	assert.Equal(t, "mention", enq.userCalls[0].EventType)
}

func TestNoteCreateHook_SkipsSelfReplyAndMention(t *testing.T) {
	svc, enq, userRepo, _ := newTestService(t)
	userRepo.hooks["h_alice"] = &model.Webhook{
		ID: "h_alice", UserID: "alice", Active: true,
		On: pq.StringArray{"note", "reply", "renote", "mention"},
	}
	idGen, _ := id.NewGenerator("aidx")
	h := webhook.NewNoteCreateHook(svc, idGen)

	h.OnNoteCreated(
		&model.Note{ID: "n5", UserID: "alice", Mentions: pq.StringArray{"alice"}, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "alice", Username: "alice", UsernameLower: "alice"},
		&model.Note{ID: "np", UserID: "alice"},
		&model.Note{ID: "nt", UserID: "alice"},
	)
	// author(alice)のnoteイベントが1件だけ。reply/renote/mentionは自己発火抑制。
	require.Len(t, enq.userCalls, 1)
	assert.Equal(t, "note", enq.userCalls[0].EventType)
}

func TestNoteCreateHook_NilSafe(t *testing.T) {
	var h *webhook.NoteCreateHook
	h.OnNoteCreated(nil, nil, nil, nil)
	idGen, _ := id.NewGenerator("aidx")
	h = webhook.NewNoteCreateHook(nil, idGen)
	h.OnNoteCreated(nil, nil, nil, nil)
}

func TestReactionCreateHook(t *testing.T) {
	svc, enq, userRepo, _ := newTestService(t)
	userRepo.hooks["h1"] = &model.Webhook{
		ID: "h1", UserID: "alice", Active: true, On: pq.StringArray{"reaction"},
	}
	idGen, _ := id.NewGenerator("aidx")
	h := webhook.NewReactionCreateHook(svc, idGen)

	h.OnReactionCreated(
		&model.Note{ID: "n1", UserID: "alice", Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "bob", Username: "bob", UsernameLower: "bob"},
		"\U0001F44D",
	)
	require.Len(t, enq.userCalls, 1)
	assert.Equal(t, "reaction", enq.userCalls[0].EventType)
}

func TestReactionCreateHook_NilSafe(t *testing.T) {
	var h *webhook.ReactionCreateHook
	h.OnReactionCreated(nil, nil, "")
	idGen, _ := id.NewGenerator("aidx")
	h = webhook.NewReactionCreateHook(nil, idGen)
	h.OnReactionCreated(nil, nil, "")
}

func TestFollowingHook_FiresFollowAndFollowed(t *testing.T) {
	svc, enq, userRepo, _ := newTestService(t)
	userRepo.hooks["h_follow"] = &model.Webhook{
		ID: "h_follow", UserID: "alice", Active: true, On: pq.StringArray{"follow"},
	}
	userRepo.hooks["h_followed"] = &model.Webhook{
		ID: "h_followed", UserID: "bob", Active: true, On: pq.StringArray{"followed"},
	}
	h := webhook.NewFollowingHook(svc)

	follower := &model.User{ID: "alice", Username: "alice", UsernameLower: "alice"}
	followee := &model.User{ID: "bob", Username: "bob", UsernameLower: "bob"}
	h.OnFollow(follower, followee)
	h.OnFollowed(follower, followee)

	events := make(map[string]int)
	for _, c := range enq.userCalls {
		events[c.EventType]++
	}
	assert.Equal(t, 1, events["follow"])
	assert.Equal(t, 1, events["followed"])
}

func TestFollowingHook_Unfollow(t *testing.T) {
	svc, enq, userRepo, _ := newTestService(t)
	userRepo.hooks["h_u"] = &model.Webhook{
		ID: "h_u", UserID: "alice", Active: true, On: pq.StringArray{"unfollow"},
	}
	h := webhook.NewFollowingHook(svc)
	h.OnUnfollow(
		&model.User{ID: "alice", Username: "alice", UsernameLower: "alice"},
		&model.User{ID: "bob", Username: "bob", UsernameLower: "bob"},
	)
	require.Len(t, enq.userCalls, 1)
	assert.Equal(t, "unfollow", enq.userCalls[0].EventType)
}

func TestFollowingHook_NilSafe(t *testing.T) {
	var h *webhook.FollowingHook
	h.OnFollow(nil, nil)
	h.OnUnfollow(nil, nil)
	h.OnFollowed(nil, nil)
	h = webhook.NewFollowingHook(nil)
	h.OnFollow(nil, nil)
	h.OnUnfollow(nil, nil)
	h.OnFollowed(nil, nil)
}

func TestSignupHook_FiresUserCreated(t *testing.T) {
	svc, enq, _, sysRepo := newTestService(t)
	sysRepo.hooks["sh1"] = &model.SystemWebhook{
		ID: "sh1", IsActive: true, On: pq.StringArray{"userCreated"},
	}
	h := webhook.NewSignupHook(svc)
	h.OnUserCreated(&model.User{ID: "alice", Username: "alice", UsernameLower: "alice"})
	require.Len(t, enq.systemCalls, 1)
	assert.Equal(t, "sh1", enq.systemCalls[0].WebhookID)
	assert.Equal(t, "userCreated", enq.systemCalls[0].EventType)
}

func TestSignupHook_NilSafe(t *testing.T) {
	var h *webhook.SignupHook
	h.OnUserCreated(nil)
	h = webhook.NewSignupHook(nil)
	h.OnUserCreated(nil)
}
