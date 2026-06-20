package stream

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatID(t *testing.T) {
	assert.Equal(t, "0", formatID(0))
	assert.Equal(t, "1", formatID(1))
	assert.Equal(t, "f", formatID(15))
	assert.Equal(t, "10", formatID(16))
	assert.Equal(t, "ff", formatID(255))
}

func TestManager_AllocateIDIncrements(t *testing.T) {
	m := NewManager(nil, nil)
	a := m.allocateID()
	b := m.allocateID()
	assert.NotEqual(t, a, b)
}

func TestManager_RegisterUnregister(t *testing.T) {
	m := NewManager(nil, nil)
	c := NewConnection("c1", nil, newFakeConn())
	m.register(c)
	assert.Equal(t, 1, m.Count())
	assert.Equal(t, c, m.Get("c1"))
	m.unregister("c1")
	assert.Equal(t, 0, m.Count())
	assert.Nil(t, m.Get("c1"))
}

func TestManager_ShutdownClosesEverything(t *testing.T) {
	m := NewManager(nil, nil)
	fc := newFakeConn()
	c := NewConnection("c1", nil, fc)
	m.register(c)
	m.Shutdown()
	assert.Equal(t, 0, m.Count())
	assert.True(t, fc.isClosed())
}

func TestManager_AcceptRegistersAndUnregistersOnClose(t *testing.T) {
	// Accept は *websocket.Conn を要求するが、connection_test の fakeConn を
	// 直接渡すために register/unregister の経路だけテストする。
	m := NewManager(nil, nil)
	fc := newFakeConn()
	id := m.allocateID()
	c := NewConnection(id, nil, fc)
	c.SetCloseHandler(func() { m.unregister(id) })
	m.register(c)
	assert.Equal(t, 1, m.Count())

	c.Close()
	assert.Equal(t, 0, m.Count())
}

// TestManager_AcceptViaHTTPTestServer exercises the Accept path with a real
// *websocket.Conn pair. Accept は内部で readLoop をブロックするので別 goroutine
// で起動し、close 後に登録が解除されることを確認する。
func TestManager_AcceptViaHTTPTestServer(t *testing.T) {
	m := NewManager(nil, nil)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go m.Accept(conn, &model.User{ID: "alice"})
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	dialer := websocket.Dialer{HandshakeTimeout: time.Second}
	conn, _, err := dialer.Dial(url, nil)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return m.Count() == 1 }, time.Second, 10*time.Millisecond)
	_ = conn.Close()
	require.Eventually(t, func() bool { return m.Count() == 0 }, time.Second, 10*time.Millisecond)
}

// stubMuteBlockLookup records the userID Accept passes so the wiring can be
// asserted without inspecting the (otherwise un-addressable) Connection.
type stubMuteBlockLookup struct {
	mu         sync.Mutex
	calledWith string
	snap       *MuteBlockSnapshot
}

func (s *stubMuteBlockLookup) MuteBlockSnapshotForUser(userID string) *MuteBlockSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calledWith = userID
	return s.snap
}

func (s *stubMuteBlockLookup) called() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calledWith
}

// TestManager_AcceptInvokesMuteBlockLookup verifies Accept fetches and attaches
// the viewer's mute/block snapshot at connection setup (#1711).
func TestManager_AcceptInvokesMuteBlockLookup(t *testing.T) {
	m := NewManager(nil, nil)
	lookup := &stubMuteBlockLookup{snap: &MuteBlockSnapshot{Muting: map[string]struct{}{"x": {}}}}
	m.SetMuteBlockSnapshotLookup(lookup)

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go m.Accept(conn, &model.User{ID: "alice"})
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	dialer := websocket.Dialer{HandshakeTimeout: time.Second}
	conn, _, err := dialer.Dial(url, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	require.Eventually(t, func() bool { return lookup.called() == "alice" }, time.Second, 10*time.Millisecond)
}

// #1942: Accept は policy provider を接続確立時に呼び、Connection に policies を attach する。
type stubPolicyProvider struct {
	mu         sync.Mutex
	calledWith string
}

func (s *stubPolicyProvider) GetUserPolicies(userID string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calledWith = userID
	return map[string]any{"ltlAvailable": false}
}
func (s *stubPolicyProvider) called() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calledWith
}

func TestManager_AcceptInvokesPolicyProvider(t *testing.T) {
	m := NewManager(nil, nil)
	provider := &stubPolicyProvider{}
	m.SetPolicyProvider(provider)

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go m.Accept(conn, &model.User{ID: "alice"})
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	dialer := websocket.Dialer{HandshakeTimeout: time.Second}
	conn, _, err := dialer.Dial(url, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	require.Eventually(t, func() bool { return provider.called() == "alice" }, time.Second, 10*time.Millisecond)
}

// TestManager_AcceptDispatchesConnectMessages verifies that Accept wires the
// Dispatcher into the Connection so that client `connect` envelopes reach the
// registered Channel factory and the topic is registered on the bus.
func TestManager_AcceptDispatchesConnectMessages(t *testing.T) {
	bus := newStubBus()
	registry := NewRegistry()
	holder := &fakeChannelHolder{}
	registry.Register("test", func(ctx ChannelContext) Channel {
		ch := &fakeChannel{ctx: ctx}
		holder.Value = ch
		return ch
	})
	m := NewManager(registry, bus)

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go m.Accept(conn, &model.User{ID: "alice"})
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	dialer := websocket.Dialer{HandshakeTimeout: time.Second}
	conn, _, err := dialer.Dial(url, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	require.NoError(t, conn.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"connect","body":{"id":"abc","channel":"test"}}`)))

	require.Eventually(t, func() bool {
		bus.mu.Lock()
		defer bus.mu.Unlock()
		return len(bus.subscribed) > 0
	}, time.Second, 10*time.Millisecond)
	require.NotNil(t, holder.Value)
}
