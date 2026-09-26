package stream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shiroha-a/mk/internal/misc/credkey"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func closeFramesOf(f *fakeConn) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.closeFrames))
	copy(out, f.closeFrames)
	return out
}

// 閉じると決める前にキューへ載っていたものは送り切り、そのあと close frame を
// 1 度だけ送る。以後の Send は拒否する。
func TestConnection_CloseWithCode_FlushesQueueThenSendsCloseFrame(t *testing.T) {
	fc := newFakeConn()
	c := NewConnection("c1", &model.User{ID: "alice"}, fc)
	// Start 前に積んでおくと、writeLoop が起動した時点でキューと drain の合図が
	// 同時に揃う。どちらを先に select しても両方送られることを見る。
	require.NoError(t, c.Send(map[string]string{"type": "myTokenRegenerated"}))
	require.NoError(t, c.Send(map[string]string{"type": "second"}))

	c.CloseWithCode(RevokeCloseCode, "credential revoked")
	assert.Error(t, c.Send(map[string]string{"type": "late"}), "閉じると決めた後の Send は拒否する")

	done := make(chan struct{})
	go func() {
		c.Start()
		close(done)
	}()
	require.Eventually(t, fc.isClosed, 2*time.Second, 5*time.Millisecond)
	<-done

	require.Equal(t, 2, fc.writeCount())
	assert.Contains(t, string(fc.writeAt(0)), "myTokenRegenerated")
	assert.Contains(t, string(fc.writeAt(1)), "second")
	frames := closeFramesOf(fc)
	require.Len(t, frames, 1)
	assert.Equal(t, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "credential revoked"), frames[0])

	// 二重呼び出しは no-op。
	c.CloseWithCode(RevokeCloseCode, "again")
	assert.Len(t, closeFramesOf(fc), 1)
}

// writeLoop が動いていなくても (Start 前 / 書き込みで詰まっている) 必ず閉じる。
func TestConnection_CloseWithCode_FallsBackWhenWriterIsNotRunning(t *testing.T) {
	orig := closeFlushTimeout
	closeFlushTimeout = 20 * time.Millisecond
	t.Cleanup(func() { closeFlushTimeout = orig })

	fc := newFakeConn()
	closedByHandler := make(chan struct{})
	c := NewConnection("c1", nil, fc)
	c.SetCloseHandler(func() { close(closedByHandler) })
	c.CloseWithCode(RevokeCloseCode, "x")

	select {
	case <-closedByHandler:
	case <-time.After(2 * time.Second):
		t.Fatal("close handler was not invoked by the fallback timer")
	}
	assert.True(t, fc.isClosed())
}

// 既に閉じた接続には close frame を送らない。
func TestConnection_CloseWithCode_AfterCloseIsNoop(t *testing.T) {
	fc := newFakeConn()
	c := NewConnection("c1", nil, fc)
	c.Close()
	c.CloseWithCode(RevokeCloseCode, "x")
	assert.Empty(t, closeFramesOf(fc))
}

// 書き込みに失敗したら close frame を送らずに閉じる。
func TestConnection_CloseWithCode_WriteErrorStillCloses(t *testing.T) {
	fc := newFakeConn()
	fc.writeErr = errors.New("broken pipe")
	c := NewConnection("c1", nil, fc)
	require.NoError(t, c.Send(map[string]string{"type": "x"}))
	c.CloseWithCode(RevokeCloseCode, "x")
	go c.Start()
	require.Eventually(t, fc.isClosed, 2*time.Second, 5*time.Millisecond)
	assert.Empty(t, closeFramesOf(fc))
}

type revokeFixture struct {
	m     *Manager
	conns map[string]*fakeConn
}

// newRevokeFixture registers:
//   - a1: alice / native token A
//   - a2: alice / native token B (別端末で再ログインした後の新トークン)
//   - a3: alice / app token X
//   - b1: bob   / native token A と同じ文字列 (鍵が一致しても user が違えば閉じない)
//   - n1: 匿名
func newRevokeFixture(t *testing.T, bus PubSubBus) *revokeFixture {
	t.Helper()
	m := NewManager(NewRegistry(), bus)
	f := &revokeFixture{m: m, conns: map[string]*fakeConn{}}
	add := func(id string, user *model.User, cred string) {
		fc := newFakeConn()
		c := NewConnection(id, user, fc)
		c.SetCredential(cred)
		c.SetCloseHandler(func() { m.unregister(id) })
		m.register(c)
		go c.Start()
		f.conns[id] = fc
	}
	alice := &model.User{ID: "alice"}
	add("a1", alice, credkey.Native("token-A"))
	add("a2", alice, credkey.Native("token-B"))
	add("a3", alice, credkey.AccessToken("app-X"))
	add("b1", &model.User{ID: "bob"}, credkey.Native("token-A"))
	add("n1", nil, "")
	t.Cleanup(m.Shutdown)
	return f
}

func (f *revokeFixture) closed() map[string]bool {
	out := map[string]bool{}
	for id, fc := range f.conns {
		out[id] = fc.isClosed()
	}
	return out
}

func TestManager_RevokeStreams_NativeTokenClosesOnlyThatToken(t *testing.T) {
	f := newRevokeFixture(t, nil)
	assert.Equal(t, 1, f.m.RevokeStreams("alice", credkey.Native("token-A")))
	require.Eventually(t, func() bool { return f.conns["a1"].isClosed() }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, map[string]bool{"a1": true, "a2": false, "a3": false, "b1": false, "n1": false}, f.closed())
	require.Len(t, closeFramesOf(f.conns["a1"]), 1)
	assert.Equal(t, websocket.FormatCloseMessage(RevokeCloseCode, revokeCloseReason), closeFramesOf(f.conns["a1"])[0])
	require.Eventually(t, func() bool { return f.m.Count() == 4 }, 2*time.Second, 5*time.Millisecond)
}

func TestManager_RevokeStreams_AccessTokenClosesOnlyThatToken(t *testing.T) {
	f := newRevokeFixture(t, nil)
	assert.Equal(t, 1, f.m.RevokeStreams("alice", credkey.AccessToken("app-X")))
	require.Eventually(t, func() bool { return f.conns["a3"].isClosed() }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, map[string]bool{"a1": false, "a2": false, "a3": true, "b1": false, "n1": false}, f.closed())
}

func TestManager_RevokeStreams_UserClosesEveryConnectionOfThatUser(t *testing.T) {
	f := newRevokeFixture(t, nil)
	assert.Equal(t, 3, f.m.RevokeStreams("alice", ""))
	require.Eventually(t, func() bool {
		c := f.closed()
		return c["a1"] && c["a2"] && c["a3"]
	}, 2*time.Second, 5*time.Millisecond)
	c := f.closed()
	assert.False(t, c["b1"], "他人の接続は閉じない")
	assert.False(t, c["n1"], "匿名接続は閉じない")
}

func TestManager_RevokeStreams_EmptyUserIsNoop(t *testing.T) {
	f := newRevokeFixture(t, nil)
	assert.Equal(t, 0, f.m.RevokeStreams("", credkey.Native("token-A")))
	assert.Equal(t, 0, f.m.RevokeStreams("", ""))
	assert.Equal(t, 5, f.m.Count())
}

// 他プロセスで処理された失効も pubsub 経由で届き、猶予の後に閉じる。
func TestManager_SubscribeStreamRevoke_ClosesAfterSettleDelay(t *testing.T) {
	bus := newStubBus()
	f := newRevokeFixture(t, bus)
	f.m.SetRevokeSettleDelay(30 * time.Millisecond)
	f.m.SubscribeStreamRevoke()
	require.Contains(t, bus.subs, StreamRevokeTopic)

	payload, err := json.Marshal(StreamRevokePayload{UserID: "alice", Credential: credkey.Native("token-A")})
	require.NoError(t, err)
	bus.deliver(StreamRevokeTopic, payload)
	// 猶予の間はまだ開いている (先に publish された event がキューに載るのを待つ)。
	assert.False(t, f.conns["a1"].isClosed())
	require.Eventually(t, func() bool { return f.conns["a1"].isClosed() }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, map[string]bool{"a1": true, "a2": false, "a3": false, "b1": false, "n1": false}, f.closed())

	// 壊れた payload / userId 欠落は無視する (全員を閉じる意味に倒さない)。
	bus.deliver(StreamRevokeTopic, []byte("{"))
	bus.deliver(StreamRevokeTopic, []byte(`{"credential":""}`))
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, map[string]bool{"a1": true, "a2": false, "a3": false, "b1": false, "n1": false}, f.closed())

	f.m.UnsubscribeStreamRevoke()
	assert.NotContains(t, bus.subs, StreamRevokeTopic)
}

func TestManager_StreamRevoke_NilBusIsNoop(t *testing.T) {
	m := NewManager(nil, nil)
	m.SubscribeStreamRevoke()
	m.UnsubscribeStreamRevoke()
}

func TestManager_ShutdownUnsubscribesStreamRevoke(t *testing.T) {
	bus := newStubBus()
	m := NewManager(NewRegistry(), bus)
	m.SubscribeStreamRevoke()
	require.Contains(t, bus.subs, StreamRevokeTopic)
	m.Shutdown()
	assert.NotContains(t, bus.subs, StreamRevokeTopic)
}

func TestManager_SettleDelay(t *testing.T) {
	m := NewManager(nil, nil)
	assert.Equal(t, defaultRevokeSettle, m.settleDelay())
	m.SetRevokeSettleDelay(5 * time.Millisecond)
	assert.Equal(t, 5*time.Millisecond, m.settleDelay())
	m.SetRevokeSettleDelay(0)
	assert.Equal(t, defaultRevokeSettle, m.settleDelay())
}

type revokeRecorder struct {
	mu       sync.Mutex
	topics   []string
	payloads []StreamRevokePayload
	err      error
}

func (r *revokeRecorder) Publish(_ context.Context, channel string, payload any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, ok := payload.(json.RawMessage)
	if !ok {
		return errors.New("unexpected payload type")
	}
	var p StreamRevokePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	r.topics = append(r.topics, channel)
	r.payloads = append(r.payloads, p)
	return r.err
}

func TestStreamRevokePublisher(t *testing.T) {
	rec := &revokeRecorder{}
	p := NewStreamRevokePublisher(rec)
	p.RevokeUserStreams("alice")
	p.RevokeNativeTokenStreams("alice", "token-A")
	p.RevokeAccessTokenStreams("alice", "app-X")
	// 空の資格情報で送ると「全接続を閉じる」意味になるので、送らない。
	p.RevokeNativeTokenStreams("alice", "")
	p.RevokeAccessTokenStreams("alice", "")
	p.RevokeUserStreams("")

	assert.Equal(t, []string{StreamRevokeTopic, StreamRevokeTopic, StreamRevokeTopic}, rec.topics)
	assert.Equal(t, []StreamRevokePayload{
		{UserID: "alice"},
		{UserID: "alice", Credential: credkey.Native("token-A")},
		{UserID: "alice", Credential: credkey.AccessToken("app-X")},
	}, rec.payloads)

	// publish 失敗は握って落ちない (失効そのものは DB と tokenCache で済んでいる)。
	rec.err = errors.New("redis down")
	p.RevokeUserStreams("alice")

	var nilPub *StreamRevokePublisher
	nilPub.RevokeUserStreams("alice")
	NewStreamRevokePublisher(nil).RevokeUserStreams("alice")
}

// 実 WebSocket で、失効した接続のクライアントが 1008 の close frame を受け取る
// こと。Accept が credential を Connection に載せていないと何も閉じない。
func TestManager_AcceptThenRevoke_ClientReceivesPolicyViolation(t *testing.T) {
	m := NewManager(NewRegistry(), nil)
	t.Cleanup(m.Shutdown)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go m.Accept(conn, &model.User{ID: "alice"}, nil, credkey.Native(r.URL.Query().Get("i")))
	}))
	defer srv.Close()

	dial := func(token string) *websocket.Conn {
		dialer := websocket.Dialer{HandshakeTimeout: time.Second}
		conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/?i="+token, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	revoked := dial("old")
	kept := dial("new")
	require.Eventually(t, func() bool { return m.Count() == 2 }, 2*time.Second, 5*time.Millisecond)

	assert.Equal(t, 1, m.RevokeStreams("alice", credkey.Native("old")))

	require.NoError(t, revoked.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, _, err := revoked.ReadMessage()
	var ce *websocket.CloseError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, websocket.ClosePolicyViolation, ce.Code)
	assert.Equal(t, revokeCloseReason, ce.Text)

	require.Eventually(t, func() bool { return m.Count() == 1 }, 2*time.Second, 5*time.Millisecond)
	require.NoError(t, kept.SetReadDeadline(time.Now().Add(100*time.Millisecond)))
	_, _, err = kept.ReadMessage()
	var ne interface{ Timeout() bool }
	require.ErrorAs(t, err, &ne, "新しいトークンの接続は開いたまま")
	assert.True(t, ne.Timeout())
}
