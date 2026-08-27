package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/entity"
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
	// users[] は **順序も** 見る。upstream notifications-grouped.ts:141 が
	// `userIds: [prev.notifierId]` で初期化し :145 が cur を push するので
	// [prev, cur] の順。降順ページでは新しい notifier が先頭になる。
	// 件数だけ見ると順序反転を取りこぼす (reaction:grouped 側と対称、#2736)。
	assert.Equal(t, []string{"carol", "bob"}, groupedUserIDs(t, resp[0]),
		"users[] の順序が upstream と一致すること")
}

// reaction group の createdAt は seed に使う prev の時刻を採る (upstream
// notifications-grouped.ts:118 の `createdAt: prev.createdAt`)。prev は一覧の並び順で
// 1 つ前なので、既定の降順ページでは新しい方 (sinceId 単独ページは昇順になるため
// 逆転する。fixture は降順で組んである)。
//
// endpoint 経由だと 2 件の通知が同一ミリ秒になりうるので、groupNotifications を
// 直接叩いて createdAt を別値に固定する。#2736 で prev/cur の引き方を index から
// id 突き合わせに変えたので、**createdAt の**取り違えを検出できるのはこのアサート
// だけ (user / reaction の対の取り違えは
// TestGrouped_GroupingSurvivesDroppedNotification 側が拾う)。
func TestGroupNotifications_ReactionGroupTakesPrevCreatedAt(t *testing.T) {
	newer := &notification.Notification{ID: "n2", Type: notification.TypeReaction, NotifierID: "carol", NoteID: "n1", Reaction: "🎉"}
	older := &notification.Notification{ID: "n1", Type: notification.TypeReaction, NotifierID: "bob", NoteID: "n1", Reaction: "👍"}
	packed := []map[string]any{
		{"id": "n2", "type": "reaction", "noteId": "n1", "reaction": "🎉", "createdAt": "2026-01-02T03:04:06.000Z", "user": map[string]any{"id": "carol"}},
		{"id": "n1", "type": "reaction", "noteId": "n1", "reaction": "👍", "createdAt": "2026-01-02T03:04:05.000Z", "user": map[string]any{"id": "bob"}},
	}

	out := groupNotifications([]*notification.Notification{newer, older}, packed, nil)
	require.Len(t, out, 1)
	assert.Equal(t, "reaction:grouped", out[0]["type"])
	assert.Equal(t, "2026-01-02T03:04:06.000Z", out[0]["createdAt"],
		"group の createdAt は prev を採ること (降順 fixture なので新しい側)")
}

// renote group は逆に cur の createdAt を採る (降順ページでは古い方。#2106 L23、
// upstream notifications-grouped.ts:139 の `createdAt: notification.createdAt`)。
// **型ごとに逆**なのが罠で、upstream も :118 が `prev.createdAt`、:139 が
// `notification.createdAt` と書き分けている。
func TestGroupNotifications_RenoteGroupTakesCurCreatedAt(t *testing.T) {
	newer := &notification.Notification{ID: "n2", Type: notification.TypeRenote, NotifierID: "carol", NoteID: "rn2"}
	older := &notification.Notification{ID: "n1", Type: notification.TypeRenote, NotifierID: "bob", NoteID: "rn1"}
	packed := []map[string]any{
		{"id": "n2", "type": "renote", "noteId": "rn2", "userId": "carol", "createdAt": "2026-01-02T03:04:06.000Z", "user": map[string]any{"id": "carol"}},
		{"id": "n1", "type": "renote", "noteId": "rn1", "userId": "bob", "createdAt": "2026-01-02T03:04:05.000Z", "user": map[string]any{"id": "bob"}},
	}
	noteByID := map[string]*model.Note{
		"rn2": {ID: "rn2", UserID: "carol", RenoteID: strPtr("target")},
		"rn1": {ID: "rn1", UserID: "bob", RenoteID: strPtr("target")},
	}

	out := groupNotifications([]*notification.Notification{newer, older}, packed, noteByID)
	require.Len(t, out, 1)
	assert.Equal(t, "renote:grouped", out[0]["type"])
	assert.Equal(t, "2026-01-02T03:04:05.000Z", out[0]["createdAt"],
		"group の createdAt は cur を採ること (降順 fixture なので古い側)")
}

// groupedUserIDs extracts the user ids of a renote:grouped row **in order**.
func groupedUserIDs(t *testing.T, row map[string]any) []string {
	t.Helper()
	users, ok := row["users"].([]any)
	require.True(t, ok, "users must be present on a renote:grouped row")
	out := make([]string, 0, len(users))
	for _, u := range users {
		m, ok := u.(map[string]any)
		require.True(t, ok)
		out = append(out, m["id"].(string))
	}
	return out
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

// #2106 N6: renote 通知の notifier user が解決できない (= hard-delete 等) 場合、その通知は
// 丸ごと drop される (upstream #packInternal の needsUser && !userIfNeed)。bob が解決不能なら
// bob の renote 通知は落ち、解決できた carol の renote 1 件のみが残る (単独なので grouped で
// なく単発の "renote")。
func TestGrouped_RenoteUnresolvedNotifierDropped(t *testing.T) {
	h, svc, userRepo, noteRepo := groupedHandler(t)
	// notifier bob は userRepo に登録しない → notifier 解決不能 → 通知ごと drop。
	userRepo.Users["carol"] = &model.User{ID: "carol", Username: "carol"}
	noteRepo.Notes["rn1"] = &model.Note{ID: "rn1", UserID: "bob", Visibility: "public", RenoteID: strPtr("target"), User: &model.User{ID: "bob"}}
	noteRepo.Notes["rn2"] = &model.Note{ID: "rn2", UserID: "carol", Visibility: "public", RenoteID: strPtr("target"), User: &model.User{ID: "carol"}}

	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "carol", Type: notification.TypeRenote, NoteID: "rn2"})
	require.NoError(t, err)
	_, err = svc.Create(ctx, notification.CreateInput{NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeRenote, NoteID: "rn1"})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{"limit":50}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))

	resp := decodeGrouped(t, rec.Body.Bytes())
	require.Len(t, resp, 1, "bob (解決不能 notifier) の通知は drop され carol の 1 件のみ")
	assert.Equal(t, "renote", resp[0]["type"], "残り 1 件は単発の renote")
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

// limit が endpoint 全体として効くことを確認する。3 つの別 note への reaction を
// limit=2 で取ると 2 件になる。
//
// **切り詰めているのは grouping 後の slice ではなく `svc.List`。** List は
// `len(out) >= limit` で打ち切るので、grouping が件数を増やさない以上あの slice は
// 到達しない (grouped.go 側のコメント参照)。ここが固定しているのは endpoint の
// 外形であって slice の実装ではない。
func TestGrouped_LimitAppliesToResult(t *testing.T) {
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

// renote 通知の **renote note 自体** (= 通知の NoteID が指す note) が削除済 / 非可視で
// pack できない場合、通知行ごと drop する (#1953)。grouped 経路でも note-required
// 通知が落ちて空になることを確認する (以前は noteId だけの行を残していた)。
//
// renoteTargetID が "target note" と呼ぶのは その note の RenoteID のほうなので
// 取り違えないこと。ここで落としているのは renote note 側。
//
// 削除済 note の drop は upstream packMany と同じだが、**不可視 note まで落とすのは
// mk-go 独自** (upstream は hideNote で blank して行を残す)。
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

// #2736: packer が通知を drop すると filtered と packed の長さがずれ、
// grouped が index out of range で panic して 500 を返していた。
// role 削除済の roleAssigned は packer が通知ごと落とす。
func TestGrouped_DroppedRoleAssignedDoesNotPanic(t *testing.T) {
	h, svc := newTestHandler(t)
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bobuser"}
	h.SetRepos(userRepo, noteRepo)
	// role 削除済 = lookup が false を返す
	h.SetRoleLookup(func(string) (map[string]any, bool) { return nil, false })

	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", Type: notification.TypeRoleAssigned,
		Extra: map[string]any{"roleId": "deleted-role"},
	})
	require.NoError(t, err)
	_, err = svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow,
	})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1, "drop された roleAssigned は結果に出ない")
	assert.Equal(t, "follow", resp[0]["type"])
}

// invitation 削除済の chatRoomInvitationReceived も同じく drop される。
// accept / reject で日常的に起きるので、こちらも固定する。
func TestGrouped_DroppedChatInvitationDoesNotPanic(t *testing.T) {
	h, svc := newTestHandler(t)
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bobuser"}
	h.SetRepos(userRepo, noteRepo)
	h.SetChatInvitationLookup(func(string, string) (map[string]any, bool) { return nil, false })

	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob",
		Type:  notification.TypeChatRoomInvitationReceived,
		Extra: map[string]any{"invitationId": "gone"},
	})
	require.NoError(t, err)
	_, err = svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow,
	})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "follow", resp[0]["type"])
}

// grouped 側も同じ resolver を通ること (Show とは別の pack 呼び出し)。
func TestGrouped_ResolvesNoteFiles(t *testing.T) {
	h, svc := newTestHandler(t)
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	driveRepo := testutil.NewMockDriveFileRepository()
	driveRepo.Files["f1"] = &model.DriveFile{
		ID: "f1", UserID: strPtr("bob"), Name: "a.png", Type: "image/png",
		URL: "https://example.com/f1",
	}
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bobuser"}
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "bob", Visibility: "public",
		FileIDs: model.StringArray{"f1"},
		User:    &model.User{ID: "bob", Username: "bobuser"},
	}
	h.SetRepos(userRepo, noteRepo)
	h.SetQueryService(corenote.NewQueryService(noteRepo, testutil.NewMockFollowingRepository()))
	h.SetNoteFieldResolver(entity.NewNoteFieldResolver(driveRepo, nil, nil, nil, nil, idGen))

	_, err := svc.Create(context.Background(), notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeMention, NoteID: "n1",
	})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	note := resp[0]["note"].(map[string]any)
	require.Len(t, note["files"].([]any), 1)
}

// drop が grouping の対応付けを壊していないこと。#2736 の修正は filtered の index を
// 使わなくなったので、group に別の通知が混ざらないことを user/reaction の対で見る
// (件数だけ見ると off-by-one を取りこぼす)。
func TestGrouped_GroupingSurvivesDroppedNotification(t *testing.T) {
	h, svc := newTestHandler(t)
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bobuser"}
	userRepo.Users["carol"] = &model.User{ID: "carol", Username: "carol"}
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "alice", Visibility: "public",
		User: &model.User{ID: "alice", Username: "alice"},
	}
	h.SetRepos(userRepo, noteRepo)
	h.SetQueryService(corenote.NewQueryService(noteRepo, testutil.NewMockFollowingRepository()))
	h.SetRoleLookup(func(string) (map[string]any, bool) { return nil, false })

	ctx := context.Background()
	// 古い順に作る (一覧は newest first)。drop される roleAssigned は末尾 (= 最古)。
	_, err := svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", Type: notification.TypeRoleAssigned,
		Extra: map[string]any{"roleId": "deleted-role"},
	})
	require.NoError(t, err)
	for _, spec := range []struct{ notifier, reaction string }{{"bob", "\U0001F44D"}, {"carol", "\U0001F389"}} {
		_, err = svc.Create(ctx, notification.CreateInput{
			NotifieeID: "alice", NotifierID: spec.notifier, Type: notification.TypeReaction,
			NoteID: "n1", Reaction: spec.reaction,
		})
		require.NoError(t, err)
	}

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1, "2 件の reaction が 1 グループに畳まれること")
	assert.Equal(t, "reaction:grouped", resp[0]["type"])
	// 対応 (user↔reaction) だけでなく **順序** も見る。upstream は [prev, cur] の順に
	// push するので newest first。map に畳むと順序反転を取りこぼす。
	assert.Equal(t,
		[]groupedReaction{{User: "carol", Reaction: "\U0001F389"}, {User: "bob", Reaction: "\U0001F44D"}},
		reactionEntries(t, resp[0]),
		"group の各 entry で user と reaction の対応と順序が保たれること")
}

// #2736: **列の先頭 (= 最新) が drop されるケース。** ここが id 突き合わせでないと
// 壊れる唯一の形で、「panic だけ bounds-guard で塞ぐ」素朴な修正はこの fixture が
// 無いと素通りする (drop が末尾や中間にあるうちは index 版と出力が一致してしまう)。
//
// 先頭が落ちると後続の index が 1 つずつずれるので、bounds-guard 版は 2 件の
// reaction を別グループとして出す。
func TestGrouped_DroppedHeadKeepsGrouping(t *testing.T) {
	h, svc := newTestHandler(t)
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bobuser"}
	userRepo.Users["carol"] = &model.User{ID: "carol", Username: "carol"}
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "alice", Visibility: "public",
		User: &model.User{ID: "alice", Username: "alice"},
	}
	h.SetRepos(userRepo, noteRepo)
	h.SetQueryService(corenote.NewQueryService(noteRepo, testutil.NewMockFollowingRepository()))
	h.SetRoleLookup(func(string) (map[string]any, bool) { return nil, false })

	ctx := context.Background()
	// 古い順に作る。一覧は newest first なので drop される roleAssigned が先頭に来る。
	for _, spec := range []struct{ notifier, reaction string }{{"bob", "\U0001F44D"}, {"carol", "\U0001F389"}} {
		_, err := svc.Create(ctx, notification.CreateInput{
			NotifieeID: "alice", NotifierID: spec.notifier, Type: notification.TypeReaction,
			NoteID: "n1", Reaction: spec.reaction,
		})
		require.NoError(t, err)
	}
	_, err := svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", Type: notification.TypeRoleAssigned,
		Extra: map[string]any{"roleId": "deleted-role"},
	})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1, "先頭 drop の後ろの 2 件は 1 グループに畳まれること")
	assert.Equal(t, "reaction:grouped", resp[0]["type"])
	assert.Equal(t,
		[]groupedReaction{{User: "carol", Reaction: "\U0001F389"}, {User: "bob", Reaction: "\U0001F44D"}},
		reactionEntries(t, resp[0]))
}

// #2736: pack で drop された通知は出力に出ないが、**グループの区切りとしては残る**。
// upstream i/notifications-grouped は raw 列で grouping してから packGroupedMany で
// null を落とす順なので、間に drop を挟んだ 2 件の同一 note reaction は畳まれない。
func TestGrouped_DroppedNotificationBreaksGrouping(t *testing.T) {
	h, svc := newTestHandler(t)
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bobuser"}
	userRepo.Users["carol"] = &model.User{ID: "carol", Username: "carol"}
	noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "alice", Visibility: "public",
		User: &model.User{ID: "alice", Username: "alice"},
	}
	h.SetRepos(userRepo, noteRepo)
	h.SetQueryService(corenote.NewQueryService(noteRepo, testutil.NewMockFollowingRepository()))
	h.SetRoleLookup(func(string) (map[string]any, bool) { return nil, false })

	ctx := context.Background()
	// 古い順: reaction(bob) → drop される roleAssigned → reaction(carol)。
	_, err := svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeReaction,
		NoteID: "n1", Reaction: "\U0001F44D",
	})
	require.NoError(t, err)
	_, err = svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", Type: notification.TypeRoleAssigned,
		Extra: map[string]any{"roleId": "deleted-role"},
	})
	require.NoError(t, err)
	_, err = svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "carol", Type: notification.TypeReaction,
		NoteID: "n1", Reaction: "\U0001F389",
	})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications-grouped", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Grouped(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 2, "drop を挟んだ reaction は畳まれず 2 行のまま")
	for _, row := range resp {
		assert.Equal(t, "reaction", row["type"], "grouped に畳まれていないこと")
	}
}

// groupedReaction is one {user, reaction} pair of a reaction:grouped row.
type groupedReaction struct {
	User     string
	Reaction string
}

// reactionEntries extracts the {user.id, reaction} pairs of a reaction:grouped
// row **in order**, so both the pairing and the ordering can be asserted.
func reactionEntries(t *testing.T, row map[string]any) []groupedReaction {
	t.Helper()
	entries, ok := row["reactions"].([]any)
	require.True(t, ok, "reactions must be present on a grouped row")
	out := make([]groupedReaction, 0, len(entries))
	for _, e := range entries {
		m, ok := e.(map[string]any)
		require.True(t, ok)
		u, ok := m["user"].(map[string]any)
		require.True(t, ok, "each entry must carry its own user")
		out = append(out, groupedReaction{User: u["id"].(string), Reaction: m["reaction"].(string)})
	}
	return out
}
