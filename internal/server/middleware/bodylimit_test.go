package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

// #2075: BodyLimitByPath は path 別に body size を cap する (/api 1MiB / inbox 64KiB /
// multipart 除外 / その他 無制限)。超過は 413。
func TestBodyLimitByPath(t *testing.T) {
	e := echo.New()
	e.Use(BodyLimitByPath())
	// body を読む handler。chunked 超過時に limitedReader の 413 を伝播する。
	h := func(c echo.Context) error {
		if _, err := io.ReadAll(c.Request().Body); err != nil {
			var he *echo.HTTPError
			if errors.As(err, &he) {
				return he
			}
			return c.NoContent(http.StatusBadRequest)
		}
		return c.NoContent(http.StatusOK)
	}
	e.POST("/api/meta", h)
	e.POST("/api/drive/files/create", h)
	e.POST("/inbox", h)
	e.POST("/users/:id/inbox", h)
	e.POST("/nolimit", h)

	post := func(path, ct string, n int, chunked bool) int {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(bytes.Repeat([]byte("a"), n)))
		if ct != "" {
			req.Header.Set(echo.HeaderContentType, ct)
		}
		if chunked {
			req.ContentLength = -1 // limitedReader 経路 (read 中の 413) を通す
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}

	const mb = 1024 * 1024
	const kb = 1024

	// /api → 1MiB (= 1048576)。境界: ちょうどは許容、+1 は 413。
	assert.Equal(t, http.StatusRequestEntityTooLarge, post("/api/meta", "application/json", mb+1, false), "/api > 1MiB は 413")
	assert.Equal(t, http.StatusOK, post("/api/meta", "application/json", mb, false), "/api ちょうど 1MiB は許容")
	assert.Equal(t, http.StatusOK, post("/api/meta", "application/json", 500*kb, false))
	// /api chunked 超過 → handler の read で 413 伝播。
	assert.Equal(t, http.StatusRequestEntityTooLarge, post("/api/meta", "application/json", mb+1, true), "/api chunked 超過も 413")

	// upload endpoint (drive/files/create) は除外 → 2MB multipart でも通過。
	assert.Equal(t, http.StatusOK, post("/api/drive/files/create", "multipart/form-data; boundary=x", 2*mb, false), "upload endpoint は除外")
	// HIGH-1 regression: 非 upload endpoint への multipart は 1MiB が効く (DoS bypass 防止)。
	assert.Equal(t, http.StatusRequestEntityTooLarge, post("/api/meta", "multipart/form-data; boundary=x", 2*mb, false), "非 upload endpoint の multipart は 1MiB cap")
	assert.Equal(t, http.StatusOK, post("/api/meta", "multipart/form-data; boundary=x", 500*kb, false), "非 upload endpoint の小 multipart は通過")

	// inbox → 64KiB (= 65536)。
	assert.Equal(t, http.StatusRequestEntityTooLarge, post("/inbox", "application/activity+json", 64*kb+1, false), "inbox > 64KiB は 413")
	assert.Equal(t, http.StatusOK, post("/inbox", "application/activity+json", 64*kb, false), "inbox ちょうど 64KiB は許容")
	assert.Equal(t, http.StatusRequestEntityTooLarge, post("/users/u1/inbox", "application/activity+json", 64*kb+1, false), "/users/:id/inbox も 64KiB")

	// 制限対象外 path は無制限。
	assert.Equal(t, http.StatusOK, post("/nolimit", "application/json", 2*mb, false), "対象外 path は無制限")
}
