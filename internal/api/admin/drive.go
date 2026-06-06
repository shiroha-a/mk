package admin

import (
	"encoding/json"
	"log/slog"
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

// driveCleanupBatchSize はバルク削除の 1 バッチ件数。upstream の cursor 巡回
// (8 件) より大きめにして round-trip を減らす。
const driveCleanupBatchSize = 100

// driveCleanupMaxBatches は無限ループ防止の安全弁 (= 約 100 万件)。
const driveCleanupMaxBatches = 10000

// DriveCleanRemoteFiles handles POST /api/admin/drive/clean-remote-files.
//
// upstream CleanRemoteFilesProcessorService はキャッシュ済みリモートファイル
// (userHost IS NOT NULL AND isLink=false) を物理オブジェクトごと削除する。
// storage backend が配線されていれば list→object storage 削除→DB 行削除を
// バッチで回す。未配線時は DB 行のみ削除 (条件は修正済 = isLink=false)。
func (h *Handler) DriveCleanRemoteFiles(c echo.Context) error {
	if h.driveFileRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	if h.storageDeleter == nil {
		// storage 未配線: DB 行のみ削除 (condition は isLink=false に修正済)。
		_, _ = h.driveFileRepo.DeleteRemoteCache()
		return c.NoContent(http.StatusNoContent)
	}
	if err := h.deleteFilesBatched(c, h.driveFileRepo.ListRemoteCache); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	return c.NoContent(http.StatusNoContent)
}

// deleteFilesBatched lists files via list() in batches, deletes each file's
// object-storage objects, then deletes the DB rows, until none remain. Because
// the rows are deleted each round, list() naturally returns the next batch
// (driveCleanupMaxBatches caps the loop against a non-shrinking repo).
//
// storage object 削除は best-effort (失敗しても DB 行は消す)。同期ループで
// upstream のような job 再試行ができないため、storage 失敗で DB 削除を止めると
// 同じ行を re-list し続けてしまう。失敗は slog で観測可能にし orphan を検知できる
// ようにする。list / DeleteByIDs (= DB) の失敗は hard error として返し、handler が
// 500 を返す (DB-only 経路の 500 と整合)。
func (h *Handler) deleteFilesBatched(c echo.Context, list func(limit int) ([]*model.DriveFile, error)) error {
	for i := 0; i < driveCleanupMaxBatches; i++ {
		files, err := list(driveCleanupBatchSize)
		if err != nil {
			slog.ErrorContext(c.Request().Context(), "drive cleanup: list failed", "err", err)
			return err
		}
		if len(files) == 0 {
			return nil
		}
		ids := make([]string, 0, len(files))
		for _, f := range files {
			h.deleteFileStorageObjects(c, f)
			ids = append(ids, f.ID)
		}
		if _, err := h.driveFileRepo.DeleteByIDs(ids); err != nil {
			slog.ErrorContext(c.Request().Context(), "drive cleanup: DeleteByIDs failed", "err", err)
			return err
		}
		if len(files) < driveCleanupBatchSize {
			return nil
		}
	}
	return nil
}

// deleteFileStorageObjects removes a file's primary / thumbnail / webpublic
// objects from object storage. best-effort: 欠損 object はエラーにならない
// (Storage.Delete の契約) が、それ以外の失敗は slog.Warn で残す (DB 行は消すので
// orphan を後追いできるように)。upstream は thumbnailUrl/webpublicUrl の有無で
// gate するが、mk-go は access key の有無で判定する (best-effort なので余分な key
// への Delete は no-op)。storedInternal=true でローカル disk にある object を S3
// backend で消そうとするケース (#1414 の二系統 storage) は本経路では消えない。
func (h *Handler) deleteFileStorageObjects(c echo.Context, f *model.DriveFile) {
	if h.storageDeleter == nil || f == nil {
		return
	}
	for _, key := range []*string{f.AccessKey, f.ThumbnailAccessKey, f.WebpublicAccessKey} {
		if key != nil && *key != "" {
			if err := h.storageDeleter.Delete(*key); err != nil {
				slog.WarnContext(c.Request().Context(), "drive cleanup: storage delete failed (object may be orphaned)",
					"fileId", f.ID, "err", err)
			}
		}
	}
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
