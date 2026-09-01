package users

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

// failingUserListRepo makes every list lookup look like a database failure.
type failingUserListRepo struct {
	*testutil.MockUserListRepository
	err error
}

func (r *failingUserListRepo) FindByID(string) (*model.UserList, error) { return nil, r.err }

// **DB 障害を「そんなリストは無い」にしない** (#2792)。
//
// 公開リストの複製で障害を 400 にすると「リストが消えた」と読めてしまう。
func TestListsCreateFromPublic_DBFailureIsNot4xx(t *testing.T) {
	h, _ := newTestHandler(t)
	h.SetUserListRepo(&failingUserListRepo{
		MockUserListRepository: testutil.NewMockUserListRepository(),
		err:                    errors.New("dial tcp 127.0.0.1:5432: connect: connection refused"),
	})

	rec := postStub(h.ListsCreateFromPublic, `{"listId":"l1","name":"n"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code,
		"DB 障害が 4xx に化けている (#2792)")
}

// list を引く endpoint をまとめて固定する。ここが無いと guard を巻き戻しても
// CI が緑のまま通る (#2792)。
func TestUserLists_DBFailureIsNot4xx(t *testing.T) {
	me := &model.User{ID: "u1"}
	dbErr := errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")

	newFailing := func(t *testing.T) *Handler {
		t.Helper()
		h, _ := newTestHandler(t)
		h.SetUserListRepo(&failingUserListRepo{
			MockUserListRepository: testutil.NewMockUserListRepository(),
			err:                    dbErr,
		})
		// favorite / unfavorite は repo が未配線だと lookup 前に 204 で抜ける。
		h.SetUserListFavoriteRepo(testutil.NewMockUserListFavoriteRepository())
		return h
	}

	for _, tt := range []struct {
		name string
		run  func(*Handler) int
	}{
		{"users/lists/favorite", func(h *Handler) int {
			return postStub(h.ListsFavorite, `{"listId":"l1"}`, me).Code
		}},
		{"users/lists/unfavorite", func(h *Handler) int {
			return postStub(h.ListsUnfavorite, `{"listId":"l1"}`, me).Code
		}},
		{"users/lists/update", func(h *Handler) int {
			return postStub(h.ListsUpdate, `{"listId":"l1","name":"n"}`, me).Code
		}},
		{"users/lists/update-membership", func(h *Handler) int {
			return postStub(h.ListsUpdateMembership, `{"listId":"l1","userId":"u2"}`, me).Code
		}},
		{"users/lists/get-memberships", func(h *Handler) int {
			return postStub(h.ListsGetMemberships, `{"listId":"l1"}`, me).Code
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, http.StatusInternalServerError, tt.run(newFailing(t)),
				"DB 障害が 4xx に化けている (#2792)")
		})
	}
}
