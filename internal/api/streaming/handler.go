// Package streaming provides the WebSocket /streaming endpoint that Misskey
// clients connect to for live timeline / notification updates.
package streaming

import (
	"net/http"
	"slices"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// ConnectionAcceptor receives an upgraded WebSocket connection, an optional
// authenticated user, and the OAuth2 scopes the connection is limited to.
// パッケージ間の循環依存を避けるため interface で受け取る
// (実装は internal/stream の Manager)。
//
// scopes は **app access_token のときだけ non-nil**。nil は「スコープによる
// 制限が無い」を意味し、native login token (フロントエンドが使う) と
// 匿名接続がそちらに当たる。mk-go は cookie 認証を持たない。空の non-nil slice は「スコープを 1 つも持たない app token」で、
// nil とは区別する。
type ConnectionAcceptor interface {
	Accept(conn *websocket.Conn, user *model.User, scopes []string)
}

// connectionScopes returns the scope list to enforce for this connection, or
// nil when the caller is not scope-limited.
//
// native login token (IsApp=false) は upstream でも scope の概念を持たないので
// nil を返し、従来どおり全 channel を許可する。app token は
// scope が空でも non-nil を返し、「何も許可されていない」を表現する。
func connectionScopes(scope *middleware.AuthScope) []string {
	if scope == nil || !scope.IsApp {
		return nil
	}
	if scope.Scopes == nil {
		return []string{}
	}
	return scope.Scopes
}

// Handler upgrades incoming HTTP requests to a WebSocket and hands the
// resulting connection to a ConnectionAcceptor. 認証はミドルウェア (RequireAuth
// ではなく Authenticate のみ) でベストエフォートに行い、未認証接続も許容する。
type Handler struct {
	upgrader websocket.Upgrader
	acceptor ConnectionAcceptor
}

// NewHandler constructs a Handler. acceptor が nil の場合、upgrade 直後に
// 接続を閉じる (テスト用)。
func NewHandler(acceptor ConnectionAcceptor) *Handler {
	return &Handler{
		upgrader: websocket.Upgrader{
			// クライアントの Origin チェックは外側 (CORS / リバースプロキシ) の責務に
			// 任せて無条件に許可する。
			CheckOrigin: func(*http.Request) bool { return true },
		},
		acceptor: acceptor,
	}
}

// Stream handles GET /streaming. WebSocket への upgrade に成功したら接続を
// acceptor に渡し、失敗したら 400 / 426 のまま終了する (gorilla/websocket が
// 自動で適切な status を返す)。
func (h *Handler) Stream(c echo.Context) error {
	// app access_token は read:account を持っていないと WebSocket を使えない
	// (upstream StreamingApiServerService:64)。upgrade する前に弾かないと、
	// 権限の無いトークンでも接続だけは張れてしまう。native login token
	// (IsApp=false) と匿名接続は従来どおり通す。
	if scope := middleware.GetAuthScope(c); scope != nil && scope.IsApp && !slices.Contains(scope.Scopes, "read:account") {
		return c.JSON(http.StatusForbidden, apierr.PermissionDenied())
	}
	// WebSocket でない GET には 503 を返す。本家 (@fastify/websocket) が
	// upgrade 以外を受け付けずに 503 で落とすのに揃える。gorilla に任せると
	// 400 になり、「まだ実装されていない」のか「WS 専用」なのか区別が付かない。
	if !websocket.IsWebSocketUpgrade(c.Request()) {
		return c.NoContent(http.StatusServiceUnavailable)
	}
	conn, err := h.upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		// gorilla/websocket は Upgrade 失敗時にレスポンスヘッダを既に書き込んで
		// いる。Echo に追加のレスポンスを書かせないため nil を返す。
		return nil
	}
	user := middleware.GetUser(c)
	// channel ごとの scope 判定 (read:chat 等) に使う。ここで渡さないと
	// stream 側は permissions=nil のままになり、HasPermission が常に true を
	// 返して RequiredPermission が実質無効になる。
	scopes := connectionScopes(middleware.GetAuthScope(c))
	if h.acceptor == nil {
		_ = conn.Close()
		return nil
	}
	h.acceptor.Accept(conn, user, scopes)
	return nil
}
