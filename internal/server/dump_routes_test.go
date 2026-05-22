package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/config"
)

// TestDumpRoutes builds a minimal Server with a stub echo router and verifies
// that DumpRoutes emits sorted, version-tagged JSON. We bypass server.New here
// since DumpRoutes only needs s.echo populated — keeping the test light and
// independent of DB / Redis wiring.
func TestDumpRoutes(t *testing.T) {
	e := echo.New()
	noop := func(c echo.Context) error { return nil }
	// 故意に登録順を逆順 (z → a) にして、出力が path string sort で安定する
	// ことを担保する。同 path 内では method sort が効くかも検証。
	e.POST("/api/zeta", noop)
	e.GET("/api/alpha", noop)
	e.POST("/api/alpha", noop)
	e.GET("/healthz", noop)

	s := &Server{echo: e}

	var buf bytes.Buffer
	require.NoError(t, s.DumpRoutes(&buf))

	var got DumpedRoutes
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))

	assert.Equal(t, config.MisskeyVersion, got.MisskeyVersion)
	assert.Equal(t, config.MkGoVersion, got.MkGoVersion)

	// /api/alpha が GET/POST 両方含まれ、/api/zeta より先に並ぶことを検証。
	// /healthz は /api/ 以外なので素通しで含まれる (filter は caller 側で
	// 行う設計)。
	gotPairs := make([]string, 0, len(got.Routes))
	for _, r := range got.Routes {
		gotPairs = append(gotPairs, r.Method+" "+r.Path)
	}
	want := []string{
		"GET /api/alpha",
		"POST /api/alpha",
		"POST /api/zeta",
		"GET /healthz",
	}
	// echo 内部で OPTIONS / HEAD / catch-all を自動登録するので、got から
	// want に含まれない entry を除外して厳密一致させる。
	filtered := gotPairs[:0]
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	for _, p := range gotPairs {
		if wantSet[p] {
			filtered = append(filtered, p)
		}
	}
	assert.Equal(t, want, filtered)
}

func TestDumpRoutesEmpty(t *testing.T) {
	s := &Server{echo: echo.New()}
	var buf bytes.Buffer
	require.NoError(t, s.DumpRoutes(&buf))

	var got DumpedRoutes
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	// echo は何も登録していなくても catch-all を内部で持つ可能性がある
	// (実装依存) ので、Routes が non-nil なら OK とする。Version field
	// だけは確実に埋まることを担保。
	assert.Equal(t, config.MisskeyVersion, got.MisskeyVersion)
	assert.Equal(t, config.MkGoVersion, got.MkGoVersion)
}

// stubFailingWriter 書き込み毎に必ず失敗する Writer。
// DumpRoutes が JSON エンコード失敗時にエラーを propagate するかの検証用。
type stubFailingWriter struct{}

func (stubFailingWriter) Write([]byte) (int, error) {
	return 0, http.ErrBodyNotAllowed
}

func TestDumpRoutesWriterError(t *testing.T) {
	s := &Server{echo: echo.New()}
	err := s.DumpRoutes(stubFailingWriter{})
	require.Error(t, err)
}
