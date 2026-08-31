package avatardecorations

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

var testDB *gorm.DB

func init() {
	testDB = testutil.MustOpenTestDB()
	testutil.ApplyMigrations(testDB)
}

func call(t *testing.T, h *Handler) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	require.NoError(t, h.Get(e.NewContext(req, rec)))
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	return got
}

func seed(t *testing.T, roleIDs ...string) {
	t.Helper()
	require.NoError(t, testDB.Exec(`DELETE FROM "avatar_decoration"`).Error)
	cat := "hats"
	require.NoError(t, testDB.Create(&model.AvatarDecoration{
		ID: "dec1", Name: "hat", URL: "https://example.test/hat.png",
		Description: "a hat", RoleIDs: model.StringArray(roleIDs), Category: &cat,
	}).Error)
}

func TestGet(t *testing.T) {
	t.Run("returns the catalog with every field", func(t *testing.T) {
		seed(t)
		got := decode(t, call(t, NewHandler(testDB, nil)))

		require.Len(t, got, 1)
		for _, k := range []string{
			"id", "name", "description", "url",
			"roleIdsThatCanBeUsedThisDecoration", "category",
		} {
			assert.Contains(t, got[0], k)
		}
		assert.Equal(t, "hats", got[0]["category"])
	})

	t.Run("drops role ids that no longer exist", func(t *testing.T) {
		// 削除済みロールの ID が残ると、管理画面が存在しないロールを表示する
		// (#1543)。
		seed(t, "roleA", "gone")
		h := NewHandler(testDB, func() (map[string]bool, error) {
			return map[string]bool{"roleA": true}, nil
		})

		got := decode(t, call(t, h))
		assert.Equal(t, []any{"roleA"}, got[0]["roleIdsThatCanBeUsedThisDecoration"])
	})

	t.Run("role lookup failure keeps the ids verbatim", func(t *testing.T) {
		// **空集合として扱わない。** 全 roleId が落ちて、ロール限定の
		// デコレーションが誰にも使えなくなる。古い ID が残るほうが害が小さい。
		seed(t, "roleA", "gone")
		h := NewHandler(testDB, func() (map[string]bool, error) {
			return nil, errors.New("boom")
		})

		got := decode(t, call(t, h))
		assert.Equal(t, []any{"roleA", "gone"}, got[0]["roleIdsThatCanBeUsedThisDecoration"])
	})

	t.Run("unwired role provider keeps the ids verbatim", func(t *testing.T) {
		seed(t, "roleA", "gone")

		got := decode(t, call(t, NewHandler(testDB, nil)))
		assert.Equal(t, []any{"roleA", "gone"}, got[0]["roleIdsThatCanBeUsedThisDecoration"])
	})

	t.Run("no rows returns an empty array, not null", func(t *testing.T) {
		require.NoError(t, testDB.Exec(`DELETE FROM "avatar_decoration"`).Error)

		rec := call(t, NewHandler(testDB, nil))
		assert.JSONEq(t, `[]`, rec.Body.String())
	})

	t.Run("unwired db returns an empty array", func(t *testing.T) {
		rec := call(t, NewHandler(nil, nil))
		assert.JSONEq(t, `[]`, rec.Body.String())
	})
}
