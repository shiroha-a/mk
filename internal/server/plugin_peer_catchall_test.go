package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsPluginPeerPath(t *testing.T) {
	for _, p := range []string{
		"/api/plugin/demo/_peer",
		"/api/plugin/a/_peer",
	} {
		assert.Truef(t, isPluginPeerPath(p), "%q は peer の受け口", p)
	}
	for _, p := range []string{
		"",
		"/api/plugin//_peer",      // プラグイン名が空
		"/api/plugin/a/b/_peer",   // 名前は 1 セグメント
		"/api/plugin/demo/_peers", // 別のパス
		"/api/plugin/demo/status", // 通常のルート
		"/api/plugin/_peer",       // 名前が無い
		"/api/notes/create",       // プラグイン以外
		"/nodeinfo/2.1/_peer",     // /api の外
	} {
		assert.Falsef(t, isPluginPeerPath(p), "%q は peer の受け口ではない", p)
	}
}

// **受け口が無いことが相手に伝わること。** 200 + {} だと送信側から
// 「プラグインが空の応答を返した」と区別が付かず、OnReply が偽の応答で
// 呼ばれる (#2822)。
func TestAPICatchall_PeerPathIsNotFound(t *testing.T) {
	call := func(method, path string) (int, string) {
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(method, path, nil), rec)
		require.NoError(t, apiCatchall(c))
		return rec.Code, rec.Body.String()
	}

	code, body := call(http.MethodPost, "/api/plugin/demo/_peer")
	assert.Equal(t, http.StatusNotFound, code, "受け口の無いプラグインへの POST は 404")
	assert.Contains(t, body, "UNKNOWN_API_ENDPOINT")

	// **プラグインの通常のルートは 200 + {} のまま。** ここまで 404 にすると、
	// 設定で無効化したプラグインのフロントエンドが例外を受け取るようになる。
	code, body = call(http.MethodPost, "/api/plugin/demo/status")
	assert.Equal(t, http.StatusOK, code)
	assert.JSONEq(t, `{}`, body)

	// upstream 未実装エンドポイントの pass-through も従来どおり。
	code, _ = call(http.MethodPost, "/api/notes/create")
	assert.Equal(t, http.StatusOK, code)

	// GET は従来どおり 404。
	code, _ = call(http.MethodGet, "/api/notes/create")
	assert.Equal(t, http.StatusNotFound, code)
}
