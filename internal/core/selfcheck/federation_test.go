package selfcheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// healthyServer serves the shape a correctly configured instance returns.
func healthyServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var base string
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL

	mux.HandleFunc("/.well-known/webfinger", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/jrd+json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"subject": r.URL.Query().Get("resource"),
			"links": []map[string]string{
				{"rel": "self", "type": "application/activity+json", "href": base + "/users/sys"},
			},
		})
	})
	mux.HandleFunc("/.well-known/nodeinfo", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"links": []map[string]string{{"rel": "http://nodeinfo.diaspora.software/ns/schema/2.1", "href": base + "/nodeinfo/2.1"}},
		})
	})
	mux.HandleFunc("/nodeinfo/2.1", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"software": map[string]string{"name": "misskey", "version": "2026.7.0"},
		})
	})
	mux.HandleFunc("/users/"+instanceActorUsername, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/activity+json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":              base + "/users/sys",
			"publicKey":       map[string]string{"id": base + "/users/sys#main-key", "publicKeyPem": "-----BEGIN PUBLIC KEY-----"},
			"assertionMethod": []map[string]string{{"type": "Multikey"}},
		})
	})
	return srv
}

func TestChecker_HealthyInstance(t *testing.T) {
	srv := healthyServer(t)
	c := NewChecker(srv.URL)
	ctx := context.Background()

	assert.Equal(t, StatusOK, c.CheckWebFinger(ctx).Status)
	assert.Equal(t, StatusOK, c.CheckNodeInfo(ctx).Status)
	assert.Equal(t, StatusOK, c.CheckActor(ctx).Status)
	// httptest は http なので TLS は対象外。
	assert.Equal(t, StatusSkip, c.CheckTLS(ctx).Status)
}

// **リバースプロキシが `/.well-known/` を転送していない**構成。内部からは
// 正常に見えるので、外形検査でしか捕まらない典型。
func TestChecker_WebFingerFallsThroughToSPA(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><div id=\"splash\">"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got := NewChecker(srv.URL).CheckWebFinger(context.Background())
	assert.Equal(t, StatusFail, got.Status)
	assert.Contains(t, got.Detail, "JSON")
	assert.NotEmpty(t, got.Hint, "直し方を示さないと運用者は動けない")
}

// 404 は転送が届いている証拠でもある。instance.actor は遅延生成なので、
// 新規インスタンスでは未作成が正常。転送設定を疑わせない。
func TestChecker_WebFingerNotFoundIsWarn(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	got := NewChecker(srv.URL).CheckWebFinger(context.Background())
	assert.Equal(t, StatusWarn, got.Status)
	assert.Contains(t, got.Detail, "instance.actor")
	assert.NotContains(t, got.Hint, "リバースプロキシ", "転送設定を疑わせない")
}

// 500 のような他の異常はそのまま fail。
func TestChecker_WebFingerServerErrorFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	got := NewChecker(srv.URL).CheckWebFinger(context.Background())
	assert.Equal(t, StatusFail, got.Status)
	assert.Contains(t, got.Detail, "500")
}

// `url` のホスト名と実際に配信しているホスト名がずれている構成。
func TestChecker_WebFingerSubjectMismatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/webfinger", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"subject": "acct:instance.actor@other.example"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got := NewChecker(srv.URL).CheckWebFinger(context.Background())
	assert.Equal(t, StatusFail, got.Status)
	assert.Contains(t, got.Hint, "ホスト名")
}

func TestChecker_WebFingerWithoutSelfLink(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/webfinger", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"subject": r.URL.Query().Get("resource"),
			"links":   []map[string]string{{"rel": "http://webfinger.net/rel/profile-page", "type": "text/html"}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got := NewChecker(srv.URL).CheckWebFinger(context.Background())
	assert.Equal(t, StatusFail, got.Status)
	assert.Contains(t, got.Detail, "self link")
}

// 署名鍵が載っていない actor。連合先は投稿を受理できない。
func TestChecker_ActorWithoutPublicKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/users/"+instanceActorUsername, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/activity+json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "https://x.example/users/sys"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got := NewChecker(srv.URL).CheckActor(context.Background())
	assert.Equal(t, StatusFail, got.Status)
	assert.Contains(t, got.Detail, "publicKey")
}

// actor が SPA シェルに落ちている (Content-Type が HTML)。
func TestChecker_ActorServedAsHTML(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/users/"+instanceActorUsername, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html>"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got := NewChecker(srv.URL).CheckActor(context.Background())
	assert.Equal(t, StatusFail, got.Status)
	assert.Contains(t, got.Detail, "Content-Type")
}

// actor.id の host が config の url と違う。連合先は actor.id 側を正とする。
func TestChecker_ActorHostMismatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/users/"+instanceActorUsername, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/activity+json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":        "https://other.example/users/sys",
			"publicKey": map[string]string{"publicKeyPem": "-----BEGIN PUBLIC KEY-----"},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got := NewChecker(srv.URL).CheckActor(context.Background())
	assert.Equal(t, StatusFail, got.Status)
	assert.Contains(t, got.Detail, "other.example")
}

// Ed25519 が無くても RSA だけで連合はできる。fail にしない。
func TestChecker_ActorWithoutEd25519IsWarn(t *testing.T) {
	mux := http.NewServeMux()
	var base string
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL
	mux.HandleFunc("/users/"+instanceActorUsername, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/activity+json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":        base + "/users/sys",
			"publicKey": map[string]string{"publicKeyPem": "-----BEGIN PUBLIC KEY-----"},
		})
	})

	got := NewChecker(srv.URL).CheckActor(context.Background())
	assert.Equal(t, StatusWarn, got.Status)
}

func TestChecker_NodeInfoDiscoveryBroken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/nodeinfo", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"links": []any{}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got := NewChecker(srv.URL).CheckNodeInfo(context.Background())
	assert.Equal(t, StatusFail, got.Status)
}

// discovery は引けるが本体が 404。
func TestChecker_NodeInfoDocumentMissing(t *testing.T) {
	mux := http.NewServeMux()
	var base string
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL
	mux.HandleFunc("/.well-known/nodeinfo", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"links": []map[string]string{{"rel": "2.1", "href": base + "/nodeinfo/2.1"}},
		})
	})

	got := NewChecker(srv.URL).CheckNodeInfo(context.Background())
	assert.Equal(t, StatusFail, got.Status)
}

// 到達できないホスト。連合の全検査が fail になり、ヒントを持つ。
func TestChecker_UnreachableHost(t *testing.T) {
	c := NewChecker("http://127.0.0.1:1")
	ctx := context.Background()
	for _, got := range []Result{c.CheckWebFinger(ctx), c.CheckNodeInfo(ctx), c.CheckActor(ctx)} {
		assert.Equal(t, StatusFail, got.Status)
		assert.NotEmpty(t, got.Hint)
	}
}

func TestChecker_CheckConfig(t *testing.T) {
	cases := []struct {
		url    string
		want   Status
		detail string
	}{
		{"https://example.com", StatusOK, ""},
		{"http://example.com", StatusWarn, "http"},
		{"", StatusFail, "空"},
		{"ftp://example.com", StatusFail, "scheme"},
		{"https://", StatusFail, "host"},
		{"://bad", StatusFail, ""},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			got := NewChecker(tc.url).CheckConfig()
			assert.Equal(t, tc.want, got.Status)
			if tc.detail != "" {
				assert.Contains(t, got.Detail, tc.detail)
			}
			if got.Status != StatusOK {
				assert.NotEmpty(t, got.Hint)
			}
		})
	}
}

// url が壊れているときは連合検査を skip する (無意味な fail を並べない)。
func TestChecker_SkipsWhenURLInvalid(t *testing.T) {
	c := NewChecker("://bad")
	ctx := context.Background()
	assert.Equal(t, StatusSkip, c.CheckWebFinger(ctx).Status)
	assert.Equal(t, StatusSkip, c.CheckActor(ctx).Status)
	assert.Equal(t, StatusSkip, c.CheckTLS(ctx).Status)
}

// リダイレクトは追わない。`url` が最終的な公開 URL である前提が崩れていること
// 自体を伝えるべきなので、追って成功にしてしまうと問題が隠れる。
func TestChecker_DoesNotFollowRedirects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/webfinger", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example/wf", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got := NewChecker(srv.URL).CheckWebFinger(context.Background())
	require.Equal(t, StatusFail, got.Status)
	assert.Contains(t, got.Detail, "302")
}

// 403 は転送の問題ではなく連合そのものが無効の合図。実際に doctor を走らせて
// 見つけた分岐で、転送設定を疑うヒントを出すと運用者を誤誘導する。
func TestChecker_FederationDisabledGivesSpecificHint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := NewChecker(srv.URL)

	wf := c.CheckWebFinger(context.Background())
	require.Equal(t, StatusFail, wf.Status)
	assert.Contains(t, wf.Detail, "連合が無効")
	assert.Contains(t, wf.Hint, "federation")
	assert.NotContains(t, wf.Hint, "リバースプロキシ", "転送設定を疑わせない")

	ni := c.CheckNodeInfo(context.Background())
	require.Equal(t, StatusFail, ni.Status)
	assert.Contains(t, ni.Detail, "連合が無効")
}
