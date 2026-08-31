package meta

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

func callServerInfo(t *testing.T, h *Handler) map[string]any {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	require.NoError(t, h.ServerInfo(e.NewContext(req, rec)))
	require.Equal(t, http.StatusOK, rec.Code)

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	return got
}

func TestServerInfo(t *testing.T) {
	// 公開エンドポイントは machine / cpu / mem / fs のみ。os / node / psql /
	// redis / net は admin 専用で、ここに出ると未認証に環境情報が漏れる。
	adminOnly := []string{"os", "node", "psql", "redis", "net"}

	t.Run("stats enabled returns the public shape only", func(t *testing.T) {
		repo := testutil.NewMockMetaRepository()
		repo.Meta = &model.Meta{ID: "x", EnableServerMachineStats: true}
		got := callServerInfo(t, NewHandler(&config.Config{}, repo))

		assert.Contains(t, got, "machine")
		assert.Contains(t, got, "cpu")
		for _, k := range adminOnly {
			assert.NotContains(t, got, k)
		}
	})

	t.Run("stats disabled still returns the full shape", func(t *testing.T) {
		// **フィールドごと落とさない。** frontend の server-metric widget は
		// 各キーを直接読む。
		repo := testutil.NewMockMetaRepository()
		repo.Meta = &model.Meta{ID: "x", EnableServerMachineStats: false}
		got := callServerInfo(t, NewHandler(&config.Config{}, repo))

		assert.Contains(t, got, "machine")
		assert.Contains(t, got, "cpu")
		for _, k := range adminOnly {
			assert.NotContains(t, got, k)
		}
	})

	t.Run("meta fetch failure falls back to the empty shape", func(t *testing.T) {
		repo := testutil.NewMockMetaRepository() // Meta が nil = ErrNotFound
		got := callServerInfo(t, NewHandler(&config.Config{}, repo))

		assert.Contains(t, got, "machine")
	})

	t.Run("unwired meta repo does not panic", func(t *testing.T) {
		got := callServerInfo(t, NewHandler(&config.Config{}, nil))
		assert.Contains(t, got, "machine")
	})
}
