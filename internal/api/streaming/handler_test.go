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
	// scopes は Accept が受け取った値をそのまま積む。**捨てると handler から
	// acceptor への配線が無検査になる** (#streaming-scope の原因がまさにその形)。
	scopes [][]string
}

func (s *stubAcceptor) Accept(conn *websocket.Conn, user *model.User, scopes []string) {
	s.mu.Lock()
	s.accepted++
	s.users = append(s.users, user)
	s.scopes = append(s.scopes, scopes)
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

// AuthScope から接続の scope 制限へのマッピング。**これは変換だけ**で、
// handler が acceptor へ実際に渡しているかは
// TestStream_ForwardsScopesThroughStream が見る。
func TestConnectionScopes(t *testing.T) {
	cases := []struct {
		name  string
		scope *middleware.AuthScope
		want  []string
	}{
		{
			name:  "app token forwards its scopes",
			scope: &middleware.AuthScope{IsApp: true, Scopes: []string{"read:account", "read:chat"}},
			want:  []string{"read:account", "read:chat"},
		},
		{
			// 空でも non-nil。nil にすると「制限なし」の意味になってしまう。
			name:  "app token with no scopes stays restricted",
			scope: &middleware.AuthScope{IsApp: true, Scopes: nil},
			want:  []string{},
		},
		{
			// native login token は upstream でも scope を持たないので、
			// 従来どおり全 channel を許可する。
			name:  "native token is unrestricted",
			scope: &middleware.AuthScope{IsApp: false, Scopes: []string{"read:account"}},
			want:  nil,
		},
		{
			name:  "anonymous is unrestricted",
			scope: nil,
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := connectionScopes(tc.scope)
			if tc.want == nil {
				assert.Nil(t, got, "制限なしは nil でなければならない")
				return
			}
			assert.NotNil(t, got, "app token は空でも non-nil")
			assert.Equal(t, tc.want, got)
		})
	}
}

// handler から acceptor への配線そのものを Stream 経由で固定する。
//
// **変換関数だけ試しても意味が無い。** 元の脆弱性は「正しい判定関数はあるのに
// production から呼ばれていない」形で入っており、同じ形は handler 側でも作れる
// (Accept へ nil を渡すだけ)。ここは実際に WebSocket を張って、acceptor が
// 受け取った値を見る。
func TestStream_ForwardsScopesThroughStream(t *testing.T) {
	cases := []struct {
		name  string
		scope *middleware.AuthScope
		want  []string
	}{
		// app token は read:account を持たないと upgrade 前に 403 になるので、
		// 通過させるケースは必ず含める。
		{
			name:  "app token forwards its scopes",
			scope: &middleware.AuthScope{IsApp: true, Scopes: []string{"read:account"}},
			want:  []string{"read:account"},
		},
		{
			name:  "native token is unrestricted",
			scope: &middleware.AuthScope{IsApp: false, Scopes: []string{"read:account"}},
			want:  nil,
		},
		{
			name:  "anonymous is unrestricted",
			scope: nil,
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acc := &stubAcceptor{}
			h := NewHandler(acc)
			e := echo.New()
			e.GET("/streaming", h.Stream, func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					c.Set(string(middleware.UserContextKey), &model.User{ID: "alice"})
					if tc.scope != nil {
						c.Set(string(middleware.AuthScopeContextKey), tc.scope)
					}
					return next(c)
				}
			})
			srv := httptest.NewServer(e)
			t.Cleanup(srv.Close)

			dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
			conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/streaming", nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })

			require.Eventually(t, func() bool {
				acc.mu.Lock()
				defer acc.mu.Unlock()
				return acc.accepted == 1
			}, 2*time.Second, 10*time.Millisecond)

			acc.mu.Lock()
			got := acc.scopes[0]
			acc.mu.Unlock()

			if tc.want == nil {
				assert.Nil(t, got, "制限なしは nil で渡す必要がある")
				return
			}
			assert.NotNil(t, got, "app token の scope が acceptor へ届いていない")
			assert.Equal(t, tc.want, got)
		})
	}
}
