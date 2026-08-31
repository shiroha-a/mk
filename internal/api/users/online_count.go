package users

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// OnlineCount returns the number of locally active users.
//
// GET|POST /api/get-online-users-count
//
// **両メソッドを受ける** (router 側で登録)。frontend は `misskeyApiGet` で GET
// 呼び出しするので、POST だけだと catchall に落ちて `count` が undefined になり、
// admin overview が「NaN 人」を表示していた (#421)。
//
// 数えられなければ 0 を返す。ここで 500 にすると overview 全体が開かなくなる。
func (h *Handler) OnlineCount(c echo.Context) error {
	var count int64
	if h.userRepo != nil {
		count, _ = h.userRepo.CountOnlineUsers()
	}
	return c.JSON(http.StatusOK, map[string]any{"count": count})
}
