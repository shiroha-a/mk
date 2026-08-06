// Package streaming provides the WebSocket /streaming endpoint that Misskey
// clients connect to for live timeline / notification updates.
package streaming

import (
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// ConnectionAcceptor receives an upgraded WebSocket connection and an optional
// authenticated user. パッケージ間の循環依存を避けるため interface で受け取る
// (実装は internal/stream の Manager)。
type ConnectionAcceptor interface {
	Accept(conn *websocket.Conn, user *model.User)
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
	if h.acceptor == nil {
		_ = conn.Close()
		return nil
	}
	h.acceptor.Accept(conn, user)
	return nil
}
