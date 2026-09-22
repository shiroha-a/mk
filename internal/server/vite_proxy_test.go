package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// **Vite dev server へのプロキシが落としやすい 3 点を固定する。**
//
// `httputil.ReverseProxy` を `Director` から `Rewrite` へ移したときに、どれも
// 黙って壊れる — (a) `SetURL` を忘れると宛先が解決されない、(b) `SetURL` は
// `Out.Host` を書き換えないので、代入しないと dev server が別 origin として
// 扱う、(c) **`Rewrite` は X-Forwarded-* を落としてから呼ばれる**ので、
// `Director` 版で `ServeHTTP` が自動で足していた `X-Forwarded-For` が消える。
//
// いずれもコンパイルは通るうえ、開発時にしか通らない経路なので気付きにくい。
func TestNewViteProxy_ForwardsWithRemoteHost(t *testing.T) {
	var gotHost, gotPath, gotXFF string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotPath = r.URL.Path
		gotXFF = r.Header.Get("X-Forwarded-For")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	remote, err := url.Parse(backend.URL)
	require.NoError(t, err)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/vite/main.ts", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	rec := httptest.NewRecorder()

	require.NoError(t, newViteProxy(backend.URL)(e.NewContext(req, rec)))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String(), "dev server の応答をそのまま返すこと")
	assert.Equal(t, remote.Host, gotHost, "dev server には remote の Host を送ること")
	assert.Equal(t, "/vite/main.ts", gotPath, "パスはそのまま転送すること")
	assert.Equal(t, "203.0.113.9", gotXFF, "X-Forwarded-For を落とさないこと")
}
