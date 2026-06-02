// Package drive provides /api/drive/* endpoints.
package drive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/pagination"
	coredrive "github.com/shiroha-a/mk/internal/core/drive"
	"github.com/shiroha-a/mk/internal/core/notesfilter"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"gorm.io/datatypes"
)

// Handler handles drive-related API endpoints.
type Handler struct {
	svc          *coredrive.Service
	idGen        id.Generator
	fileRepo     repository.DriveFileRepository
	folderRepo   repository.DriveFolderRepository
	noteRepo     repository.NoteRepository
	userRepo     repository.UserRepository
	instanceRepo repository.InstanceRepository
	emojiRepo    repository.EmojiRepository
	bufReader    entity.BufferedReactionsReader
	fieldRes     *entity.NoteFieldResolver
	urlUploader  URLUploadProcessor
}

// SetURLUploader wires the processor used by /drive/files/upload-from-url to
// download a remote file in the background and notify the user via the main
// stream. Without it the endpoint degrades to a 204 no-op.
func (h *Handler) SetURLUploader(p URLUploadProcessor) {
	h.urlUploader = p
}

// SetNoteFieldResolver wires the shared resolver that fills Files /
// MyReaction / Channel on packed notes (#426)。
func (h *Handler) SetNoteFieldResolver(r *entity.NoteFieldResolver) {
	h.fieldRes = r
}

// NewHandler creates a new drive Handler.
func NewHandler(svc *coredrive.Service, idGen id.Generator) *Handler {
	return &Handler{svc: svc, idGen: idGen}
}

// SetRepos attaches repositories for drive listing endpoints.
func (h *Handler) SetRepos(fileRepo repository.DriveFileRepository, folderRepo repository.DriveFolderRepository, noteRepo repository.NoteRepository) {
	h.fileRepo = fileRepo
	h.folderRepo = folderRepo
	h.noteRepo = noteRepo
}

// SetUserRepo attaches a UserRepository so drive file responses can embed
// the owning user as `user` (TS schema: DriveFile.user: UserLite). Without
// it the `user` field is omitted.
func (h *Handler) SetUserRepo(r repository.UserRepository) {
	h.userRepo = r
}

// SetInstanceRepo attaches an InstanceRepository so drive/files/attached-notes
// populates UserLite.Instance for remote users (#277).
func (h *Handler) SetInstanceRepo(r repository.InstanceRepository) {
	h.instanceRepo = r
}

func (h *Handler) instanceLookup() entity.InstanceLookup {
	if h.instanceRepo == nil {
		return nil
	}
	return h.instanceRepo
}

// SetEmojiRepo attaches an EmojiRepository so custom emoji shortcodes in
// note text and user displayNames get resolved to URLs.
func (h *Handler) SetEmojiRepo(r repository.EmojiRepository) {
	h.emojiRepo = r
}

// SetReactionReader wires a BufferedReactionsReader so PackNote / PackNotes
// can merge in-flight buffered reaction deltas (#647)。
func (h *Handler) SetReactionReader(r entity.BufferedReactionsReader) {
	h.bufReader = r
}

func (h *Handler) reactionReader() entity.BufferedReactionsReader {
	return h.bufReader
}

func (h *Handler) emojiLookup() entity.EmojiLookup {
	if h.emojiRepo == nil {
		return nil
	}
	return h.emojiRepo
}

// packDriveFileFull packs a drive file and, when repositories are wired,
// embeds the owning folder/user as nested objects. Matches Misskey's
// packDriveFileSchema which exposes both `folder` and `user`.
//
// 注: 戻り値は upstream の `pack(file, { detail: true, withUser: true })`
// 相当 (= owner ID と user 込み) で drive/files/show のような detail 経路
// で使う。それ以外の経路は対応する helper を使うこと:
//
//   - `drive/files/create` (= self single, `pack(file, { self: true })`):
//     `packDriveFileSelfSingle` (#812)
//   - `drive/files/find` / `find-by-hash` (= self list,
//     `packMany(files, { self: true })`):
//     `packDriveFileSelfList` (#818)
//
// 本関数を上記 self 経路で直接 c.JSON に渡すと shape drift する。
func (h *Handler) packDriveFileFull(f *model.DriveFile) entity.DriveFileEntity {
	// list loop (FilesList / FilesFind / Stream) からも呼ばれ、1ファイル
	// 当たり最大2 DB read (O(N) queries)が発生する。1ページ上限が10-100
	// 程度なので実害は小さいが、大量ファイルを返す admin list 等では
	// batch化の余地あり (follow-up issueで検討)。
	var folder *model.DriveFolder
	if h.folderRepo != nil && f.FolderID != nil {
		if fo, err := h.folderRepo.FindByID(*f.FolderID); err == nil {
			folder = fo
		}
	}
	var user *model.User
	if h.userRepo != nil && f.UserID != nil {
		if u, err := h.userRepo.FindByID(*f.UserID); err == nil {
			user = u
		}
	}
	return entity.PackDriveFileWithRelations(f, h.idGen, folder, user)
}

// packDriveFileSelfSingle packs a drive file for the "self single" path used
// by drive/files/create.
//
// upstream Misskey TS は `pack(file, { self: true })` (= withUser=false /
// detail=false) を呼び、`folder=null` / `user=null` / `userId=null` を
// 返す (DriveFileEntityService.ts:191-222)。mk-go は packDriveFileFull が
// 常に folder / user を resolve するため、helper で post-fixup して shape
// を整合させる (#812 / #818)。
//
// drive/files/find / find-by-hash の self list path とは異なり userId も
// **null にする** 点に注意 (= pack は detail=false で userId を返さない、
// packNullable とは別経路)。
func (h *Handler) packDriveFileSelfSingle(f *model.DriveFile) entity.DriveFileEntity {
	e := h.packDriveFileFull(f)
	e.Folder = nil
	e.UserID = nil
	e.User = nil
	return e
}

// packDriveFileSelfList packs a drive file for the "self list" path used by
// drive/files/find と drive/files/find-by-hash.
//
// upstream Misskey TS は `packMany(files, { self: true })` 経由で
// `packNullable` を呼び、`folder=null` / `user=null` / `userId=owner ID`
// を返す (DriveFileEntityService.ts:225-261, withUser/detail デフォルト
// false)。mk-go は packDriveFileFull が常に folder / user を resolve する
// ため、helper で post-fixup して shape を整合させる (#818)。
//
// drive/files/create の self single path (#812) とは異なり userId は
// **owner ID を維持** する点に注意 (= packNullable は userId を常時返す)。
func (h *Handler) packDriveFileSelfList(f *model.DriveFile) entity.DriveFileEntity {
	e := h.packDriveFileFull(f)
	e.Folder = nil
	e.User = nil
	return e
}

// readMultipartFile extracts the uploaded file's bytes and original filename.
// テスト用に差し替え可能な変数として定義する。Open()/ReadAll()はechoの
// multipartパース後ではメモリまたはtempfile由来のreaderしか返さないため
// 実用上失敗しない (FormFile以外のerrorパスは defensive 扱い)。
var readMultipartFile = func(c echo.Context) ([]byte, string, error) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return nil, "", err
	}
	src, _ := fileHeader.Open()
	defer src.Close()
	body, _ := io.ReadAll(src)
	return body, fileHeader.Filename, nil
}

// FilesCreate handles POST /api/drive/files/create.
// multipart/form-data with the file under "file"; optional fields: name,
// folderId, comment, isSensitive, force.
func (h *Handler) FilesCreate(c echo.Context) error {
	user := middleware.GetUser(c)

	body, filename, err := readMultipartFile(c)
	if err != nil {
		return apierr.JSONInvalidParam(c)
	}

	in := coredrive.UploadInput{
		User:           user,
		Body:           body,
		Name:           filename,
		RequestIP:      requestIPFromContext(c),
		RequestHeaders: requestHeadersForDrive(c),
	}
	if v := c.FormValue("name"); v != "" {
		in.Name = v
	}
	if v := c.FormValue("folderId"); v != "" {
		in.FolderID = &v
	}
	if v := c.FormValue("comment"); v != "" {
		in.Comment = &v
	}
	if c.FormValue("isSensitive") == "true" {
		in.IsSensitive = true
	}
	if c.FormValue("force") == "true" {
		in.Force = true
	}

	f, err := h.svc.Upload(c.Request().Context(), in)
	if err != nil {
		switch {
		case errors.Is(err, coredrive.ErrFolderNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FOLDER", "No such folder.", "d77545ec-1283-4b73-bbe1-e90e1da6a4e7"))
		case errors.Is(err, coredrive.ErrAccessDenied):
			return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", "fe8d7103-0ea8-4ec3-814d-f8b401dc69e9"))
		case errors.Is(err, coredrive.ErrUnallowedFileType):
			// upstream drive/files/create の unallowedFileType。frontend は
			// この UUID で error mapping するので code/id 完全一致が要求される。
			return c.JSON(http.StatusBadRequest, apierr.Error("UNALLOWED_FILE_TYPE",
				"Cannot upload the file because it is an unallowed file type.",
				"4becd248-7f2c-48c4-a9f0-75edc4f9a1ea"))
		case errors.Is(err, coredrive.ErrMaxFileSizeExceeded):
			// upstream drive/files/create は httpStatusCode:413 を明示する
			return c.JSON(http.StatusRequestEntityTooLarge, apierr.Error("MAX_FILE_SIZE_EXCEEDED", "Max file size exceeded.", "b9d8c348-33f0-4673-b9a9-5d4da058977a"))
		case errors.Is(err, coredrive.ErrNoFreeSpace):
			return c.JSON(http.StatusBadRequest, apierr.Error("NO_FREE_SPACE", "No free space.", "d08dbc37-a6a9-463a-8c47-96c32ab5f064"))
		}
		return apierr.JSONInternalError(c)
	}
	// upstream `drive/files/create` は `pack(file, { self: true })` の
	// self single path 経路 (#812)。helper が folder / userId / user を
	// 一律 null 化する (旧 inline 版は folder の nil 化を見落としていて
	// folderId 指定 upload で drift していたが、helper 化で同時に解消)。
	return c.JSON(http.StatusOK, h.packDriveFileSelfSingle(f))
}

// FileIDRequest is the body for show/delete and similar single-file ops.
type FileIDRequest struct {
	FileID string `json:"fileId"`
}

// FilesShow handles POST /api/drive/files/show.
func (h *Handler) FilesShow(c echo.Context) error {
	user := middleware.GetUser(c)
	var req FileIDRequest
	if err := c.Bind(&req); err != nil || req.FileID == "" {
		return apierr.JSONInvalidParam(c)
	}
	f, err := h.svc.Show(user, req.FileID)
	if err != nil {
		return mapFileError(c, err, fileEndpointShow)
	}
	return c.JSON(http.StatusOK, h.packDriveFileFull(f))
}

// FilesUpdateRequest is the body for drive/files/update.
type FilesUpdateRequest struct {
	FileID      string  `json:"fileId"`
	Name        *string `json:"name"`
	FolderID    *string `json:"folderId"`
	Comment     *string `json:"comment"`
	IsSensitive *bool   `json:"isSensitive"`
	// folderIdをnullに設定したい場合用 (JSON nullの判定不可なので明示フラグ)
	UnsetFolder bool `json:"unsetFolder"`
}

// FilesUpdate handles POST /api/drive/files/update.
func (h *Handler) FilesUpdate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req FilesUpdateRequest
	if err := c.Bind(&req); err != nil || req.FileID == "" {
		return apierr.JSONInvalidParam(c)
	}
	in := coredrive.UpdateInput{
		Name:        req.Name,
		IsSensitive: req.IsSensitive,
	}
	if req.Comment != nil {
		c2 := req.Comment
		in.Comment = &c2
	}
	if req.UnsetFolder {
		var nilPtr *string
		in.FolderID = &nilPtr
	} else if req.FolderID != nil {
		fid := req.FolderID
		in.FolderID = &fid
	}

	f, err := h.svc.Update(user, req.FileID, in)
	if err != nil {
		return mapFileError(c, err, fileEndpointUpdate)
	}
	// upstream は updateFile 後 `pack(file.id, { self: true })` を返す
	// (= self single shape、DriveService.ts)。本実装も #812 の create と
	// 同じ self single helper を使う (folder / userId / user 全 null)。
	return c.JSON(http.StatusOK, h.packDriveFileSelfSingle(f))
}

// FilesDelete handles POST /api/drive/files/delete.
func (h *Handler) FilesDelete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req FileIDRequest
	if err := c.Bind(&req); err != nil || req.FileID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.svc.Delete(user, req.FileID); err != nil {
		return mapFileError(c, err, fileEndpointDelete)
	}
	return c.NoContent(http.StatusNoContent)
}

// FilesFindByHashRequest is the body for drive/files/find-by-hash.
type FilesFindByHashRequest struct {
	MD5 string `json:"md5"`
}

// FilesFindByHash handles POST /api/drive/files/find-by-hash.
func (h *Handler) FilesFindByHash(c echo.Context) error {
	user := middleware.GetUser(c)
	var req FilesFindByHashRequest
	if err := c.Bind(&req); err != nil || req.MD5 == "" {
		return apierr.JSONInvalidParam(c)
	}
	f, err := h.svc.FindByHash(user, req.MD5)
	if err != nil {
		return mapFileError(c, err, fileEndpointFindByHash)
	}
	// upstream `packMany(files, { self: true })` 経路 (#818)。
	return c.JSON(http.StatusOK, []entity.DriveFileEntity{h.packDriveFileSelfList(f)})
}

// FoldersCreateRequest is the body for drive/folders/create.
type FoldersCreateRequest struct {
	Name     string  `json:"name"`
	ParentID *string `json:"parentId"`
}

// FoldersCreate handles POST /api/drive/folders/create.
func (h *Handler) FoldersCreate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req FoldersCreateRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	if req.Name == "" {
		req.Name = "Untitled"
	}
	f, err := h.svc.CreateFolder(user, req.Name, req.ParentID)
	if err != nil {
		return mapFolderError(c, err, folderEndpointCreate)
	}
	return c.JSON(http.StatusOK, entity.PackDriveFolder(f, h.idGen))
}

// FolderIDRequest is the body for show/delete folder ops.
type FolderIDRequest struct {
	FolderID string `json:"folderId"`
}

// FoldersShow handles POST /api/drive/folders/show.
//
// upstream Misskey TS は `pack(folder, { detail: true })` で foldersCount /
// filesCount / parent (recursive) を埋めて返す。本実装も同 shape に揃える
// (#845)。default mode (= create / update / find) は影響なし。
func (h *Handler) FoldersShow(c echo.Context) error {
	user := middleware.GetUser(c)
	var req FolderIDRequest
	if err := c.Bind(&req); err != nil || req.FolderID == "" {
		return apierr.JSONInvalidParam(c)
	}
	f, err := h.svc.ShowFolder(user, req.FolderID)
	if err != nil {
		return mapFolderError(c, err, folderEndpointShow)
	}
	return c.JSON(http.StatusOK, h.packDriveFolderDetail(f))
}

// packDriveFolderDetail packs a folder in upstream "detail" mode for
// drive/folders/show. Adds foldersCount / filesCount / parent (recursive
// detail pack) to the default 4 field. count は best-effort で取れない
// ときは 0 を返す (= upstream も Promise が reject されると 0 相当)。
//
// repository が wired されていない (= 旧 test handler) と count は 0、
// parent は nil。これは production runtime には影響しない (= router で
// 必ず wired される)。
func (h *Handler) packDriveFolderDetail(f *model.DriveFolder) entity.DriveFolderEntity {
	e := entity.PackDriveFolder(f, h.idGen)
	foldersCount := 0
	filesCount := 0
	if h.folderRepo != nil {
		if n, err := h.folderRepo.CountChildFolders(f.ID); err != nil {
			// silent 0 を返すと真の DB error と空 state が区別できないので
			// 運用 debug 用に Warn で記録する。frontend には response が
			// 0 で返るので影響なし。
			slog.Warn("packDriveFolderDetail: CountChildFolders failed", "folderID", f.ID, "err", err)
		} else {
			foldersCount = n
		}
	}
	if h.fileRepo != nil {
		if n, err := h.fileRepo.CountByFolder(f.ID); err != nil {
			slog.Warn("packDriveFolderDetail: CountByFolder failed", "folderID", f.ID, "err", err)
		} else {
			filesCount = n
		}
	}
	e.FoldersCount = &foldersCount
	e.FilesCount = &filesCount
	if f.ParentID != nil && h.folderRepo != nil {
		if parent, err := h.folderRepo.FindByID(*f.ParentID); err == nil {
			parentPacked := h.packDriveFolderDetail(parent)
			e.Parent = &parentPacked
		} else {
			// parent FindByID error は recursion 切断の原因。同上で Warn。
			slog.Warn("packDriveFolderDetail: parent FindByID failed", "folderID", f.ID, "parentID", *f.ParentID, "err", err)
		}
	}
	return e
}

// FoldersUpdateRequest is the body for drive/folders/update.
type FoldersUpdateRequest struct {
	FolderID    string  `json:"folderId"`
	Name        *string `json:"name"`
	ParentID    *string `json:"parentId"`
	UnsetParent bool    `json:"unsetParent"`
}

// FoldersUpdate handles POST /api/drive/folders/update.
func (h *Handler) FoldersUpdate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req FoldersUpdateRequest
	if err := c.Bind(&req); err != nil || req.FolderID == "" {
		return apierr.JSONInvalidParam(c)
	}
	in := coredrive.UpdateFolderInput{Name: req.Name}
	if req.UnsetParent {
		var nilPtr *string
		in.ParentID = &nilPtr
	} else if req.ParentID != nil {
		pid := req.ParentID
		in.ParentID = &pid
	}
	f, err := h.svc.UpdateFolder(user, req.FolderID, in)
	if err != nil {
		return mapFolderError(c, err, folderEndpointUpdate)
	}
	return c.JSON(http.StatusOK, entity.PackDriveFolder(f, h.idGen))
}

// FoldersDelete handles POST /api/drive/folders/delete.
func (h *Handler) FoldersDelete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req FolderIDRequest
	if err := c.Bind(&req); err != nil || req.FolderID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.svc.DeleteFolder(user, req.FolderID); err != nil {
		return mapFolderError(c, err, folderEndpointDelete)
	}
	return c.NoContent(http.StatusNoContent)
}

// helpers

// fileEndpoint identifies which drive/files endpoint produced a file error, so
// mapFileError can return the upstream-canonical per-endpoint UUID. Misskey TS
// uses a distinct UUID per endpoint for NO_SUCH_FILE / ACCESS_DENIED (#1336)。
type fileEndpoint int

const (
	fileEndpointShow fileEndpoint = iota
	fileEndpointUpdate
	fileEndpointDelete
	fileEndpointFindByHash
)

func mapFileError(c echo.Context, err error, ep fileEndpoint) error {
	switch {
	case errors.Is(err, coredrive.ErrFileNotFound):
		var id string
		switch ep {
		case fileEndpointShow, fileEndpointFindByHash:
			id = "067bc436-2718-4795-b0fb-ecbe43949e31"
		case fileEndpointUpdate:
			id = "e7778c7e-3af9-49cd-9690-6dbc3e6c972d"
		case fileEndpointDelete:
			id = "908939ec-e52b-4458-b395-1025195cea58"
		default:
			panic(fmt.Sprintf("mapFileError: unknown fileEndpoint %d", ep))
		}
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FILE", "No such file.", id))
	case errors.Is(err, coredrive.ErrFolderNotFound):
		// folder 不在は drive/files/update の parent 指定経路でのみ発生する。
		id := "1069098f-c281-440f-b085-f9932edbe091"
		if ep == fileEndpointUpdate {
			id = "ea8fb7a5-af77-4a08-b608-c0218176cd73"
		}
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FOLDER", "No such folder.", id))
	case errors.Is(err, coredrive.ErrAccessDenied):
		var id string
		switch ep {
		case fileEndpointShow:
			id = "25b73c73-68b1-41d0-bad1-381cfdf6579f"
		case fileEndpointUpdate:
			id = "01a53b27-82fc-445b-a0c1-b558465a8ed2"
		case fileEndpointDelete:
			id = "5eb8d909-2540-4970-90b8-dd6f86088121"
		case fileEndpointFindByHash:
			// find-by-hash は ACCESS_DENIED を golden 未定義。汎用 id を使う。
			id = "fe8d7103-0ea8-4ec3-814d-f8b401dc69e9"
		default:
			panic(fmt.Sprintf("mapFileError: unknown fileEndpoint %d", ep))
		}
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", id))
	case errors.Is(err, coredrive.ErrCannotUnmarkSensitive):
		// upstream drive/files/update の restrictedByRole は i/update と別 UUID
		// (7f59dccb-...)。endpoint 単位で frontend が i18n 引きするので、
		// upstream の per-endpoint UUID をそのまま使う (#1028)。
		return c.JSON(http.StatusForbidden, apierr.Error("RESTRICTED_BY_ROLE",
			"This feature is restricted by your role.",
			"7f59dccb-f465-75ab-5cf4-3ce44e3282f7"))
	}
	return apierr.JSONInternalError(c)
}

// folderEndpoint identifies which drive/folders endpoint produced a folder
// error, so that mapFolderError can return the upstream-canonical UUID for
// NO_SUCH_FOLDER. Misskey TS uses a distinct UUID per endpoint (#977):
//   - create: 53326628-a00d-40a6-a3cd-8975105c0f95 (parent not found)
//   - show:   d74ab9eb-bb09-4bba-bf24-fb58f761e1e9
//   - update: f7974dac-2c0d-4a27-926e-23583b28e98e (target not found)
//   - delete: 1069098f-c281-440f-b085-f9932edbe091
type folderEndpoint int

const (
	folderEndpointCreate folderEndpoint = iota
	folderEndpointShow
	folderEndpointUpdate
	folderEndpointDelete
)

func mapFolderError(c echo.Context, err error, ep folderEndpoint) error {
	switch {
	case errors.Is(err, coredrive.ErrFolderNotFound):
		var id string
		switch ep {
		case folderEndpointCreate:
			id = "53326628-a00d-40a6-a3cd-8975105c0f95"
		case folderEndpointShow:
			id = "d74ab9eb-bb09-4bba-bf24-fb58f761e1e9"
		case folderEndpointUpdate:
			id = "f7974dac-2c0d-4a27-926e-23583b28e98e"
		case folderEndpointDelete:
			id = "1069098f-c281-440f-b085-f9932edbe091"
		default:
			// 将来 endpoint を追加して switch case を漏らすと UUID drift で
			// frontend / spec が壊れるので fail-fast する。
			panic(fmt.Sprintf("mapFolderError: unknown folderEndpoint %d", ep))
		}
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_FOLDER", "No such folder.", id))
	case errors.Is(err, coredrive.ErrParentFolderNotFound):
		// folders/update のみ NO_SUCH_PARENT_FOLDER を区別する。
		// folders/create の parent 不在は upstream も NO_SUCH_FOLDER 扱い
		// (ErrParentFolderNotFound はそもそも CreateFolder では返らない)。
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_PARENT_FOLDER", "No such parent folder.", "ce104e3a-faaf-49d5-b459-10ff0cbbcaa1"))
	case errors.Is(err, coredrive.ErrAccessDenied):
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", "fe8d7103-0ea8-4ec3-814d-f8b401dc69e9"))
	case errors.Is(err, coredrive.ErrFolderNotEmpty):
		return c.JSON(http.StatusBadRequest, apierr.Error("HAS_CHILD_FILES_OR_FOLDERS", "Folder is not empty.", "b0fc8a17-963c-405d-bfbc-859a487295e1"))
	}
	return apierr.JSONInternalError(c)
}

// --- Drive listing endpoints ---

// Usage handles POST /api/drive — returns usage summary.
func (h *Handler) Usage(c echo.Context) error {
	user := middleware.GetUser(c)
	var usage int64
	if h.fileRepo != nil {
		usage, _ = h.fileRepo.UsageByUser(user.ID)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"capacity": int64(1024 * 1024 * 1024), // 1GB default
		"usage":    usage,
	})
}

// FilesList handles POST /api/drive/files — lists user's files.
func (h *Handler) FilesList(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Limit     int     `json:"limit"`
		SinceID   string  `json:"sinceId"`
		UntilID   string  `json:"untilId"`
		SinceDate *int64  `json:"sinceDate"`
		UntilDate *int64  `json:"untilDate"`
		FolderID  *string `json:"folderId"`
		Type      string  `json:"type"`
	}
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	if h.fileRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	files, err := h.fileRepo.ListByUser(user.ID, req.FolderID, untilID, sinceID, req.Limit)
	if err != nil {
		return c.JSON(http.StatusOK, []any{})
	}
	out := make([]entity.DriveFileEntity, 0, len(files))
	for _, f := range files {
		out = append(out, h.packDriveFileFull(f))
	}
	return c.JSON(http.StatusOK, out)
}

// FilesFind handles POST /api/drive/files/find — search by name.
func (h *Handler) FilesFind(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Name     string  `json:"name"`
		FolderID *string `json:"folderId"`
	}
	if err := c.Bind(&req); err != nil || req.Name == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.fileRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	files, err := h.fileRepo.FindByName(user.ID, req.Name, req.FolderID)
	if err != nil {
		return c.JSON(http.StatusOK, []any{})
	}
	out := make([]entity.DriveFileEntity, 0, len(files))
	for _, f := range files {
		// upstream `packMany(files, { self: true })` 経路 (#818)。
		out = append(out, h.packDriveFileSelfList(f))
	}
	return c.JSON(http.StatusOK, out)
}

// FilesCheckExistence handles POST /api/drive/files/check-existence.
func (h *Handler) FilesCheckExistence(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		MD5 string `json:"md5"`
	}
	if err := c.Bind(&req); err != nil || req.MD5 == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.fileRepo == nil {
		return c.JSON(http.StatusOK, false)
	}
	exists, _ := h.fileRepo.ExistsByMD5(user.ID, req.MD5)
	return c.JSON(http.StatusOK, exists)
}

// FilesAttachedNotes handles POST /api/drive/files/attached-notes.
//
// frontend Paginator (cursor mode) は { sinceId / untilId / limit } を投げて
// くる。upstream Misskey TS と同じく cursor で keyset pagination する。
// 過去はページング非対応で同じ note を返し続ける無限スクロール bug が
// 出ていた (#488)。
//
// 旧実装は fileId を viewer 所有か検証せず直に `noteRepo.ListByFileID` を
// 叩いていたため、認証 viewer が他人 owner の fileID を投げて当該 file を
// attach した followers / specified note を本文ごと列挙できる IDOR が
// 成立していた (#1470)。upstream TS と同じく viewer 所有 file のみ許可し、
// 不存在 / owner mismatch / system file (userId IS NULL) はいずれも
// NO_SUCH_FILE で隠蔽する。`fileRepo` 未配線も同じく fail-closed。
//
// owner check 通過後の note は upstream `notes/create` の `checkFileIDs`
// が「file は author 所有」を強制するため必然的に viewer 自身の note で
// あり、CanSeeNote は本人 short-circuit で常に true (= 追加の
// FilterVisible が無くても leak しない)。
func (h *Handler) FilesAttachedNotes(c echo.Context) error {
	viewer := middleware.GetUser(c)
	var req struct {
		FileID    string `json:"fileId"`
		Limit     int    `json:"limit"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
	}
	if err := c.Bind(&req); err != nil || req.FileID == "" {
		return apierr.JSONInvalidParam(c)
	}
	// upstream Misskey TS の attached-notes.ts と同じ semantics: viewer 所有
	// file のみ許可 (#1470 IDOR fix)。upstream 専用 UUID
	// `c118ece3-2e4b-4296-99d1-51756e32d232` を使い、汎用 NoSuchFile
	// (UUIDNoSuchFile) と区別する。
	if h.fileRepo == nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_FILE", "No such file.", "c118ece3-2e4b-4296-99d1-51756e32d232"))
	}
	f, err := h.fileRepo.FindByID(req.FileID)
	if err != nil || f.UserID == nil || *f.UserID != viewer.ID {
		return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_FILE", "No such file.", "c118ece3-2e4b-4296-99d1-51756e32d232"))
	}
	// fileIds配列にfileIDを含むノートを検索 (PostgreSQL配列演算子)
	if h.noteRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	notes, err := h.noteRepo.ListByFileID(req.FileID, sinceID, untilID, req.Limit)
	if err != nil {
		return c.JSON(http.StatusOK, []any{})
	}
	notes = notesfilter.ApplyHardMute(h.userRepo, viewer, notes)
	out := entity.PackNotes(c.Request().Context(), notes, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
	h.fieldRes.Apply(out, viewer)
	return c.JSON(http.StatusOK, out)
}

// FilesUploadFromURL handles POST /api/drive/files/upload-from-url.
//
// upstream Misskey TS drive/files/upload-from-url と同 semantics: URL を指定して
// サーバーに非同期でファイルを download させる。endpoint は即 204 を返し、
// download / 保存はバックグラウンドで実行され、完了後に user の main stream へ
// `urlUploadFinished` ({marker, file}) を publish する (#1217)。
func (h *Handler) FilesUploadFromURL(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		URL         string  `json:"url"`
		FolderID    *string `json:"folderId"`
		IsSensitive bool    `json:"isSensitive"`
		Comment     *string `json:"comment"`
		Marker      *string `json:"marker"`
		Force       bool    `json:"force"`
	}
	if err := c.Bind(&req); err != nil || req.URL == "" {
		return apierr.JSONInvalidParam(c)
	}
	// upstream paramDef は comment maxLength=512。ajv の maxLength は文字数
	// (code unit) 基準なので byte 長ではなく rune 数で判定する (多バイト文字を
	// 過剰に弾かないため)。超過は INVALID_PARAM。
	if req.Comment != nil && utf8.RuneCountInString(*req.Comment) > 512 {
		return apierr.JSONInvalidParam(c)
	}
	// uploader 未配線 (= 単体テスト等) は upstream と同じ空レスポンスで返す。
	if h.urlUploader == nil || user == nil {
		return c.NoContent(http.StatusNoContent)
	}
	// upstream は fire-and-forget。request context は応答後に cancel されるため
	// download はバックグラウンド context で継続する。
	in := URLUploadInput{
		User:        user,
		URL:         req.URL,
		FolderID:    req.FolderID,
		Comment:     req.Comment,
		Marker:      req.Marker,
		IsSensitive: req.IsSensitive,
		Force:       req.Force,
		RequestIP:   c.RealIP(),
	}
	go h.urlUploader.Process(context.Background(), in)
	return c.NoContent(http.StatusNoContent)
}

// FilesMoveBulk handles POST /api/drive/files/move-bulk.
func (h *Handler) FilesMoveBulk(c echo.Context) error {
	var req struct {
		FileIDs  []string `json:"fileIds"`
		FolderID *string  `json:"folderId"`
	}
	if err := c.Bind(&req); err != nil || len(req.FileIDs) == 0 {
		return apierr.JSONInvalidParam(c)
	}
	if h.fileRepo != nil {
		_ = h.fileRepo.UpdateBulkFolder(req.FileIDs, req.FolderID)
	}
	return c.NoContent(http.StatusNoContent)
}

// Stream handles POST /api/drive/stream — lists all files (unfoldered).
func (h *Handler) Stream(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Limit     int    `json:"limit"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
		Type      string `json:"type"`
	}
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	if h.fileRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	files, err := h.fileRepo.ListByUser(user.ID, nil, untilID, sinceID, req.Limit)
	if err != nil {
		return c.JSON(http.StatusOK, []any{})
	}
	out := make([]entity.DriveFileEntity, 0, len(files))
	for _, f := range files {
		out = append(out, h.packDriveFileFull(f))
	}
	return c.JSON(http.StatusOK, out)
}

// FoldersList handles POST /api/drive/folders — lists user's folders.
func (h *Handler) FoldersList(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Limit     int     `json:"limit"`
		SinceID   string  `json:"sinceId"`
		UntilID   string  `json:"untilId"`
		SinceDate *int64  `json:"sinceDate"`
		UntilDate *int64  `json:"untilDate"`
		FolderID  *string `json:"folderId"`
	}
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	if h.folderRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	folders, err := h.folderRepo.ListByUser(user.ID, req.FolderID, untilID, sinceID, req.Limit)
	if err != nil {
		return c.JSON(http.StatusOK, []any{})
	}
	out := make([]map[string]any, 0, len(folders))
	for _, f := range folders {
		out = append(out, map[string]any{
			"id":       f.ID,
			"name":     f.Name,
			"parentId": f.ParentID,
		})
	}
	return c.JSON(http.StatusOK, out)
}

// FoldersFind handles POST /api/drive/folders/find — search by name.
func (h *Handler) FoldersFind(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Name     string  `json:"name"`
		ParentID *string `json:"parentId"`
	}
	if err := c.Bind(&req); err != nil || req.Name == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.folderRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	folders, err := h.folderRepo.FindByName(user.ID, req.Name, req.ParentID)
	if err != nil {
		return c.JSON(http.StatusOK, []any{})
	}
	out := make([]map[string]any, 0, len(folders))
	for _, f := range folders {
		out = append(out, map[string]any{
			"id":       f.ID,
			"name":     f.Name,
			"parentId": f.ParentID,
		})
	}
	return c.JSON(http.StatusOK, out)
}

// requestIPFromContext extracts the client IP for drive upload provenance
// tracking (admin/drive/show-file の IP タブで参照、#1148)。echo の RealIP
// は X-Forwarded-For / X-Real-IP を考慮した resolved IP を返すので、nginx
// 等の reverse proxy 配下でも本物の client IP を取得できる。空文字なら
// nil を返して `requestIp` column を NULL のままにする。
func requestIPFromContext(c echo.Context) *string {
	ip := c.RealIP()
	if ip == "" {
		return nil
	}
	return &ip
}

// sensitiveRequestHeaders は drive_file.requestHeaders に保存しない header
// 名 (小文字 canonical) の deny-list。mk-go 独自 hardening として、upstream
// Misskey TS が全 header を生のまま保存する設計から divergence する (#1148)。
//
// 根拠: drive_file.requestHeaders は admin の moderation 用途で参照される
// (admin/drive/show-file の IP タブ) が、`Authorization` 等の credential
// header を保存すると DB dump 流出時に user の Bearer token / session
// cookie が芋づる式に漏れる。moderation 価値より credential 流出 risk が
// 上回るので保存時点で除外する。upstream で同 hardening を入れる PR は
// shiroha-a/misskey-ts fork 経由で中長期検討。
var sensitiveRequestHeaders = map[string]struct{}{
	"authorization":       {},
	"cookie":              {},
	"set-cookie":          {},
	"x-api-key":           {},
	"api-key":             {},
	"proxy-authorization": {},
}

// requestHeadersForDrive snapshots the inbound HTTP request headers into a
// jsonb blob stored on drive_file.requestHeaders (#1148)。`sensitiveRequest
// Headers` deny-list で credential 系を除外。`map[string]string` で保存する
// (慣習として 1 要素 header が大半、複数値の場合は最初を採用)。空 header
// set なら nil を返して列を NULL のままにする。
func requestHeadersForDrive(c echo.Context) datatypes.JSON {
	req := c.Request()
	if req == nil || len(req.Header) == 0 {
		return nil
	}
	out := make(map[string]string, len(req.Header))
	for k, vs := range req.Header {
		// HTTP header 名は case-insensitive なので canonical な小文字で
		// deny-list と比較する。Go の http.Header は MIME canonical
		// (= "Authorization" 等の Title-Case) で正規化済なので、こちらは
		// 単純に ToLower で揃える。
		if _, deny := sensitiveRequestHeaders[strings.ToLower(k)]; deny {
			continue
		}
		if len(vs) > 0 {
			out[k] = vs[0]
		}
	}
	if len(out) == 0 {
		return nil
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return datatypes.JSON(raw)
}
