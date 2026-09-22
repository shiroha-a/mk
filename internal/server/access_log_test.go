package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/stretchr/testify/assert"
)

// **アクセスログに credential を出さない。**
//
// request URI は query を含むので、素で出すと `?i=<token>` の形で**有効な token**が
// ログファイルに残る。`redact.URI` でそこだけ伏せている。**この配線を固定する
// テストが長らく 1 つも無く**、`LoggerWithConfig` (SA1019) から
// `RequestLoggerWithConfig` へ移すときに黙って壊せる状態だった。形ではなく
// **出力そのもの**を見るのはそのため。
//
// 変異検証: `LogValuesFunc` の `redact.URI(...)` を素の `c.Request().RequestURI`
// に戻すと token がそのまま出て落ちる。`redact.Placeholder` にすると今度は
// `limit=10` まで消えて落ちる (伏せすぎも検出する)。
func TestAccessLogConfig_RedactsToken(t *testing.T) {
	var buf bytes.Buffer

	e := echo.New()
	e.Use(echomw.RequestLoggerWithConfig(accessLogConfig(&buf)))
	e.GET("/api/notes/timeline", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/notes/timeline?i=SECRET-TOKEN-VALUE&limit=10", nil)
	e.ServeHTTP(httptest.NewRecorder(), req)

	line := buf.String()
	assert.NotContains(t, line, "SECRET-TOKEN-VALUE",
		"token がアクセスログに残っている。LogValuesFunc の redact.URI が外れた")
	assert.Contains(t, line, "limit=10",
		"秘密でないパラメータまで消している。redact が広すぎる")
	assert.Contains(t, line, "/api/notes/timeline", "パス自体は残すこと")
	assert.Contains(t, line, "GET", "メソッドを残すこと")
}
