package i

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"github.com/shiroha-a/mk/internal/model"
)

// failingSKRepo makes every security key lookup look like a database failure.
type failingSKRepo struct {
	*inMemorySecurityKeyRepo
	err error
}

func (r *failingSKRepo) FindByID(string) (*model.UserSecurityKey, error) { return nil, r.err }

// **DB 障害を「そんなキーは無い」にしない** (#2792)。
//
// セキュリティキーの更新で障害を 400 にすると、利用者は「キーが消えた」と
// 判断して登録し直す。監視でも 5xx が立たない。
func TestUpdateKey_DBFailureIsNot4xx(t *testing.T) {
	h, userRepo := newExtraHandler(t)
	u := &model.User{ID: "u1", Username: "u1", UsernameLower: "u1",
		AvatarDecorations: datatypes.JSON([]byte("[]"))}
	require.NoError(t, userRepo.Create(u))
	h.SetWebAuthn(nil, &failingSKRepo{
		inMemorySecurityKeyRepo: newInMemSKRepo(),
		err:                     errors.New("dial tcp 127.0.0.1:5432: connect: connection refused"),
	})

	rec := postRegistryWithScope(h.TwoFAUpdateKey, `{"credentialId":"c1","name":"x"}`, u, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
