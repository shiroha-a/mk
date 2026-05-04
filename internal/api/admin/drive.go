package admin

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
)

// DriveCleanRemoteFiles handles POST /api/admin/drive/clean-remote-files.
func (h *Handler) DriveCleanRemoteFiles(c echo.Context) error {
	// 単一 DELETE 文なので同期実行で十分。将来バッチ化が必要ならここを
	// queue ジョブに差し替える。
	if h.driveFileRepo != nil {
		_, _ = h.driveFileRepo.DeleteRemoteCache()
	}
	return c.NoContent(http.StatusNoContent)
}

// DriveCleanup handles POST /api/admin/drive/cleanup.
func (h *Handler) DriveCleanup(c echo.Context) error {
	if h.driveFileRepo != nil {
		_, _ = h.driveFileRepo.DeleteOrphans()
	}
	return c.NoContent(http.StatusNoContent)
}

// SystemUserIDToken は `/api/admin/drive/files` の `userId` パラメータで
// 「system 所有 (UserID IS NULL) の drive file」を一覧する特殊値。custom
// emoji の copy / import zip 経路で蓄積される system file (#670 で導入)
// を admin UI から可視化する経路 (#686)。`@` 接頭辞は実 user ID と衝突
// しない (aidx ID は英数字のみ) ため安全。
const SystemUserIDToken = "@system"

// DriveFiles handles POST /api/admin/drive/files.
func (h *Handler) DriveFiles(c echo.Context) error {
	if h.driveFileRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	var req struct {
		Limit    int    `json:"limit"`
		SinceID  string `json:"sinceId"`
		UntilID  string `json:"untilId"`
		UserID   string `json:"userId"`
		Origin   string `json:"origin"`
		Host     string `json:"host"`
		Hostname string `json:"hostname"`
		Type     string `json:"type"`
	}
	_ = c.Bind(&req)
	if req.Limit <= 0 || req.Limit > 100 {
		req.Limit = 30
	}
	// userId に @system が指定されたら system file 専用 listing を返す。
	// origin / host filter は意味を持たない (system file はユーザーに紐付か
	// ないので) ので、type と pagination のみを取り回す。
	if req.UserID == SystemUserIDToken {
		files, err := h.driveFileRepo.ListSystemFiles(req.Type, req.UntilID, req.SinceID, req.Limit)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, apierr.InternalError())
		}
		out := make([]entity.DriveFileEntity, 0, len(files))
		for _, f := range files {
			out = append(out, h.packDriveFileAdmin(f))
		}
		return c.JSON(http.StatusOK, out)
	}
	switch req.Origin {
	case "", "combined", "local", "remote":
	default:
		req.Origin = "combined"
	}
	// upstream は host も hostname もどちらも受けて同じ意味で扱う。
	// frontend (admin/instance-info) が hostname で投げるため互換上両方サポート。
	host := req.Host
	if host == "" {
		host = req.Hostname
	}
	files, err := h.driveFileRepo.ListForAdmin(req.UserID, req.Origin, host, req.Type, req.UntilID, req.SinceID, req.Limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	out := make([]entity.DriveFileEntity, 0, len(files))
	for _, f := range files {
		out = append(out, h.packDriveFileAdmin(f))
	}
	return c.JSON(http.StatusOK, out)
}

// packDriveFileAdmin packs a drive file and embeds the owning user as
// nested `user` when userRepo is wired. Folder is left nil because the
// admin handler does not currently wire a DriveFolderRepository.
func (h *Handler) packDriveFileAdmin(f *model.DriveFile) entity.DriveFileEntity {
	// user repoが未設定なら user は nil fallback。folder は admin handler
	// に folderRepoがないため常に nil (必要になれば拡張)。
	var user *model.User
	if h.userRepo != nil && f.UserID != nil {
		if u, err := h.userRepo.FindByID(*f.UserID); err == nil {
			user = u
		}
	}
	return entity.PackDriveFileWithRelations(f, h.idGen, nil, user)
}

// DriveShowFile handles POST /api/admin/drive/show-file.
// Accepts either a fileId or a url as identifier (Misskey 本家互換)。
func (h *Handler) DriveShowFile(c echo.Context) error {
	var req struct {
		FileID string `json:"fileId"`
		URL    string `json:"url"`
	}
	if err := c.Bind(&req); err != nil || (req.FileID == "" && req.URL == "") {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "fileId or url is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.driveFileRepo == nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FILE", "No such file.", "ac4f7b11-1a6e-47e3-bf3d-3dce9a0e07ab"))
	}
	if req.FileID != "" {
		file, err := h.driveFileRepo.FindByID(req.FileID)
		if err != nil {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FILE", "No such file.", "ac4f7b11-1a6e-47e3-bf3d-3dce9a0e07ab"))
		}
		return c.JSON(http.StatusOK, h.packDriveFileAdmin(file))
	}
	// url 指定時は adminDB を使って url / thumbnailUrl / webpublicUrl いずれか
	// に一致する 1 件を引く。 driveFileRepo には該当 API が無いため raw query で。
	if h.adminDB == nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FILE", "No such file.", "ac4f7b11-1a6e-47e3-bf3d-3dce9a0e07ab"))
	}
	var file model.DriveFile
	if err := h.adminDB.Where(
		`"url" = ? OR "thumbnailUrl" = ? OR "webpublicUrl" = ?`,
		req.URL, req.URL, req.URL,
	).First(&file).Error; err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FILE", "No such file.", "ac4f7b11-1a6e-47e3-bf3d-3dce9a0e07ab"))
	}
	return c.JSON(http.StatusOK, h.packDriveFileAdmin(&file))
}
