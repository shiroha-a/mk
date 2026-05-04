package repository

import (
	"strings"
	"testing"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatRepository_Rooms(t *testing.T) {
	repo := NewChatRepository(testDB)
	user := insertTestUser(t, "u_chat_1", "chatuser1")
	defer cleanupUser(t, user.ID)

	// CreateRoom
	room := &model.ChatRoom{ID: "cr_1", Name: "Test Room", OwnerID: user.ID, Description: "desc"}
	require.NoError(t, repo.CreateRoom(room))
	defer testDB.Exec(`DELETE FROM "chat_room" WHERE id = ?`, room.ID)

	// FindRoomByID
	found, err := repo.FindRoomByID("cr_1")
	require.NoError(t, err)
	assert.Equal(t, "Test Room", found.Name)

	// FindRoomByID - not found
	_, err = repo.FindRoomByID("ghost")
	assert.Error(t, err)

	// UpdateRoom
	found.Name = "Updated"
	require.NoError(t, repo.UpdateRoom(found))

	// ListRoomsByOwner
	rooms, err := repo.ListRoomsByOwner(user.ID)
	require.NoError(t, err)
	assert.Len(t, rooms, 1)
	assert.Equal(t, "Updated", rooms[0].Name)

	// ListJoinedRooms (empty - no membership yet)
	joined, err := repo.ListJoinedRooms(user.ID)
	require.NoError(t, err)
	assert.Empty(t, joined)

	// DeleteRoom
	require.NoError(t, repo.DeleteRoom("cr_1"))
	_, err = repo.FindRoomByID("cr_1")
	assert.Error(t, err)
}

func TestChatRepository_Messages(t *testing.T) {
	repo := NewChatRepository(testDB)
	user1 := insertTestUser(t, "u_chat_2", "chatuser2")
	user2 := insertTestUser(t, "u_chat_3", "chatuser3")
	defer cleanupUser(t, user1.ID)
	defer cleanupUser(t, user2.ID)

	room := &model.ChatRoom{ID: "cr_2", Name: "Room", OwnerID: user1.ID}
	require.NoError(t, repo.CreateRoom(room))
	defer testDB.Exec(`DELETE FROM "chat_room" WHERE id = ?`, room.ID)

	// CreateMessage (room message)
	msg := &model.ChatMessage{
		ID: "cm_1", FromUserID: user1.ID, ToRoomID: &room.ID,
		Reads: pq.StringArray{}, Reactions: pq.StringArray{},
	}
	text := "hello"
	msg.Text = &text
	require.NoError(t, repo.CreateMessage(msg))
	defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, msg.ID)

	// FindMessageByID
	found, err := repo.FindMessageByID("cm_1")
	require.NoError(t, err)
	assert.Equal(t, "hello", *found.Text)

	// FindMessageByID - not found
	_, err = repo.FindMessageByID("ghost")
	assert.Error(t, err)

	// ListMessagesByRoom
	msgs, err := repo.ListMessagesByRoom(room.ID, 10)
	require.NoError(t, err)
	assert.Len(t, msgs, 1)

	// ListMessagesByRoom - default limit
	msgs2, err := repo.ListMessagesByRoom(room.ID, 0)
	require.NoError(t, err)
	assert.Len(t, msgs2, 1)

	// CreateMessage (DM)
	dm := &model.ChatMessage{
		ID: "cm_2", FromUserID: user1.ID, ToUserID: &user2.ID,
		Reads: pq.StringArray{}, Reactions: pq.StringArray{},
	}
	dmText := "dm"
	dm.Text = &dmText
	require.NoError(t, repo.CreateMessage(dm))
	defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, dm.ID)

	// ListMessagesByUser
	dms, err := repo.ListMessagesByUser(user1.ID, user2.ID, 10)
	require.NoError(t, err)
	assert.Len(t, dms, 1)

	// ListMessagesByUser - default limit
	dms2, err := repo.ListMessagesByUser(user1.ID, user2.ID, 0)
	require.NoError(t, err)
	assert.Len(t, dms2, 1)

	// SearchMessages
	results, err := repo.SearchMessages(user1.ID, "dm", 10)
	require.NoError(t, err)
	assert.Len(t, results, 1)

	// SearchMessages - default limit
	results2, err := repo.SearchMessages(user1.ID, "dm", 0)
	require.NoError(t, err)
	assert.Len(t, results2, 1)

	// MarkRead
	require.NoError(t, repo.MarkRead(user2.ID, "cm_2"))

	// CountUnread
	count, err := repo.CountUnread(user2.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	// DeleteMessage
	require.NoError(t, repo.DeleteMessage("cm_1"))
}

func TestChatRepository_Membership(t *testing.T) {
	repo := NewChatRepository(testDB)
	user := insertTestUser(t, "u_chat_4", "chatuser4")
	defer cleanupUser(t, user.ID)

	room := &model.ChatRoom{ID: "cr_3", Name: "Room", OwnerID: user.ID}
	require.NoError(t, repo.CreateRoom(room))
	defer testDB.Exec(`DELETE FROM "chat_room" WHERE id = ?`, room.ID)

	// CreateMembership
	mem := &model.ChatRoomMembership{ID: "mem_1", UserID: user.ID, RoomID: room.ID}
	require.NoError(t, repo.CreateMembership(mem))
	defer testDB.Exec(`DELETE FROM "chat_room_membership" WHERE id = ?`, mem.ID)

	// FindMembership
	found, err := repo.FindMembership(user.ID, room.ID)
	require.NoError(t, err)
	assert.Equal(t, false, found.IsMuted)

	// FindMembership - not found
	_, err = repo.FindMembership("ghost", room.ID)
	assert.Error(t, err)

	// UpdateMembership
	found.IsMuted = true
	require.NoError(t, repo.UpdateMembership(found))

	// ListMembersByRoom
	members, err := repo.ListMembersByRoom(room.ID)
	require.NoError(t, err)
	assert.Len(t, members, 1)

	// ListJoinedRooms (now has membership)
	joined, err := repo.ListJoinedRooms(user.ID)
	require.NoError(t, err)
	assert.Len(t, joined, 1)

	// DeleteMembership
	require.NoError(t, repo.DeleteMembership(user.ID, room.ID))
}

func TestChatRepository_Reactions(t *testing.T) {
	repo := NewChatRepository(testDB)
	user1 := insertTestUser(t, "u_chat_rx1", "chatrx1")
	user2 := insertTestUser(t, "u_chat_rx2", "chatrx2")
	defer cleanupUser(t, user1.ID)
	defer cleanupUser(t, user2.ID)

	msg := &model.ChatMessage{
		ID: "cm_rx", FromUserID: user1.ID, ToUserID: &user2.ID,
		Reads: pq.StringArray{}, Reactions: pq.StringArray{},
	}
	require.NoError(t, repo.CreateMessage(msg))
	defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, msg.ID)

	// AddReaction
	require.NoError(t, repo.AddReaction(msg.ID, user1.ID+"/👍"))
	require.NoError(t, repo.AddReaction(msg.ID, user2.ID+"/❤️"))

	found, _ := repo.FindMessageByID(msg.ID)
	assert.Len(t, found.Reactions, 2)

	// RemoveReaction
	require.NoError(t, repo.RemoveReaction(msg.ID, user1.ID+"/👍"))
	found, _ = repo.FindMessageByID(msg.ID)
	assert.Len(t, found.Reactions, 1)
}

func TestChatRepository_DeliveryStatus(t *testing.T) {
	repo := NewChatRepository(testDB)
	user1 := insertTestUser(t, "u_chat_ds1", "chatds1")
	user2 := insertTestUser(t, "u_chat_ds2", "chatds2")
	defer cleanupUser(t, user1.ID)
	defer cleanupUser(t, user2.ID)

	msg := &model.ChatMessage{
		ID: "cm_ds", FromUserID: user1.ID, ToUserID: &user2.ID,
		Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{},
	}
	require.NoError(t, repo.CreateMessage(msg))
	defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, msg.ID)

	require.NoError(t, repo.UpdateDeliveryStatus(msg.ID, true, false))
	found, _ := repo.FindMessageByID(msg.ID)
	assert.True(t, found.IsDelivering)
	assert.False(t, found.IsDeliverFailed)

	require.NoError(t, repo.UpdateDeliveryStatus(msg.ID, false, true))
	found, _ = repo.FindMessageByID(msg.ID)
	assert.False(t, found.IsDelivering)
	assert.True(t, found.IsDeliverFailed)
}

func TestChatRepository_MarkAllRead(t *testing.T) {
	repo := NewChatRepository(testDB)
	user1 := insertTestUser(t, "u_chat_mr1", "chatmr1")
	user2 := insertTestUser(t, "u_chat_mr2", "chatmr2")
	defer cleanupUser(t, user1.ID)
	defer cleanupUser(t, user2.ID)

	text := "hi"
	for _, id := range []string{"cm_mr1", "cm_mr2"} {
		m := &model.ChatMessage{
			ID: id, FromUserID: user1.ID, ToUserID: &user2.ID, Text: &text,
			Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{},
		}
		require.NoError(t, repo.CreateMessage(m))
		defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, id)
	}

	count, _ := repo.CountUnread(user2.ID)
	assert.EqualValues(t, 2, count)

	require.NoError(t, repo.MarkAllRead(user2.ID))
	count, _ = repo.CountUnread(user2.ID)
	assert.EqualValues(t, 0, count)
}

func TestChatRepository_ListHistory(t *testing.T) {
	repo := NewChatRepository(testDB)
	me := insertTestUser(t, "u_chat_h1", "chath1")
	other1 := insertTestUser(t, "u_chat_h2", "chath2")
	other2 := insertTestUser(t, "u_chat_h3", "chath3")
	defer cleanupUser(t, me.ID)
	defer cleanupUser(t, other1.ID)
	defer cleanupUser(t, other2.ID)

	// ownerはmembershipレコードなしでも暗黙メンバーとして扱われることを検証する
	// (明示的なCreateMembershipを入れない)。
	room := &model.ChatRoom{ID: "cr_h1", Name: "HistRoom", OwnerID: me.ID}
	require.NoError(t, repo.CreateRoom(room))
	defer testDB.Exec(`DELETE FROM "chat_room" WHERE id = ?`, room.ID)

	text := "msg"
	// DM: me -> other1 (2件、最新はcm_h2)
	for _, id := range []string{"cm_h1", "cm_h2"} {
		m := &model.ChatMessage{
			ID: id, FromUserID: me.ID, ToUserID: &other1.ID, Text: &text,
			Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{},
		}
		require.NoError(t, repo.CreateMessage(m))
		defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, id)
	}
	// DM: me -> other2
	dm3 := &model.ChatMessage{
		ID: "cm_h3", FromUserID: me.ID, ToUserID: &other2.ID, Text: &text,
		Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{},
	}
	require.NoError(t, repo.CreateMessage(dm3))
	defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, dm3.ID)
	// ルームメッセージ
	rm := &model.ChatMessage{
		ID: "cm_h4", FromUserID: me.ID, ToRoomID: &room.ID, Text: &text,
		Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{},
	}
	require.NoError(t, repo.CreateMessage(rm))
	defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, rm.ID)

	// 3会話あるはず: me-other1, me-other2, room:cr_h1
	hist, err := repo.ListHistory(me.ID, 10)
	require.NoError(t, err)
	assert.Len(t, hist, 3)
	// 最新順: cm_h4 > cm_h3 > cm_h2 (cm_h1はme-other1会話の古い方で除外)
	assert.Equal(t, "cm_h4", hist[0].ID)

	// limit=1で最新1会話のみ
	hist1, err := repo.ListHistory(me.ID, 1)
	require.NoError(t, err)
	assert.Len(t, hist1, 1)

	// limit=0はデフォルト10
	histDef, err := repo.ListHistory(me.ID, 0)
	require.NoError(t, err)
	assert.Len(t, histDef, 3)
}

// #692: 履歴ゼロ件 → 空 slice (nil) を返す経路。limit clamp と異なり ids
// 空の早期 return ブランチをカバー。
func TestChatRepository_ListHistory_Variants_NoMessages(t *testing.T) {
	repo := NewChatRepository(testDB)

	uh, err := repo.ListUserHistory("nope_user_id", 10)
	require.NoError(t, err)
	assert.Empty(t, uh)
	rh, err := repo.ListRoomHistory("nope_user_id", 10)
	require.NoError(t, err)
	assert.Empty(t, rh)
}

// #692: ListUserHistory は DM 限定 (toRoomId NULL) で per-peer 最新を返す。
func TestChatRepository_ListUserHistory(t *testing.T) {
	repo := NewChatRepository(testDB)
	me := insertTestUser(t, "u_chat_uh1", "chatuh1")
	other1 := insertTestUser(t, "u_chat_uh2", "chatuh2")
	other2 := insertTestUser(t, "u_chat_uh3", "chatuh3")
	defer cleanupUser(t, me.ID)
	defer cleanupUser(t, other1.ID)
	defer cleanupUser(t, other2.ID)
	room := &model.ChatRoom{ID: "cr_uh1", Name: "Room", OwnerID: me.ID}
	require.NoError(t, repo.CreateRoom(room))
	defer testDB.Exec(`DELETE FROM "chat_room" WHERE id = ?`, room.ID)

	text := "msg"
	// DM 2 件 (me<->other1)
	for _, id := range []string{"cm_uh1", "cm_uh2"} {
		m := &model.ChatMessage{
			ID: id, FromUserID: me.ID, ToUserID: &other1.ID, Text: &text,
			Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{},
		}
		require.NoError(t, repo.CreateMessage(m))
		defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, id)
	}
	dm3 := &model.ChatMessage{
		ID: "cm_uh3", FromUserID: me.ID, ToUserID: &other2.ID, Text: &text,
		Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{},
	}
	require.NoError(t, repo.CreateMessage(dm3))
	defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, dm3.ID)
	// room メッセージ → ListUserHistory では除外されるはず
	rm := &model.ChatMessage{
		ID: "cm_uh4", FromUserID: me.ID, ToRoomID: &room.ID, Text: &text,
		Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{},
	}
	require.NoError(t, repo.CreateMessage(rm))
	defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, rm.ID)

	hist, err := repo.ListUserHistory(me.ID, 10)
	require.NoError(t, err)
	// 2 つの DM 会話 (me-other1, me-other2)。room は除外。
	assert.Len(t, hist, 2)
	for _, h := range hist {
		assert.Nil(t, h.ToRoomID, "room メッセージが混入してはいけない")
	}

	// limit clamp: 0 -> 10, 大きすぎる値 -> 100
	defHist, _ := repo.ListUserHistory(me.ID, 0)
	assert.Len(t, defHist, 2)
	clampHist, _ := repo.ListUserHistory(me.ID, 9999)
	assert.Len(t, clampHist, 2)
}

// #692: ListRoomHistory は room 限定 (toRoomId NOT NULL かつ owner / member) で
// per-room 最新を返す。
func TestChatRepository_ListRoomHistory(t *testing.T) {
	repo := NewChatRepository(testDB)
	owner := insertTestUser(t, "u_chat_rh1", "chatrh1")
	other := insertTestUser(t, "u_chat_rh2", "chatrh2")
	defer cleanupUser(t, owner.ID)
	defer cleanupUser(t, other.ID)
	r1 := &model.ChatRoom{ID: "cr_rh1", Name: "R1", OwnerID: owner.ID}
	r2 := &model.ChatRoom{ID: "cr_rh2", Name: "R2", OwnerID: owner.ID}
	require.NoError(t, repo.CreateRoom(r1))
	require.NoError(t, repo.CreateRoom(r2))
	defer testDB.Exec(`DELETE FROM "chat_room" WHERE id IN (?, ?)`, r1.ID, r2.ID)

	text := "rmsg"
	// r1 に 2 件、r2 に 1 件
	for _, msg := range []*model.ChatMessage{
		{ID: "cm_rh1", FromUserID: owner.ID, ToRoomID: &r1.ID, Text: &text, Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{}},
		{ID: "cm_rh2", FromUserID: owner.ID, ToRoomID: &r1.ID, Text: &text, Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{}},
		{ID: "cm_rh3", FromUserID: owner.ID, ToRoomID: &r2.ID, Text: &text, Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{}},
	} {
		require.NoError(t, repo.CreateMessage(msg))
		defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, msg.ID)
	}
	// 1on1 DM は除外される
	dm := &model.ChatMessage{
		ID: "cm_rh4", FromUserID: owner.ID, ToUserID: &other.ID, Text: &text,
		Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{},
	}
	require.NoError(t, repo.CreateMessage(dm))
	defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, dm.ID)

	// owner は r1 / r2 のオーナーなので両方含む
	hist, err := repo.ListRoomHistory(owner.ID, 10)
	require.NoError(t, err)
	assert.Len(t, hist, 2)
	for _, h := range hist {
		require.NotNil(t, h.ToRoomID)
	}

	// other はメンバーシップ無し → 空
	histOther, err := repo.ListRoomHistory(other.ID, 10)
	require.NoError(t, err)
	assert.Empty(t, histOther)

	// limit clamp
	defHist, _ := repo.ListRoomHistory(owner.ID, 0)
	assert.Len(t, defHist, 2)
	clampHist, _ := repo.ListRoomHistory(owner.ID, 9999)
	assert.Len(t, clampHist, 2)
}

// #692: MarkAllReadFromUser は (sender→reader) の DM だけを既読化し、
// 他人発の DM や room メッセージには触らない。
func TestChatRepository_MarkAllReadFromUser(t *testing.T) {
	repo := NewChatRepository(testDB)
	reader := insertTestUser(t, "u_chat_mrf1", "chatmrf1")
	sender := insertTestUser(t, "u_chat_mrf2", "chatmrf2")
	stranger := insertTestUser(t, "u_chat_mrf3", "chatmrf3")
	defer cleanupUser(t, reader.ID)
	defer cleanupUser(t, sender.ID)
	defer cleanupUser(t, stranger.ID)

	text := "x"
	// sender → reader (DM, 2 件)
	for _, id := range []string{"cm_mrf_s1", "cm_mrf_s2"} {
		m := &model.ChatMessage{
			ID: id, FromUserID: sender.ID, ToUserID: &reader.ID, Text: &text,
			Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{},
		}
		require.NoError(t, repo.CreateMessage(m))
		defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, id)
	}
	// stranger → reader (別人発、対象外)
	mStranger := &model.ChatMessage{
		ID: "cm_mrf_x", FromUserID: stranger.ID, ToUserID: &reader.ID, Text: &text,
		Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{},
	}
	require.NoError(t, repo.CreateMessage(mStranger))
	defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, mStranger.ID)

	require.NoError(t, repo.MarkAllReadFromUser(reader.ID, sender.ID))

	// sender 発のものは reads に reader が入る
	mS1, err := repo.FindMessageByID("cm_mrf_s1")
	require.NoError(t, err)
	assert.Contains(t, []string(mS1.Reads), reader.ID)
	// stranger 発のものは触られない
	mX, err := repo.FindMessageByID("cm_mrf_x")
	require.NoError(t, err)
	assert.NotContains(t, []string(mX.Reads), reader.ID)

	// 冪等性: 2 回呼んでもエラーにならず、reads が膨らまない
	require.NoError(t, repo.MarkAllReadFromUser(reader.ID, sender.ID))
	mAfter, err := repo.FindMessageByID("cm_mrf_s1")
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(strings.Join([]string(mAfter.Reads), ","), reader.ID))
}

// #692: MarkAllReadInRoom は同じ部屋の他人発メッセージを既読化、自分発は触らない。
func TestChatRepository_MarkAllReadInRoom(t *testing.T) {
	repo := NewChatRepository(testDB)
	owner := insertTestUser(t, "u_chat_mrr1", "chatmrr1")
	other := insertTestUser(t, "u_chat_mrr2", "chatmrr2")
	defer cleanupUser(t, owner.ID)
	defer cleanupUser(t, other.ID)
	room := &model.ChatRoom{ID: "cr_mrr", Name: "R", OwnerID: owner.ID}
	require.NoError(t, repo.CreateRoom(room))
	defer testDB.Exec(`DELETE FROM "chat_room" WHERE id = ?`, room.ID)

	text := "y"
	// other 発 (reader=owner にとって既読対象)
	mOther := &model.ChatMessage{
		ID: "cm_mrr_o1", FromUserID: other.ID, ToRoomID: &room.ID, Text: &text,
		Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{},
	}
	require.NoError(t, repo.CreateMessage(mOther))
	defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, mOther.ID)
	// owner 自身発 (既読化対象外)
	mSelf := &model.ChatMessage{
		ID: "cm_mrr_self", FromUserID: owner.ID, ToRoomID: &room.ID, Text: &text,
		Reads: pq.StringArray{}, Reactions: pq.StringArray{}, Emojis: pq.StringArray{},
	}
	require.NoError(t, repo.CreateMessage(mSelf))
	defer testDB.Exec(`DELETE FROM "chat_message" WHERE id = ?`, mSelf.ID)

	require.NoError(t, repo.MarkAllReadInRoom(owner.ID, room.ID))

	mOtherAfter, err := repo.FindMessageByID("cm_mrr_o1")
	require.NoError(t, err)
	assert.Contains(t, []string(mOtherAfter.Reads), owner.ID)

	mSelfAfter, err := repo.FindMessageByID("cm_mrr_self")
	require.NoError(t, err)
	assert.NotContains(t, []string(mSelfAfter.Reads), owner.ID, "自分発メッセージは既読化対象外")
}

func TestChatRepository_ListInvitationsByUserAndRoom(t *testing.T) {
	repo := NewChatRepository(testDB)
	user1 := insertTestUser(t, "u_chat_li1", "chatli1")
	user2 := insertTestUser(t, "u_chat_li2", "chatli2")
	defer cleanupUser(t, user1.ID)
	defer cleanupUser(t, user2.ID)

	room := &model.ChatRoom{ID: "cr_li", Name: "Room", OwnerID: user1.ID}
	require.NoError(t, repo.CreateRoom(room))
	defer testDB.Exec(`DELETE FROM "chat_room" WHERE id = ?`, room.ID)

	inv := &model.ChatRoomInvitation{ID: "inv_li1", UserID: user2.ID, RoomID: room.ID}
	require.NoError(t, repo.CreateInvitation(inv))
	defer testDB.Exec(`DELETE FROM "chat_room_invitation" WHERE id = ?`, inv.ID)

	// ListInvitationsByUser: ignored=false
	rows, err := repo.ListInvitationsByUser(user2.ID, false)
	require.NoError(t, err)
	assert.Len(t, rows, 1)

	// ignored=trueは0件
	rows2, err := repo.ListInvitationsByUser(user2.ID, true)
	require.NoError(t, err)
	assert.Empty(t, rows2)

	// ListInvitationsByRoom
	roomInvs, err := repo.ListInvitationsByRoom(room.ID)
	require.NoError(t, err)
	assert.Len(t, roomInvs, 1)
}

func TestChatRepository_Invitation(t *testing.T) {
	repo := NewChatRepository(testDB)
	user := insertTestUser(t, "u_chat_5", "chatuser5")
	defer cleanupUser(t, user.ID)

	room := &model.ChatRoom{ID: "cr_4", Name: "Room", OwnerID: user.ID}
	require.NoError(t, repo.CreateRoom(room))
	defer testDB.Exec(`DELETE FROM "chat_room" WHERE id = ?`, room.ID)

	// CreateInvitation
	inv := &model.ChatRoomInvitation{ID: "inv_1", UserID: user.ID, RoomID: room.ID}
	require.NoError(t, repo.CreateInvitation(inv))
	defer testDB.Exec(`DELETE FROM "chat_room_invitation" WHERE id = ?`, inv.ID)

	// FindInvitation
	found, err := repo.FindInvitation(user.ID, room.ID)
	require.NoError(t, err)
	assert.Equal(t, "inv_1", found.ID)

	// FindInvitation - not found
	_, err = repo.FindInvitation("ghost", room.ID)
	assert.Error(t, err)

	// DeleteInvitation
	require.NoError(t, repo.DeleteInvitation("inv_1"))
}
