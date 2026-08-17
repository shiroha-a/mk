package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/plugin"
)

// --- test doubles ---

type fakePeerResolver struct {
	host string
	pem  string
	err  error
}

func (r *fakePeerResolver) ResolveActor(string) (*model.User, error) {
	if r.err != nil {
		return nil, r.err
	}
	h := r.host
	return &model.User{ID: "u1", Host: &h}, nil
}

func (r *fakePeerResolver) PublicKeyForKeyID(string, string) (string, error) {
	return r.pem, r.err
}

type fakePeerBlocker struct {
	blocked map[string]bool
	// allowOnly, when non-empty, mimics federation=specified.
	allowOnly map[string]bool
}

func (b *fakePeerBlocker) IsBlocked(host string) bool { return b.blocked[host] }
func (b *fakePeerBlocker) IsAllowed(host string) bool {
	if len(b.allowOnly) == 0 {
		return true
	}
	return b.allowOnly[host]
}

type fakePeerLister struct {
	byHost map[string][]string
	err    error
}

func (l *fakePeerLister) Plugins(_ context.Context, host string) ([]string, error) {
	if l.err != nil {
		return nil, l.err
	}
	return l.byHost[host], nil
}

func testIDGenerator(t *testing.T) id.Generator {
	t.Helper()
	g, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	return g
}

func testPeer(t *testing.T, deps *pluginPeerDeps) *pluginPeer {
	t.Helper()
	if deps.idGen == nil {
		deps.idGen = testIDGenerator(t)
	}
	if deps.selfHost == "" {
		deps.selfHost = "self.example"
	}
	return &pluginPeer{name: "demo", peered: true, deps: deps, logger: testLogger()}
}

// --- Send のガード ---

// 宣言していないプラグインは経路そのものを使えない。
func TestPluginPeer_RequiresDeclaration(t *testing.T) {
	p := &pluginPeer{name: "demo", peered: false, deps: &pluginPeerDeps{}, logger: testLogger()}

	_, err := p.Send(context.Background(), "other.example", map[string]any{})
	assert.ErrorIs(t, err, errNotPeered)

	_, err = p.Has(context.Background(), "other.example")
	assert.ErrorIs(t, err, errNotPeered)
}

// **送る前に落とす。** ブロックしている相手には接続そのものをしない。
func TestPluginPeer_SendRefusesBlockedAndSelf(t *testing.T) {
	p := testPeer(t, &pluginPeerDeps{
		blocker: &fakePeerBlocker{blocked: map[string]bool{"bad.example": true}},
		remote:  &fakePeerLister{byHost: map[string][]string{"bad.example": {"demo"}, "ok.example": {"demo"}}},
	})

	_, err := p.Send(context.Background(), "bad.example", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ブロック")

	_, err = p.Send(context.Background(), "self.example", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "自分自身")

	_, err = p.Send(context.Background(), "", map[string]any{})
	assert.Error(t, err)
}

// federation=specified で許可していない相手にも送らない。
func TestPluginPeer_SendRespectsAllowlist(t *testing.T) {
	p := testPeer(t, &pluginPeerDeps{
		blocker: &fakePeerBlocker{allowOnly: map[string]bool{"ok.example": true}},
		remote:  &fakePeerLister{byHost: map[string][]string{"other.example": {"demo"}}},
	})

	_, err := p.Send(context.Background(), "other.example", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ブロック")
}

// 相手が同じプラグインを持たなければ送らない (無関係なインスタンスに
// こちらの都合でリクエストを飛ばさない)。
func TestPluginPeer_SendRequiresSamePlugin(t *testing.T) {
	p := testPeer(t, &pluginPeerDeps{
		remote: &fakePeerLister{byHost: map[string][]string{"other.example": {"another"}}},
	})

	_, err := p.Send(context.Background(), "other.example", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "持っていません")
}

func TestPluginPeer_Has(t *testing.T) {
	p := testPeer(t, &pluginPeerDeps{
		blocker: &fakePeerBlocker{blocked: map[string]bool{"bad.example": true}},
		remote: &fakePeerLister{byHost: map[string][]string{
			"ok.example":  {"demo", "other"},
			"bad.example": {"demo"},
		}},
	})
	ctx := context.Background()

	got, err := p.Has(ctx, "ok.example")
	require.NoError(t, err)
	assert.True(t, got)

	got, err = p.Has(ctx, "none.example")
	require.NoError(t, err)
	assert.False(t, got, "宣言していない相手")

	got, err = p.Has(ctx, "bad.example")
	require.NoError(t, err)
	assert.False(t, got, "ブロックしている相手は持っていても false")

	got, err = p.Has(ctx, "self.example")
	require.NoError(t, err)
	assert.False(t, got, "自分自身")
}

// ホストの表記ゆれを吸収する。URL で渡されても動くこと。
func TestNormalizePeerHost(t *testing.T) {
	for in, want := range map[string]string{
		"Example.COM":          "example.com",
		"https://example.com":  "example.com",
		"https://example.com/": "example.com",
		"  example.com  ":      "example.com",
		"example.com:3000":     "example.com:3000",
		"xn--r8jz45g.example":  "xn--r8jz45g.example", // IDN は punycode で来る
	} {
		assert.Equal(t, want, normalizePeerHost(in), in)
	}
}

// ホスト名の形をしていない値は空にする。
//
// **`@` が要点。** 素通しすると peerURL / nodeinfo の URL に連結されて userinfo
// として解釈され、宣言した宛先とは別のホストへ署名付きのリクエストが飛ぶ。
func TestNormalizePeerHost_RejectsNonHost(t *testing.T) {
	for _, in := range []string{
		"",
		"example.test@10.0.0.1",
		"example.test@evil.example",
		"example.test/../../evil",
		"example.test?x=1",
		"example.test#frag",
		"example.test/path",
		"example.test:80/x",
		"exa mple.test",
		"example.test:notaport",
		"-example.test",
		"example.test.",
		"[::1]",
		"日本語.example", // punycode 化されていない値は受けない
	} {
		assert.Equal(t, "", normalizePeerHost(in), in)
	}
}

// --- 受信 ---

func peerRequest(t *testing.T, body []byte, sign func(*http.Request)) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	// 絶対 URL で作る。署名は Host を対象に含むので、実際の送信と同じ形に
	// しないと検証できない。
	req := httptest.NewRequest(http.MethodPost, "https://self.example/api/plugin/demo/_peer",
		strings.NewReader(string(body)))
	req.Host = "self.example"
	if sign != nil {
		sign(req)
	}
	rec := httptest.NewRecorder()
	return echo.New().NewContext(req, rec), rec
}

// 署名が無ければ受け付けない。**名乗りは信じない。**
func TestPluginPeer_ServeRejectsUnsigned(t *testing.T) {
	p := testPeer(t, &pluginPeerDeps{keyCache: activitypub.NewPublicKeyCache(4)})
	p.Handle(func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("署名を検証せずにハンドラを呼んだ")
		return nil, nil
	})

	body, _ := json.Marshal(peerEnvelope{ID: "x", Payload: json.RawMessage(`{}`)})
	c, rec := peerRequest(t, body, nil)
	require.NoError(t, p.echoHandler()(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// 上限を超える本文は読み切らずに落とす。
func TestPluginPeer_ServeRejectsOversizedBody(t *testing.T) {
	p := testPeer(t, &pluginPeerDeps{keyCache: activitypub.NewPublicKeyCache(4)})
	p.Handle(func(context.Context, string, json.RawMessage) (any, error) { return nil, nil })

	c, rec := peerRequest(t, []byte(strings.Repeat("a", peerMaxBody+10)), nil)
	require.NoError(t, p.echoHandler()(c))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

// 署名が通っても、ハンドラのエラー文面は相手に返さない
// (プラグインの内部事情が他インスタンスに漏れる)。
func TestPluginPeer_ServeHidesHandlerError(t *testing.T) {
	key, pem := testPeerKeypair(t)
	p := testPeer(t, &pluginPeerDeps{
		keyCache: activitypub.NewPublicKeyCache(4),
		resolver: &fakePeerResolver{host: "sender.example", pem: pem},
	})
	p.Handle(func(context.Context, string, json.RawMessage) (any, error) {
		return nil, assertAnError{}
	})

	body, _ := json.Marshal(peerEnvelope{ID: "x", Payload: json.RawMessage(`{"a":1}`)})
	c, rec := peerRequest(t, body, func(r *http.Request) { signPeerRequest(t, r, key, body) })
	require.NoError(t, p.echoHandler()(c))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "内部の詳細")
}

// 署名が通れば、送信元は **鍵の持ち主** として渡る。
func TestPluginPeer_ServePassesVerifiedSender(t *testing.T) {
	key, pem := testPeerKeypair(t)
	p := testPeer(t, &pluginPeerDeps{
		keyCache: activitypub.NewPublicKeyCache(4),
		resolver: &fakePeerResolver{host: "sender.example", pem: pem},
	})

	var gotFrom string
	var gotPayload json.RawMessage
	p.Handle(func(_ context.Context, from string, payload json.RawMessage) (any, error) {
		gotFrom, gotPayload = from, payload
		return map[string]any{"ok": true}, nil
	})

	body, _ := json.Marshal(peerEnvelope{ID: "x", Payload: json.RawMessage(`{"a":1}`)})
	c, rec := peerRequest(t, body, func(r *http.Request) { signPeerRequest(t, r, key, body) })
	require.NoError(t, p.echoHandler()(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "sender.example", gotFrom)
	assert.JSONEq(t, `{"a":1}`, string(gotPayload))
	assert.JSONEq(t, `{"ok":true}`, rec.Body.String())
}

// ブロックしている相手からは受け取らない。
func TestPluginPeer_ServeRejectsBlockedSender(t *testing.T) {
	key, pem := testPeerKeypair(t)
	p := testPeer(t, &pluginPeerDeps{
		keyCache: activitypub.NewPublicKeyCache(4),
		resolver: &fakePeerResolver{host: "sender.example", pem: pem},
		blocker:  &fakePeerBlocker{blocked: map[string]bool{"sender.example": true}},
	})
	p.Handle(func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("ブロックした相手のハンドラを呼んだ")
		return nil, nil
	})

	body, _ := json.Marshal(peerEnvelope{ID: "x", Payload: json.RawMessage(`{}`)})
	c, rec := peerRequest(t, body, func(r *http.Request) { signPeerRequest(t, r, key, body) })
	require.NoError(t, p.echoHandler()(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// ハンドラ未登録は「こちらが受けられない」と伝える (相手のせいではない)。
func TestPluginPeer_ServeWithoutHandler(t *testing.T) {
	key, pem := testPeerKeypair(t)
	p := testPeer(t, &pluginPeerDeps{
		keyCache: activitypub.NewPublicKeyCache(4),
		resolver: &fakePeerResolver{host: "sender.example", pem: pem},
	})

	body, _ := json.Marshal(peerEnvelope{ID: "x", Payload: json.RawMessage(`{}`)})
	c, rec := peerRequest(t, body, func(r *http.Request) { signPeerRequest(t, r, key, body) })
	require.NoError(t, p.echoHandler()(c))
	assert.Equal(t, http.StatusNotImplemented, rec.Code)
}

// --- 予約パス ---

// プラグインに受け口を奪わせない。
func TestReservedPluginPath(t *testing.T) {
	assert.True(t, reservedPluginPath("/_peer"))
	assert.True(t, reservedPluginPath("/_anything"))
	assert.False(t, reservedPluginPath("/peer"))
	assert.False(t, reservedPluginPath("/me"))
}

func TestPluginRouter_RefusesReservedPath(t *testing.T) {
	r := &pluginRouter{group: echo.New().Group("/api/plugin/demo")}

	r.POST("/_peer", func(plugin.Request) (any, error) { return nil, nil })
	require.Error(t, r.err, "予約パスの登録は失敗させる")
	assert.Contains(t, r.err.Error(), "予約")

	r2 := &pluginRouter{group: echo.New().Group("/api/plugin/demo")}
	r2.GET("/_x", func(plugin.Request) (any, error) { return nil, nil })
	assert.Error(t, r2.err)
}

type assertAnError struct{}

func (assertAnError) Error() string { return "内部の詳細" }

// testLogger keeps test output quiet while still exercising the log paths.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testPeerKeypair returns a signing key and the matching public PEM.
func testPeerKeypair(t *testing.T) (*activitypub.PrivateKey, string) {
	t.Helper()
	privPEM, pubPEM, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://sender.example/users/u1#main-key", privPEM)
	require.NoError(t, err)
	return key, pubPEM
}

// signPeerRequest signs req the same way the sender does.
func signPeerRequest(t *testing.T, req *http.Request, key *activitypub.PrivateKey, body []byte) {
	t.Helper()
	digest := activitypub.SHA256Digest(body)
	require.NoError(t, activitypub.SignRequest(req, key, digest,
		[]string{"(request-target)", "date", "host", "digest"}))
}

// 送信先の URL を実際に確かめる。
//
// **`/api` を落とすと SPA catchall に落ちて 405 が返る。** 「相手が受け取らない」
// という形で出るので、送信側のログだけ見ても原因が分かりにくい。実際にこれで
// 相互に入れても何も出ない不具合を踏んだ。
func TestPluginPeer_DeliverURLAndRoundTrip(t *testing.T) {
	var gotPath, gotSig, gotDigest string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSig = r.Header.Get("Signature")
		gotDigest = r.Header.Get("Digest")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"score":7}`))
	}))
	defer srv.Close()

	key, _ := testPeerKeypair(t)
	p := testPeer(t, &pluginPeerDeps{
		client: srv.Client(),
		signer: &fakePeerSigner{key: key},
		// httptest は http なので、既定の https 決め打ちでは繋がらない。
		// **パスは実装のものをそのまま使う** — ここを差し替えると URL の
		// 検証にならない。
		urlFor: func(_, plugin string) string {
			return srv.URL + peerAPIPrefix + plugin + peerPath
		},
	})

	replies := make(chan json.RawMessage, 1)
	p.OnReply(func(_ context.Context, _, _ string, reply json.RawMessage) error {
		replies <- reply
		return nil
	})

	// deliver は Send のガードを通さず直接叩く (宛先の検査は別テスト)。
	envelope, err := json.Marshal(peerEnvelope{ID: "id1", Payload: json.RawMessage(`{"user":"alice"}`)})
	require.NoError(t, err)
	p.deliver("other.example", "id1", envelope)

	assert.Equal(t, "/api/plugin/demo/_peer", gotPath, "/api を含む正しいパスへ送る")
	assert.NotEmpty(t, gotSig, "署名を付ける")
	assert.NotEmpty(t, gotDigest, "Digest を付ける")
	assert.JSONEq(t, `{"id":"id1","payload":{"user":"alice"}}`, string(gotBody))

	select {
	case reply := <-replies:
		assert.JSONEq(t, `{"score":7}`, string(reply))
	default:
		t.Fatal("OnReply が呼ばれていない")
	}
}

type fakePeerSigner struct{ key *activitypub.PrivateKey }

func (s *fakePeerSigner) Signer() (*activitypub.PrivateKey, error) { return s.key, nil }
