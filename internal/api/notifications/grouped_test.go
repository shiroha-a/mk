package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// strPtr is a tiny helper for *string fields on model rows.
func strPtr(s string) *string { return &s }

// groupedHandler wires a handler with user / note repos + query service so the
// grouped endpoint can resolve notifier users and viewer-visible notes.
func groupedHandler(t *testing.T) (*Handler, *notification.Service, *testutil.MockUserRepository, *testutil.MockNoteRepository) {
	t.Helper()
	h, svc := newTestHandler(t)
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	h.SetRepos(userRepo, noteRepo)
	h.SetQueryService(corenote.NewQueryService(noteRepo, testutil.NewMockFollowingRepository()))
	return h, svc, userRepo, noteRepo
}

func decodeGrouped(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(body, &resp))
	return resp
}

func TestGrouped_Empty(t *testing.T) {
	h, _, _, _ := groupedHandler(t)
	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, decodeGrouped(t, rec.Body.Bytes()))
}

func TestGrouped_InvalidParam(t *testing.T) {
	h, _, _, _ := groupedHandler(t)
	// malformed JSON body → bind error → INVALID_PARAM.
	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// 同一 note への連続 reaction が reaction:grouped に畳み込まれることを確認する。
func TestGrouped_ReactionsGroupOnSameNote(t *testing.T) {
	h, svc, userRepo, noteRepo := groupedHandler(t)
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	userRepo.Users["carol"] = &model.User{ID: "carol", Username: "carol"}
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "alice", Visibility: "public", User: &model.User{ID: "alice", Username: "alice"}}

	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeReaction, NoteID: "n1", Reaction: "👍"})
	require.NoError(t, err)
	_, err = svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "carol", Type: notification.TypeReaction, NoteID: "n1", Reaction: "🎉"})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{"limit":50}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))
	require.Equal(t, http.StatusOK, rec.Code)

	resp := decodeGrouped(t, rec.Body.Bytes())
	require.Len(t, resp, 1)
	assert.Equal(t, "reaction:grouped", resp[0]["type"])
	assert.NotNil(t, resp[0]["note"])
	reactions, ok := resp[0]["reactions"].([]any)
	require.True(t, ok)
	require.Len(t, reactions, 2)
	// 各 entry は {user, reaction}。
	for _, r := range reactions {
		entry := r.(map[string]any)
		assert.NotNil(t, entry["user"])
		assert.NotEmpty(t, entry["reaction"])
	}
}

// TestGrouped_NonVisibleEmbedHidden locks in the #1546/#1570 fix: a followers-only
// renote embedded in a notifications-grouped note must be blanked for a viewer who
// cannot see it. notehide.HideNotificationNotes fail-closes on the unset
// package-level following repo, so a non-follower viewer never sees the embed body.
func TestGrouped_NonVisibleEmbedHidden(t *testing.T) {
	h, svc, userRepo, noteRepo := groupedHandler(t)
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	secret := "secret"
	renote := &model.Note{ID: "emb", UserID: "stranger", Visibility: "followers", User: &model.User{ID: "stranger", Username: "stranger"}, Text: &secret}
	noteRepo.Notes["emb"] = renote
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "bob", Visibility: "public", User: &model.User{ID: "bob", Username: "bob"}, RenoteID: strPtr("emb"), Renote: renote}

	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeReaction, NoteID: "n1", Reaction: "👍"})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{"limit":50}`)
	setAuth(c, &model.User{ID: "alice"}) // alice does not follow stranger
	require.NoError(t, h.Grouped(c))
	require.Equal(t, http.StatusOK, rec.Code)

	resp := decodeGrouped(t, rec.Body.Bytes())
	require.Len(t, resp, 1)
	note, ok := resp[0]["note"].(map[string]any)
	require.True(t, ok, "notification must carry a note")
	renoteMap, ok := note["renote"].(map[string]any)
	require.True(t, ok, "embedded renote must be present")
	assert.Equal(t, true, renoteMap["isHidden"], "followers renote embed must be hidden from a non-follower viewer (#1546 IDOR guard)")
	assert.Nil(t, renoteMap["text"], "hidden embed text must be blanked")
}

// 異なる note への reaction は別グループ (= 単体 notification) のまま残る。
func TestGrouped_ReactionsDifferentNotesStaySeparate(t *testing.T) {
	h, svc, userRepo, noteRepo := groupedHandler(t)
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "alice", Visibility: "public", User: &model.User{ID: "alice"}}
	noteRepo.Notes["n2"] = &model.Note{ID: "n2", UserID: "alice", Visibility: "public", User: &model.User{ID: "alice"}}

	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeReaction, NoteID: "n1", Reaction: "👍"})
	require.NoError(t, err)
	_, err = svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeReaction, NoteID: "n2", Reaction: "👍"})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{"limit":50}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))

	resp := decodeGrouped(t, rec.Body.Bytes())
	require.Len(t, resp, 2)
	assert.Equal(t, "reaction", resp[0]["type"])
	assert.Equal(t, "reaction", resp[1]["type"])
}

// 同一 target note への連続 renote が renote:grouped (users[]) に畳み込まれる。
func TestGrouped_RenotesGroupOnSameTarget(t *testing.T) {
	h, svc, userRepo, noteRepo := groupedHandler(t)
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	userRepo.Users["carol"] = &model.User{ID: "carol", Username: "carol"}
	// 2 つの renote note は別 ID だが同じ target ("target") を renote する。
	noteRepo.Notes["rn1"] = &model.Note{ID: "rn1", UserID: "bob", Visibility: "public", RenoteID: strPtr("target"), User: &model.User{ID: "bob"}}
	noteRepo.Notes["rn2"] = &model.Note{ID: "rn2", UserID: "carol", Visibility: "public", RenoteID: strPtr("target"), User: &model.User{ID: "carol"}}

	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeRenote, NoteID: "rn1"})
	require.NoError(t, err)
	_, err = svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "carol", Type: notification.TypeRenote, NoteID: "rn2"})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{"limit":50}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))

	resp := decodeGrouped(t, rec.Body.Bytes())
	require.Len(t, resp, 1)
	assert.Equal(t, "renote:grouped", resp[0]["type"])
	users, ok := resp[0]["users"].([]any)
	require.True(t, ok)
	require.Len(t, users, 2)
}

// 異なる target を renote する notification は別グループのまま残る。
func TestGrouped_RenotesDifferentTargetsStaySeparate(t *testing.T) {
	h, svc, userRepo, noteRepo := groupedHandler(t)
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	noteRepo.Notes["rn1"] = &model.Note{ID: "rn1", UserID: "bob", Visibility: "public", RenoteID: strPtr("t1"), User: &model.User{ID: "bob"}}
	noteRepo.Notes["rn2"] = &model.Note{ID: "rn2", UserID: "bob", Visibility: "public", RenoteID: strPtr("t2"), User: &model.User{ID: "bob"}}

	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeRenote, NoteID: "rn1"})
	require.NoError(t, err)
	_, err = svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeRenote, NoteID: "rn2"})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{"limit":50}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))

	resp := decodeGrouped(t, rec.Body.Bytes())
	require.Len(t, resp, 2)
	assert.Equal(t, "renote", resp[0]["type"])
	assert.Equal(t, "renote", resp[1]["type"])
}

// renote grouping で notifier user が解決できない場合でも group は成立し、
// users[] は解決できた分のみになる (renoteUsers の空初期化分岐を覆う)。
func TestGrouped_RenoteGroupWithUnresolvedFirstUser(t *testing.T) {
	h, svc, userRepo, noteRepo := groupedHandler(t)
	// 最初の notifier (bob) は userRepo に登録しない → packed["user"] 不在。
	userRepo.Users["carol"] = &model.User{ID: "carol", Username: "carol"}
	noteRepo.Notes["rn1"] = &model.Note{ID: "rn1", UserID: "bob", Visibility: "public", RenoteID: strPtr("target"), User: &model.User{ID: "bob"}}
	noteRepo.Notes["rn2"] = &model.Note{ID: "rn2", UserID: "carol", Visibility: "public", RenoteID: strPtr("target"), User: &model.User{ID: "carol"}}

	ctx := context.Background()
	// carol を先に作る → list は newest-first で [bob(rn1), carol(rn2)]。
	// 先頭 (bob) が未解決なので renoteUsers は空初期化、続く carol だけ users[] に入る。
	_, err := svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "carol", Type: notification.TypeRenote, NoteID: "rn2"})
	require.NoError(t, err)
	_, err = svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeRenote, NoteID: "rn1"})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{"limit":50}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))

	resp := decodeGrouped(t, rec.Body.Bytes())
	require.Len(t, resp, 1)
	assert.Equal(t, "renote:grouped", resp[0]["type"])
	users, _ := resp[0]["users"].([]any)
	// bob は解決不能なので carol の 1 件のみ。
	assert.Len(t, users, 1)
}

// reaction group の後ろに非グループ通知 (follow) が並ぶと grouping が途切れる。
func TestGrouped_MixedTypesBreakGroup(t *testing.T) {
	h, svc, userRepo, noteRepo := groupedHandler(t)
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	userRepo.Users["carol"] = &model.User{ID: "carol", Username: "carol"}
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "alice", Visibility: "public", User: &model.User{ID: "alice"}}

	ctx := context.Background()
	// 作成順 (古い→新しい): follow, reaction, reaction。
	// list は newest-first なので [react2, react1, follow]。
	_, err := svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow})
	require.NoError(t, err)
	_, err = svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeReaction, NoteID: "n1", Reaction: "👍"})
	require.NoError(t, err)
	_, err = svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "carol", Type: notification.TypeReaction, NoteID: "n1", Reaction: "🎉"})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{"limit":50}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))

	resp := decodeGrouped(t, rec.Body.Bytes())
	require.Len(t, resp, 2)
	assert.Equal(t, "reaction:grouped", resp[0]["type"])
	assert.Equal(t, "follow", resp[1]["type"])
}

// grouping 後に limit で slice されることを確認する。3 つの別 note への
// reaction を limit=2 で取ると 2 件に切り詰められる。
func TestGrouped_LimitSlicesAfterGrouping(t *testing.T) {
	h, svc, userRepo, noteRepo := groupedHandler(t)
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	for _, id := range []string{"n1", "n2", "n3"} {
		noteRepo.Notes[id] = &model.Note{ID: id, UserID: "alice", Visibility: "public", User: &model.User{ID: "alice"}}
	}

	ctx := context.Background()
	for _, id := range []string{"n1", "n2", "n3"} {
		_, err := svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeReaction, NoteID: id, Reaction: "👍"})
		require.NoError(t, err)
	}

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{"limit":2}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))

	resp := decodeGrouped(t, rec.Body.Bytes())
	require.Len(t, resp, 2)
}

// renote 通知だが target note が削除済 / 非可視で pack できない場合、upstream
// packMany は通知行ごと drop する (#1953)。grouped 経路でも note-required 通知が
// 落ちて空になることを確認する (以前は noteId だけの行を残していた)。
func TestGrouped_RenoteWithUnresolvableTargetDropped(t *testing.T) {
	h, svc, userRepo, noteRepo := groupedHandler(t)
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	// renote note 自体を repo に登録しない → noteByID に乗らず note pack 不能。
	_ = noteRepo

	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeRenote, NoteID: "rn1"})
	require.NoError(t, err)
	_, err = svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeRenote, NoteID: "rn2"})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{"limit":50}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))

	resp := decodeGrouped(t, rec.Body.Bytes())
	require.Len(t, resp, 0, "note を pack できない note-required 通知は drop される (#1953)")
}

// grouped endpoint も markAsRead の暗黙副作用を持つ (#420 と同挙動)。
func TestGrouped_MarksAsRead(t *testing.T) {
	h, svc, userRepo, _ := groupedHandler(t)
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	pub := &stubMainPublisher{}
	svc.SetMainStreamPublisher(pub)

	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Contains(t, pub.types("alice"), "readAllNotifications",
		"Grouped should publish readAllNotifications by default")
}

// markAsRead:false を明示した grouped 要求は既読化副作用を発火させない。
func TestGrouped_MarkAsReadFalseSkipsRead(t *testing.T) {
	h, svc, userRepo, _ := groupedHandler(t)
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	pub := &stubMainPublisher{}
	svc.SetMainStreamPublisher(pub)

	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{"markAsRead":false}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))
	require.Equal(t, http.StatusOK, rec.Code)

	assert.NotContains(t, pub.types("alice"), "readAllNotifications",
		"Grouped with markAsRead:false must not publish readAllNotifications")
}

// #2062: includeTypes/excludeTypes の enum 外は 400、enum 内 (obsolete 含む) は通る。
func TestGrouped_NotificationTypeEnum(t *testing.T) {
	h, _, _, _ := groupedHandler(t)
	// enum 外 type → 400。
	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{"includeTypes":["bogus"]}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code, "enum 外 includeTypes は 400 (#2062)")

	// excludeTypes enum 外 → 400。
	c, rec = newJSONRequest(t, "/api/i/notifications-grouped", `{"excludeTypes":["nope"]}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code, "enum 外 excludeTypes は 400 (#2062)")

	// enum 内 (obsolete pollVote 含む) → 200。
	c, rec = newJSONRequest(t, "/api/i/notifications-grouped", `{"excludeTypes":["reaction","pollVote"]}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))
	assert.Equal(t, http.StatusOK, rec.Code, "enum 内 (obsolete 含む) は通る")
}
