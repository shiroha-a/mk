package entity

import (
	"encoding/json"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
)

// DriveFileProperties mirrors the `properties` sub-object in Misskey's
// packedDriveFileSchema. All fields are optional so omitempty produces the
// same JSON shape as TS (fields absent rather than null). Width/Height are
// in pixels, Orientation is the EXIF orientation code (1-8), and AvgColor
// is an "rgb(r,g,b)" string.
type DriveFileProperties struct {
	Width       *int    `json:"width,omitempty"`
	Height      *int    `json:"height,omitempty"`
	Orientation *int    `json:"orientation,omitempty"`
	AvgColor    *string `json:"avgColor,omitempty"`
}

// DriveFileEntity is the drive file representation returned by API endpoints.
type DriveFileEntity struct {
	ID           string              `json:"id"`
	CreatedAt    string              `json:"createdAt"`
	Name         string              `json:"name"`
	Type         string              `json:"type"`
	MD5          string              `json:"md5"`
	Size         int                 `json:"size"`
	IsSensitive  bool                `json:"isSensitive"`
	Blurhash     *string             `json:"blurhash"`
	Properties   DriveFileProperties `json:"properties"`
	Comment      *string             `json:"comment"`
	URL          string              `json:"url"`
	ThumbnailURL *string             `json:"thumbnailUrl"`
	WebpublicURL *string             `json:"webpublicUrl"`
	FolderID     *string             `json:"folderId"`
	UserID       *string             `json:"userId"`
	// Folder は upstream が detail packing で常に出す (= null or DriveFolder)。
	// 旧実装は omitempty で省略していたが drop-in shape drift だったため #812
	// で外した。caller (= packDriveFileFull) で pre-fetch してセットし、未設定
	// なら nil → JSON `null` として出す。
	Folder *DriveFolderEntity `json:"folder"`
	// User も upstream の detail packing で常に出す (= null or UserLite)。
	// 同じく #812 で omitempty を外した。
	User *UserLite `json:"user"`
}

// PackDriveFile converts a model.DriveFile to a DriveFileEntity. The Folder
// and User fields are left nil; callers that need the nested objects should
// fetch them via DriveFolderRepository / UserRepository and assign
// &PackDriveFolder(...) / &PackUserLite(...) to the returned entity.
func PackDriveFile(f *model.DriveFile, idGen id.Generator) DriveFileEntity {
	createdAt := ""
	if t, err := idGen.ParseTime(f.ID); err == nil {
		createdAt = t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	var props DriveFileProperties
	// model.Properties は datatypes.JSON (=[]byte)。パース失敗時は空struct
	// で返して UI を壊さない (CLAUDE.md「エラー処理は best-effort」方針)。
	if len(f.Properties) > 0 {
		_ = json.Unmarshal(f.Properties, &props)
	}
	return DriveFileEntity{
		ID:           f.ID,
		CreatedAt:    createdAt,
		Name:         f.Name,
		Type:         f.Type,
		MD5:          f.MD5,
		Size:         f.Size,
		IsSensitive:  f.IsSensitive,
		Blurhash:     f.Blurhash,
		Properties:   props,
		Comment:      f.Comment,
		URL:          driveFilePublicURL(f),
		ThumbnailURL: resolveThumbnailURL(f),
		WebpublicURL: driveFileWebpublicURL(f),
		FolderID:     f.FolderID,
		UserID:       f.UserID,
	}
}

// isRemoteFile reports whether the drive file belongs to a remote user
// (userHost set). Remote files are stored link-format with an external `url`,
// so their URLs must be routed through the media proxy (issue #1529).
func isRemoteFile(f *model.DriveFile) bool {
	return f.UserHost != nil && *f.UserHost != ""
}

// driveFileTarget returns the upstream URL to proxy: the canonical `uri` when
// present, else the stored `url` (both are the external original for remote
// link-format files).
func driveFileTarget(f *model.DriveFile) string {
	if f.URI != nil && *f.URI != "" {
		return *f.URI
	}
	return f.URL
}

// driveFilePublicURL mirrors upstream getPublicUrl: remote files are proxied
// (when proxying is enabled), local files keep their (local) url. Local URLs are
// served by this instance so they never leak the client IP.
func driveFilePublicURL(f *model.DriveFile) string {
	if isRemoteFile(f) && shouldProxyRemoteFile() {
		return proxyMediaURL(driveFileTarget(f), "")
	}
	return f.URL
}

// driveFileWebpublicURL proxies a remote file's webpublicUrl (rare for
// link-format remotes, but guard against leaking it when present).
func driveFileWebpublicURL(f *model.DriveFile) *string {
	if f.WebpublicURL == nil || *f.WebpublicURL == "" {
		return f.WebpublicURL
	}
	if isRemoteFile(f) && shouldProxyRemoteFile() {
		u := proxyMediaURL(*f.WebpublicURL, "")
		return &u
	}
	return f.WebpublicURL
}

// resolveThumbnailURL emulates upstream Misskey's
// DriveFileEntityService.getThumbnailUrl fallback: when the row's
// thumbnailUrl column is NULL (typical for link-format remote files
// since mk-go doesn't generate thumbnails for unfetched remote
// originals), fall back to webpublicUrl, then to the original url for
// image MIME types. Frontend (`MkMediaImage.vue:100`) accesses
// `image.thumbnailUrl!` with a non-null assertion, so returning null
// for an image breaks the timeline thumbnail rendering entirely
// (#460). For non-image types we keep null — frontend skips the
// thumbnail render path for those.
func resolveThumbnailURL(f *model.DriveFile) *string {
	// リモートファイルは thumbnailUrl(外部 doc.Icon 由来)も含め生 URL が漏洩源に
	// なるため、proxy 有効時は常にメディアプロキシ(static mode)経由にする
	// (本家 getThumbnailUrl の userHost!=null / isLink&&proxyRemoteFiles 分岐相当)。
	if isRemoteFile(f) && shouldProxyRemoteFile() {
		u := proxyMediaURL(driveFileTarget(f), "static")
		return &u
	}
	if f.ThumbnailURL != nil && *f.ThumbnailURL != "" {
		return f.ThumbnailURL
	}
	if !isImageMime(f.Type) {
		return nil
	}
	if f.WebpublicURL != nil && *f.WebpublicURL != "" {
		return f.WebpublicURL
	}
	if f.URL != "" {
		u := f.URL
		return &u
	}
	return nil
}

// isImageMime reports whether `type` field denotes a still image. The
// upstream check (`isMimeImage(type, 'sharp-convertible-image')`) maps
// to this set when ignoring server-side image processing capability —
// for the thumbnail fallback path we just need a "browser will render
// this as <img>" predicate.
func isImageMime(t string) bool {
	if len(t) < 6 {
		return false
	}
	return t[:6] == "image/"
}

// PackDriveFileWithRelations is PackDriveFile + optional folder/user
// embedding. The caller is responsible for fetching the related rows (so
// batch/cached access is possible). Pass nil for fields you do not want to
// expose — JSON 上は `null` として出る (#812 で omitempty を外した)。
func PackDriveFileWithRelations(f *model.DriveFile, idGen id.Generator, folder *model.DriveFolder, user *model.User) DriveFileEntity {
	out := PackDriveFile(f, idGen)
	if folder != nil {
		packed := PackDriveFolder(folder, idGen)
		out.Folder = &packed
	}
	if user != nil {
		lite := PackUserLite(user)
		out.User = &lite
	}
	return out
}

// DriveFolderEntity is the drive folder representation returned by API endpoints.
type DriveFolderEntity struct {
	ID        string  `json:"id"`
	CreatedAt string  `json:"createdAt"`
	Name      string  `json:"name"`
	ParentID  *string `json:"parentId"`
	// detail mode (= drive/folders/show) で埋まる field 群 (#845)。
	// upstream Misskey TS は `pack(folder, { detail: true })` で返す。
	// default mode (= folders/create / update / find) では omitempty で
	// 省略され、shape は default 4 field のまま。
	FoldersCount *int               `json:"foldersCount,omitempty"`
	FilesCount   *int               `json:"filesCount,omitempty"`
	Parent       *DriveFolderEntity `json:"parent,omitempty"`
}

// PackDriveFolder converts a model.DriveFolder to a DriveFolderEntity in
// **default mode** (= 4 field のみ)。folders/create / update / find /
// (file pack 内の embedded folder) で使う。
func PackDriveFolder(f *model.DriveFolder, idGen id.Generator) DriveFolderEntity {
	createdAt := ""
	if t, err := idGen.ParseTime(f.ID); err == nil {
		createdAt = t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return DriveFolderEntity{
		ID:        f.ID,
		CreatedAt: createdAt,
		Name:      f.Name,
		ParentID:  f.ParentID,
	}
}
