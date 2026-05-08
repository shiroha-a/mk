package federation

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
	client := &http.Client{
		Transport: redirectTransport{target: srv.URL},
		Timeout:   2 * time.Second,
	}
	f := NewRemoteStatsFetcher(client)

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

func TestRemoteStatsFetcher_Fetch_RemoteFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	client := &http.Client{Transport: redirectTransport{target: srv.URL}, Timeout: 2 * time.Second}
	f := NewRemoteStatsFetcher(client)

	stats := f.Fetch(context.Background(), "missing.example", "alice")
	assert.Nil(t, stats)
}

func TestRemoteStatsFetcher_Fetch_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>not json</html>`))
	}))
	defer srv.Close()
	client := &http.Client{Transport: redirectTransport{target: srv.URL}, Timeout: 2 * time.Second}
	f := NewRemoteStatsFetcher(client)
	assert.Nil(t, f.Fetch(context.Background(), "h", "u"))
}

func TestRemoteStatsFetcher_Fetch_PartialPayload(t *testing.T) {
	// followersCount だけ返ってくるケース。rest は 0 fallback で valid 扱い。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"followersCount":99}`))
	}))
	defer srv.Close()
	client := &http.Client{Transport: redirectTransport{target: srv.URL}, Timeout: 2 * time.Second}
	f := NewRemoteStatsFetcher(client)

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
