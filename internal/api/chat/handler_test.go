package chat

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	corechat "github.com/shiroha-a/mk/internal/core/chat"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errMock = assert.AnError

type mockChatRepo struct {
	rooms       map[string]*model.ChatRoom
	messages    map[string]*model.ChatMessage
	memberships map[string]*model.ChatRoomMembership
	invitations map[string]*model.ChatRoomInvitation
	createErr   error
}

func newMock() *mockChatRepo {
	return &mockChatRepo{
		rooms:       make(map[string]*model.ChatRoom),
		messages:    make(map[string]*model.ChatMessage),
		memberships: make(map[string]*model.ChatRoomMembership),
		invitations: make(map[string]*model.ChatRoomInvitation),
	}
}

func (m *mockChatRepo) CreateRoom(r *model.ChatRoom) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.rooms[r.ID] = r
	return nil
}
func (m *mockChatRepo) FindRoomByID(id string) (*model.ChatRoom, error) {
	if r, ok := m.rooms[id]; ok {
		return r, nil
	}
	return nil, errMock
}
func (m *mockChatRepo) UpdateRoom(r *model.ChatRoom) error { m.rooms[r.ID] = r; return nil }
func (m *mockChatRepo) DeleteRoom(id string) error         { delete(m.rooms, id); return nil }
func (m *mockChatRepo) ListRoomsByOwner(ownerID string) ([]*model.ChatRoom, error) {
	var result []*model.ChatRoom
	for _, r := range m.rooms {
		if r.OwnerID == ownerID {
			result = append(result, r)
		}
	}
	return result, nil
}
func (m *mockChatRepo) ListJoinedRooms(_ string) ([]*model.ChatRoom, error) {
	return []*model.ChatRoom{}, nil
}
func (m *mockChatRepo) CreateMessage(msg *model.ChatMessage) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.messages[msg.ID] = msg
	return nil
}
func (m *mockChatRepo) FindMessageByID(id string) (*model.ChatMessage, error) {
	if msg, ok := m.messages[id]; ok {
		return msg, nil
	}
	return nil, errMock
}
func (m *mockChatRepo) UpdateMessage(msg *model.ChatMessage) error {
	m.messages[msg.ID] = msg
	return nil
}
func (m *mockChatRepo) DeleteMessage(id string) error { delete(m.messages, id); return nil }
func (m *mockChatRepo) ListMessagesByRoom(_ string, _ int) ([]*model.ChatMessage, error) {
	return []*model.ChatMessage{}, nil
}
func (m *mockChatRepo) ListMessagesByUser(_, _ string, _ int) ([]*model.ChatMessage, error) {
	return []*model.ChatMessage{}, nil
}
func (m *mockChatRepo) SearchMessages(_, _ string, _ int) ([]*model.ChatMessage, error) {
	return []*model.ChatMessage{}, nil
}
func (m *mockChatRepo) CreateMembership(mem *model.ChatRoomMembership) error {
	m.memberships[mem.UserID+":"+mem.RoomID] = mem
	return nil
}
func (m *mockChatRepo) FindMembership(userID, roomID string) (*model.ChatRoomMembership, error) {
	if mem, ok := m.memberships[userID+":"+roomID]; ok {
		return mem, nil
	}
	return nil, errMock
}
func (m *mockChatRepo) UpdateMembership(mem *model.ChatRoomMembership) error {
	m.memberships[mem.UserID+":"+mem.RoomID] = mem
	return nil
}
func (m *mockChatRepo) DeleteMembership(userID, roomID string) error {
	delete(m.memberships, userID+":"+roomID)
	return nil
}
func (m *mockChatRepo) ListMembersByRoom(_ string) ([]*model.ChatRoomMembership, error) {
	return []*model.ChatRoomMembership{}, nil
}
func (m *mockChatRepo) CreateInvitation(inv *model.ChatRoomInvitation) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.invitations[inv.ID] = inv
	return nil
}
func (m *mockChatRepo) DeleteInvitation(id string) error { delete(m.invitations, id); return nil }
func (m *mockChatRepo) FindInvitation(userID, roomID string) (*model.ChatRoomInvitation, error) {
	for _, inv := range m.invitations {
		if inv.UserID == userID && inv.RoomID == roomID {
			return inv, nil
		}
	}
	return nil, errMock
}
func (m *mockChatRepo) CountUnread(_ string) (int64, error) { return 0, nil }
func (m *mockChatRepo) MarkRead(_, _ string) error          { return nil }
func (m *mockChatRepo) ListInvitationsByUser(_ string, _ bool) ([]*model.ChatRoomInvitation, error) {
	return nil, nil
}
func (m *mockChatRepo) ListInvitationsByRoom(_ string) ([]*model.ChatRoomInvitation, error) {
	return nil, nil
}
func (m *mockChatRepo) UpdateDeliveryStatus(_ string, _, _ bool) error { return nil }
func (m *mockChatRepo) ListHistory(_ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
}
func (m *mockChatRepo) ListUserHistory(_ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
}
func (m *mockChatRepo) ListRoomHistory(_ string, _ int) ([]*model.ChatMessage, error) {
	return nil, nil
}
func (m *mockChatRepo) MarkAllRead(_ string) error                         { return nil }
func (m *mockChatRepo) MarkAllReadFromUser(_, _ string) error              { return nil }
func (m *mockChatRepo) MarkAllReadInRoom(_, _ string) error                { return nil }
func (m *mockChatRepo) AddReaction(_, _ string) error                      { return nil }
func (m *mockChatRepo) RemoveReaction(_, _ string) error                   { return nil }
func (m *mockChatRepo) UpdateInvitation(_ *model.ChatRoomInvitation) error { return nil }
func (m *mockChatRepo) FindMessageByURI(_ string) (*model.ChatMessage, error) {
	return nil, errors.New("not found")
}

func newTestHandler() (*Handler, *mockChatRepo) {
	repo := newMock()
	idGen, _ := id.NewGenerator("aidx")
	return NewHandler(repo, idGen), repo
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
	assert.Len(t, repo.rooms, 1)
}

func TestRoomsCreate_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsCreate, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRoomsCreate_Error(t *testing.T) {
	h, repo := newTestHandler()
	repo.createErr = errMock
	rec := post(h.RoomsCreate, `{"name":"x"}`, u1)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRoomsShow_Success(t *testing.T) {
	h, repo := newTestHandler()
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", Name: "room", OwnerID: "u1", Owner: u1}
	rec := post(h.RoomsShow, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRoomsShow_NotFound(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsShow, `{"roomId":"ghost"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRoomsShow_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsShow, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRoomsUpdate_Success(t *testing.T) {
	h, repo := newTestHandler()
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", Name: "old", OwnerID: "u1"}
	rec := post(h.RoomsUpdate, `{"roomId":"r1","name":"new"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "new", resp["name"])
}

func TestRoomsUpdate_NotOwner(t *testing.T) {
	h, repo := newTestHandler()
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "other"}
	rec := post(h.RoomsUpdate, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRoomsUpdate_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsUpdate, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

type failUpdateRoomRepo struct{ *mockChatRepo }

func (f *failUpdateRoomRepo) UpdateRoom(_ *model.ChatRoom) error { return errMock }

func TestRoomsUpdate_Error(t *testing.T) {
	base := newMock()
	base.rooms["r1"] = &model.ChatRoom{ID: "r1", Name: "x", OwnerID: "u1"}
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&failUpdateRoomRepo{base}, idGen)
	rec := post(h.RoomsUpdate, `{"roomId":"r1","name":"y"}`, u1)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRoomsDelete_Success(t *testing.T) {
	h, repo := newTestHandler()
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "u1"}
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
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "other"}
	rec := post(h.RoomsDelete, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRoomsOwned(t *testing.T) {
	h, repo := newTestHandler()
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "u1"}
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
	repo.memberships["u1:r1"] = &model.ChatRoomMembership{UserID: "u1", RoomID: "r1"}
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
	repo.memberships["u1:r1"] = &model.ChatRoomMembership{UserID: "u1", RoomID: "r1", IsMuted: true}
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
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "u1"}
	rec := post(h.RoomsTransferOwnership, `{"roomId":"r1","userId":"u2"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "u2", repo.rooms["r1"].OwnerID)
}

func TestRoomsTransferOwnership_NotOwner(t *testing.T) {
	h, repo := newTestHandler()
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "other"}
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
	assert.Len(t, repo.messages, 1)
	_ = text
}

func TestMessagesCreate_Error(t *testing.T) {
	h, repo := newTestHandler()
	repo.createErr = errMock
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
	repo.messages["m1"] = &model.ChatMessage{
		ID: "m1", FromUserID: "u1", FromUser: u1,
		Reads: pq.StringArray{}, Reactions: pq.StringArray{},
	}
	rec := post(h.MessagesShow, `{"messageId":"m1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "alice")
}

func TestRoomsUpdate_WithDescription(t *testing.T) {
	h, repo := newTestHandler()
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", Name: "old", OwnerID: "u1"}
	rec := post(h.RoomsUpdate, `{"roomId":"r1","description":"desc"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "desc", repo.rooms["r1"].Description)
}

type mockJoinedRepo struct{ *mockChatRepo }

func (m *mockJoinedRepo) ListJoinedRooms(_ string) ([]*model.ChatRoom, error) {
	return []*model.ChatRoom{{ID: "r1", Name: "room", OwnerID: "u1"}}, nil
}

func TestRoomsJoined_WithRooms(t *testing.T) {
	base := newMock()
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&mockJoinedRepo{base}, idGen)
	rec := post(h.RoomsJoined, `{}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
}

type mockMsgRepo struct{ *mockChatRepo }

func (m *mockMsgRepo) ListMessagesByRoom(_ string, _ int) ([]*model.ChatMessage, error) {
	return []*model.ChatMessage{{ID: "m1", FromUserID: "u1", Reads: pq.StringArray{}, Reactions: pq.StringArray{}}}, nil
}
func (m *mockMsgRepo) SearchMessages(_, _ string, _ int) ([]*model.ChatMessage, error) {
	return []*model.ChatMessage{{ID: "m1", FromUserID: "u1", Reads: pq.StringArray{}, Reactions: pq.StringArray{}}}, nil
}

func TestMessages_WithData(t *testing.T) {
	base := newMock()
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&mockMsgRepo{base}, idGen)
	rec := post(h.Messages, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
}

func TestMessagesSearch_WithData(t *testing.T) {
	base := newMock()
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
	repo.messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u1", Reads: pq.StringArray{}, Reactions: pq.StringArray{}}
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
	repo.messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u1"}
	rec := post(h.MessagesUpdate, `{"messageId":"m1","text":"updated"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMessagesUpdate_NotOwner(t *testing.T) {
	h, repo := newTestHandler()
	repo.messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "other"}
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
	repo.messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u1"}
	rec := post(h.MessagesDelete, `{"messageId":"m1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMessagesDelete_NotOwner(t *testing.T) {
	h, repo := newTestHandler()
	repo.messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "other"}
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

func newHandlerWithService(t *testing.T) (*Handler, *mockChatRepo) {
	t.Helper()
	repo := newMock()
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
	assert.Len(t, repo.messages, 1)
}

func TestMessagesCreate_Service_ToRoom(t *testing.T) {
	h, repo := newHandlerWithService(t)
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "u1"}
	rec := post(h.MessagesCreate, `{"text":"hi","toRoomId":"r1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, repo.messages, 1)
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
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: "otherUser"}
	rec := post(h.MessagesCreate, `{"text":"hi","toRoomId":"r1"}`, u1)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestMessagesDelete_Service(t *testing.T) {
	h, repo := newHandlerWithService(t)
	toID := "u2"
	repo.messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u1", ToUserID: &toID}
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
	repo.messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u2", ToUserID: &toID}
	rec := post(h.MessagesDelete, `{"messageId":"m1"}`, u1)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestMessagesUpdate_Service(t *testing.T) {
	h, repo := newHandlerWithService(t)
	toID := "u2"
	orig := "old"
	repo.messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u1", ToUserID: &toID, Text: &orig}
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
	repo.messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u2", ToUserID: &toID, Text: &orig}
	rec := post(h.MessagesUpdate, `{"messageId":"m1","text":"new"}`, u1)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestMessagesRead_Service(t *testing.T) {
	h, repo := newHandlerWithService(t)
	toID := "u1"
	repo.messages["m1"] = &model.ChatMessage{ID: "m1", FromUserID: "u2", ToUserID: &toID}
	rec := post(h.MessagesRead, `{"messageId":"m1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMessagesRead_Service_NotFound(t *testing.T) {
	h, _ := newHandlerWithService(t)
	rec := post(h.MessagesRead, `{"messageId":"ghost"}`, u1)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

type mockUserMsgRepo struct{ *mockChatRepo }

func (m *mockUserMsgRepo) ListMessagesByUser(_, _ string, _ int) ([]*model.ChatMessage, error) {
	return []*model.ChatMessage{{ID: "m2", FromUserID: "u2", Reads: pq.StringArray{}, Reactions: pq.StringArray{}}}, nil
}

func TestMessages_ListByUser(t *testing.T) {
	base := newMock()
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
	h, _ := newTestHandler()
	rec := post(h.InvitationsCreate, `{"roomId":"r1","userId":"u2"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestInvitationsCreate_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.InvitationsCreate, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInvitationsCreate_Error(t *testing.T) {
	h, repo := newTestHandler()
	repo.createErr = errMock
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
	repo.invitations["i1"] = &model.ChatRoomInvitation{ID: "i1", UserID: "u1", RoomID: "r1"}
	rec := post(h.InvitationsAccept, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Len(t, repo.memberships, 1)
}

func TestInvitationsAccept_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.InvitationsAccept, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInvitationsReject(t *testing.T) {
	h, repo := newTestHandler()
	repo.invitations["i1"] = &model.ChatRoomInvitation{ID: "i1", UserID: "u1", RoomID: "r1"}
	rec := post(h.InvitationsReject, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, repo.invitations)
}

func TestInvitationsReject_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.InvitationsReject, `{}`, u1)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
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
	rec := post(h.History, `{}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	rec2 := post(h.History, `{"limit":5}`, u1)
	assert.Equal(t, http.StatusOK, rec2.Code)
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
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: u1.ID}
	rec := post(h.MessagesCreateToUser, `{"text":"hi","toUserId":"u2"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMessagesCreateToRoom(t *testing.T) {
	h, repo := newTestHandler()
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: u1.ID}
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

func TestInvitationsIgnore(t *testing.T) {
	h, repo := newTestHandler()
	assert.Equal(t, http.StatusBadRequest, post(h.InvitationsIgnore, `{}`, u1).Code)
	repo.invitations["inv1"] = &model.ChatRoomInvitation{ID: "inv1", UserID: u1.ID, RoomID: "r1"}
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
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: u1.ID}
	rec := post(h.InvitationsOutbox, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
	// non-owner → 404
	assert.Equal(t, http.StatusNotFound, post(h.InvitationsOutbox, `{"roomId":"r1"}`, u2).Code)
}

func TestRoomsJoin(t *testing.T) {
	h, repo := newTestHandler()
	assert.Equal(t, http.StatusBadRequest, post(h.RoomsJoin, `{}`, u1).Code)
	assert.Equal(t, http.StatusNotFound, post(h.RoomsJoin, `{"roomId":"ghost"}`, u1).Code)
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: u2.ID}
	rec := post(h.RoomsJoin, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRoomsJoining(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.RoomsJoining, `{}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRoomsMembers(t *testing.T) {
	h, _ := newTestHandler()
	assert.Equal(t, http.StatusBadRequest, post(h.RoomsMembers, `{}`, u1).Code)
	rec := post(h.RoomsMembers, `{"roomId":"r1"}`, u1)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMembersUpdateMembership_Auth(t *testing.T) {
	h, repo := newTestHandler()
	repo.rooms["r1"] = &model.ChatRoom{ID: "r1", OwnerID: u1.ID}
	repo.memberships["u2:r1"] = &model.ChatRoomMembership{ID: "m1", UserID: u2.ID, RoomID: "r1"}
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
