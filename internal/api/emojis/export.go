package emojis

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	coretransfer "github.com/shiroha-a/mk/internal/core/transfer"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// ExportEnqueuer is the subset of the queue client this endpoint needs.
type ExportEnqueuer interface {
	EnqueueExport(payload queue.ExportPayload) error
}

// SetExportEnqueuer wires the queue used by POST /api/export-custom-emojis.
func (h *Handler) SetExportEnqueuer(q ExportEnqueuer) { h.exportQueue = q }

// ExportCustomEmojis queues a custom-emoji export for the caller.
//
// POST /api/export-custom-emojis
//
// **投入に失敗しても 204 を返す。** upstream も `queueService.createExportCustomEmojisJob`
// を await せず即 204 で、結果はドライブに出るファイルで確認する作りになっている。
// ここを 500 にすると upstream と挙動がずれる。失敗はログに残す。
func (h *Handler) ExportCustomEmojis(c echo.Context) error {
	user := middleware.GetUser(c)
	if user == nil {
		return c.NoContent(http.StatusNoContent)
	}
	if h.exportQueue == nil {
		slog.Warn("export-custom-emojis: queue not wired", "user", user.ID)
		return c.NoContent(http.StatusNoContent)
	}
	if err := h.exportQueue.EnqueueExport(queue.ExportPayload{
		UserID: user.ID,
		Type:   coretransfer.ExportCustomEmojis,
	}); err != nil {
		slog.Warn("export-custom-emojis: enqueue failed", "user", user.ID, "err", err)
	}
	return c.NoContent(http.StatusNoContent)
}
