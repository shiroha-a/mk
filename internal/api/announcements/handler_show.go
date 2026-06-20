package announcements

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Show handles POST /api/announcements/show.
//
// userId-targeted announcement (= a.UserID != nil) は当該 user 本人にしか
// 閲覧させない。upstream Misskey TS 2026.5.4 fix
// (`AnnouncementService.getAnnouncement`) と一致した shape で、me==null
// (= 非ログイン) または me.ID != *a.UserID なら NO_SUCH_ANNOUNCEMENT を返す。
//
// 旧 mk-go の Show は権限 check が一切なく:
//   - 非ログイン user が user-targeted な admin 通知を閲覧できてしまう
//     (upstream 2026.5.4 で塞いだ穴)
//   - ログイン中でも他人宛の user-targeted announcement を閲覧できてしまう
//     (upstream 2026.5.3 以前から閉じている穴、mk-go 単体での実装漏れ)
//
// の 2 重の drop-in regression / privacy hole 状態だったため両方塞ぐ
// (#1164 Phase C)。
func (h *Handler) Show(c echo.Context) error {
	var req struct {
		AnnouncementID string `json:"announcementId"`
	}
	if err := c.Bind(&req); err != nil || req.AnnouncementID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "announcementId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	a, err := h.repo.FindByID(req.AnnouncementID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ANNOUNCEMENT", "No such announcement.", "b57b5e1d-4f49-404a-9edb-46b00268f121"))
	}
	me := middleware.GetUser(c)
	if a.UserID != nil {
		if me == nil || *a.UserID != me.ID {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ANNOUNCEMENT", "No such announcement.", "b57b5e1d-4f49-404a-9edb-46b00268f121"))
		}
	}
	item := packAnnouncement(a, h.idGen)
	// upstream getAnnouncement は announcement_reads を引いて isRead=(read!==null)
	// を pack する。匿名では isRead を undefined にし key を省略する。mk-go は read
	// 状態を見ずに常に false を返していた (#1955)。
	if me != nil {
		read, _ := h.repo.IsRead(me.ID, a.ID)
		item["isRead"] = read
	} else {
		delete(item, "isRead")
	}
	return c.JSON(http.StatusOK, item)
}
