package entity

import (
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPackNotification_NilInput(t *testing.T) {
	assert.Nil(t, PackNotification(nil, nil, nil, nil, nil, nil))
}

func TestPackNotification_FollowType_WithUser(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	nid := idGen.Generate(time.Now())
	n := &notification.Notification{
		ID:         nid,
		CreatedAt:  time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC),
		Type:       notification.TypeFollow,
		NotifierID: "u_bob",
	}
	user := &model.User{ID: "u_bob", Username: "bob", UsernameLower: "bob"}
	out := PackNotification(n, user, nil, idGen, nil, nil)
	assert.Equal(t, nid, out["id"])
	assert.Equal(t, "2026-04-19T12:00:00.000Z", out["createdAt"])
	assert.Equal(t, "follow", out["type"])
	// userId はnotifierIdから取る (TS互換)
	assert.Equal(t, "u_bob", out["userId"])
	// user は packed UserLite
	u, ok := out["user"].(UserLite)
	assert.True(t, ok)
	assert.Equal(t, "bob", u.Username)
	// note はfollowタイプでは無い
	_, hasNote := out["note"]
	assert.False(t, hasNote)
}

func TestPackNotification_ReactionType_WithNote(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	nid := idGen.Generate(time.Now())
	noteID := idGen.Generate(time.Now())
	n := &notification.Notification{
		ID:         nid,
		CreatedAt:  time.Now(),
		Type:       notification.TypeReaction,
		NotifierID: "u_alice",
		NoteID:     noteID,
		Reaction:   ":smile:",
	}
	user := &model.User{ID: "u_alice", Username: "alice"}
	note := &model.Note{ID: noteID, UserID: "u_me", Text: ptrStr("hi")}
	out := PackNotification(n, user, note, idGen, nil, nil)
	assert.Equal(t, "reaction", out["type"])
	assert.Equal(t, "u_alice", out["userId"])
	assert.Equal(t, ":smile:", out["reaction"])
	// note は packed で返る
	_, hasNote := out["note"]
	assert.True(t, hasNote)
}

func TestPackNotification_PollVote_WithChoice(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	choice := 2
	n := &notification.Notification{
		ID:         "x",
		CreatedAt:  time.Now(),
		Type:       notification.TypePollVote,
		NotifierID: "u_voter",
		NoteID:     "n1",
		Choice:     &choice,
	}
	out := PackNotification(n, nil, nil, idGen, nil, nil)
	assert.Equal(t, 2, out["choice"])
	// user / note は nil なので含まれない
	_, hasUser := out["user"]
	assert.False(t, hasUser)
	_, hasNote := out["note"]
	assert.False(t, hasNote)
}

func TestPackNotification_PopulatesInstanceOnUser(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	remoteHost := "remote.example"
	themeColor := "#cafe00"
	instLookup := &stubInstanceLookup{data: map[string]*model.Instance{
		remoteHost: {Host: remoteHost, ThemeColor: &themeColor},
	}}

	n := &notification.Notification{
		ID:         "x",
		CreatedAt:  time.Now(),
		Type:       notification.TypeRenote,
		NotifierID: "u_remote",
	}
	user := &model.User{ID: "u_remote", Username: "remote", Host: &remoteHost}

	out := PackNotification(n, user, nil, idGen, instLookup, nil)

	u, ok := out["user"].(UserLite)
	require.True(t, ok)
	require.NotNil(t, u.Instance, "user.instance must be set for remote notifier")
	assert.Equal(t, themeColor, *u.Instance.ThemeColor)
}

func TestPackNotification_ExtraFields_Merged(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	n := &notification.Notification{
		ID:        "x",
		CreatedAt: time.Now(),
		Type:      notification.TypeExportCompleted,
		Extra:     map[string]any{"fileId": "df_1"},
	}
	out := PackNotification(n, nil, nil, idGen, nil, nil)
	assert.Equal(t, "df_1", out["fileId"])
}

// #1249 以前に永続化された exportCompleted 通知は exportedEntity に mk-go 内部値
// (plural / kebab) を持つ。misskey_dart の enum decode を落とさないよう read 時に
// singular / camelCase へ正規化する (#1391)。
func TestPackNotification_NormalizesExportedEntity(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	cases := map[string]string{
		"notes":         "note",
		"favorites":     "favorite",
		"user-lists":    "userList",
		"antennas":      "antenna",
		"clips":         "clip",
		"mute":          "muting",
		"custom-emojis": "customEmoji",
		// already-canonical / unknown はそのまま。
		"note":      "note",
		"following": "following",
		"blocking":  "blocking",
		"weird":     "weird",
	}
	for stored, want := range cases {
		n := &notification.Notification{
			ID:        "x",
			CreatedAt: time.Now(),
			Type:      notification.TypeExportCompleted,
			Extra:     map[string]any{"exportedEntity": stored, "fileId": "df_1"},
		}
		out := PackNotification(n, nil, nil, idGen, nil, nil)
		assert.Equal(t, want, out["exportedEntity"], "stored=%q", stored)
		assert.Equal(t, "df_1", out["fileId"], "stored=%q: other extra fields preserved", stored)
	}
}

func ptrStr(s string) *string { return &s }

// PackNotifications は N 件の通知を pack しても InstanceLookup を 1 回 /
// EmojiLookup は host 数分しか叩かないことを確認する (#428 N+1 解消)。
// 単品 PackNotification を loop で呼ぶと 1 件あたり 2 + 2 (note 用 +
// user 用) クエリ発生するため、20 件で 80 クエリにふくらむ前提を防ぐ。
func TestPackNotifications_BatchesResolvers(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	hostA := "a.example"
	hostB := "b.example"
	themeColor := "#cafe00"
	instLookup := &stubInstanceLookup{data: map[string]*model.Instance{
		hostA: {Host: hostA, ThemeColor: &themeColor},
		hostB: {Host: hostB, ThemeColor: &themeColor},
	}}
	emojiLookup := &stubEmojiLookup{data: map[string][]*model.Emoji{}}

	// 3 件の通知: 同じ host を含む / 別 host / local の混在で host 集約も確認。
	mkItem := func(id string, host *string) NotificationItem {
		u := &model.User{ID: "u" + id, Username: "u" + id, UsernameLower: "u" + id, Host: host}
		return NotificationItem{
			N: &notification.Notification{
				ID: id, CreatedAt: time.Now(), Type: notification.TypeFollow, NotifierID: "u" + id,
			},
			User: u,
		}
	}
	items := []NotificationItem{
		mkItem("n1", &hostA),
		mkItem("n2", &hostA),
		mkItem("n3", &hostB),
	}

	out := PackNotifications(items, idGen, instLookup, emojiLookup)
	require.Len(t, out, 3)

	assert.Len(t, instLookup.calls, 1, "InstanceResolver は 1 batch fetch のみ")
	// host は a/b の 2 つ uniq 化されている。
	assert.ElementsMatch(t, []string{hostA, hostB}, instLookup.calls[0])

	// remote user の Instance が乗っていることだけ抜き取り検証。
	for _, item := range out {
		u, ok := item["user"].(UserLite)
		require.True(t, ok)
		require.NotNil(t, u.Instance)
		assert.Equal(t, themeColor, *u.Instance.ThemeColor)
	}
}

// item 内で N == nil な要素は skip され、残りは入力順序を保つことを確認。
func TestPackNotifications_SkipsNilNotifications(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	items := []NotificationItem{
		{N: &notification.Notification{ID: "x1", CreatedAt: time.Now(), Type: notification.TypeFollow}},
		{N: nil},
		{N: &notification.Notification{ID: "x2", CreatedAt: time.Now(), Type: notification.TypeFollow}},
	}
	out := PackNotifications(items, idGen, nil, nil)
	require.Len(t, out, 2)
	assert.Equal(t, "x1", out[0]["id"])
	assert.Equal(t, "x2", out[1]["id"])
}

// note を持つ通知も 1 batch で resolve されることを確認 (note.user の Instance
// と top-level notifier の Instance が同じ host でも fetch は 1 回)。
func TestPackNotifications_NoteAndUserShareInstanceFetch(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	host := "remote.example"
	themeColor := "#abcdef"
	instLookup := &stubInstanceLookup{data: map[string]*model.Instance{
		host: {Host: host, ThemeColor: &themeColor},
	}}

	notifier := &model.User{ID: "u_a", Username: "a", UsernameLower: "a", Host: &host}
	noteAuthor := &model.User{ID: "u_b", Username: "b", UsernameLower: "b", Host: &host}
	note := &model.Note{ID: "n_1", UserID: "u_b", User: noteAuthor, Text: ptrStr("hi")}

	n := &notification.Notification{
		ID: "x", CreatedAt: time.Now(), Type: notification.TypeReply,
		NotifierID: "u_a", NoteID: "n_1",
	}

	out := PackNotifications([]NotificationItem{{N: n, User: notifier, Note: note}}, idGen, instLookup, nil)
	require.Len(t, out, 1)
	assert.Len(t, instLookup.calls, 1, "host 重複は uniq 化されて 1 回の fetch")
	assert.ElementsMatch(t, []string{host}, instLookup.calls[0])
}
