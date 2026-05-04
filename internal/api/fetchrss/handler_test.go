package fetchrss

import (
	"context"
	"encoding/json"
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

	"github.com/labstack/echo/v4"
	"github.com/mmcdole/gofeed"
	ext "github.com/mmcdole/gofeed/extensions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleRSS2 = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/">
<channel>
  <title>Example Feed</title>
  <link>https://example.com/</link>
  <description>An example feed</description>
  <image>
    <url>https://example.com/icon.png</url>
    <title>Example Icon</title>
  </image>
  <item>
    <title>First Post</title>
    <link>https://example.com/posts/1</link>
    <guid isPermaLink="false">post-1</guid>
    <pubDate>Fri, 01 May 2026 12:00:00 GMT</pubDate>
    <dc:creator>Alice</dc:creator>
    <category>tech</category>
    <category>golang</category>
    <description>&lt;p&gt;Hello   world&lt;/p&gt;</description>
    <enclosure url="https://example.com/audio.mp3" length="1234" type="audio/mpeg"/>
  </item>
</channel>
</rss>`

const sampleAtom = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom Sample</title>
  <link href="https://atom.example.com/"/>
  <updated>2026-05-01T12:00:00Z</updated>
  <id>urn:uuid:atom-feed</id>
  <entry>
    <title>Atom Entry</title>
    <link href="https://atom.example.com/e/1"/>
    <id>urn:uuid:atom-1</id>
    <updated>2026-05-01T12:00:00Z</updated>
    <author><name>Bob</name></author>
    <content type="html">&lt;p&gt;Atom &lt;b&gt;content&lt;/b&gt;&lt;/p&gt;</content>
  </entry>
</feed>`

// testUserAgent is the UA string used for handler-under-test wiring. Static
// so individual tests can assert on it without threading the value around.
const testUserAgent = "Misskey-Go/test (https://example.test)"

// startFeedServer launches an httptest server returning the supplied body and
// returns a Handler whose HTTP client targets that server. We bypass SSRF
// concerns at the transport layer because httptest binds to 127.0.0.1; the
// real wiring uses the SSRF-safe transport from server/outbound_http.go.
func newTestHandler(_ *testing.T, srv *httptest.Server) *Handler {
	return New(&http.Client{Timeout: FetchTimeout, Transport: srv.Client().Transport}, testUserAgent)
}

func newRequestCtx(method, target string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	return c, rec
}

func TestFetchRSS_BasicRSS2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.Header.Get("Accept"), "application/rss+xml")
		// Misskey-Go/<ver> 相当の UA が付くことを guard する。UA 必須の RSS
		// 配信サーバ (Cloudflare 含む) で 403 にならない契約の regression 検知。
		assert.Equal(t, testUserAgent, r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, sampleRSS2)
	}))
	t.Cleanup(srv.Close)

	h := newTestHandler(t, srv)
	c, rec := newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(srv.URL))
	require.NoError(t, h.Fetch(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, "Example Feed", resp["title"])
	assert.Equal(t, "https://example.com/", resp["link"])
	assert.Equal(t, "An example feed", resp["description"])

	img, ok := resp["image"].(map[string]any)
	require.True(t, ok, "image must be present")
	assert.Equal(t, "https://example.com/icon.png", img["url"])
	assert.Equal(t, "Example Icon", img["title"])

	items, ok := resp["items"].([]any)
	require.True(t, ok, "items must be array")
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	assert.Equal(t, "First Post", item["title"])
	assert.Equal(t, "https://example.com/posts/1", item["link"])
	assert.Equal(t, "post-1", item["guid"])
	assert.Equal(t, "Alice", item["creator"])
	assert.Equal(t, "Fri, 01 May 2026 12:00:00 GMT", item["pubDate"])

	// isoDate は RFC3339 UTC で生 pubDate と一致する時刻に正規化される
	iso, ok := item["isoDate"].(string)
	require.True(t, ok)
	parsed, err := time.Parse(time.RFC3339Nano, iso)
	require.NoError(t, err)
	assert.True(t, parsed.UTC().Equal(time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)))

	cats, _ := item["categories"].([]any)
	assert.Equal(t, []any{"tech", "golang"}, cats)

	encl, ok := item["enclosure"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://example.com/audio.mp3", encl["url"])
	assert.Equal(t, "audio/mpeg", encl["type"])
	// length は Misskey schema では number。json.Unmarshal は float64 になる
	assert.EqualValues(t, 1234, encl["length"])

	// contentSnippet は description の HTML タグ除去 + 連続 whitespace 縮約
	assert.Equal(t, "Hello world", item["contentSnippet"])

	assert.Equal(t, "public, max-age=180", rec.Header().Get("Cache-Control"))
}

func TestFetchRSS_Atom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		fmt.Fprint(w, sampleAtom)
	}))
	t.Cleanup(srv.Close)

	h := newTestHandler(t, srv)
	c, rec := newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(srv.URL))
	require.NoError(t, h.Fetch(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "Atom Sample", resp["title"])
	items := resp["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	assert.Equal(t, "Atom Entry", item["title"])
	assert.Equal(t, "Bob", item["creator"])
	assert.Equal(t, "Atom content", item["contentSnippet"])

	// Atom は published を持たないので isoDate は updated から派生する
	iso, ok := item["isoDate"].(string)
	require.True(t, ok)
	_, err := time.Parse(time.RFC3339Nano, iso)
	require.NoError(t, err)
}

func TestFetchRSS_POSTBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, sampleRSS2)
	}))
	t.Cleanup(srv.Close)

	h := newTestHandler(t, srv)
	body := fmt.Sprintf(`{"url":%q}`, srv.URL)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/fetch-rss", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Fetch(c))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestFetchRSS_MissingURL(t *testing.T) {
	h := New(&http.Client{Timeout: FetchTimeout}, testUserAgent)
	c, rec := newRequestCtx(http.MethodGet, "/api/fetch-rss")
	require.NoError(t, h.Fetch(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "INVALID_URL", errObj["code"])
	// 空 URL と「URL は http(s) のみ」は別 ID で frontend が分岐できる
	assert.Equal(t, "9c5ad7d3-6e15-4f3a-87b8-39ec2e91d5a3", errObj["id"])
}

func TestFetchRSS_InvalidScheme(t *testing.T) {
	h := New(&http.Client{Timeout: FetchTimeout}, testUserAgent)
	c, rec := newRequestCtx(http.MethodGet, "/api/fetch-rss?url=file:///etc/passwd")
	require.NoError(t, h.Fetch(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "INVALID_URL", errObj["code"])
	assert.Equal(t, "f5b2bd41-7c0a-4d49-b8c8-3d3a4d9b8e21", errObj["id"])
}

func TestFetchRSS_EmptyURLAfterTrim(t *testing.T) {
	h := New(&http.Client{Timeout: FetchTimeout}, testUserAgent)
	c, rec := newRequestCtx(http.MethodGet, "/api/fetch-rss?url=%20%20")
	require.NoError(t, h.Fetch(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFetchRSS_NoHostInURL(t *testing.T) {
	h := New(&http.Client{Timeout: FetchTimeout}, testUserAgent)
	c, rec := newRequestCtx(http.MethodGet, "/api/fetch-rss?url=http://")
	require.NoError(t, h.Fetch(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFetchRSS_UpstreamErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	h := newTestHandler(t, srv)
	c, rec := newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(srv.URL))
	require.NoError(t, h.Fetch(c))
	assert.Equal(t, http.StatusBadGateway, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "UPSTREAM_ERROR", errObj["code"])
}

func TestFetchRSS_UnparseableBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "not a valid rss or atom")
	}))
	t.Cleanup(srv.Close)

	h := newTestHandler(t, srv)
	c, rec := newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(srv.URL))
	require.NoError(t, h.Fetch(c))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestFetchRSS_OversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Cap is 1 MiB; send 2 MiB. ReadAllLimit returns ErrMaxBytesExceeded so
		// fetchFeed surfaces UPSTREAM_ERROR before we ever call gofeed.
		w.Header().Set("Content-Length", fmt.Sprintf("%d", 2<<20))
		w.Write(make([]byte, 2<<20))
	}))
	t.Cleanup(srv.Close)

	h := newTestHandler(t, srv)
	c, rec := newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(srv.URL))
	require.NoError(t, h.Fetch(c))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

// TestFetchRSS_DialFails covers the case where the outbound transport itself
// rejects the connection (which is how SSRF blocks surface in production).
func TestFetchRSS_DialFails(t *testing.T) {
	failingClient := &http.Client{
		Timeout: FetchTimeout,
		Transport: roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			return nil, errors.New("ssrf block: connection to private IP refused")
		}),
	}
	h := New(failingClient, testUserAgent)
	c, rec := newRequestCtx(http.MethodGet, "/api/fetch-rss?url=http://10.0.0.1/feed.xml")
	require.NoError(t, h.Fetch(c))
	assert.Equal(t, http.StatusBadGateway, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "UPSTREAM_ERROR", errObj["code"])
	// resolved IP / SSRF 文言が err.Error() 経由で漏れないこと: 静的メッセージ
	// に置き換わっており、敏感な substring を含まない。
	msg, _ := errObj["message"].(string)
	assert.Equal(t, "Failed to fetch feed.", msg)
	assert.NotContains(t, msg, "ssrf")
	assert.NotContains(t, msg, "10.0.0.1")
}

// TestFetchRSS_NoUserAgent covers the wiring path where userAgent="" — the
// handler must omit the header rather than send "User-Agent: ".
func TestFetchRSS_NoUserAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Go's http server normalizes a missing UA to "Go-http-client/1.1".
		// What we really want to assert is that *we* don't override it with
		// our caller-provided string when it's empty.
		assert.NotEqual(t, testUserAgent, r.Header.Get("User-Agent"))
		fmt.Fprint(w, sampleRSS2)
	}))
	t.Cleanup(srv.Close)

	h := New(&http.Client{Timeout: FetchTimeout, Transport: srv.Client().Transport}, "")
	c, rec := newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(srv.URL))
	require.NoError(t, h.Fetch(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// --- pack helper unit tests (carve out edge cases that don't surface from
// the full RSS / Atom samples) ---

// --- #683 server-side cache + singleflight tests ---

// TestFetchRSS_CacheHit: 同 URL の 2 回目以降は upstream に到達せず in-memory
// cache から bytes をそのまま返す。upstream hit カウントが 1 のまま増えない
// ことで cache 経路が機能している保証になる。
func TestFetchRSS_CacheHit(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, sampleRSS2)
	}))
	t.Cleanup(srv.Close)

	h := newTestHandler(t, srv)

	// 1 回目: miss → upstream fetch
	c1, rec1 := newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(srv.URL))
	require.NoError(t, h.Fetch(c1))
	require.Equal(t, http.StatusOK, rec1.Code)

	// 2 回目: hit → upstream は呼ばれないはず
	c2, rec2 := newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(srv.URL))
	require.NoError(t, h.Fetch(c2))
	require.Equal(t, http.StatusOK, rec2.Code)

	assert.EqualValues(t, 1, hits.Load(), "cache hit should not reach upstream")
	// hit / miss どちらも JSON body は同一であるべき
	assert.JSONEq(t, rec1.Body.String(), rec2.Body.String())
	// hit でも Cache-Control は同じ値
	assert.Equal(t, "public, max-age=180", rec2.Header().Get("Cache-Control"))
}

// TestFetchRSS_CacheExpiry: TTL を過ぎると cache が破棄され upstream に再アクセス
// する。SetClock で時計を進めるため Sleep 不要。
func TestFetchRSS_CacheExpiry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, sampleRSS2)
	}))
	t.Cleanup(srv.Close)

	h := newTestHandler(t, srv)
	now := time.Now()
	h.SetClock(func() time.Time { return now })
	h.SetCacheTTL(60 * time.Second)

	c1, _ := newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(srv.URL))
	require.NoError(t, h.Fetch(c1))

	// 時計を TTL ぶん超えて進める
	now = now.Add(61 * time.Second)

	c2, _ := newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(srv.URL))
	require.NoError(t, h.Fetch(c2))

	assert.EqualValues(t, 2, hits.Load(), "expired entry should trigger re-fetch")
}

// TestFetchRSS_Singleflight: 同 URL に対する concurrent miss は 1 fetch に
// 集約され、upstream へは 1 回だけ届くこと (thundering herd 防止)。
func TestFetchRSS_Singleflight(t *testing.T) {
	var hits atomic.Int32
	// upstream を意図的に少し遅延させて、N 個の caller が同時に singleflight
	// 待ちに入るウィンドウを作る。
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		<-gate // gate が close されるまでブロック
		fmt.Fprint(w, sampleRSS2)
	}))
	t.Cleanup(srv.Close)

	h := newTestHandler(t, srv)

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	results := make([]int, N)
	for i := range N {
		go func(idx int) {
			defer wg.Done()
			c, rec := newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(srv.URL))
			if err := h.Fetch(c); err != nil {
				t.Errorf("Fetch %d: %v", idx, err)
				return
			}
			results[idx] = rec.Code
		}(i)
	}

	// 1 件目の goroutine が upstream に到達するまで polling で待つ
	// (= sf.Do の closure 実行が始まったことの観測可能シグナル)。固定
	// time.Sleep だと slow CI で前段が走り切らず flaky になり得るため。
	waitForUpstreamHit(t, &hits, 1, 2*time.Second)
	// 残り 19 caller も sf.Do に到達するまでの猶予 (sf.Do 内部で coalesce
	// される時間)。closure 実行は既に始まっているので gate 解放前に到着すれば
	// 全員が同じ result を共有する。
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	for i, code := range results {
		assert.Equal(t, http.StatusOK, code, "request %d should be 200", i)
	}
	// 重要: upstream に届いた件数は 1 (singleflight が coalesce した)
	assert.EqualValues(t, 1, hits.Load(), "concurrent fetches must coalesce to one upstream call")
}

// waitForUpstreamHit polls until the atomic counter reaches `want` or the
// timeout elapses. fixed time.Sleep だと slow CI で flaky になりがちな
// 「upstream に届いた」の同期を polling で観測する。
func waitForUpstreamHit(t *testing.T, hits *atomic.Int32, want int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for hits.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("upstream hit did not reach %d within %v (got %d)", want, timeout, hits.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

// TestFetchRSS_Singleflight_CallerCancelDoesNotAbortPeers: caller A の context
// cancel が in-flight の upstream fetch を中断せず、coalesced peer B も成功する
// ことを保証する (Devin review #720 FLAG-1 regression guard)。
//
// 修正前: sf.Do 内 fetch が caller A の request ctx を共有していたため、A の
// disconnect で fetchFeed が ctx canceled error を返し B も 502 を受け取った。
// 修正後: closure は context.Background() + 5s timeout で fetch するので
// caller cancel から切り離される。
func TestFetchRSS_Singleflight_CallerCancelDoesNotAbortPeers(t *testing.T) {
	var hits atomic.Int32
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		<-gate
		fmt.Fprint(w, sampleRSS2)
	}))
	t.Cleanup(srv.Close)

	h := newTestHandler(t, srv)

	target := "/api/fetch-rss?url=" + url.QueryEscape(srv.URL)

	// Caller A: 途中で cancel する request context を持つ。
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	e := echo.New()
	reqA := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctxA)
	reqA.Header.Set("Content-Type", "application/json")
	recA := httptest.NewRecorder()
	cA := e.NewContext(reqA, recA)

	// Caller B: cancel しない。同 URL なので sf.Do で A に coalesce される。
	cB, recB := newRequestCtx(http.MethodGet, target)

	// 重要: caller A が先に sf.Do に入って closure を実行する状態を保証する
	// (= 後続 B が coalesce されるレースを再現可能にする)。
	// goroutine scheduling だけだと B が先に走ることもあるので、A の upstream
	// 到達 (hits == 1) を確認してから B を起動する。
	var wg sync.WaitGroup
	wg.Add(1)
	var errA error
	go func() { defer wg.Done(); errA = h.Fetch(cA) }()
	waitForUpstreamHit(t, &hits, 1, 2*time.Second)

	// この時点で A が closure 実行中 (fetchFeed で gate 待機)。B を後から起動。
	wg.Add(1)
	var errB error
	go func() { defer wg.Done(); errB = h.Fetch(cB) }()
	// B が sf.Do に到達して coalesce されるための grace。
	time.Sleep(50 * time.Millisecond)

	// ここで caller A をキャンセル。修正前なら fetchCtx に伝播して fetchFeed
	// が ctx canceled で abort、B も 502 を受け取る。
	cancelA()
	// 修正前のコードで cancel 検知が http.Client → fetchFeed → closure に
	// 伝播するのにかかる時間を確保する (regression guard 必須の wait)。
	// 修正後は fetchCtx が caller から切り離されているのでこの 20ms の意味は
	// 無いが、un-fixed code でテストが正しく failing する条件を作るには
	// この sleep が無いと close(gate) が cancel 伝播より先に走って false
	// negative になる。
	time.Sleep(20 * time.Millisecond)

	// gate を開けて upstream を返答させる。fetch が cancel されていなければ
	// body が読めて両 caller に同じ JSON が届く。
	close(gate)
	wg.Wait()

	require.NoError(t, errA)
	require.NoError(t, errB)
	// A (キャンセル側) も 200 で完走するはず: writeCachedJSON は ctx を
	// 観測しないので、キャンセルされていても response は body を書き出して
	// 完了する。「A の cancel が response 自体を壊さない」契約の guard。
	assert.Equal(t, http.StatusOK, recA.Code, "cancelled caller still completes the response")
	// B (キャンセルしていない) は 200 で完走しているはず。これが 502 になっていたら
	// caller cancel が peer に伝播してしまっている (regression)。
	assert.Equal(t, http.StatusOK, recB.Code, "peer caller must not be aborted by sibling cancel")
	assert.EqualValues(t, 1, hits.Load(), "upstream should be hit only once")
}

// TestFetchRSS_ErrorNotCached: upstream 5xx の場合は cache されず、次回呼び出し
// で再度 upstream に到達する (短期障害から復旧した feed が古い 502 で塞がれない)。
func TestFetchRSS_ErrorNotCached(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	h := newTestHandler(t, srv)

	for range 3 {
		c, rec := newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(srv.URL))
		require.NoError(t, h.Fetch(c))
		require.Equal(t, http.StatusBadGateway, rec.Code)
	}
	assert.EqualValues(t, 3, hits.Load(), "errors must not be cached")
}

// TestFetchRSS_CacheEvictionAtCapacity: cache 容量超過時に最古 entry を 1 件
// 落とす振る舞いを保証する。SetCacheMaxEntries(2) + 3 URL push で 1 件 evict。
func TestFetchRSS_CacheEvictionAtCapacity(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// クエリで feed 種別を分岐しない (どの URL でも同一 body) — テストは
		// hit カウントだけで evict を観測するため body 内容は重要ではない。
		_ = r
		fmt.Fprint(w, sampleRSS2)
	}))
	t.Cleanup(srv.Close)

	h := newTestHandler(t, srv)
	h.SetCacheMaxEntries(2)
	now := time.Now()
	h.SetClock(func() time.Time { return now })

	urlA := srv.URL + "/?feed=a"
	urlB := srv.URL + "/?feed=b"
	urlC := srv.URL + "/?feed=c"

	// A 投入 (cache: {A})
	c, _ := newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(urlA))
	require.NoError(t, h.Fetch(c))

	// B 投入 (cache: {A, B}). A の expiresAt より B の expiresAt は新しい
	now = now.Add(time.Second)
	c, _ = newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(urlB))
	require.NoError(t, h.Fetch(c))

	// C 投入 (容量 2 超 → 最古 expiresAt の A が evict される。cache: {B, C})
	now = now.Add(time.Second)
	c, _ = newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(urlC))
	require.NoError(t, h.Fetch(c))

	// ここまでで upstream は 3 回叩かれている
	assert.EqualValues(t, 3, hits.Load())

	// B は cache hit (upstream 不変)
	c, _ = newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(urlB))
	require.NoError(t, h.Fetch(c))
	assert.EqualValues(t, 3, hits.Load(), "B should hit cache")

	// A は evict 済み → re-fetch (upstream 4 回目)
	c, _ = newRequestCtx(http.MethodGet, "/api/fetch-rss?url="+url.QueryEscape(urlA))
	require.NoError(t, h.Fetch(c))
	assert.EqualValues(t, 4, hits.Load(), "A should have been evicted and re-fetched")
}

// TestFetchRSS_SetCacheTTL_RejectsNonPositive: SetCacheTTL は 0 / 負値を無視
// する (default を維持) ことを assert。設定ミスで cache が事実上無効化されない
// 安全策。
func TestFetchRSS_SetCacheTTL_RejectsNonPositive(t *testing.T) {
	h := New(&http.Client{Timeout: FetchTimeout}, testUserAgent)
	original := h.cacheTTL
	h.SetCacheTTL(0)
	assert.Equal(t, original, h.cacheTTL)
	h.SetCacheTTL(-1 * time.Second)
	assert.Equal(t, original, h.cacheTTL)
}

// TestFetchRSS_SetCacheMaxEntries_RejectsNonPositive: 同様に soft cap も
// 0 / 負値で上書きされない。
func TestFetchRSS_SetCacheMaxEntries_RejectsNonPositive(t *testing.T) {
	h := New(&http.Client{Timeout: FetchTimeout}, testUserAgent)
	original := h.cacheMaxLen
	h.SetCacheMaxEntries(0)
	assert.Equal(t, original, h.cacheMaxLen)
	h.SetCacheMaxEntries(-10)
	assert.Equal(t, original, h.cacheMaxLen)
}

// TestFetchRSS_SetClock_NilIgnored: nil clock を渡しても default time.Now が
// 維持されること。
func TestFetchRSS_SetClock_NilIgnored(t *testing.T) {
	h := New(&http.Client{Timeout: FetchTimeout}, testUserAgent)
	h.SetClock(nil)
	// 直接比較不可だが、cache 経路が時計を呼べることで indirect に保証する
	require.NotNil(t, h.now)
}

func TestPackImage_Nil(t *testing.T) {
	assert.Nil(t, packImage(nil))
}

func TestPackEnclosure_BadLength(t *testing.T) {
	// length が parse できない値の場合は length フィールドだけ省略し、url/type は残す
	out := packEnclosure([]*gofeed.Enclosure{{
		URL:    "https://example.com/audio.mp3",
		Type:   "audio/mpeg",
		Length: "not-a-number",
	}})
	require.NotNil(t, out)
	assert.Equal(t, "https://example.com/audio.mp3", out["url"])
	assert.Equal(t, "audio/mpeg", out["type"])
	_, hasLen := out["length"]
	assert.False(t, hasLen, "length must be omitted when not numeric")
}

func TestPackEnclosure_Empty(t *testing.T) {
	assert.Nil(t, packEnclosure(nil))
	assert.Nil(t, packEnclosure([]*gofeed.Enclosure{nil}))
	assert.Nil(t, packEnclosure([]*gofeed.Enclosure{{}}))
}

func TestPackImage_TitleOnly(t *testing.T) {
	// URL 空の Image (本来は schema 違反だが防御的に省略する)
	assert.Nil(t, packImage(&gofeed.Image{Title: "lonely"}))
}

func TestPackImage_NoTitle(t *testing.T) {
	out := packImage(&gofeed.Image{URL: "https://example.com/i.png"})
	require.NotNil(t, out)
	assert.Equal(t, "https://example.com/i.png", out["url"])
	_, hasTitle := out["title"]
	assert.False(t, hasTitle)
}

func TestPackITunes_Nil(t *testing.T) {
	assert.Nil(t, packITunes(&gofeed.Feed{}))
	// extension 全フィールド空 → 結果も nil
	assert.Nil(t, packITunes(&gofeed.Feed{ITunesExt: &ext.ITunesFeedExtension{}}))
}

func TestPackITunes_Full(t *testing.T) {
	feed := &gofeed.Feed{ITunesExt: &ext.ITunesFeedExtension{
		Image:    "https://example.com/cover.jpg",
		Author:   "Show Host",
		Summary:  "All about feeds",
		Explicit: "no",
		Keywords: "tech, golang , , rss",
		Owner: &ext.ITunesOwner{
			Name:  "Owner Name",
			Email: "owner@example.com",
		},
		Categories: []*ext.ITunesCategory{
			{Text: "Technology", Subcategory: &ext.ITunesCategory{Text: "Programming"}},
			{Text: "News"},
			nil,
			{Text: ""},
		},
	}}
	out := packITunes(feed)
	require.NotNil(t, out)
	assert.Equal(t, "https://example.com/cover.jpg", out["image"])
	assert.Equal(t, "Show Host", out["author"])
	assert.Equal(t, "All about feeds", out["summary"])
	assert.Equal(t, "no", out["explicit"])
	assert.Equal(t, []string{"tech", "golang", "rss"}, out["keywords"])
	assert.Equal(t, []string{"Technology", "Programming", "News"}, out["categories"])
	owner := out["owner"].(map[string]any)
	assert.Equal(t, "Owner Name", owner["name"])
	assert.Equal(t, "owner@example.com", owner["email"])
}

func TestPackITunes_KeywordsAllBlank(t *testing.T) {
	out := packITunes(&gofeed.Feed{ITunesExt: &ext.ITunesFeedExtension{
		Keywords: ", ,  ,",
	}})
	// blank-only keywords は categories と同じく省略する
	assert.Nil(t, out)
}

func TestPackITunes_OwnerNameOnly(t *testing.T) {
	out := packITunes(&gofeed.Feed{ITunesExt: &ext.ITunesFeedExtension{
		Owner: &ext.ITunesOwner{Name: "Solo"},
	}})
	require.NotNil(t, out)
	owner := out["owner"].(map[string]any)
	assert.Equal(t, "Solo", owner["name"])
	_, hasEmail := owner["email"]
	assert.False(t, hasEmail)
}

func TestItemCreator_Authors(t *testing.T) {
	got := itemCreator(&gofeed.Item{
		Authors: []*gofeed.Person{{Name: "Atom Author"}},
		Author:  &gofeed.Person{Name: "Deprecated"},
	})
	assert.Equal(t, "Atom Author", got)
}

func TestItemCreator_DeprecatedAuthor(t *testing.T) {
	got := itemCreator(&gofeed.Item{
		Author: &gofeed.Person{Name: "Old Author"},
	})
	assert.Equal(t, "Old Author", got)
}

func TestItemCreator_DublinCoreFallback(t *testing.T) {
	got := itemCreator(&gofeed.Item{
		DublinCoreExt: &ext.DublinCoreExtension{Creator: []string{"DC Creator"}},
	})
	assert.Equal(t, "DC Creator", got)
}

func TestItemCreator_Empty(t *testing.T) {
	assert.Equal(t, "", itemCreator(&gofeed.Item{}))
	// nil authors / empty arrays
	assert.Equal(t, "", itemCreator(&gofeed.Item{Authors: []*gofeed.Person{nil}}))
	assert.Equal(t, "", itemCreator(&gofeed.Item{Authors: []*gofeed.Person{{}}}))
}

func TestContentSnippet_Empty(t *testing.T) {
	assert.Equal(t, "", contentSnippet(&gofeed.Item{}))
}

func TestContentSnippet_DescriptionFallback(t *testing.T) {
	got := contentSnippet(&gofeed.Item{Description: "<b>Hello</b>"})
	assert.Equal(t, "Hello", got)
}

func TestPackItem_PublishedFallback(t *testing.T) {
	// PublishedParsed が無く UpdatedParsed しか無い場合 isoDate は updated 由来
	updated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	out := packItem(&gofeed.Item{
		Title:         "x",
		UpdatedParsed: &updated,
	})
	iso, ok := out["isoDate"].(string)
	require.True(t, ok)
	parsed, err := time.Parse(time.RFC3339Nano, iso)
	require.NoError(t, err)
	assert.True(t, parsed.UTC().Equal(updated))
}
