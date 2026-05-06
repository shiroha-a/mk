package activitypub

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/safehttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestWebFingerClient(t *testing.T, handler http.HandlerFunc) (*WebFingerClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewWebFingerClient(srv.Client(), "test-agent")
	c.SetEndpointOverride(func(_ string) string { return srv.URL + "/.well-known/webfinger" })
	return c, srv
}

func TestWebFingerClient_LookupActorURI_Success(t *testing.T) {
	c, srv := newTestWebFingerClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/.well-known/webfinger", r.URL.Path)
		resource := r.URL.Query().Get("resource")
		assert.Equal(t, "acct:alice@example.com", resource)
		assert.Contains(t, r.Header.Get("Accept"), "application/jrd+json")
		assert.Equal(t, "test-agent", r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/jrd+json")
		fmt.Fprint(w, `{
			"subject": "acct:alice@example.com",
			"links": [
				{"rel": "http://webfinger.net/rel/profile-page", "type": "text/html", "href": "https://example.com/@alice"},
				{"rel": "self", "type": "application/activity+json", "href": "https://example.com/users/alice"}
			]
		}`)
	})
	_ = srv
	uri, err := c.LookupActorURI("alice", "example.com")
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/users/alice", uri)
}

func TestWebFingerClient_LookupActorURI_LDJSONTypeAccepted(t *testing.T) {
	c, _ := newTestWebFingerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{
			"subject": "acct:alice@example.com",
			"links": [
				{"rel": "self", "type": "application/ld+json; profile=\"https://www.w3.org/ns/activitystreams\"", "href": "https://example.com/users/alice"}
			]
		}`)
	})
	uri, err := c.LookupActorURI("alice", "example.com")
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/users/alice", uri)
}

func TestWebFingerClient_LookupActorURI_NoSelfLink(t *testing.T) {
	c, _ := newTestWebFingerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{
			"subject": "acct:alice@example.com",
			"links": [
				{"rel": "http://webfinger.net/rel/profile-page", "type": "text/html", "href": "https://example.com/@alice"}
			]
		}`)
	})
	_, err := c.LookupActorURI("alice", "example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no self link")
}

func TestWebFingerClient_LookupActorURI_SelfLinkWithUnrelatedType(t *testing.T) {
	c, _ := newTestWebFingerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{
			"subject": "acct:alice@example.com",
			"links": [
				{"rel": "self", "type": "text/html", "href": "https://example.com/@alice"}
			]
		}`)
	})
	_, err := c.LookupActorURI("alice", "example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no self link")
}

func TestWebFingerClient_LookupActorURI_SelfLinkEmptyHref(t *testing.T) {
	c, _ := newTestWebFingerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{
			"subject": "acct:alice@example.com",
			"links": [
				{"rel": "self", "type": "application/activity+json"}
			]
		}`)
	})
	_, err := c.LookupActorURI("alice", "example.com")
	require.Error(t, err)
}

func TestWebFingerClient_LookupActorURI_ResponseTooLarge(t *testing.T) {
	// #323: size cap を超えるレスポンスは ErrResponseTooLarge を伝搬する。
	oversized := make([]byte, int(MaxBodyBytes)+10)
	c, _ := newTestWebFingerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(oversized)
	})
	_, err := c.LookupActorURI("alice", "example.com")
	require.Error(t, err)
	assert.True(t, errors.Is(err, safehttp.ErrResponseTooLarge))
}

func TestWebFingerClient_LookupActorURI_InvalidJSON(t *testing.T) {
	c, _ := newTestWebFingerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `not json`)
	})
	_, err := c.LookupActorURI("alice", "example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse response")
}

func TestWebFingerClient_LookupActorURI_Non2xx(t *testing.T) {
	c, _ := newTestWebFingerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.LookupActorURI("ghost", "example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 404")
}

func TestWebFingerClient_LookupActorURI_NetworkError(t *testing.T) {
	c := NewWebFingerClient(http.DefaultClient, "ua")
	c.SetEndpointOverride(func(_ string) string { return "http://127.0.0.1:1/.well-known/webfinger" })
	_, err := c.LookupActorURI("a", "example.com")
	require.Error(t, err)
}

func TestWebFingerClient_LookupActorURI_EmptyArgs(t *testing.T) {
	c := NewWebFingerClient(nil, "ua")
	_, err := c.LookupActorURI("", "example.com")
	require.Error(t, err)
	_, err = c.LookupActorURI("alice", "")
	require.Error(t, err)
}

func TestWebFingerClient_DefaultEndpoint(t *testing.T) {
	// SetEndpointOverride を使わないデフォルト経路の確認。
	// 実ネットワークには行かないが、URL が https://<host>/.well-known/webfinger で
	// 組まれていることを確認する。
	c := NewWebFingerClient(nil, "ua")
	got := c.endpoint("example.com")
	assert.Equal(t, "https://example.com/.well-known/webfinger", got)
	assert.True(t, strings.HasPrefix(got, "https://"))
}

func TestWebFingerClient_ResourceIsURLEncoded(t *testing.T) {
	c, _ := newTestWebFingerClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.RawQuery
		// query parser decodes the value for us, but the raw must contain
		// a percent-encoded "@" as part of a correct resource query.
		decoded, _ := url.ParseQuery(raw)
		assert.Equal(t, "acct:edge@ex.example", decoded.Get("resource"))
		fmt.Fprint(w, `{
			"subject": "acct:edge@ex.example",
			"links": [{"rel": "self", "type": "application/activity+json", "href": "https://ex.example/users/edge"}]
		}`)
	})
	uri, err := c.LookupActorURI("edge", "ex.example")
	require.NoError(t, err)
	assert.Equal(t, "https://ex.example/users/edge", uri)
}

// 同一 acct への並行 lookup は 1 つの HTTP リクエストに collapse される
// (#300 3-7)。サーバ側で in-flight な状態を作って 16 並行呼び出しが全部
// 同じ結果を受け取り、HTTP fetch 数が並行数より大幅に少ないことを担保する。
//
// flake 抑制 (#789): 旧実装は close(gate) 前に追従 goroutine が
// singleflight.Do に到達するのを sleep 無しで期待していたため、CI の
// 高負荷スケジューリング下では leader が gate 解放前に完了して 2 回目以降
// の wave が新たに leader になり HTTP fetch が 3 まで届くケースが稀に
// あった。本テストでは:
//  1. 全 goroutine が c.LookupActorURI を呼び出した瞬間に counter を進める
//  2. counter == N まで wait してから少し息を入れて singleflight 登録の
//     内部処理が完了するのを待つ
//  3. 閾値も「並行数の 1/4 以下」と寛容に取る (= 16 並行なら 4 fetch まで
//     OK)。dedupe が機能していれば余裕で満たせる、scheduling 由来の race
//     では破綻しない値。
func TestWebFingerClient_LookupActorURI_DedupesConcurrentCalls(t *testing.T) {
	var calls atomic.Int64
	gate := make(chan struct{})
	c, _ := newTestWebFingerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		<-gate // すべての呼び出しが singleflight 集約待ちになるまで blockする
		fmt.Fprint(w, `{
			"subject": "acct:alice@example.com",
			"links": [{"rel": "self", "type": "application/activity+json", "href": "https://example.com/users/alice"}]
		}`)
	})

	const N = 16
	var wg sync.WaitGroup
	var entered atomic.Int64
	results := make([]string, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			entered.Add(1)
			results[i], errs[i] = c.LookupActorURI("alice", "example.com")
		}(i)
	}
	// 全 goroutine が LookupActorURI 呼び出し直前まで来たことを確認する
	// (= singleflight.Do に enqueue されるまでもう一息)。spin-wait で
	// 待つが timeout を切って test がハングしないようにする。
	deadline := time.Now().Add(2 * time.Second)
	for entered.Load() < int64(N) {
		if time.Now().After(deadline) {
			t.Fatalf("only %d/%d goroutines reached LookupActorURI", entered.Load(), N)
		}
		time.Sleep(time.Millisecond)
	}
	// LookupActorURI に入った直後と singleflight.Do の内部 mutex 取得には
	// わずかなずれがある。leader が handler 内 (gate 待ち) に到達して、後続
	// が singleflight 集約に乗るまでの時間を確実に与える。
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()

	for i := 0; i < N; i++ {
		require.NoError(t, errs[i], "call %d", i)
		assert.Equal(t, "https://example.com/users/alice", results[i])
	}
	// 16 並行が 1-2 fetch に collapse されるのを期待するが、scheduling 次第
	// で leader 完了 → 後続が新 leader になる二段ウェーブで 3-4 まで届く。
	// dedupe が完全に壊れた regression は呼び出し数 == N で容易に検出できる
	// ので、閾値は N/4 (= 4) に取って scheduling 起因の flake を吸収する。
	assert.LessOrEqual(t, calls.Load(), int64(N/4),
		"singleflight must collapse concurrent same-acct lookups; got %d HTTP fetches for %d callers", calls.Load(), N)
}

func TestIsActivityPubLinkType(t *testing.T) {
	cases := map[string]bool{
		"application/activity+json": true,
		"application/ld+json":       true,
		"application/ld+json; profile=\"https://www.w3.org/ns/activitystreams\"": true,
		"application/jrd+json": false,
		"text/html":            false,
		"":                     false,
	}
	for input, want := range cases {
		assert.Equal(t, want, isActivityPubLinkType(input), input)
	}
}
