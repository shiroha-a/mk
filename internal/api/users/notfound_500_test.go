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
