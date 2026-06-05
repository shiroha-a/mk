package chat_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lib/pq"
	corechat "github.com/shiroha-a/mk/internal/core/chat"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeChatRepo / newFakeRepo は testutil.MockChatRepository に集約された
// (#709)。本ファイル / ap_delivery_test.go の呼び出し点を保つために alias
// を残しているが、新規 test は testutil.NewMockChatRepository() を直接
// 呼ぶこと。既存呼び出しも段階的に置換していく方針。
//
// Deprecated: use testutil.NewMockChatRepository directly.
func newFakeRepo() *testutil.MockChatRepository {
	return testutil.NewMockChatRepository()
}

// --- capture publisher ---

type capturePublisher struct {
	mu        sync.Mutex
	userCalls []userCall
	roomCalls []roomCall
}

type userCall struct {
	from, to, eventType string
	body                any
}

type roomCall struct {
	roomID, eventType string
	body              any
}

func (p *capturePublisher) PublishUserMessage(_ context.Context, from, to, t string, body any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.userCalls = append(p.userCalls, userCall{from, to, t, body})
}

func (p *capturePublisher) PublishRoomMessage(_ context.Context, roomID, t string, body any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.roomCalls = append(p.roomCalls, roomCall{roomID, t, body})
}

// stubMainPublisher captures PublishMainEvent calls for newChatMessage tests.
type stubMainPublisher struct {
	mu    sync.Mutex
	calls []mainEventCall
}

type mainEventCall struct {
	userID, eventType string
	body              any
}

func (p *stubMainPublisher) PublishMainEvent(userID, eventType string, body any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, mainEventCall{userID, eventType, body})
}

// --- helper ---

func newSvc(t *testing.T) (*corechat.Service, *testutil.MockChatRepository, *capturePublisher) {
	t.Helper()
	repo := newFakeRepo()
	pub := &capturePublisher{}
	idGen, _ := id.NewGenerator("aidx")
	svc := corechat.NewService(repo, idGen)
	svc.SetStreamingPublisher(pub)
	return svc, repo, pub
}

// --- CreateMessageToUser ---

func TestCreateMessageToUser_Success(t *testing.T) {
	svc, _, pub := newSvc(t)
	msg, err := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hello", "")
	require.NoError(t, err)
	assert.NotEmpty(t, msg.ID)
	require.NotNil(t, msg.Text)
	assert.Equal(t, "hello", *msg.Text)
	require.Len(t, pub.userCalls, 1)
	assert.Equal(t, "alice", pub.userCalls[0].from)
	assert.Equal(t, "bob", pub.userCalls[0].to)
	assert.Equal(t, corechat.EventMessage, pub.userCalls[0].eventType)
}

func TestCreateMessageToUser_WithFile(t *testing.T) {
	svc, _, _ := newSvc(t)
	msg, err := svc.CreateMessageToUser(context.Background(), "alice", "bob", "", "file_x")
	require.NoError(t, err)
	require.NotNil(t, msg.FileID)
	assert.Equal(t, "file_x", *msg.FileID)
}

func TestCreateMessageToUser_EmptySender(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.CreateMessageToUser(context.Background(), "", "bob", "hi", "")
	assert.ErrorIs(t, err, corechat.ErrInvalidTarget)
}

func TestCreateMessageToUser_EmptyRecipient(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.CreateMessageToUser(context.Background(), "alice", "", "hi", "")
	assert.ErrorIs(t, err, corechat.ErrInvalidTarget)
}

func TestCreateMessageToUser_RepoError(t *testing.T) {
	svc, repo, pub := newSvc(t)
	repo.CreateErr = errors.New("db boom")
	_, err := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hi", "")
	assert.Error(t, err)
	assert.Empty(t, pub.userCalls)
}

// recipient (bob) が sender (alice) を block していると DM は ErrChatBlocked。
func TestCreateMessageToUser_BlockedByRecipient(t *testing.T) {
	svc, _, pub := newSvc(t)
	blocks := testutil.NewMockBlockingRepository()
	// blocker=bob, blockee=alice。
	require.NoError(t, blocks.Create(&model.Blocking{ID: "b1", BlockerID: "bob", BlockeeID: "alice"}))
	svc.SetBlockingRepo(blocks)

	_, err := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hi", "")
	assert.ErrorIs(t, err, corechat.ErrChatBlocked)
	assert.Empty(t, pub.userCalls, "blocked DM must not be persisted/published")
}

// block 関係が無ければ通常どおり送信できる (block check は逆方向を見ない)。
func TestCreateMessageToUser_NotBlockedPasses(t *testing.T) {
	svc, _, _ := newSvc(t)
	blocks := testutil.NewMockBlockingRepository()
	// alice が bob を block していても、bob→alice は送れる(逆方向 block は無関係)。
	require.NoError(t, blocks.Create(&model.Blocking{ID: "b1", BlockerID: "alice", BlockeeID: "bob"}))
	svc.SetBlockingRepo(blocks)

	_, err := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hi", "")
	require.NoError(t, err)
}

// block check の DB error は fail-closed (送信せず error を返す)。
func TestCreateMessageToUser_BlockCheckFailClosed(t *testing.T) {
	svc, _, pub := newSvc(t)
	blocks := testutil.NewMockBlockingRepository()
	blocks.ExistsErr = errors.New("db down")
	svc.SetBlockingRepo(blocks)

	_, err := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hi", "")
	require.Error(t, err)
	assert.Empty(t, pub.userCalls, "fail-closed: message must not be sent on block-check error")
}

// 連合 inbound DM (CreateMessageViaAP) でも recipient が sender を block して
// いれば拒否される (#parity review chat-block-1)。
func TestCreateMessageViaAP_BlockedByRecipient(t *testing.T) {
	svc, _, pub := newSvc(t)
	blocks := testutil.NewMockBlockingRepository()
	// blocker=bob (local recipient), blockee=alice (remote sender)。
	require.NoError(t, blocks.Create(&model.Blocking{ID: "b1", BlockerID: "bob", BlockeeID: "alice"}))
	svc.SetBlockingRepo(blocks)

	remoteSender := &model.User{ID: "alice"}
	_, err := svc.CreateMessageViaAP(context.Background(), "https://remote/notes/1", remoteSender, "bob", "hi")
	assert.ErrorIs(t, err, corechat.ErrChatBlocked)
	assert.Empty(t, pub.userCalls, "blocked inbound DM must not be persisted/published")
}

// --- CreateMessageToRoom ---

func seedRoom(t *testing.T, repo *testutil.MockChatRepository, id string, owner string, members ...string) {
	t.Helper()
	repo.Rooms[id] = &model.ChatRoom{ID: id, Name: "test-room", OwnerID: owner}
	for _, u := range members {
		repo.Memberships[u+":"+id] = &model.ChatRoomMembership{UserID: u, RoomID: id}
	}
}

func TestCreateMessageToRoom_Success(t *testing.T) {
	svc, repo, pub := newSvc(t)
	seedRoom(t, repo, "r1", "alice", "bob")

	msg, err := svc.CreateMessageToRoom(context.Background(), "bob", "r1", "hello room", "")
	require.NoError(t, err)
	require.NotNil(t, msg.ToRoomID)
	assert.Equal(t, "r1", *msg.ToRoomID)
	require.Len(t, pub.roomCalls, 1)
	assert.Equal(t, "r1", pub.roomCalls[0].roomID)
	assert.Equal(t, corechat.EventMessage, pub.roomCalls[0].eventType)
}

// block は 1-on-1 DM のみに効き、room メッセージには影響しない (upstream の
// createMessageToRoom も checkBlocked を呼ばない、#parity review chat-block-3)。
func TestCreateMessageToRoom_BlockDoesNotAffectRoom(t *testing.T) {
	svc, repo, _ := newSvc(t)
	seedRoom(t, repo, "r1", "alice", "bob")
	blocks := testutil.NewMockBlockingRepository()
	// alice と bob が相互 block していても room メッセージは通る。
	require.NoError(t, blocks.Create(&model.Blocking{ID: "b1", BlockerID: "alice", BlockeeID: "bob"}))
	require.NoError(t, blocks.Create(&model.Blocking{ID: "b2", BlockerID: "bob", BlockeeID: "alice"}))
	svc.SetBlockingRepo(blocks)

	_, err := svc.CreateMessageToRoom(context.Background(), "bob", "r1", "hi room", "")
	require.NoError(t, err, "room message must not be blocked by 1-on-1 block")
}

func TestCreateMessageToRoom_OwnerIsImplicitMember(t *testing.T) {
	svc, repo, _ := newSvc(t)
	seedRoom(t, repo, "r1", "alice") // bob not a member

	_, err := svc.CreateMessageToRoom(context.Background(), "alice", "r1", "hi", "")
	require.NoError(t, err)
}

func TestCreateMessageToRoom_NotMember(t *testing.T) {
	svc, repo, _ := newSvc(t)
	seedRoom(t, repo, "r1", "alice")

	_, err := svc.CreateMessageToRoom(context.Background(), "carol", "r1", "hi", "")
	assert.ErrorIs(t, err, corechat.ErrForbidden)
}

// --- newChatMessage emit (Phase 7-4b-4 / #298) ---

func TestCreateMessageToUser_PublishesNewChatMessage(t *testing.T) {
	svc, _, _ := newSvc(t)
	main := &stubMainPublisher{}
	svc.SetMainStreamPublisher(main)

	_, err := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hi", "")
	require.NoError(t, err)

	main.mu.Lock()
	defer main.mu.Unlock()
	require.Len(t, main.calls, 1)
	// sender (alice) には送らず recipient (bob) にだけ emit。
	assert.Equal(t, "bob", main.calls[0].userID)
	assert.Equal(t, "newChatMessage", main.calls[0].eventType)
	body, ok := main.calls[0].body.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "alice", body["fromUserId"])
	assert.Equal(t, "bob", body["toUserId"])
	assert.Equal(t, "hi", body["text"])
}

func TestCreateMessageToUser_NoMainPublisher_NoEmit(t *testing.T) {
	svc, _, _ := newSvc(t)
	// SetMainStreamPublisher を呼ばない状態で正常終了することを確認。
	_, err := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hi", "")
	require.NoError(t, err)
}

func TestCreateMessageViaAP_PublishesNewChatMessageToLocalRecipient(t *testing.T) {
	svc, _, _ := newSvc(t)
	main := &stubMainPublisher{}
	svc.SetMainStreamPublisher(main)

	// Remote → local DM: fromUser は remote (host set)、toUserID はローカル。
	remoteHost := "remote.example"
	fromUser := &model.User{ID: "remote_user", Host: &remoteHost}
	_, err := svc.CreateMessageViaAP(context.Background(), "https://remote.example/n/1", fromUser, "bob", "hello from remote")
	require.NoError(t, err)

	main.mu.Lock()
	defer main.mu.Unlock()
	require.Len(t, main.calls, 1)
	assert.Equal(t, "bob", main.calls[0].userID)
	assert.Equal(t, "newChatMessage", main.calls[0].eventType)
	body, ok := main.calls[0].body.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "remote_user", body["fromUserId"])
	assert.Equal(t, "bob", body["toUserId"])
}

func TestCreateMessageToRoom_PublishesNewChatMessageToMembersExceptSender(t *testing.T) {
	svc, repo, _ := newSvc(t)
	// owner=alice, members=bob,carol。sender=bob の場合 alice と carol に emit。
	seedRoom(t, repo, "r1", "alice", "bob", "carol")
	main := &stubMainPublisher{}
	svc.SetMainStreamPublisher(main)

	_, err := svc.CreateMessageToRoom(context.Background(), "bob", "r1", "room hi", "")
	require.NoError(t, err)

	main.mu.Lock()
	defer main.mu.Unlock()
	// emit 対象 userID を set で検証 (順序は非決定的)。
	gotUserIDs := make(map[string]bool, len(main.calls))
	for _, c := range main.calls {
		assert.Equal(t, "newChatMessage", c.eventType)
		gotUserIDs[c.userID] = true
	}
	assert.Equal(t, map[string]bool{"alice": true, "carol": true}, gotUserIDs)
}

func TestCreateMessageToRoom_OwnerAsSender_EmitsToMembersOnly(t *testing.T) {
	svc, repo, _ := newSvc(t)
	seedRoom(t, repo, "r1", "alice", "bob")
	main := &stubMainPublisher{}
	svc.SetMainStreamPublisher(main)

	// owner=alice が送信 → bob にだけ emit (alice は sender 自身なので除外)。
	_, err := svc.CreateMessageToRoom(context.Background(), "alice", "r1", "hi", "")
	require.NoError(t, err)

	main.mu.Lock()
	defer main.mu.Unlock()
	require.Len(t, main.calls, 1)
	assert.Equal(t, "bob", main.calls[0].userID)
}

func TestCreateMessageToRoom_RoomNotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.CreateMessageToRoom(context.Background(), "alice", "ghost", "hi", "")
	assert.ErrorIs(t, err, corechat.ErrNotFound)
}

func TestCreateMessageToRoom_EmptyTarget(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.CreateMessageToRoom(context.Background(), "alice", "", "hi", "")
	assert.ErrorIs(t, err, corechat.ErrInvalidTarget)
}

func TestCreateMessageToRoom_WithFile(t *testing.T) {
	svc, repo, _ := newSvc(t)
	seedRoom(t, repo, "r1", "alice")
	msg, err := svc.CreateMessageToRoom(context.Background(), "alice", "r1", "", "file_y")
	require.NoError(t, err)
	require.NotNil(t, msg.FileID)
}

func TestCreateMessageToRoom_RepoError(t *testing.T) {
	svc, repo, _ := newSvc(t)
	seedRoom(t, repo, "r1", "alice")
	repo.CreateErr = errors.New("db boom")
	_, err := svc.CreateMessageToRoom(context.Background(), "alice", "r1", "hi", "")
	assert.Error(t, err)
}

// --- DeleteMessage ---

func TestDeleteMessage_UserDM(t *testing.T) {
	svc, _, pub := newSvc(t)
	msg, _ := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hi", "")
	pub.userCalls = nil // reset

	require.NoError(t, svc.DeleteMessage(context.Background(), "alice", msg.ID))
	require.Len(t, pub.userCalls, 1)
	assert.Equal(t, corechat.EventDeleted, pub.userCalls[0].eventType)
}

func TestDeleteMessage_Room(t *testing.T) {
	svc, repo, pub := newSvc(t)
	seedRoom(t, repo, "r1", "alice")
	msg, _ := svc.CreateMessageToRoom(context.Background(), "alice", "r1", "hi", "")
	pub.roomCalls = nil

	require.NoError(t, svc.DeleteMessage(context.Background(), "alice", msg.ID))
	require.Len(t, pub.roomCalls, 1)
	assert.Equal(t, corechat.EventDeleted, pub.roomCalls[0].eventType)
}

func TestDeleteMessage_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	err := svc.DeleteMessage(context.Background(), "alice", "ghost")
	assert.ErrorIs(t, err, corechat.ErrNotFound)
}

func TestDeleteMessage_NotAuthor(t *testing.T) {
	svc, _, _ := newSvc(t)
	msg, _ := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hi", "")
	err := svc.DeleteMessage(context.Background(), "carol", msg.ID)
	assert.ErrorIs(t, err, corechat.ErrForbidden)
}

func TestDeleteMessage_RepoError(t *testing.T) {
	svc, repo, _ := newSvc(t)
	msg, _ := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hi", "")
	repo.DeleteErr = errors.New("db boom")
	err := svc.DeleteMessage(context.Background(), "alice", msg.ID)
	assert.Error(t, err)
}

// --- UpdateMessage ---

func TestUpdateMessage_Success(t *testing.T) {
	svc, _, pub := newSvc(t)
	msg, _ := svc.CreateMessageToUser(context.Background(), "alice", "bob", "original", "")
	pub.userCalls = nil

	updated, err := svc.UpdateMessage(context.Background(), "alice", msg.ID, "edited")
	require.NoError(t, err)
	require.NotNil(t, updated.Text)
	assert.Equal(t, "edited", *updated.Text)
	require.Len(t, pub.userCalls, 1)
	assert.Equal(t, corechat.EventEdited, pub.userCalls[0].eventType)
}

func TestUpdateMessage_Room(t *testing.T) {
	svc, repo, pub := newSvc(t)
	seedRoom(t, repo, "r1", "alice")
	msg, _ := svc.CreateMessageToRoom(context.Background(), "alice", "r1", "original", "")
	pub.roomCalls = nil

	_, err := svc.UpdateMessage(context.Background(), "alice", msg.ID, "edited")
	require.NoError(t, err)
	require.Len(t, pub.roomCalls, 1)
	assert.Equal(t, corechat.EventEdited, pub.roomCalls[0].eventType)
}

func TestUpdateMessage_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.UpdateMessage(context.Background(), "alice", "ghost", "x")
	assert.ErrorIs(t, err, corechat.ErrNotFound)
}

func TestUpdateMessage_NotAuthor(t *testing.T) {
	svc, _, _ := newSvc(t)
	msg, _ := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hi", "")
	_, err := svc.UpdateMessage(context.Background(), "carol", msg.ID, "edited")
	assert.ErrorIs(t, err, corechat.ErrForbidden)
}

func TestUpdateMessage_RepoError(t *testing.T) {
	svc, repo, _ := newSvc(t)
	msg, _ := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hi", "")
	repo.UpdateErr = errors.New("db boom")
	_, err := svc.UpdateMessage(context.Background(), "alice", msg.ID, "edited")
	assert.Error(t, err)
}

// --- MarkReadByMessageID ---

func TestMarkReadByMessageID_DM(t *testing.T) {
	svc, _, pub := newSvc(t)
	msg, _ := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hi", "")
	pub.userCalls = nil

	require.NoError(t, svc.MarkReadByMessageID(context.Background(), "bob", msg.ID))
	require.Len(t, pub.userCalls, 1)
	assert.Equal(t, corechat.EventRead, pub.userCalls[0].eventType)
}

func TestMarkReadByMessageID_Room(t *testing.T) {
	svc, repo, pub := newSvc(t)
	seedRoom(t, repo, "r1", "alice", "bob")
	msg, _ := svc.CreateMessageToRoom(context.Background(), "alice", "r1", "hi", "")
	pub.roomCalls = nil

	require.NoError(t, svc.MarkReadByMessageID(context.Background(), "bob", msg.ID))
	require.Len(t, pub.roomCalls, 1)
	assert.Equal(t, corechat.EventRead, pub.roomCalls[0].eventType)
}

func TestMarkReadByMessageID_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	err := svc.MarkReadByMessageID(context.Background(), "bob", "ghost")
	assert.ErrorIs(t, err, corechat.ErrNotFound)
}

// --- IsRoomMember ---

func TestIsRoomMember_Member(t *testing.T) {
	svc, repo, _ := newSvc(t)
	seedRoom(t, repo, "r1", "alice", "bob")
	ok, err := svc.IsRoomMember("bob", "r1")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestIsRoomMember_Owner(t *testing.T) {
	svc, repo, _ := newSvc(t)
	seedRoom(t, repo, "r1", "alice") // alice is owner, no explicit membership
	ok, err := svc.IsRoomMember("alice", "r1")
	require.NoError(t, err)
	assert.True(t, ok, "owner should implicitly be a member")
}

func TestIsRoomMember_NonMember(t *testing.T) {
	svc, repo, _ := newSvc(t)
	seedRoom(t, repo, "r1", "alice")
	ok, err := svc.IsRoomMember("carol", "r1")
	require.NoError(t, err)
	assert.False(t, ok)
}

// --- nil publisher fallback ---

func TestService_NilPublisherStillWorks(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	svc := corechat.NewService(newFakeRepo(), idGen)
	// No publisher set; operations should still succeed (no panic)
	_, err := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hi", "")
	assert.NoError(t, err)
}

// --- sanity: reads array mutation round-trip ---

func TestMarkRead_AppendsToReads(t *testing.T) {
	svc, repo, _ := newSvc(t)
	msg, _ := svc.CreateMessageToUser(context.Background(), "alice", "bob", "hi", "")
	require.NoError(t, svc.MarkReadByMessageID(context.Background(), "bob", msg.ID))
	stored := repo.Messages[msg.ID]
	require.NotNil(t, stored)
	assert.Equal(t, pq.StringArray{"bob"}, stored.Reads)
}

// --- HasPermissionToViewRoomInfo (upstream 2026.5.4 / #1164 Phase C) ---

// stubModeratorChecker satisfies corechat.ModeratorChecker for tests where we
// need to flip the "is moderator" answer per case without dragging in the full
// role service.
type stubModeratorChecker struct {
	moderators map[string]bool
}

func (s *stubModeratorChecker) IsModerator(userID string) bool {
	return s.moderators[userID]
}

func TestHasPermissionToViewRoomInfo_NilRoom(t *testing.T) {
	svc, _, _ := newSvc(t)
	ok, err := svc.HasPermissionToViewRoomInfo("alice", nil)
	require.NoError(t, err)
	assert.False(t, ok, "nil room must return false (defensive)")
}

func TestHasPermissionToViewRoomInfo_OwnerTrue(t *testing.T) {
	svc, _, _ := newSvc(t)
	room := &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	ok, err := svc.HasPermissionToViewRoomInfo("alice", room)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestHasPermissionToViewRoomInfo_MemberTrue(t *testing.T) {
	svc, repo, _ := newSvc(t)
	room := &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	repo.Memberships["bob:r1"] = &model.ChatRoomMembership{UserID: "bob", RoomID: "r1"}
	ok, err := svc.HasPermissionToViewRoomInfo("bob", room)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestHasPermissionToViewRoomInfo_InvitationTrue(t *testing.T) {
	svc, repo, _ := newSvc(t)
	room := &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	repo.Invitations["inv1"] = &model.ChatRoomInvitation{ID: "inv1", UserID: "carol", RoomID: "r1"}
	ok, err := svc.HasPermissionToViewRoomInfo("carol", room)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestHasPermissionToViewRoomInfo_NonMemberFalse(t *testing.T) {
	svc, _, _ := newSvc(t)
	room := &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	ok, err := svc.HasPermissionToViewRoomInfo("dave", room)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestHasPermissionToViewRoomInfo_ModeratorBypass(t *testing.T) {
	svc, _, _ := newSvc(t)
	svc.SetModeratorChecker(&stubModeratorChecker{moderators: map[string]bool{"moderator1": true}})
	room := &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	ok, err := svc.HasPermissionToViewRoomInfo("moderator1", room)
	require.NoError(t, err)
	assert.True(t, ok, "moderator が non-owner non-member でも閲覧できる")
}

func TestHasPermissionToViewRoomInfo_ModeratorCheckerNotConsulted(t *testing.T) {
	svc, _, _ := newSvc(t)
	// moderator checker 未配線 (= nil) のとき、moderator bypass 経路は skip
	// される。non-owner / non-member は常に false。
	room := &model.ChatRoom{ID: "r1", OwnerID: "alice"}
	ok, err := svc.HasPermissionToViewRoomInfo("someone", room)
	require.NoError(t, err)
	assert.False(t, ok)
}
