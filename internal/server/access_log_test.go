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
// `${uri}` は query を含むので、素で出すと `?i=<token>` の形で**有効な token**が
// ログファイルに残る。`${custom}` + `redact.URI` でそこだけ伏せているが、
// **この配線を固定するテストが長らく 1 つも無かった** — `LoggerWithConfig` は
// SA1019 (非推奨) で、移行先の `RequestLoggerWithConfig` には `CustomTagFunc` に
// 相当するものが無いため redact を書き直すことになる。そのとき黙って壊せる状態
// だったので、ここで形ではなく**出力そのもの**を見る。
//
// 変異検証: `accessLogConfig` の `Format` を `${custom}` から `${uri}` へ戻すと
// token がそのまま出て落ちる。`CustomTagFunc` を素の `RequestURI` にしても同じ。
func TestAccessLogConfig_RedactsToken(t *testing.T) {
	var buf bytes.Buffer
	cfg := accessLogConfig()
	cfg.Output = &buf

	e := echo.New()
	e.Use(echomw.LoggerWithConfig(cfg)) //nolint:staticcheck // SA1019: 本番と同じ API を検査するため (accessLogConfig の doc を参照)
	e.GET("/api/notes/timeline", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/notes/timeline?i=SECRET-TOKEN-VALUE&limit=10", nil)
	e.ServeHTTP(httptest.NewRecorder(), req)

	line := buf.String()
	assert.NotContains(t, line, "SECRET-TOKEN-VALUE",
		"token がアクセスログに残っている。`${custom}` + redact.URI の配線が外れた")
	assert.Contains(t, line, "limit=10",
		"秘密でないパラメータまで消している。redact が広すぎる")
	assert.Contains(t, line, "/api/notes/timeline", "パス自体は残すこと")
	assert.Contains(t, line, "GET", "メソッドを残すこと")
}
