package signup_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	apisignup "github.com/shiroha-a/mk/internal/api/signup"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

// handlerWithLookups is the subset of *apisignup.Handler this file drives.
type handlerWithLookups = *apisignup.Handler

func TestUsernameAvailable(t *testing.T) {
	setup := func(t *testing.T) (h handlerWithLookups, users *testutil.MockUserRepository, used *testutil.MockUsedUsernameRepository, meta *testutil.MockMetaRepository) {
		t.Helper()
		hh, users, meta := newTestHandler(t)
		used = testutil.NewMockUsedUsernameRepository()
		hh.SetUsernameLookups(users, used)
		return hh, users, used, meta
	}

	available := func(t *testing.T, h handlerWithLookups, body string) bool {
		t.Helper()
		rec := doPost(h.UsernameAvailable, body)
		require.Equal(t, http.StatusOK, rec.Code)
		var got map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		v, ok := got["available"].(bool)
		require.True(t, ok, "available が bool でない: %s", rec.Body.String())
		return v
	}

	t.Run("a fresh username is available", func(t *testing.T) {
		h, _, _, _ := setup(t)
		assert.True(t, available(t, h, `{"username":"alice"}`))
	})

	t.Run("an existing user makes it unavailable", func(t *testing.T) {
		h, users, _, _ := setup(t)
		users.Users["u1"] = &model.User{
			ID: "u1", Username: "alice", UsernameLower: "alice",
			AvatarDecorations: datatypes.JSON([]byte("[]")),
		}
		assert.False(t, available(t, h, `{"username":"Alice"}`))
	})

	t.Run("a released username stays unavailable", func(t *testing.T) {
		// used_usernames を見ないと、退会した利用者の名前を他人が取って
		// 過去の投稿の宛先に化ける (#2080)。
		h, _, used, _ := setup(t)
		used.Usernames["alice"] = true
		assert.False(t, available(t, h, `{"username":"alice"}`))
	})

	t.Run("a preserved username is unavailable", func(t *testing.T) {
		h, _, _, meta := setup(t)
		meta.Meta = &model.Meta{ID: "x", PreservedUsernames: model.StringArray{"admin"}}
		assert.False(t, available(t, h, `{"username":"admin"}`))
	})

	t.Run("an invalid format is a 400", func(t *testing.T) {
		h, _, _, _ := setup(t)
		rec := doPost(h.UsernameAvailable, `{"username":"has space"}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("a malformed body is a 400", func(t *testing.T) {
		h, _, _, _ := setup(t)
		rec := doPost(h.UsernameAvailable, `{`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("unwired lookups report unavailable, not available", func(t *testing.T) {
		// **空振りして true を返さない。** 実際には取れない名前を案内するうえ、
		// 既存アカウントの username を空いていると見せることになる。
		h, _, _ := newTestHandler(t)
		assert.False(t, available(t, h, `{"username":"alice"}`))
	})
}
