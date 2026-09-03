package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/activitypub"
)

func TestPeerRateLimiter_BurstThenRefill(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := newPeerRateLimiter()
	l.now = func() time.Time { return now }

	for i := 0; i < int(peerRateBurst); i++ {
		require.True(t, l.allow("host:a.example"), "burst %d 回目までは通る", i+1)
	}
	assert.False(t, l.allow("host:a.example"), "burst を使い切ったら落とす")
	assert.True(t, l.allow("host:b.example"), "別の key は独立している")

	// 1 秒で rate 分だけ戻る。
	now = now.Add(time.Second)
	for i := 0; i < int(peerRateLimit); i++ {
		require.True(t, l.allow("host:a.example"), "補充 %d 回目", i+1)
	}
	assert.False(t, l.allow("host:a.example"), "補充分を超えたら落とす")

	// 十分待てば burst まで戻るが、それ以上は貯まらない。
	now = now.Add(time.Hour)
	for i := 0; i < int(peerRateBurst); i++ {
		require.True(t, l.allow("host:a.example"))
	}
	assert.False(t, l.allow("host:a.example"), "burst を超えて貯まらない")
}

// key は相手のホストと IP なので外から無限に増やせる。**制限側が資源を食う
// 形にしない。**
func TestPeerRateLimiter_BoundsKeys(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := newPeerRateLimiter()
	l.now = func() time.Time { return now }
	l.max = 8

	for i := 0; i < 200; i++ {
		// 使い切った bucket にする (満タンなら捨ててよい、では消えない状態)。
		key := fmt.Sprintf("host:%d.example", i)
		for j := 0; j < int(peerRateBurst); j++ {
			l.allow(key)
		}
		require.LessOrEqual(t, len(l.buckets), l.max, "key 数が上限を超えない (i=%d)", i)
	}
}

// nil レシーバは許可する (テストが組み立てる deps は limiter を持たない)。
func TestPeerRateLimiter_NilAllows(t *testing.T) {
	var l *peerRateLimiter
	assert.True(t, l.allow("host:a.example"))
	assert.True(t, newPeerRateLimiter().allow(""), "空の key は数えない")
}

// 受け口は **署名検証の前に** IP で落とす。検証は actor 解決と公開鍵検証を
// 伴うので、ここを通すと署名を持たない相手が高い処理を無制限に起こせる。
func TestPluginPeer_ServeRateLimitsBeforeVerify(t *testing.T) {
	limiter := newPeerRateLimiter()
	limiter.burst = 1
	limiter.rate = 0

	p := testPeer(t, &pluginPeerDeps{
		keyCache: activitypub.NewPublicKeyCache(4),
		limiter:  limiter,
	})
	p.Handle(func(context.Context, string, json.RawMessage) (any, error) { return nil, nil })

	// 1 回目は署名が無いので 401 まで進む。
	c, rec := peerRequest(t, []byte(`{"id":"1","payload":{}}`), nil)
	require.NoError(t, p.echoHandler()(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// 2 回目は枠が無いので、署名検証まで行かずに 429。
	c, rec = peerRequest(t, []byte(`{"id":"2","payload":{}}`), nil)
	require.NoError(t, p.echoHandler()(c))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "60", rec.Header().Get("Retry-After"))
}

// IP を通しても、**確定したホストごとの枠**で落とす。IP は前段のプロキシや
// 同居インスタンスで共有されうるので、認証後の枠を別に持つ。
//
// **IP を変えて確かめる。** 同じ IP から 2 回投げると ip: の枠で落ちてしまい、
// host: の枠を見ていなくてもテストが通る (実測で 200 のまま緑になった)。
func TestPluginPeer_ServeRateLimitsVerifiedHost(t *testing.T) {
	limiter := newPeerRateLimiter()
	limiter.burst = 1
	limiter.rate = 0

	key, pem := testPeerKeypair(t)
	p := testPeer(t, &pluginPeerDeps{
		keyCache: activitypub.NewPublicKeyCache(4),
		resolver: &fakePeerResolver{host: "sender.example", pem: pem},
		limiter:  limiter,
	})
	calls := 0
	p.Handle(func(context.Context, string, json.RawMessage) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})

	serve := func(ip string) int {
		body, _ := json.Marshal(peerEnvelope{ID: "x", Payload: json.RawMessage(`{"a":1}`)})
		req := httptest.NewRequest(http.MethodPost, "https://self.example/api/plugin/demo/_peer",
			strings.NewReader(string(body)))
		req.Host = "self.example"
		req.RemoteAddr = ip + ":1234"
		signPeerRequest(t, req, key, body)
		rec := httptest.NewRecorder()
		require.NoError(t, p.echoHandler()(echo.New().NewContext(req, rec)))
		return rec.Code
	}

	assert.Equal(t, http.StatusOK, serve("192.0.2.1"))
	// 別の IP なので ip: の枠は空いているが、host: の枠が無い。
	assert.Equal(t, http.StatusTooManyRequests, serve("192.0.2.2"))
	assert.Equal(t, 1, calls, "落とした分はプラグインのハンドラまで届かない")
}
