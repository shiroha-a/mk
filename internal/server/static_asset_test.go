package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServeStaticAssetDir_SetsCacheControlHeader verifies that
// serveStaticAssetDir attaches the long-lived Cache-Control header used
// by /twemoji and /fluent-emoji routes. Regression guard for the upstream
// Misskey TS `ms('30 days')` cache policy.
func TestServeStaticAssetDir_SetsCacheControlHeader(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1f4dd.png"), []byte("fake-png"), 0o644))

	e := echo.New()
	serveStaticAssetDir(e, "/fluent-emoji", dir)

	req := httptest.NewRequest(http.MethodGet, "/fluent-emoji/1f4dd.png", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "public, max-age=2592000, immutable", rec.Header().Get("Cache-Control"),
		"long-lived static asset route must set 30-day immutable Cache-Control")
	assert.Equal(t, "fake-png", rec.Body.String())
}

// TestServeStaticAssetDir_NonExistentFile returns 404 without panic when
// the requested asset does not exist (Echo's c.File contract).
func TestServeStaticAssetDir_NonExistentFile(t *testing.T) {
	dir := t.TempDir()
	e := echo.New()
	serveStaticAssetDir(e, "/fluent-emoji", dir)

	req := httptest.NewRequest(http.MethodGet, "/fluent-emoji/missing.png", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestServeStaticAssetDir_PathTraversalBlocked verifies the filepath.Clean
// guard rejects "../" traversal attempts (matches Echo's own Static safety).
func TestServeStaticAssetDir_PathTraversalBlocked(t *testing.T) {
	dir := t.TempDir()
	// 親ディレクトリにファイルを置いて、`../parent.txt` で抜けられたら検知。
	parent := filepath.Dir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(parent, "parent.txt"), []byte("secret"), 0o644))
	defer os.Remove(filepath.Join(parent, "parent.txt"))

	e := echo.New()
	serveStaticAssetDir(e, "/fluent-emoji", dir)

	req := httptest.NewRequest(http.MethodGet, "/fluent-emoji/..%2Fparent.txt", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// filepath.Clean("/" + "../parent.txt") = "/parent.txt" → join with dir
	// → dir/parent.txt (= 存在しない) → 404。secret 内容は絶対に返らない。
	assert.NotEqual(t, http.StatusOK, rec.Code, "path traversal must not succeed")
	assert.NotContains(t, rec.Body.String(), "secret")
}
