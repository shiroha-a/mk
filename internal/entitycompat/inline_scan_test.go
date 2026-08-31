package entitycompat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scanInlineRoutes / countInlineHandlers 自身のテスト。
//
// **これが無いと guard が空振りする。** `TestErrorIDDrift` の inline guard は
// 「0 件なら PASS」の向きなので、抽出器が壊れて何も拾えなくなっても緑のまま
// 通る。実際、レシーバ判定を壊した状態で inline endpoint を足しても検出でき
// なかった (#2791 のレビューで発覚)。
func writeRouter(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "router.go")
	src := "package server\n\nfunc setup() {\n" + body + "\n}\n"
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))
	return path
}

func TestScanInlineRoutes(t *testing.T) {
	path := writeRouter(t, `
	api.POST("/inline", func(c echo.Context) error { return nil })
	api.POST("/method", h.Method)
	bound := func(c echo.Context) error { return nil }
	api.GET("/bound", bound)
`)
	got := scanInlineRoutes(t, path)

	// closure 直書きと ident 束縛は拾う。`h.Method` は parseRoutes 側の担当。
	assert.Contains(t, got, "inline")
	assert.Contains(t, got, "bound")
	assert.NotContains(t, got, "method")
	assert.Len(t, got, 2)
}

func TestCountInlineHandlers(t *testing.T) {
	t.Run("counts closures under the api group tree", func(t *testing.T) {
		path := writeRouter(t, `
	api := s.echo.Group("/api")
	api.POST("/direct", func(c echo.Context) error { return nil })
	promoGroup := api.Group("/promo")
	promoGroup.POST("/read", func(c echo.Context) error { return nil })
	nested := promoGroup.Group("/deep")
	nested.PUT("/x", func(c echo.Context) error { return nil })
`)
		// **group 経由を数えられることが要点。** `scanInlineRoutes` は endpoint
		// パスを復元するために `api` ident に限るので、ここが同じ制限だと
		// `api.Group(...)` へ移すだけで guard を素通りする。
		assert.Equal(t, []string{"/direct", "/read", "/x"}, countInlineHandlers(t, path))
	})

	t.Run("ignores routes outside the api group", func(t *testing.T) {
		// `/healthz` や frontend の catchall は error id gate の対象ではないし、
		// `internal/api` に移すものでもない。
		path := writeRouter(t, `
	api := s.echo.Group("/api")
	s.echo.GET("/healthz", func(c echo.Context) error { return nil })
	pprofGroup := s.echo.Group("/debug/pprof")
	pprofGroup.GET("/:name", func(c echo.Context) error { return nil })
	api.POST("/only-this", func(c echo.Context) error { return nil })
`)
		assert.Equal(t, []string{"/only-this"}, countInlineHandlers(t, path))
	})

	t.Run("ignores method references", func(t *testing.T) {
		path := writeRouter(t, `
	api := s.echo.Group("/api")
	api.POST("/a", usersHandler.List)
	api.GET("/b", metaHandler.ServerInfo)
`)
		assert.Empty(t, countInlineHandlers(t, path))
	})

	t.Run("follows ident-bound closures", func(t *testing.T) {
		path := writeRouter(t, `
	api := s.echo.Group("/api")
	onlineUsersHandler := func(c echo.Context) error { return nil }
	api.GET("/count", onlineUsersHandler)
	api.POST("/count", onlineUsersHandler)
`)
		// 両メソッド登録なので 2 件。
		assert.Equal(t, []string{"/count", "/count"}, countInlineHandlers(t, path))
	})
}
