package chat

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	corechat "github.com/shiroha-a/mk/internal/core/chat"
	"github.com/shiroha-a/mk/internal/entitycompat/shapetest"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errMock = assert.AnError

func newTestHandler() (*Handler, *testutil.MockChatRepository) {
	repo := testutil.NewMockChatRepository()
	idGen, _ := id.NewGenerator("aidx")
	return NewHandler(repo, idGen), repo
}

// newTestHandlerWithService は chat Service を inject 済みの Handler を返す。
// /api/chat/rooms/show 等 service 経由の権限 gate (upstream 2026.5.4) を
// 走らせる test で利用する。
func newTestHandlerWithService() (*Handler, *testutil.MockChatRepository) {
	repo := testutil.NewMockChatRepository()
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(repo, idGen)
	svc := corechat.NewService(repo, idGen)
	h.SetService(svc)
	return h, repo
}

func post(handler func(echo.Context) error, body string, user *model.User) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if user != nil {
		c.Set(string(middleware.UserContextKey), user)
	}
	_ = handler(c)
	return rec
}

var u1 = &model.User{ID: "u1", Username: "alice"}
var u2 = &model.User{ID: "u2", Username: "bob"}

// --- Rooms ---

func TestRoomsCreate_Success(t *testing.T) {
	h, repo := newTestHandler()
	rec := post(h.RoomsCreate, `{"name":"test room"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, repo.Rooms, 1)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	shapetest.Assert(t, "ChatRoom", resp) // L3 (#1284)
}

func TestRoomsCreate_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsCreate, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRoomsCreate_Error(t *testing.T) {
	h, repo := newTestHandler()
	repo.CreateErr = errMock
	rec := post(h.RoomsCreate, `{"name":"x"}`, u1)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRoomsShow_Success(t *testing.T) {
	h, repo := newTestHandler()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", Name: "room", OwnerID: "u1", Owner: u1}
	rec := post(h.RoomsShow, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRoomsShow_NotFound(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsShow, `{"roomId":"ghost"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// upstream 2026.5.4 hasPermissionToViewRoomInfo gate: 他人の room に対する
// show は 404 NO_SUCH_ROOM を返す (= owner / member / 招待 / moderator 以外)。
// 旧 mk-go は権限 check 無しで誰でも room メタを取れる drop-in regression
// 状態だったので、本テストで gate を guard する (#1164 Phase C)。
func TestRoomsShow_NonOwnerNonMember(t *testing.T) {
	h, repo := newTestHandlerWithService()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", Name: "owners-only", OwnerID: "u1", Owner: u1}
	// u2 は room の owner でも member でも 招待 でもない → 404
	rec := post(h.RoomsShow, `{"roomId":"r1"}`, u2)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// 招待受信者は閲覧できる (owner でも member でもない user が room を見る経路)。
func TestRoomsShow_InvitationAllowed(t *testing.T) {
	h, repo := newTestHandlerWithService()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", Name: "invited", OwnerID: "u1", Owner: u1}
	repo.Invitations["u2:r1"] = &model.ChatRoomInvitation{ID: "inv1", RoomID: "r1", UserID: "u2"}
	rec := post(h.RoomsShow, `{"roomId":"r1"}`, u2)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// member は閲覧できる。
func TestRoomsShow_MemberAllowed(t *testing.T) {
	h, repo := newTestHandlerWithService()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", Name: "with-member", OwnerID: "u1", Owner: u1}
	repo.Memberships["u2:r1"] = &model.ChatRoomMembership{UserID: "u2", RoomID: "r1"}
	rec := post(h.RoomsShow, `{"roomId":"r1"}`, u2)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRoomsShow_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsShow, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRoomsUpdate_Success(t *testing.T) {
	h, repo := newTestHandler()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", Name: "old", OwnerID: "u1"}
	rec := post(h.RoomsUpdate, `{"roomId":"r1","name":"new"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "new", resp["name"])
}

func TestRoomsUpdate_NotOwner(t *testing.T) {
	h, repo := newTestHandler()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "other"}
	rec := post(h.RoomsUpdate, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRoomsUpdate_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsUpdate, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

type failUpdateRoomRepo struct{ *testutil.MockChatRepository }

func (f *failUpdateRoomRepo) UpdateRoom(_ *model.ChatRoom) error { return errMock }

func TestRoomsUpdate_Error(t *testing.T) {
	base := testutil.NewMockChatRepository()
	base.Rooms["r1"] = &model.ChatRoom{ID: "r1", Name: "x", OwnerID: "u1"}
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&failUpdateRoomRepo{base}, idGen)
	rec := post(h.RoomsUpdate, `{"roomId":"r1","name":"y"}`, u1)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRoomsDelete_Success(t *testing.T) {
	h, repo := newTestHandler()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "u1"}
	rec := post(h.RoomsDelete, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRoomsDelete_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsDelete, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRoomsDelete_NotOwner(t *testing.T) {
	h, repo := newTestHandler()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "other"}
	rec := post(h.RoomsDelete, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRoomsOwned(t *testing.T) {
	h, repo := newTestHandler()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "u1"}
	rec := post(h.RoomsOwned, `{}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRoomsJoined(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsJoined, `{}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRoomsLeave(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsLeave, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRoomsLeave_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsLeave, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRoomsMute(t *testing.T) {
	h, repo := newTestHandler()
	repo.Memberships["u1:r1"] = &model.ChatRoomMembership{UserID: "u1", RoomID: "r1"}
	rec := post(h.RoomsMute, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRoomsMute_NoMembership(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsMute, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRoomsMute_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsMute, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRoomsUnmute(t *testing.T) {
	h, repo := newTestHandler()
	repo.Memberships["u1:r1"] = &model.ChatRoomMembership{UserID: "u1", RoomID: "r1", IsMuted: true}
	rec := post(h.RoomsUnmute, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRoomsUnmute_NoMembership(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsUnmute, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRoomsUnmute_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsUnmute, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRoomsTransferOwnership(t *testing.T) {
	h, repo := newTestHandler()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "u1"}
	rec := post(h.RoomsTransferOwnership, `{"roomId":"r1","userId":"u2"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "u2", repo.Rooms["r1"].OwnerID)
}

func TestRoomsTransferOwnership_NotOwner(t *testing.T) {
	h, repo := newTestHandler()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "other"}
	rec := post(h.RoomsTransferOwnership, `{"roomId":"r1","userId":"u2"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRoomsTransferOwnership_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsTransferOwnership, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- Messages ---

func TestMessagesCreate_Success(t *testing.T) {
	h, repo := newTestHandler()
	text := "hello"
	rec := post(h.MessagesCreate, `{"text":"hello","toRoomId":"r1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, repo.Messages, 1)
	_ = text

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	shapetest.Assert(t, "ChatMessage", resp) // L3 (#1288)
}

func TestMessagesCreate_Error(t *testing.T) {
	h, repo := newTestHandler()
	repo.CreateErr = errMock
	rec := post(h.MessagesCreate, `{"text":"x"}`, u1)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestMessagesCreate_InvalidJSON(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.MessagesCreate, `invalid`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMessages_ListEmpty(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.Messages, `{}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMessagesSearch_InvalidJSON(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.MessagesSearch, `invalid`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMessagesShow_WithFromUser(t *testing.T) {
	h, repo := newTestHandler()
	repo.Messages["m1"] = &model.ChatMessage{
		ID: "m1", FromUserID: "u1", FromUser: u1,
		Reads: pq.StringArray{}, Reactions: pq.StringArray{"u2/👍"},
	}
	rec := post(h.MessagesShow, `{"messageId":"m1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "alice")
	// misskey_dart の ChatMessage.fromJson は reactions を {reaction} object の
	// 配列として cast する。"<userId>/<reaction>" 文字列のまま返すと落ちる (#1246)。
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	reactions, ok := body["reactions"].([]any)
	require.True(t, ok, "reactions must be an array")
	require.Len(t, reactions, 1)
	r0, ok := reactions[0].(map[string]any)
	require.True(t, ok, "each reaction must be an object")
	assert.Equal(t, "👍", r0["reaction"], "userId prefix must be stripped")
}

func TestRoomsUpdate_WithDescription(t *testing.T) {
	h, repo := newTestHandler()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", Name: "old", OwnerID: "u1"}
	rec := post(h.RoomsUpdate, `{"roomId":"r1","description":"desc"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "desc", repo.Rooms["r1"].Description)
}

type mockJoinedRepo struct{ *testutil.MockChatRepository }

func (m *mockJoinedRepo) ListJoinedRooms(_ string) ([]*model.ChatRoom, error) {
	return []*model.ChatRoom{{ID: "r1", Name: "room", OwnerID: "u1"}}, nil
}

func TestRoomsJoined_WithRooms(t *testing.T) {
	base := testutil.NewMockChatRepository()
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&mockJoinedRepo{base}, idGen)
	rec := post(h.RoomsJoined, `{}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
}

type mockMsgRepo struct{ *testutil.MockChatRepository }

func (m *mockMsgRepo) ListMessagesByRoom(_ string, _ int) ([]*model.ChatMessage, error) {
	return []*model.ChatMessage{{ID: "m1", FromUserID: "u1", Reads: pq.StringArray{}, Reactions: pq.StringArray{}}}, nil
}
func (m *mockMsgRepo) SearchMessages(_, _ string, _ int) ([]*model.ChatMessage, error) {
	return []*model.ChatMessage{{ID: "m1", FromUserID: "u1", Reads: pq.StringArray{}, Reactions: pq.StringArray{}}}, nil
}

func TestMessages_WithData(t *testing.T) {
	base := testutil.NewMockChatRepository()
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&mockMsgRepo{base}, idGen)
	rec := post(h.Messages, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
}

func TestMessagesSearch_WithData(t *testing.T) {
	base := testutil.NewMockChatRepository()
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&mockMsgRepo{base}, idGen)
	rec := post(h.MessagesSearch, `{"query":"test"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
}

func TestMessagesShow_Success(t *testing.T) {
	h, repo := newTestHandler()
	repo.Messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u1", Reads: pq.StringArray{}, Reactions: pq.StringArray{}}
	rec := post(h.MessagesShow, `{"messageId":"m1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMessagesShow_NotFound(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.MessagesShow, `{"messageId":"ghost"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMessagesShow_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.MessagesShow, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMessagesUpdate_Success(t *testing.T) {
	h, repo := newTestHandler()
	repo.Messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u1"}
	rec := post(h.MessagesUpdate, `{"messageId":"m1","text":"updated"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMessagesUpdate_NotOwner(t *testing.T) {
	h, repo := newTestHandler()
	repo.Messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "other"}
	rec := post(h.MessagesUpdate, `{"messageId":"m1"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMessagesUpdate_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.MessagesUpdate, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMessagesDelete_Success(t *testing.T) {
	h, repo := newTestHandler()
	repo.Messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u1"}
	rec := post(h.MessagesDelete, `{"messageId":"m1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMessagesDelete_NotOwner(t *testing.T) {
	h, repo := newTestHandler()
	repo.Messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "other"}
	rec := post(h.MessagesDelete, `{"messageId":"m1"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMessagesDelete_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.MessagesDelete, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMessagesRead(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.MessagesRead, `{"messageId":"m1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMessagesRead_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.MessagesRead, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMessages_List(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.Messages, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// --- Phase 9.8: service wiring tests ---

func newHandlerWithService(t *testing.T) (*Handler, *testutil.MockChatRepository) {
	t.Helper()
	repo := testutil.NewMockChatRepository()
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(repo, idGen)
	svc := corechat.NewService(repo, idGen)
	h.SetService(svc)
	return h, repo
}

func TestMessagesCreate_Service_ToUser(t *testing.T) {
	h, repo := newHandlerWithService(t)
	rec := post(h.MessagesCreate, `{"text":"hi","toUserId":"u2"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, repo.Messages, 1)
}

func TestMessagesCreate_Service_ToRoom(t *testing.T) {
	h, repo := newHandlerWithService(t)
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "u1"}
	rec := post(h.MessagesCreate, `{"text":"hi","toRoomId":"r1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, repo.Messages, 1)
}

func TestMessagesCreate_Service_NoTarget(t *testing.T) {
	h, _ := newHandlerWithService(t)
	rec := post(h.MessagesCreate, `{"text":"hi"}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMessagesCreate_Service_RoomNotFound(t *testing.T) {
	h, _ := newHandlerWithService(t)
	rec := post(h.MessagesCreate, `{"text":"hi","toRoomId":"ghost"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMessagesCreate_Service_RoomForbidden(t *testing.T) {
	h, repo := newHandlerWithService(t)
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "otherUser"}
	rec := post(h.MessagesCreate, `{"text":"hi","toRoomId":"r1"}`, u1)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestMessagesDelete_Service(t *testing.T) {
	h, repo := newHandlerWithService(t)
	toID := "u2"
	repo.Messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u1", ToUserID: &toID}
	rec := post(h.MessagesDelete, `{"messageId":"m1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMessagesDelete_Service_NotFound(t *testing.T) {
	h, _ := newHandlerWithService(t)
	rec := post(h.MessagesDelete, `{"messageId":"ghost"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMessagesDelete_Service_Forbidden(t *testing.T) {
	h, repo := newHandlerWithService(t)
	toID := "u1"
	repo.Messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u2", ToUserID: &toID}
	rec := post(h.MessagesDelete, `{"messageId":"m1"}`, u1)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestMessagesUpdate_Service(t *testing.T) {
	h, repo := newHandlerWithService(t)
	toID := "u2"
	orig := "old"
	repo.Messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u1", ToUserID: &toID, Text: &orig}
	rec := post(h.MessagesUpdate, `{"messageId":"m1","text":"new"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMessagesUpdate_Service_NotFound(t *testing.T) {
	h, _ := newHandlerWithService(t)
	rec := post(h.MessagesUpdate, `{"messageId":"ghost","text":"x"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMessagesUpdate_Service_Forbidden(t *testing.T) {
	h, repo := newHandlerWithService(t)
	toID := "u1"
	orig := "old"
	repo.Messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u2", ToUserID: &toID, Text: &orig}
	rec := post(h.MessagesUpdate, `{"messageId":"m1","text":"new"}`, u1)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestMessagesRead_Service(t *testing.T) {
	h, repo := newHandlerWithService(t)
	toID := "u1"
	repo.Messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u2", ToUserID: &toID}
	rec := post(h.MessagesRead, `{"messageId":"m1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMessagesRead_Service_NotFound(t *testing.T) {
	h, _ := newHandlerWithService(t)
	rec := post(h.MessagesRead, `{"messageId":"ghost"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

type mockUserMsgRepo struct{ *testutil.MockChatRepository }

func (m *mockUserMsgRepo) ListMessagesByUser(_, _ string, _ int) ([]*model.ChatMessage, error) {
	return []*model.ChatMessage{{ID: "m2", FromUserID: "u2", Reads: pq.StringArray{}, Reactions: pq.StringArray{}}}, nil
}

func TestMessages_ListByUser(t *testing.T) {
	base := testutil.NewMockChatRepository()
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&mockUserMsgRepo{base}, idGen)
	rec := post(h.Messages, `{"userId":"u2"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
}

func TestMessages_InvalidJSON(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.Messages, `invalid`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMessagesSearch(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.MessagesSearch, `{"query":"hello"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestReactionsCreate(t *testing.T) {
	h, _ := newTestHandler()
	// missing params → 400
	assert.Equal(t, http.StatusBadRequest, post(h.ReactionsCreate, `{}`, u1).Code)
	// valid
	rec := post(h.ReactionsCreate, `{"messageId":"m1","reaction":"👍"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestReactionsDelete(t *testing.T) {
	h, _ := newTestHandler()
	// missing params → 400
	assert.Equal(t, http.StatusBadRequest, post(h.ReactionsDelete, `{}`, u1).Code)
	// valid
	rec := post(h.ReactionsDelete, `{"messageId":"m1","reaction":"👍"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// --- Invitations ---

func TestInvitationsCreate_Success(t *testing.T) {
	h, repo := newTestHandler()
	userRepo := testutil.NewMockUserRepository()
	userRepo.Users["u2"] = u2
	h.SetUserRepo(userRepo)
	// owner を populate して nested room.owner (required UserLite) も検証する。
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "r1", Name: "General", OwnerID: u1.ID, Owner: u1}))
	rec := post(h.InvitationsCreate, `{"roomId":"r1","userId":"u2"}`, u1)
	require.Equal(t, http.StatusOK, rec.Code)
	// upstream res は packed ChatRoomInvitation {id, createdAt, userId, user, roomId, room}。
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp["id"])
	assert.NotEmpty(t, resp["createdAt"])
	assert.Equal(t, "u2", resp["userId"])
	assert.Equal(t, "r1", resp["roomId"])
	user, ok := resp["user"].(map[string]any)
	require.True(t, ok, "user (UserLite) must be resolved")
	assert.Equal(t, "u2", user["id"])
	room, ok := resp["room"].(map[string]any)
	require.True(t, ok, "room (ChatRoom) must be present")
	assert.Equal(t, "r1", room["id"])
	owner, ok := room["owner"].(map[string]any)
	require.True(t, ok, "room.owner (UserLite) must be present")
	assert.Equal(t, "u1", owner["id"])
	// invitation 行が永続化されている。
	assert.Len(t, repo.Invitations, 1)
}

// userRepo 配線時に invitee user が存在しない (FindByID err) なら、upstream
// packRoomInvitation の throw と同じく 500 を返し、invitation 行も作らない。
func TestInvitationsCreate_InviteeNotFound(t *testing.T) {
	h, repo := newTestHandler()
	h.SetUserRepo(testutil.NewMockUserRepository()) // invitee 未登録
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "r1", OwnerID: u1.ID}))
	rec := post(h.InvitationsCreate, `{"roomId":"r1","userId":"ghost"}`, u1)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, repo.Invitations, "解決不能な invitee には invitation 行を作らない")
}

// userRepo 未配線 (degraded path) では invitation を作成しつつ user field を省略する。
func TestInvitationsCreate_NoUserRepoOmitsUser(t *testing.T) {
	h, repo := newTestHandler() // SetUserRepo しない
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "r1", OwnerID: u1.ID}))
	rec := post(h.InvitationsCreate, `{"roomId":"r1","userId":"u2"}`, u1)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	_, hasUser := resp["user"]
	assert.False(t, hasUser, "userRepo 未配線では user field を省略する")
	assert.Len(t, repo.Invitations, 1)
}

// upstream: inviter==invitee は 'yourself' で拒否 (generic 500)。
func TestInvitationsCreate_Yourself(t *testing.T) {
	h, repo := newTestHandler()
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "r1", OwnerID: u1.ID}))
	rec := post(h.InvitationsCreate, `{"roomId":"r1","userId":"u1"}`, u1)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, repo.Invitations)
}

// upstream: 既に member / 既に招待済みは拒否 (generic 500)。
func TestInvitationsCreate_AlreadyMemberOrInvited(t *testing.T) {
	h, repo := newTestHandler()
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "r1", OwnerID: u1.ID}))
	// already member
	require.NoError(t, repo.CreateMembership(&model.ChatRoomMembership{ID: "m1", UserID: "u2", RoomID: "r1"}))
	assert.Equal(t, http.StatusInternalServerError, post(h.InvitationsCreate, `{"roomId":"r1","userId":"u2"}`, u1).Code)
	// already invited
	require.NoError(t, repo.CreateInvitation(&model.ChatRoomInvitation{ID: "i1", UserID: "u3", RoomID: "r1"}))
	assert.Equal(t, http.StatusInternalServerError, post(h.InvitationsCreate, `{"roomId":"r1","userId":"u3"}`, u1).Code)
}

// upstream: membership 数が MAX_ROOM_MEMBERS(50) 以上なら 'room is full'。
func TestInvitationsCreate_RoomFull(t *testing.T) {
	h, repo := newTestHandler()
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "r1", OwnerID: u1.ID}))
	for i := 0; i < maxRoomMembers; i++ {
		uid := "filler" + string(rune('a'+i))
		require.NoError(t, repo.CreateMembership(&model.ChatRoomMembership{ID: uid, UserID: uid, RoomID: "r1"}))
	}
	rec := post(h.InvitationsCreate, `{"roomId":"r1","userId":"u2"}`, u1)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, repo.Invitations)
}

func TestInvitationsCreate_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.InvitationsCreate, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInvitationsCreate_NotOwnerRejected(t *testing.T) {
	h, repo := newTestHandler()
	// room owner は u2。u1 は owner でないので招待を作成・連合できない。
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "r1", Name: "General", OwnerID: "u2"}))
	rec := post(h.InvitationsCreate, `{"roomId":"r1","userId":"u3"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestInvitationsCreate_Error(t *testing.T) {
	h, repo := newTestHandler()
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "r1", Name: "General", OwnerID: u1.ID}))
	repo.CreateErr = errMock
	rec := post(h.InvitationsCreate, `{"roomId":"r1","userId":"u2"}`, u1)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestInvitationsDelete(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.InvitationsDelete, `{"invitationId":"i1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestInvitationsDelete_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.InvitationsDelete, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInvitationsAccept(t *testing.T) {
	h, repo := newTestHandler()
	repo.Invitations["i1"] = &model.ChatRoomInvitation{ID: "i1", UserID: "u1", RoomID: "r1"}
	rec := post(h.InvitationsAccept, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Len(t, repo.Memberships, 1)
}

func TestInvitationsAccept_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.InvitationsAccept, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInvitationsAccept_NoInvitationRejected(t *testing.T) {
	h, repo := newTestHandler()
	// 招待が無ければ membership を作らず NO_SUCH_INVITATION。
	rec := post(h.InvitationsAccept, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, repo.Memberships)
}

func TestInvitationsReject(t *testing.T) {
	h, repo := newTestHandler()
	repo.Invitations["i1"] = &model.ChatRoomInvitation{ID: "i1", UserID: "u1", RoomID: "r1"}
	rec := post(h.InvitationsReject, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, repo.Invitations)
}

func TestInvitationsReject_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.InvitationsReject, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInvitationsReject_NoInvitationRejected(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.InvitationsReject, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMembersBan(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.MembersBan, `{"roomId":"r1","userId":"u2"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMembersBan_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.MembersBan, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMembersUpdateMembership(t *testing.T) {
	h, _ := newTestHandler()
	// missing params → 400
	assert.Equal(t, http.StatusBadRequest, post(h.MembersUpdateMembership, `{}`, u1).Code)
}

func TestHistory(t *testing.T) {
	h, _ := newTestHandler()
	// default (room=false) → ListUserHistory に dispatch
	rec := post(h.History, `{}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	rec2 := post(h.History, `{"limit":5}`, u1)
	assert.Equal(t, http.StatusOK, rec2.Code)
	// room=true → ListRoomHistory に dispatch (#692)
	rec3 := post(h.History, `{"room":true,"limit":3}`, u1)
	assert.Equal(t, http.StatusOK, rec3.Code)
	// limit clamp: 0 → 10, 9999 → 100 (内部分岐の coverage 確保)
	assert.Equal(t, http.StatusOK, post(h.History, `{"limit":0}`, u1).Code)
	assert.Equal(t, http.StatusOK, post(h.History, `{"limit":9999}`, u1).Code)
}

// #692 review #7: UserTimeline / RoomTimeline がそれぞれ
// MarkAllReadFromUser / MarkAllReadInRoom を呼んでチャット画面を開いた瞬間に
// 既読化することを assert する (handler 側の wiring を guard)。
type readMarkSpyRepo struct {
	*testutil.MockChatRepository
	markFromUserCalled struct{ reader, fromUser string }
	markInRoomCalled   struct{ reader, room string }
}

func (m *readMarkSpyRepo) MarkAllReadFromUser(reader, fromUser string) error {
	m.markFromUserCalled.reader = reader
	m.markFromUserCalled.fromUser = fromUser
	return nil
}

func (m *readMarkSpyRepo) MarkAllReadInRoom(reader, room string) error {
	m.markInRoomCalled.reader = reader
	m.markInRoomCalled.room = room
	return nil
}

func TestUserTimeline_MarksReadOnOpen(t *testing.T) {
	spy := &readMarkSpyRepo{MockChatRepository: testutil.NewMockChatRepository()}
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(spy, idGen)
	rec := post(h.UserTimeline, `{"userId":"u_other"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "u1", spy.markFromUserCalled.reader)
	assert.Equal(t, "u_other", spy.markFromUserCalled.fromUser)
}

func TestRoomTimeline_MarksReadOnOpen(t *testing.T) {
	spy := &readMarkSpyRepo{MockChatRepository: testutil.NewMockChatRepository()}
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(spy, idGen)
	rec := post(h.RoomTimeline, `{"roomId":"r_x"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "u1", spy.markInRoomCalled.reader)
	assert.Equal(t, "r_x", spy.markInRoomCalled.room)
}

// MarkAllRead* が err を返しても timeline 自体は 200 を返す (best-effort)。
type readMarkErrRepo struct {
	*testutil.MockChatRepository
}

func (m *readMarkErrRepo) MarkAllReadFromUser(_, _ string) error { return errMock }
func (m *readMarkErrRepo) MarkAllReadInRoom(_, _ string) error   { return errMock }

func TestTimeline_BestEffortReadMark(t *testing.T) {
	r := &readMarkErrRepo{MockChatRepository: testutil.NewMockChatRepository()}
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(r, idGen)
	assert.Equal(t, http.StatusOK, post(h.UserTimeline, `{"userId":"u2"}`, u1).Code)
	assert.Equal(t, http.StatusOK, post(h.RoomTimeline, `{"roomId":"r1"}`, u1).Code)
}

func TestUnreadCount(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.UnreadCount, `{}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, float64(0), resp["count"])
}

// --- TS-compatible aliases + new handlers ---

func TestMessagesCreateToUser(t *testing.T) {
	h, repo := newTestHandler()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: u1.ID}
	rec := post(h.MessagesCreateToUser, `{"text":"hi","toUserId":"u2"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// recipient が sender を block している場合は YOU_HAVE_BEEN_BLOCKED (403)。
func TestMessagesCreateToUser_Blocked(t *testing.T) {
	repo := testutil.NewMockChatRepository()
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(repo, idGen)
	svc := corechat.NewService(repo, idGen)
	blocks := testutil.NewMockBlockingRepository()
	// blocker=u2 (recipient), blockee=u1 (sender)。
	require.NoError(t, blocks.Create(&model.Blocking{ID: "b1", BlockerID: "u2", BlockeeID: "u1"}))
	svc.SetBlockingRepo(blocks)
	h.SetService(svc)

	rec := post(h.MessagesCreateToUser, `{"text":"hi","toUserId":"u2"}`, u1)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj, _ := resp["error"].(map[string]any)
	require.NotNil(t, errObj)
	assert.Equal(t, "YOU_HAVE_BEEN_BLOCKED", errObj["code"])
}

func TestMessagesCreateToRoom(t *testing.T) {
	h, repo := newTestHandler()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: u1.ID}
	rec := post(h.MessagesCreateToRoom, `{"text":"hi","toRoomId":"r1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUserTimeline(t *testing.T) {
	h, _ := newTestHandler()
	assert.Equal(t, http.StatusBadRequest, post(h.UserTimeline, `{}`, u1).Code)
	rec := post(h.UserTimeline, `{"userId":"u2"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	rec2 := post(h.UserTimeline, `{"userId":"u2","limit":5}`, u1)
	assert.Equal(t, http.StatusOK, rec2.Code)
}

func TestRoomTimeline(t *testing.T) {
	h, _ := newTestHandler()
	assert.Equal(t, http.StatusBadRequest, post(h.RoomTimeline, `{}`, u1).Code)
	rec := post(h.RoomTimeline, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	rec2 := post(h.RoomTimeline, `{"roomId":"r1","limit":5}`, u1)
	assert.Equal(t, http.StatusOK, rec2.Code)
}

func TestReadAll(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.ReadAll, `{}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// #692: mapChatErr が ErrChatScopeViolation / ErrChatScopeUnconfigured を
// それぞれ 403 / 500 にマップする。新規 error path の wiring guard。
func TestMapChatErr_ChatScopeBranches(t *testing.T) {
	h, _ := newTestHandler()
	e := echo.New()
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"violation", corechat.ErrChatScopeViolation, http.StatusForbidden},
		{"unconfigured", corechat.ErrChatScopeUnconfigured, http.StatusInternalServerError},
		{"unknown", errors.New("?"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec)
			_ = h.mapChatErr(c, tc.err)
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

// #692: packMessageDetailed は createdAt + toUser + toRoom を含めて返す。
func TestPackMessageDetailed_FieldsPresent(t *testing.T) {
	h, _ := newTestHandler()
	idGen, _ := id.NewGenerator("aidx")
	now := idGen.Generate(time.Now())
	host := "remote.example"
	roomID := "r1"
	toUserID := "u_to"
	msg := &model.ChatMessage{
		ID:         now,
		FromUserID: "u_from",
		ToUserID:   &toUserID,
		ToRoomID:   &roomID,
		FromUser:   &model.User{ID: "u_from", Username: "from", Host: &host},
		ToUser:     &model.User{ID: "u_to", Username: "to"},
		ToRoom:     &model.ChatRoom{ID: roomID, Name: "Room"},
		Reads:      pq.StringArray{},
		Reactions:  pq.StringArray{},
	}
	out := h.packMessageDetailed(msg, "u_from")
	assert.NotEmpty(t, out["createdAt"], "createdAt が ID から派生して埋まること")
	assert.NotNil(t, out["toUser"])
	assert.NotNil(t, out["toRoom"])
	// upstream FE 互換のため `room` alias も入る
	assert.NotNil(t, out["room"])
	// fromUser に avatarUrl の identicon fallback が入る (assertion 失敗時は
	// require.IsType がテスト終了させるので panic せずに見やすいエラーになる)。
	require.IsType(t, map[string]any{}, out["fromUser"])
	from := out["fromUser"].(map[string]any)
	assert.Equal(t, "/identicon/from@remote.example", from["avatarUrl"])
}

func TestInvitationsIgnore(t *testing.T) {
	h, repo := newTestHandler()
	assert.Equal(t, http.StatusBadRequest, post(h.InvitationsIgnore, `{}`, u1).Code)
	repo.Invitations["inv1"] = &model.ChatRoomInvitation{ID: "inv1", UserID: u1.ID, RoomID: "r1"}
	rec := post(h.InvitationsIgnore, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestInvitationsInbox(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.InvitationsInbox, `{}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestInvitationsOutbox(t *testing.T) {
	h, repo := newTestHandler()
	assert.Equal(t, http.StatusBadRequest, post(h.InvitationsOutbox, `{}`, u1).Code)
	// no room → 404
	assert.Equal(t, http.StatusNotFound, post(h.InvitationsOutbox, `{"roomId":"r1"}`, u1).Code)
	// owner can see outbox
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: u1.ID}
	rec := post(h.InvitationsOutbox, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	// non-owner → 404
	assert.Equal(t, http.StatusNotFound, post(h.InvitationsOutbox, `{"roomId":"r1"}`, u2).Code)
}

func TestRoomsJoin(t *testing.T) {
	h, repo := newTestHandler()
	// invalid param
	assert.Equal(t, http.StatusBadRequest, post(h.RoomsJoin, `{}`, u1).Code)
	// upstream: pending invitation が無いと join できない (generic 500)。
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: u2.ID}
	assert.Equal(t, http.StatusInternalServerError, post(h.RoomsJoin, `{"roomId":"r1"}`, u1).Code)
	assert.Empty(t, repo.Memberships, "invitation 無しでは membership を作らない")
	// invitation があれば join 成功し、membership 作成 + invitation 消費。
	require.NoError(t, repo.CreateInvitation(&model.ChatRoomInvitation{ID: "i1", UserID: u1.ID, RoomID: "r1"}))
	rec := post(h.RoomsJoin, `{"roomId":"r1"}`, u1)
	require.Equal(t, http.StatusNoContent, rec.Code)
	_, mErr := repo.FindMembership(u1.ID, "r1")
	assert.NoError(t, mErr, "membership が作成される")
	assert.Empty(t, repo.Invitations, "join 成功で invitation は消費される")
}

// 既存 member が invitation 付きで再 join しても冪等に 204 (membership 重複なし)
// + invitation 消費。room-full でも既存 member なら拒否しない (TS との意図的差異)。
func TestRoomsJoin_Idempotent(t *testing.T) {
	h, repo := newTestHandler()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: u2.ID}
	require.NoError(t, repo.CreateMembership(&model.ChatRoomMembership{ID: "m1", UserID: u1.ID, RoomID: "r1"}))
	require.NoError(t, repo.CreateInvitation(&model.ChatRoomInvitation{ID: "i1", UserID: u1.ID, RoomID: "r1"}))
	// 念のため room を満杯にしても既存 member は通る。
	for i := 0; i < maxRoomMembers; i++ {
		uid := "filler" + string(rune('a'+i))
		require.NoError(t, repo.CreateMembership(&model.ChatRoomMembership{ID: uid, UserID: uid, RoomID: "r1"}))
	}
	rec := post(h.RoomsJoin, `{"roomId":"r1"}`, u1)
	require.Equal(t, http.StatusNoContent, rec.Code)
	mems, _ := repo.ListMembersByRoom("r1")
	assert.Len(t, mems, maxRoomMembers+1, "membership は重複作成されない (既存 m1 + filler 50)")
	assert.Empty(t, repo.Invitations, "再 join でも invitation は消費される")
}

// upstream: membership 数が MAX_ROOM_MEMBERS(50) 以上の room へは新規 join 不可。
func TestRoomsJoin_RoomFull(t *testing.T) {
	h, repo := newTestHandler()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: u2.ID}
	require.NoError(t, repo.CreateInvitation(&model.ChatRoomInvitation{ID: "i1", UserID: u1.ID, RoomID: "r1"}))
	for i := 0; i < maxRoomMembers; i++ {
		uid := "filler" + string(rune('a'+i))
		require.NoError(t, repo.CreateMembership(&model.ChatRoomMembership{ID: uid, UserID: uid, RoomID: "r1"}))
	}
	rec := post(h.RoomsJoin, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	// room-full 失敗時は invitation を消費せず membership も作らない (再試行可能)。
	assert.NotEmpty(t, repo.Invitations, "room-full では invitation を消費しない")
	_, mErr := repo.FindMembership(u1.ID, "r1")
	assert.Error(t, mErr, "room-full では membership を作らない")
}

// upstream joining.ts は ChatRoomMembership[] (room 付き, id 降順) を返す。
func TestRoomsJoining(t *testing.T) {
	h, repo := newTestHandler()
	idGen, _ := id.NewGenerator("aidx")
	mid1 := idGen.Generate(time.Now())
	mid2 := idGen.Generate(time.Now().Add(time.Second)) // newer
	// owner を populate して nested room.owner (required UserLite) も検証する。
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "r1", Name: "General", OwnerID: u2.ID, Owner: u2}))
	require.NoError(t, repo.CreateRoom(&model.ChatRoom{ID: "r2", Name: "Other", OwnerID: u2.ID, Owner: u2}))
	require.NoError(t, repo.CreateMembership(&model.ChatRoomMembership{ID: mid1, UserID: u1.ID, RoomID: "r1"}))
	require.NoError(t, repo.CreateMembership(&model.ChatRoomMembership{ID: mid2, UserID: u1.ID, RoomID: "r2"}))
	rec := post(h.RoomsJoining, `{}`, u1)
	require.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 2)
	// id 降順 (新しい mid2 が先頭)。
	assert.Equal(t, mid2, rows[0]["id"])
	assert.Equal(t, mid1, rows[1]["id"])
	assert.Equal(t, u1.ID, rows[0]["userId"])
	assert.NotEmpty(t, rows[0]["createdAt"])
	room, ok := rows[0]["room"].(map[string]any)
	require.True(t, ok, "membership は room (ChatRoom) を populate する")
	assert.Equal(t, "r2", room["id"])
	owner, ok := room["owner"].(map[string]any)
	require.True(t, ok, "room.owner (UserLite) must be present")
	assert.Equal(t, "u2", owner["id"])
	// joining は populateUser:false なので user は含めない。
	_, hasUser := rows[0]["user"]
	assert.False(t, hasUser)
}

// repo error 時は 500 を返す。
func TestRoomsJoining_RepoError(t *testing.T) {
	h, repo := newTestHandler()
	repo.ListMembershipsErr = errMock
	rec := post(h.RoomsJoining, `{}`, u1)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRoomsMembers(t *testing.T) {
	h, _ := newTestHandler()
	assert.Equal(t, http.StatusBadRequest, post(h.RoomsMembers, `{}`, u1).Code)
	rec := post(h.RoomsMembers, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMembersUpdateMembership_Auth(t *testing.T) {
	h, repo := newTestHandler()
	repo.Rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: u1.ID}
	repo.Memberships["u2:r1"] = &model.ChatRoomMembership{ID: "m1", UserID: u2.ID, RoomID: "r1"}
	// owner can update
	tr := true
	_ = tr
	rec := post(h.MembersUpdateMembership, `{"roomId":"r1","userId":"u2","isMuted":true}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	// non-owner → 404
	assert.Equal(t, http.StatusNotFound, post(h.MembersUpdateMembership, `{"roomId":"r1","userId":"u2"}`, u2).Code)
	// not a member → 404
	assert.Equal(t, http.StatusNotFound, post(h.MembersUpdateMembership, `{"roomId":"r1","userId":"ghost"}`, u1).Code)
}

// --- AttachedChatMessages (#1218) ---

type fakeModeratorChecker struct{ mods map[string]bool }

func (f fakeModeratorChecker) IsModerator(userID string) bool { return f.mods[userID] }

func attachedHandler(t *testing.T) (*Handler, *testutil.MockChatRepository, *testutil.MockDriveFileRepository) {
	t.Helper()
	h, repo := newTestHandler()
	fileRepo := testutil.NewMockDriveFileRepository()
	h.SetDriveFileRepo(fileRepo)
	h.SetModeratorChecker(fakeModeratorChecker{mods: map[string]bool{"mod1": true}})
	return h, repo, fileRepo
}

func seedFileMsg(repo *testutil.MockChatRepository, id, fileID string) {
	fid := fileID
	to := "u9"
	repo.Messages[id] = &model.ChatMessage{ID: id, FromUserID: "sender", ToUserID: &to, FileID: &fid}
}

func TestAttachedChatMessages_OwnerSeesMessages(t *testing.T) {
	h, repo, fileRepo := attachedHandler(t)
	owner := u1.ID
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &owner}
	seedFileMsg(repo, "m1", "f1")
	seedFileMsg(repo, "m2", "f1")
	seedFileMsg(repo, "m3", "other") // 別 file は除外

	rec := post(h.AttachedChatMessages, `{"fileId":"f1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Len(t, out, 2)
}

func TestAttachedChatMessages_NonOwnerRejected(t *testing.T) {
	h, repo, fileRepo := attachedHandler(t)
	owner := u1.ID
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &owner}
	seedFileMsg(repo, "m1", "f1")
	// u2 は owner でも moderator でもない → NoSuchFile。
	rec := post(h.AttachedChatMessages, `{"fileId":"f1"}`, u2)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAttachedChatMessages_ModeratorSeesOthersFile(t *testing.T) {
	h, repo, fileRepo := attachedHandler(t)
	owner := u1.ID
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &owner}
	seedFileMsg(repo, "m1", "f1")
	rec := post(h.AttachedChatMessages, `{"fileId":"f1"}`, &model.User{ID: "mod1"})
	assert.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Len(t, out, 1)
}

func TestAttachedChatMessages_MissingFileID(t *testing.T) {
	h, _, _ := attachedHandler(t)
	rec := post(h.AttachedChatMessages, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAttachedChatMessages_NoSuchFile(t *testing.T) {
	h, _, _ := attachedHandler(t)
	rec := post(h.AttachedChatMessages, `{"fileId":"ghost"}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAttachedChatMessages_NoFileRepoReturnsEmpty(t *testing.T) {
	h, _ := newTestHandler() // fileRepo 未配線
	rec := post(h.AttachedChatMessages, `{"fileId":"f1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}
