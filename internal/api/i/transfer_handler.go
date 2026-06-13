package i

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/transfer"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// TransferEnqueuer is the subset of queue.Enqueuer needed to schedule
// export/import jobs. 小さいインターフェースにすることで i/Handler のテスト
// mockが簡単になる。
type TransferEnqueuer interface {
	EnqueueExport(payload queue.ExportPayload) error
	EnqueueImport(payload queue.ImportPayload) error
}

// SetTransferEnqueuer attaches a TransferEnqueuer for i/export-* and
// i/import-* endpoints.
func (h *Handler) SetTransferEnqueuer(e TransferEnqueuer) {
	h.transferEnqueuer = e
}

// SetDriveFileRepo wires the DriveFileRepository used by i/import-* to verify
// that the supplied fileId is owned by the requesting user (#1555). Without
// this the import endpoints fail closed (NO_SUCH_FILE) to avoid reading another
// user's drive file.
func (h *Handler) SetDriveFileRepo(r repository.DriveFileRepository) {
	h.driveFileRepo = r
}

// exportHandler enqueues an export job for the authenticated user and
// returns 204 No Content on success, matching Misskey's behavior.
func (h *Handler) exportHandler(exportType string) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := middleware.GetUser(c)
		if h.transferEnqueuer == nil {
			return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Transfer queue not configured.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
		// export-following のみ excludeMuting / excludeInactive を受ける
		// (upstream export-following.ts paramDef)。他 type では無視される。
		var req struct {
			ExcludeMuting   bool `json:"excludeMuting"`
			ExcludeInactive bool `json:"excludeInactive"`
		}
		_ = c.Bind(&req)
		if err := h.transferEnqueuer.EnqueueExport(queue.ExportPayload{
			UserID:          u.ID,
			Type:            exportType,
			ExcludeMuting:   req.ExcludeMuting,
			ExcludeInactive: req.ExcludeInactive,
		}); err != nil {
			return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Failed to enqueue export.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
		return c.NoContent(http.StatusNoContent)
	}
}

// importNoSuchFileID maps an import type to upstream Misskey's per-endpoint
// noSuchFile error UUID. 各 i/import-* endpoint は固有の noSuchFile id を持つ
// ため、共通 importHandler でも importType ごとに本家 UUID を返す (#1555)。
func importNoSuchFileID(importType string) string {
	switch importType {
	case transfer.ImportBlocking:
		return "ebb53e5f-6574-9c0c-0b92-7ca6def56d7e"
	case transfer.ImportMuting:
		return "e674141e-bd2a-ba85-e616-aefb187c9c2a"
	case transfer.ImportUserLists:
		return "ea9cc34f-c415-4bc6-a6fe-28ac40357049"
	case transfer.ImportAntennas:
		return "3b71d086-c3fa-431c-b01d-ded65a777172"
	default: // ImportFollowing
		return "b98644cf-a5ac-4277-a502-0b8054a709a3"
	}
}

// importMaxFileSize is upstream の通常 import file 上限 (64KB)。account-move 中
// (movedAt が直近 2h 以内) は 32MB まで緩和されるが、mk-go は import-* に
// prohibitMoved gate を効かせる方針 (#1555) かつ move-in 経路は未実装のため、
// 共通の 64KB で扱う。これより大きい file は上流が move 中だけ許可する稀な
// ケースなので、保守的に弾く (= 上流が許可するものを誤って通すことはない)。
const importMaxFileSize = 64 * 1024

// importEmptyFileID maps an import type to upstream's per-endpoint emptyFile
// error UUID (#1555)。
func importEmptyFileID(importType string) string {
	switch importType {
	case transfer.ImportBlocking:
		return "6f3a4dcc-f060-a707-4950-806fbdbe60d6"
	case transfer.ImportMuting:
		return "d2f12af1-e7b4-feac-86a3-519548f2728e"
	case transfer.ImportUserLists:
		return "99efe367-ce6e-4d44-93f8-5fae7b040356"
	case transfer.ImportAntennas:
		return "7f60115d-8d93-4b0f-bd0e-3815dcbb389f"
	default: // ImportFollowing
		return "31a1b42c-06f7-42ae-8a38-a661c5c9f691"
	}
}

// importTooBigFileID maps an import type to upstream's per-endpoint tooBigFile
// error UUID。import-antennas は upstream に tooBigFile check が無いため ""
// (= size 上限 check を skip)。
func importTooBigFileID(importType string) string {
	switch importType {
	case transfer.ImportBlocking:
		return "b7fbf0b1-aeef-3b21-29ef-fadd4cb72ccf"
	case transfer.ImportMuting:
		return "9b4ada6d-d7f7-0472-0713-4f558bd1ec9c"
	case transfer.ImportUserLists:
		return "ae6e7a22-971b-4b52-b2be-fc0b9b121fe9"
	case transfer.ImportAntennas:
		return "" // upstream import-antennas は tooBigFile を持たない
	default: // ImportFollowing
		return "dee9d4ed-ad07-43ed-8b34-b2856398bc60"
	}
}

// importHandler enqueues an import job using a DriveFile uploaded by the
// user. フロントは fileId を渡してくる。本家互換。
func (h *Handler) importHandler(importType string) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := middleware.GetUser(c)
		var req struct {
			FileID      string `json:"fileId"`
			WithReplies *bool  `json:"withReplies"`
		}
		if err := c.Bind(&req); err != nil || req.FileID == "" {
			return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "fileId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
		}
		// upstream Misskey の import-* は driveFilesRepository.findOneBy({id,
		// userId: me.id}) で「リクエスト元 user 所有の file」のみ許可し、
		// 他人 / 不在 / system file (userId NULL) は NO_SUCH_FILE で弾く
		// (import-following.ts:71-73 ほか)。所有権検証を欠くと任意の認証 user が
		// 他人の drive file を import 経路に流し込めるため fail-closed にする
		// (#1555)。NO_SUCH_FILE の UUID は upstream import 系と一致させる。
		if h.driveFileRepo == nil {
			return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_FILE", "No such file.", importNoSuchFileID(importType)))
		}
		file, err := h.driveFileRepo.FindByID(req.FileID)
		if err != nil || file == nil || file.UserID == nil || *file.UserID != u.ID {
			return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_FILE", "No such file.", importNoSuchFileID(importType)))
		}
		// 同期 file 検証 (upstream は handler 内で実施)。空 file は EMPTY_FILE、
		// 上限超過は TOO_BIG_FILE (import-antennas は upstream に tooBigFile が
		// 無いので skip)。enqueue 前に弾くことで無駄な job 投入を避ける (#1555)。
		if file.Size == 0 {
			return c.JSON(http.StatusBadRequest, apierr.Error("EMPTY_FILE", "That file is empty.", importEmptyFileID(importType)))
		}
		if tooBigID := importTooBigFileID(importType); tooBigID != "" && file.Size > importMaxFileSize {
			return c.JSON(http.StatusBadRequest, apierr.Error("TOO_BIG_FILE", "That file is too big.", tooBigID))
		}
		if h.transferEnqueuer == nil {
			return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Transfer queue not configured.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
		// withReplies は import-following のみが使う job-level fallback (CSV row が
		// per-line withReplies を省略したときの default、upstream
		// `withReplies ?? job.data.withReplies`)。他 type では Importer 側で無視。
		withReplies := false
		if req.WithReplies != nil {
			withReplies = *req.WithReplies
		}
		if err := h.transferEnqueuer.EnqueueImport(queue.ImportPayload{
			UserID:      u.ID,
			Type:        importType,
			FileID:      req.FileID,
			WithReplies: withReplies,
		}); err != nil {
			return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Failed to enqueue import.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
		return c.NoContent(http.StatusNoContent)
	}
}

// Export handler factory methods — one per endpoint for router binding.
func (h *Handler) ExportNotes(c echo.Context) error {
	return h.exportHandler(transfer.ExportNotes)(c)
}
func (h *Handler) ExportFollowing(c echo.Context) error {
	return h.exportHandler(transfer.ExportFollowing)(c)
}
func (h *Handler) ExportBlocking(c echo.Context) error {
	return h.exportHandler(transfer.ExportBlocking)(c)
}
func (h *Handler) ExportMute(c echo.Context) error {
	return h.exportHandler(transfer.ExportMuting)(c)
}
func (h *Handler) ExportFavorites(c echo.Context) error {
	return h.exportHandler(transfer.ExportFavorites)(c)
}
func (h *Handler) ExportUserLists(c echo.Context) error {
	return h.exportHandler(transfer.ExportUserLists)(c)
}
func (h *Handler) ExportAntennas(c echo.Context) error {
	return h.exportHandler(transfer.ExportAntennas)(c)
}
func (h *Handler) ExportClips(c echo.Context) error {
	return h.exportHandler(transfer.ExportClips)(c)
}

// Import handler factory methods.
func (h *Handler) ImportFollowing(c echo.Context) error {
	return h.importHandler(transfer.ImportFollowing)(c)
}
func (h *Handler) ImportBlocking(c echo.Context) error {
	return h.importHandler(transfer.ImportBlocking)(c)
}
func (h *Handler) ImportMuting(c echo.Context) error {
	return h.importHandler(transfer.ImportMuting)(c)
}
func (h *Handler) ImportUserLists(c echo.Context) error {
	return h.importHandler(transfer.ImportUserLists)(c)
}
func (h *Handler) ImportAntennas(c echo.Context) error {
	return h.importHandler(transfer.ImportAntennas)(c)
}
