package server

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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

// 恒久的な失敗 (受け口が無い / 署名が通らない / ブロック) は再送しない。
//
// **#2822 で 404 を返すようにした結果、再送すると送信量が 1 → 4 に増える。**
// この層は「持っていない相手に接続しない」を設計原則にしているので、
// 送り直しても同じ答えになるものは 1 回で止める。
func TestPluginPeer_DeliverDoesNotRetryPermanentFailures(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		wantPosts     int32
		wantForgotten bool
	}{
		{name: "404 stops immediately and drops the cache", status: http.StatusNotFound, wantPosts: 1, wantForgotten: true},
		{name: "401 stops immediately", status: http.StatusUnauthorized, wantPosts: 1},
		{name: "403 stops immediately", status: http.StatusForbidden, wantPosts: 1},
		{name: "413 stops immediately", status: http.StatusRequestEntityTooLarge, wantPosts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var posts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				posts.Add(1)
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			key, _ := testPeerKeypair(t)
			lister := &fakePeerLister{byHost: map[string][]string{"other.example": {"demo"}}}
			p := testPeer(t, &pluginPeerDeps{
				client: srv.Client(),
				signer: &fakePeerSigner{key: key},
				remote: lister,
				urlFor: func(_, plugin string) string { return srv.URL + peerAPIPrefix + plugin + peerPath },
			})

			// **待ち時間を潰しておく。** 実装が壊れて再送に回ったとき、
			// 既定の 2 秒 / 10 秒 / 60 秒で待つとテストが落ちる前に固まる。
			p.retryDelays = []time.Duration{0, 0, 0}

			p.deliver("other.example", "id1", []byte(`{"id":"id1","payload":1}`))

			assert.Equal(t, tt.wantPosts, posts.Load(), "再送しない")
			assert.Equal(t, tt.wantForgotten, lister.forgotten["other.example"] > 0, "nodeinfo キャッシュの破棄")
		})
	}
}

// 一時的な失敗 (429 / 5xx) は従来どおり再送する。
func TestPluginPeer_DeliverRetriesTransientFailures(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var posts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				posts.Add(1)
				w.WriteHeader(status)
			}))
			defer srv.Close()

			key, _ := testPeerKeypair(t)
			p := testPeer(t, &pluginPeerDeps{
				client: srv.Client(),
				signer: &fakePeerSigner{key: key},
				remote: &fakePeerLister{},
				urlFor: func(_, plugin string) string { return srv.URL + peerAPIPrefix + plugin + peerPath },
			})
			// 待ち時間を潰す。**package 変数は触らない** (差し替えると別の
			// テストが起こした goroutine と競合する)。
			p.retryDelays = []time.Duration{0, 0, 0}

			p.deliver("other.example", "id1", []byte(`{"id":"id1","payload":1}`))
			assert.Equal(t, int32(len(p.retryDelays)+1), posts.Load(), "初回 + 再送の回数")
			assert.Len(t, peerRetryDelays, 3, "本番の既定は 3 段")
		})
	}
}
