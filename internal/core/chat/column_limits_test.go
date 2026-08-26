package chat_test

import (
	"context"
	"strings"
	"testing"

	corechat "github.com/shiroha-a/mk/internal/core/chat"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AP 由来の値が chat の列に収まる形で書かれることを固定する (#2726)。
//
// mock repository は列制約を持たないので、ここで見るのは「service が渡す値」。
// 列長そのものは internal/repository の情報スキーマテストが固定する。

func TestEnsureRoomViaAP_TruncatesNameAndDescription(t *testing.T) {
	svc, repo := newRoomFedService(t)
	// **全角で埋める。** byte で数える実装だと 3 倍になって落ちる。
	require.NoError(t, svc.EnsureRoomViaAP(
		"room1",
		strings.Repeat("あ", 300),
		strings.Repeat("い", 3000),
		"remoteOwner",
	))
	room, err := repo.FindRoomByID("room1")
	require.NoError(t, err)
	assert.Equal(t, 256, len([]rune(room.Name)))
	assert.Equal(t, 2048, len([]rune(room.Description)))
}

func TestEnsureRoomViaAP_StripsNUL(t *testing.T) {
	svc, repo := newRoomFedService(t)
	require.NoError(t, svc.EnsureRoomViaAP("room1", "a\x00b", "c\x00d", "remoteOwner"))
	room, err := repo.FindRoomByID("room1")
	require.NoError(t, err)
	assert.Equal(t, "ab", room.Name)
	assert.Equal(t, "cd", room.Description)
}

func TestEnsureRoomViaAP_RejectsOversizedRoomID(t *testing.T) {
	svc, repo := newRoomFedService(t)
	err := svc.EnsureRoomViaAP(strings.Repeat("a", 33), "General", "desc", "remoteOwner")
	// room id は行の身元なので切らずに拒否する。
	require.ErrorIs(t, err, corechat.ErrInvalidTarget)
	_, ferr := repo.FindRoomByID(strings.Repeat("a", 33))
	assert.Error(t, ferr, "拒否した room は作られない")

	// 上限ちょうどは通す。
	require.NoError(t, svc.EnsureRoomViaAP(strings.Repeat("a", 32), "General", "desc", "remoteOwner"))
}

func TestCreateRoomMessageViaAP_TruncatesText(t *testing.T) {
	svc, repo := newRoomFedService(t)
	sender := &model.User{ID: "remote1", Username: "remote1"}
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "R", OwnerID: sender.ID}))

	require.NoError(t, svc.CreateRoomMessageViaAP(
		"https://remote.example/chat/messages/m1", sender, "room1", strings.Repeat("あ", 5000)))
	msgs, err := repo.ListMessagesByRoom("room1", "", "", 10)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.NotNil(t, msgs[0].Text)
	assert.Equal(t, 4096, len([]rune(*msgs[0].Text)))
}

func TestCreateRoomMessageViaAP_NULOnlyTextStaysNull(t *testing.T) {
	svc, repo := newRoomFedService(t)
	sender := &model.User{ID: "remote1", Username: "remote1"}
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "R", OwnerID: sender.ID}))

	require.NoError(t, svc.CreateRoomMessageViaAP(
		"https://remote.example/chat/messages/m1", sender, "room1", "\x00"))
	msgs, err := repo.ListMessagesByRoom("room1", "", "", 10)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	// NUL だけの本文は空になる。空文字を入れず NULL のままにする (生値が空の
	// ときと同じ形)。
	assert.Nil(t, msgs[0].Text)
}

func TestCreateRoomMessageViaAP_RejectsOversizedURI(t *testing.T) {
	svc, repo := newRoomFedService(t)
	sender := &model.User{ID: "remote1", Username: "remote1"}
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "room1", Name: "R", OwnerID: sender.ID}))

	longURI := "https://remote.example/chat/messages/" + strings.Repeat("a", 512)
	err := svc.CreateRoomMessageViaAP(longURI, sender, "room1", "hi")
	// uri を捨てて行だけ作ると retry のたびに重複するので、message ごと拒否する。
	require.ErrorIs(t, err, corechat.ErrInvalidTarget)
	msgs, ferr := repo.ListMessagesByRoom("room1", "", "", 10)
	require.NoError(t, ferr)
	assert.Empty(t, msgs)
}

func TestCreateMessageViaAP_TruncatesTextAndRejectsOversizedURI(t *testing.T) {
	chatRepo := newFakeRepo()
	idGen, _ := id.NewGenerator("aidx")
	svc := corechat.NewService(chatRepo, idGen)
	sender := &model.User{ID: "remote1", Username: "remote1"}

	msg, err := svc.CreateMessageViaAP(context.Background(),
		"https://remote.example/chat-messages/1", sender, "local1", strings.Repeat("あ", 5000))
	require.NoError(t, err)
	require.NotNil(t, msg.Text)
	assert.Equal(t, 4096, len([]rune(*msg.Text)))

	longURI := "https://remote.example/chat-messages/" + strings.Repeat("a", 512)
	_, err = svc.CreateMessageViaAP(context.Background(), longURI, sender, "local1", "hi")
	require.ErrorIs(t, err, corechat.ErrInvalidTarget)
}
