package admin

import (
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/pagination"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"gorm.io/datatypes"
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
		Limit     int    `json:"limit"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
		UserID    string `json:"userId"`
		Origin    string `json:"origin"`
		Host      string `json:"host"`
		Hostname  string `json:"hostname"`
		Type      string `json:"type"`
	}
	_ = c.Bind(&req)
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	// sinceDate / untilDate を aidx prefix に正規化 (#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	// userId に @system が指定されたら system file 専用 listing を返す。
	// origin / host filter は意味を持たない (system file はユーザーに紐付か
	// ないので) ので、type と pagination のみを取り回す。
	if req.UserID == SystemUserIDToken {
		files, err := h.driveFileRepo.ListSystemFiles(req.Type, untilID, sinceID, req.Limit)
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
	files, err := h.driveFileRepo.ListForAdmin(req.UserID, req.Origin, host, req.Type, untilID, sinceID, req.Limit)
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
// レスポンス shape は upstream admin/drive/show-file.ts の custom 28-field
// 形に揃え、`requestIp` / `requestHeaders` を含む (frontend admin-file の
// IP タブで参照、それ以外は PackDriveFile + admin only field の合成、#1148)。
func (h *Handler) DriveShowFile(c echo.Context) error {
	var req struct {
		FileID string `json:"fileId"`
		URL    string `json:"url"`
	}
	if err := c.Bind(&req); err != nil || (req.FileID == "" && req.URL == "") {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "fileId or url is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.driveFileRepo == nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FILE", "No such file.", "caf3ca38-c6e5-472e-a30c-b05377dcc240"))
	}
	viewer := middleware.GetUser(c)
	if req.FileID != "" {
		file, err := h.driveFileRepo.FindByID(req.FileID)
		if err != nil {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FILE", "No such file.", "caf3ca38-c6e5-472e-a30c-b05377dcc240"))
		}
		return c.JSON(http.StatusOK, h.packAdminDriveShowFile(file, viewer))
	}
	// url 指定時は adminDB を使って url / thumbnailUrl / webpublicUrl いずれか
	// に一致する 1 件を引く。 driveFileRepo には該当 API が無いため raw query で。
	if h.adminDB == nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FILE", "No such file.", "caf3ca38-c6e5-472e-a30c-b05377dcc240"))
	}
	var file model.DriveFile
	if err := h.adminDB.Where(
		`"url" = ? OR "thumbnailUrl" = ? OR "webpublicUrl" = ?`,
		req.URL, req.URL, req.URL,
	).First(&file).Error; err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FILE", "No such file.", "caf3ca38-c6e5-472e-a30c-b05377dcc240"))
	}
	return c.JSON(http.StatusOK, h.packAdminDriveShowFile(&file, viewer))
}

// packAdminDriveShowFile builds the upstream-compatible admin/drive/show-file
// response shape. `requestIp` は viewer が moderator のときのみ含め (= 通常
// admin endpoint は moderator gate されているので常に true 経路だが、防御的
// に check)、`requestHeaders` は viewer が moderator AND owner が
// moderator でない場合のみ含める (upstream: モデレーターの個人情報を他の
// モデレーターから守る制限)。両 field とも `nil` で omit せず明示 null を
// emit する (upstream の `optional: false, nullable: true` schema 通り)。
func (h *Handler) packAdminDriveShowFile(f *model.DriveFile, viewer *model.User) map[string]any {
	resp := map[string]any{
		"id":                 f.ID,
		"userId":             f.UserID,
		"userHost":           f.UserHost,
		"isLink":             f.IsLink,
		"maybePorn":          f.MaybePorn,
		"maybeSensitive":     f.MaybeSensitive,
		"isSensitive":        f.IsSensitive,
		"folderId":           f.FolderID,
		"src":                f.Src,
		"uri":                f.URI,
		"webpublicAccessKey": f.WebpublicAccessKey,
		"thumbnailAccessKey": f.ThumbnailAccessKey,
		"accessKey":          f.AccessKey,
		"webpublicType":      f.WebpublicType,
		"webpublicUrl":       f.WebpublicURL,
		"thumbnailUrl":       f.ThumbnailURL,
		"url":                f.URL,
		"storedInternal":     f.StoredInternal,
		"properties":         driveFileProperties(f.Properties),
		"blurhash":           f.Blurhash,
		"comment":            f.Comment,
		"size":               f.Size,
		"type":               f.Type,
		"name":               f.Name,
		"md5":                f.MD5,
		"createdAt":          driveFileCreatedAtOrNil(h.idGen, f.ID),
		"requestIp":          nil,
		"requestHeaders":     nil,
	}
	// gating: 通常 admin route は moderator/admin gate 済だが、防御的 check。
	viewerIsModerator := viewer != nil && h.roleService != nil && h.roleService.IsModerator(viewer.ID)
	if viewerIsModerator {
		resp["requestIp"] = f.RequestIP
		// owner が moderator のときは headers を隠す (upstream 仕様、
		// モデレーター同士の互いの個人情報保護)。
		ownerIsModerator := false
		if f.UserID != nil && *f.UserID != "" && h.roleService != nil {
			ownerIsModerator = h.roleService.IsModerator(*f.UserID)
		}
		if !ownerIsModerator {
			resp["requestHeaders"] = driveFileRequestHeaders(f.RequestHeaders)
		}
	}
	return resp
}

// driveFileCreatedAtOrNil renders the aidx-derived createdAt ISO string,
// returning nil on parse failure (= shape becomes `"createdAt": null` rather
// than emitting a malformed string).
func driveFileCreatedAtOrNil(idGen id.Generator, aidx string) any {
	if s, err := aidxCreatedAtString(idGen, aidx); err == nil {
		return s
	}
	return nil
}

// driveFileProperties unmarshals the jsonb properties column to map for the
// admin endpoint response. Falls back to empty map on parse error.
func driveFileProperties(raw datatypes.JSON) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// driveFileRequestHeaders unmarshals the jsonb requestHeaders column to a
// map<string,string> shape that frontend admin-file.root.vue iterates with
// v-for="(v, k) in info.requestHeaders". Falls back to nil if column is
// empty / unparseable so frontend's `v-if` guard hides the section.
func driveFileRequestHeaders(raw datatypes.JSON) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
