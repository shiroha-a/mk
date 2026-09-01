package chat

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

// failingChatRepo makes every room / message lookup look like a database
// failure rather than a missing row.
type failingChatRepo struct {
	*testutil.MockChatRepository
	err error
}

func (r *failingChatRepo) FindRoomByID(string) (*model.ChatRoom, error) { return nil, r.err }
func (r *failingChatRepo) FindMessageByID(string) (*model.ChatMessage, error) {
	return nil, r.err
}

// **DB 障害を「そんな部屋 / メッセージは無い」にしない** (#2792)。
//
// chat は部屋の存在で権限を判定するので、障害を 400 で返すと「部屋が消えた」と
// 読めてしまう。監視でも 5xx が立たない。
func TestChat_DBFailureIsNot4xx(t *testing.T) {
	dbErr := errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")
	me := &model.User{ID: "u1"}

	newFailing := func() *Handler {
		repo := &failingChatRepo{MockChatRepository: testutil.NewMockChatRepository(), err: dbErr}
		idGen, _ := id.NewGenerator("aidx")
		return NewHandler(repo, idGen)
	}

	for _, tt := range []struct {
		name string
		run  func(*Handler) int
	}{
		{"chat/rooms/update", func(h *Handler) int {
			return post(h.RoomsUpdate, `{"roomId":"r1","name":"x"}`, me).Code
		}},
		{"chat/rooms/delete", func(h *Handler) int {
			return post(h.RoomsDelete, `{"roomId":"r1"}`, me).Code
		}},
		{"chat/rooms/transfer-ownership", func(h *Handler) int {
			return post(h.RoomsTransferOwnership, `{"roomId":"r1","userId":"u2"}`, me).Code
		}},
		{"chat/messages/update", func(h *Handler) int {
			return post(h.MessagesUpdate, `{"messageId":"m1","text":"x"}`, me).Code
		}},
		{"chat/messages/delete", func(h *Handler) int {
			return post(h.MessagesDelete, `{"messageId":"m1"}`, me).Code
		}},
		{"chat/rooms/invitations/create", func(h *Handler) int {
			return post(h.InvitationsCreate, `{"roomId":"r1","userId":"u2"}`, me).Code
		}},
		{"chat/rooms/members/update-membership", func(h *Handler) int {
			return post(h.MembersUpdateMembership, `{"roomId":"r1","userId":"u2","isMuted":true}`, me).Code
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, http.StatusInternalServerError, tt.run(newFailing()),
				"DB 障害が 4xx に化けている (#2792)")
		})
	}
}
