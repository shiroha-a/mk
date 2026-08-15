package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLister(t *testing.T, handler http.HandlerFunc) (*nodeInfoPeerLister, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	l := newNodeInfoPeerLister(srv.Client())
	l.urlFor = func(string) string { return srv.URL + "/nodeinfo/2.1" }
	return l, &hits
}

func TestNodeInfoPeerLister_ReadsDeclaredPlugins(t *testing.T) {
	l, hits := testLister(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/nodeinfo/2.1", r.URL.Path)
		_, _ = w.Write([]byte(`{"metadata":{"mkGoPlugins":["demo","other"]}}`))
	})

	got, err := l.Plugins(context.Background(), "other.example")
	require.NoError(t, err)
	assert.Equal(t, []string{"demo", "other"}, got)

	// **毎回は引かない。** 相手に負荷をかけないよう答えを覚える。
	got, err = l.Plugins(context.Background(), "other.example")
	require.NoError(t, err)
	assert.Equal(t, []string{"demo", "other"}, got)
	assert.Equal(t, int32(1), hits.Load(), "2 回目はキャッシュから返す")
}

// mkGoPlugins を持たない相手 (Misskey TS など) は「持っていない」。
func TestNodeInfoPeerLister_NonMkGoInstance(t *testing.T) {
	l, _ := testLister(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"software":{"name":"misskey"},"metadata":{"nodeName":"x"}}`))
	})

	got, err := l.Plugins(context.Background(), "ts.example")
	require.NoError(t, err)
	assert.Empty(t, got)
}

// **失敗は覚えない。** 一時的な不調で「持っていない」と決めつけると、
// 相手が復帰しても TTL の間ずっと送れなくなる。
func TestNodeInfoPeerLister_DoesNotCacheFailures(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	l, hits := testLister(t, func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"metadata":{"mkGoPlugins":["demo"]}}`))
	})

	_, err := l.Plugins(context.Background(), "flaky.example")
	require.Error(t, err)

	fail.Store(false)
	got, err := l.Plugins(context.Background(), "flaky.example")
	require.NoError(t, err)
	assert.Equal(t, []string{"demo"}, got)
	assert.Equal(t, int32(2), hits.Load(), "失敗後は取り直す")
}

// 否定の答えは肯定より短く覚える (入れた直後に繋がらない時間を短くする)。
func TestNodeInfoPeerLister_NegativeTTLIsShorter(t *testing.T) {
	assert.Less(t, peerLookupNegativeTTL, peerLookupTTL)

	l, _ := testLister(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"metadata":{}}`))
	})
	_, err := l.Plugins(context.Background(), "none.example")
	require.NoError(t, err)

	l.mu.Lock()
	e := l.cache["none.example"]
	l.mu.Unlock()
	assert.WithinDuration(t, time.Now().Add(peerLookupNegativeTTL), e.expiresAt, time.Minute)
}
