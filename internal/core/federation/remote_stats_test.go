package federation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoteStatsFetcher_Fetch_Success(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"notesCount":42,"followersCount":7,"followingCount":3}`))
	}))
	defer srv.Close()

	// httptest.Server は http://127.0.0.1:port を返す。fetchRemote は
	// https://<host>/... を組み立てるので、test では httptest server に直接
	// 向ける専用 client を渡してリクエストを intercept する。
	f := newRemoteStatsFetcherWithTransport(redirectTransport{target: srv.URL})

	stats := f.Fetch(context.Background(), "remote.example", "alice")
	require.NotNil(t, stats)
	assert.Equal(t, 42, stats.NotesCount)
	assert.Equal(t, 7, stats.FollowersCount)
	assert.Equal(t, 3, stats.FollowingCount)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))

	// 2 回目は cache から返るので hit 数が増えない。
	stats2 := f.Fetch(context.Background(), "remote.example", "alice")
	require.NotNil(t, stats2)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
}

func TestRemoteStatsFetcher_Fetch_NilOrEmpty(t *testing.T) {
	var f *RemoteStatsFetcher
	assert.Nil(t, f.Fetch(context.Background(), "h", "u"))

	f = NewRemoteStatsFetcher(nil)
	assert.Nil(t, f.Fetch(context.Background(), "", "u"))
	assert.Nil(t, f.Fetch(context.Background(), "h", ""))
}

func TestIsValidHost(t *testing.T) {
	// 正規 hostname / port 付きは accept。
	assert.True(t, isValidHost("example.com"))
	assert.True(t, isValidHost("example.com:8443"))
	assert.True(t, isValidHost("xn--example.com")) // IDN punycode
	assert.True(t, isValidHost("a.b.c.example.com"))

	// URL injection を試みる host は全 reject。
	assert.False(t, isValidHost("evil.com/path"))
	assert.False(t, isValidHost("evil.com?query=1"))
	assert.False(t, isValidHost("evil.com#frag"))
	assert.False(t, isValidHost("user@evil.com"))
	assert.False(t, isValidHost("evil.com /path")) // space
	assert.False(t, isValidHost(""))
}

func TestRemoteStatsFetcher_Fetch_RejectsMaliciousHost(t *testing.T) {
	// path injection を試みる host で fetch しない (= local network への
	// SSRF 試行を防ぐ)。
	f := newRemoteStatsFetcherWithTransport(http.DefaultTransport)
	assert.Nil(t, f.Fetch(context.Background(), "evil.com/internal", "alice"))
	assert.Nil(t, f.Fetch(context.Background(), "evil.com?x=1", "alice"))
}

func TestRemoteStatsFetcher_Fetch_NegativeCacheTTL(t *testing.T) {
	// 4xx の result は短い negative TTL で cache される (#943 review nit)。
	// この test は TTL 値そのものではなく「nil を cache に store する」path
	// が動くことを確認する。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	f := newRemoteStatsFetcherWithTransport(redirectTransport{target: srv.URL})

	// 1 回目で fetch → nil 返却 + negative cache 入る
	stats := f.Fetch(context.Background(), "h", "u")
	assert.Nil(t, stats)

	// 2 回目は cache hit して remote を再 hit しない (sync.Map 経由で確認)。
	if v, ok := f.cache.Load("h|u"); assert.True(t, ok, "negative cache should be populated") {
		entry := v.(cachedRemoteStats)
		assert.Nil(t, entry.stats)
		assert.Equal(t, remoteStatsNegativeTTL, entry.ttl, "negative TTL should be shorter than positive")
	}
}

func TestRemoteStatsFetcher_Fetch_RemoteFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	f := newRemoteStatsFetcherWithTransport(redirectTransport{target: srv.URL})

	stats := f.Fetch(context.Background(), "missing.example", "alice")
	assert.Nil(t, stats)
}

func TestRemoteStatsFetcher_Fetch_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>not json</html>`))
	}))
	defer srv.Close()
	f := newRemoteStatsFetcherWithTransport(redirectTransport{target: srv.URL})
	assert.Nil(t, f.Fetch(context.Background(), "h", "u"))
}

func TestRemoteStatsFetcher_Fetch_PartialPayload(t *testing.T) {
	// followersCount だけ返ってくるケース。rest は 0 fallback で valid 扱い。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"followersCount":99}`))
	}))
	defer srv.Close()
	f := newRemoteStatsFetcherWithTransport(redirectTransport{target: srv.URL})

	stats := f.Fetch(context.Background(), "h", "u")
	require.NotNil(t, stats)
	assert.Equal(t, 0, stats.NotesCount)
	assert.Equal(t, 99, stats.FollowersCount)
	assert.Equal(t, 0, stats.FollowingCount)
}

// redirectTransport は test 用の RoundTripper。https://anyhost/... を
// httptest server に向け直すことで、本番 code path (https URL を組む) を変更
// せずに test できるようにする。
type redirectTransport struct {
	target string
}

func (rt redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rewritten := req.Clone(req.Context())
	parsed, err := http.NewRequest(req.Method, rt.target+req.URL.Path, rewritten.Body)
	if err != nil {
		return nil, err
	}
	parsed.Header = rewritten.Header
	return http.DefaultTransport.RoundTrip(parsed)
}
