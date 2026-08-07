package streaming

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubAcceptor records every accepted connection and immediately closes it.
type stubAcceptor struct {
	mu       sync.Mutex
	accepted int
	users    []*model.User
}

func (s *stubAcceptor) Accept(conn *websocket.Conn, user *model.User) {
	s.mu.Lock()
	s.accepted++
	s.users = append(s.users, user)
	s.mu.Unlock()
	_ = conn.Close()
}

// dialServer is a small helper that boots an httptest server with the given
// echo handler chain and dials a WebSocket against it.
func dialServer(t *testing.T, h *Handler, withUser bool) (*websocket.Conn, *httptest.Server, *stubAcceptor) {
	t.Helper()
	e := echo.New()
	e.GET("/streaming", h.Stream, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if withUser {
				c.Set(string(middleware.UserContextKey), &model.User{ID: "alice"})
			}
			return next(c)
		}
	})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/streaming"
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	conn, _, err := dialer.Dial(url, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn, srv, nil
}

func TestStream_AcceptsAuthenticated(t *testing.T) {
	acc := &stubAcceptor{}
	h := NewHandler(acc)
	conn, _, _ := dialServer(t, h, true)
	// 接続が確立されることだけ確認
	require.NotNil(t, conn)
	// acceptor への呼び出しを少しだけ待つ
	require.Eventually(t, func() bool {
		acc.mu.Lock()
		defer acc.mu.Unlock()
		return acc.accepted == 1
	}, time.Second, 10*time.Millisecond)
	require.Len(t, acc.users, 1)
	require.NotNil(t, acc.users[0])
	assert.Equal(t, "alice", acc.users[0].ID)
}

func TestStream_AcceptsAnonymous(t *testing.T) {
	acc := &stubAcceptor{}
	h := NewHandler(acc)
	conn, _, _ := dialServer(t, h, false)
	require.NotNil(t, conn)
	require.Eventually(t, func() bool {
		acc.mu.Lock()
		defer acc.mu.Unlock()
		return acc.accepted == 1
	}, time.Second, 10*time.Millisecond)
	require.Len(t, acc.users, 1)
	assert.Nil(t, acc.users[0])
}

func TestStream_NilAcceptorClosesImmediately(t *testing.T) {
	h := NewHandler(nil)
	conn, _, _ := dialServer(t, h, false)
	// connection should be closed promptly
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err := conn.ReadMessage()
	assert.Error(t, err)
}

// 通常の HTTP GET (Upgrade ヘッダ無し) は 503。本家 (@fastify/websocket) が
// upgrade 以外を 503 で落とすのに揃えている。
func TestStream_NonWebSocketGetIsUnavailable(t *testing.T) {
	h := NewHandler(&stubAcceptor{})
	e := echo.New()
	e.GET("/streaming", h.Stream)
	req := httptest.NewRequest(http.MethodGet, "/streaming", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// app access_token は read:account が無いと WebSocket を使えない
// (upstream StreamingApiServerService:64)。
func TestStream_AppTokenWithoutReadAccountIsRejected(t *testing.T) {
	h := NewHandler(&stubAcceptor{})
	e := echo.New()
	e.GET("/streaming", func(c echo.Context) error {
		c.Set(string(middleware.AuthScopeContextKey), &middleware.AuthScope{IsApp: true, Scopes: []string{"write:notes"}})
		return h.Stream(c)
	})
	req := httptest.NewRequest(http.MethodGet, "/streaming", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// read:account があれば通る (ここでは upgrade しないので 503 まで進む)。
func TestStream_AppTokenWithReadAccountPasses(t *testing.T) {
	h := NewHandler(&stubAcceptor{})
	e := echo.New()
	e.GET("/streaming", func(c echo.Context) error {
		c.Set(string(middleware.AuthScopeContextKey), &middleware.AuthScope{IsApp: true, Scopes: []string{"read:account"}})
		return h.Stream(c)
	})
	req := httptest.NewRequest(http.MethodGet, "/streaming", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "scope は通り、非 WS として 503")
}

// native login token (IsApp=false) は scope 検査しない。
func TestStream_NativeTokenSkipsScopeCheck(t *testing.T) {
	h := NewHandler(&stubAcceptor{})
	e := echo.New()
	e.GET("/streaming", func(c echo.Context) error {
		c.Set(string(middleware.AuthScopeContextKey), &middleware.AuthScope{IsApp: false})
		return h.Stream(c)
	})
	req := httptest.NewRequest(http.MethodGet, "/streaming", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
