package testutil

import (
	sortpkg "sort"
	"strings"

	"github.com/shiroha-a/mk/internal/model"
)

// MockDriveFileRepository is a test double for repository.DriveFileRepository.
type MockDriveFileRepository struct {
	Files map[string]*model.DriveFile // keyed by ID
	// EmojiReferencedURLs は DeleteOrphans / ListOrphans の guard を mock する
	// ための「emoji が参照している drive_file.url 集合」。production の SQL は
	// `NOT EXISTS (SELECT 1 FROM emoji WHERE originalUrl = url OR publicUrl = url)`
	// で、**2 列のどちらか**が一致すれば除外する (#722)。本フィールドはその
	// 和集合を表すので、片方の列だけを模倣しているわけではない。
	//
	// 空なら emoji guard は効かない。ただしそれは「userId NULL を全削除」を
	// 意味しない — userHost 条件が別にあるため、リモートの owner 無し行は
	// emoji 参照が無くても残る (#2721)。
	EmojiReferencedURLs map[string]bool

	// BulkFolderUserID / BulkFolderFileIDs record the last UpdateBulkFolder
	// call so handler tests can assert the owning user is scoped (IDOR guard).
	BulkFolderUserID  string
	BulkFolderFileIDs []string

	// 以下は bulk cleanup (clean-remote-files / delete-all-files) のエラー経路を
	// テストするための注入フィールド。nil なら通常動作。
	ListRemoteCacheErr error
	ListByUserAllErr   error
	ListOrphansErr     error
	DeleteByIDsErr     error
	// DeleteByIDsNoOp を true にすると DeleteByIDs が err=nil で 0 件削除する
	// (= 行が縮小しない degenerate ケース。maxBatches 安全弁の終了性検証用)。
	DeleteByIDsNoOp bool
}

func NewMockDriveFileRepository() *MockDriveFileRepository {
	return &MockDriveFileRepository{Files: make(map[string]*model.DriveFile)}
}

func (m *MockDriveFileRepository) Create(f *model.DriveFile) error {
	m.Files[f.ID] = f
	return nil
}

func (m *MockDriveFileRepository) FindByID(id string) (*model.DriveFile, error) {
	f, ok := m.Files[id]
	if !ok {
		return nil, ErrNotFound
	}
	return f, nil
}

func (m *MockDriveFileRepository) FindByIDs(ids []string) ([]*model.DriveFile, error) {
	var result []*model.DriveFile
	for _, id := range ids {
		if f, ok := m.Files[id]; ok {
			result = append(result, f)
		}
	}
	return result, nil
}

func (m *MockDriveFileRepository) FindByMD5(userID, md5 string) (*model.DriveFile, error) {
	var match *model.DriveFile
	for _, f := range m.Files {
		if f.UserID != nil && *f.UserID == userID && f.MD5 == md5 {
			if match == nil || f.ID > match.ID {
				match = f
			}
		}
	}
	if match == nil {
		return nil, ErrNotFound
	}
	return match, nil
}

// FindAllByMD5 returns every md5-matching file of the user, id ASC
// (production 実装と同じ決定的順序)。
func (m *MockDriveFileRepository) FindAllByMD5(userID, md5 string) ([]*model.DriveFile, error) {
	var result []*model.DriveFile
	for _, f := range m.Files {
		if f.UserID != nil && *f.UserID == userID && f.MD5 == md5 {
			result = append(result, f)
		}
	}
	sortpkg.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (m *MockDriveFileRepository) FindByAnyURL(url string) (*model.DriveFile, error) {
	if url == "" {
		return nil, ErrNotFound
	}
	var match *model.DriveFile
	for _, f := range m.Files {
		if f.URL == url ||
			(f.WebpublicURL != nil && *f.WebpublicURL == url) ||
			(f.ThumbnailURL != nil && *f.ThumbnailURL == url) {
			if match == nil || f.ID < match.ID {
				match = f
			}
		}
	}
	if match == nil {
		return nil, ErrNotFound
	}
	return match, nil
}

func (m *MockDriveFileRepository) FindByURI(uri string) (*model.DriveFile, error) {
	if uri == "" {
		return nil, ErrNotFound
	}
	var match *model.DriveFile
	for _, f := range m.Files {
		if f.URI != nil && *f.URI == uri {
			if match == nil || f.ID < match.ID {
				match = f
			}
		}
	}
	if match == nil {
		return nil, ErrNotFound
	}
	return match, nil
}

func (m *MockDriveFileRepository) FindByAccessKey(accessKey string) (*model.DriveFile, error) {
	if accessKey == "" {
		return nil, ErrNotFound
	}
	for _, f := range m.Files {
		if f.AccessKey != nil && *f.AccessKey == accessKey {
			return f, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockDriveFileRepository) FindByAnyAccessKey(accessKey string) (*model.DriveFile, error) {
	if accessKey == "" {
		return nil, ErrNotFound
	}
	var match *model.DriveFile
	for _, f := range m.Files {
		if (f.AccessKey != nil && *f.AccessKey == accessKey) ||
			(f.ThumbnailAccessKey != nil && *f.ThumbnailAccessKey == accessKey) ||
			(f.WebpublicAccessKey != nil && *f.WebpublicAccessKey == accessKey) {
			if match == nil || f.ID < match.ID {
				match = f
			}
		}
	}
	if match == nil {
		return nil, ErrNotFound
	}
	return match, nil
}

func (m *MockDriveFileRepository) Update(id string, fields map[string]any) error {
	f, ok := m.Files[id]
	if !ok {
		return ErrNotFound
	}
	applyDriveFileFields(f, fields)
	return nil
}

func (m *MockDriveFileRepository) FindByName(userID, name string, folderID *string) ([]*model.DriveFile, error) {
	var result []*model.DriveFile
	for _, f := range m.Files {
		if f.UserID == nil || *f.UserID != userID || f.Name != name {
			continue
		}
		if !matchesDriveFolder(f.FolderID, folderID) {
			continue
		}
		result = append(result, f)
	}
	return result, nil
}

func (m *MockDriveFileRepository) ExistsByMD5(userID, md5 string) (bool, error) {
	for _, f := range m.Files {
		if f.UserID != nil && *f.UserID == userID && f.MD5 == md5 {
			return true, nil
		}
	}
	return false, nil
}

func (m *MockDriveFileRepository) CountByFolder(folderID string) (int, error) {
	n := 0
	for _, f := range m.Files {
		if f.FolderID != nil && *f.FolderID == folderID {
			n++
		}
	}
	return n, nil
}

func (m *MockDriveFileRepository) ListByFileIDs(ids []string) ([]*model.DriveFile, error) {
	return m.FindByIDs(ids)
}

func (m *MockDriveFileRepository) UsageByUser(userID string) (int64, error) {
	var total int64
	for _, f := range m.Files {
		// 本番 repo 同様 isLink=false の行のみ合算する (#1831)。
		if f.IsLink {
			continue
		}
		if f.UserID != nil && *f.UserID == userID {
			total += int64(f.Size)
		}
	}
	return total, nil
}

func (m *MockDriveFileRepository) UpdateBulkFolder(userID string, fileIDs []string, _ *string) error {
	m.BulkFolderUserID = userID
	m.BulkFolderFileIDs = fileIDs
	return nil
}

func (m *MockDriveFileRepository) Delete(f *model.DriveFile) error {
	delete(m.Files, f.ID)
	return nil
}

// matchesDriveFileType mirrors the production fileType predicate: a `/*`
// suffix means prefix match, anything else is an exact match (#1772).
//
// 無条件 prefix にすると `image/*` がどこにも当たらず (`image/*` で始まる
// MIME は存在しない)、逆に完全一致のつもりの `image/heic` が
// `image/heic-sequence` まで拾う。production
// (internal/repository/drive_file.go の ListByUser / ListForAdmin /
// ListSystemFiles) は 3 経路ともこの形。
func matchesDriveFileType(actual, fileType string) bool {
	if fileType == "" {
		return true
	}
	if strings.HasSuffix(fileType, "/*") {
		return strings.HasPrefix(actual, strings.TrimSuffix(fileType, "*"))
	}
	return actual == fileType
}

// matchesDriveFolder mirrors the production folder predicate used by both
// drive_file."folderId" and drive_folder."parentId": a nil want means
// `IS NULL`, not "any folder".
func matchesDriveFolder(actual, want *string) bool {
	if want == nil {
		return actual == nil
	}
	return actual != nil && *actual == *want
}

func (m *MockDriveFileRepository) ListByUser(userID string, folderID *string, anyFolder bool, fileType, sort, untilID, sinceID string, limit int) ([]*model.DriveFile, error) {
	var rows []*model.DriveFile
	for _, f := range m.Files {
		if f.UserID == nil || *f.UserID != userID {
			continue
		}
		if !anyFolder && !matchesDriveFolder(f.FolderID, folderID) {
			continue
		}
		if !matchesDriveFileType(f.Type, fileType) {
			continue
		}
		if untilID != "" && f.ID >= untilID {
			continue
		}
		if sinceID != "" && f.ID <= sinceID {
			continue
		}
		rows = append(rows, f)
	}
	sortpkg.Slice(rows, func(i, j int) bool {
		switch sort {
		case "-createdAt":
			return rows[i].ID < rows[j].ID
		case "+name":
			return rows[i].Name > rows[j].Name
		case "-name":
			return rows[i].Name < rows[j].Name
		case "+size":
			return rows[i].Size > rows[j].Size
		case "-size":
			return rows[i].Size < rows[j].Size
		default:
			// "+createdAt" と未指定はともに id DESC (sinceID 指定時の ASC は
			// mock では省略 — 既存テストは untilID / 無指定のみ)。
			return rows[i].ID > rows[j].ID
		}
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func applyDriveFileFields(f *model.DriveFile, fields map[string]any) {
	for k, v := range fields {
		switch k {
		case "name":
			if s, ok := v.(string); ok {
				f.Name = s
			}
		case "comment":
			if s, ok := v.(*string); ok {
				f.Comment = s
			}
		case "isSensitive":
			if b, ok := v.(bool); ok {
				f.IsSensitive = b
			}
		case "folderId":
			if s, ok := v.(*string); ok {
				f.FolderID = s
			}
		}
	}
}

// MockDriveFolderRepository is a test double for repository.DriveFolderRepository.
type MockDriveFolderRepository struct {
	Folders map[string]*model.DriveFolder
	// FilesRef は HasChildren が参照するファイルストア。テストで紐づける。
	FilesRef *MockDriveFileRepository
}

func NewMockDriveFolderRepository() *MockDriveFolderRepository {
	return &MockDriveFolderRepository{Folders: make(map[string]*model.DriveFolder)}
}

func (m *MockDriveFolderRepository) Create(f *model.DriveFolder) error {
	m.Folders[f.ID] = f
	return nil
}

func (m *MockDriveFolderRepository) FindByID(id string) (*model.DriveFolder, error) {
	f, ok := m.Folders[id]
	if !ok {
		return nil, ErrNotFound
	}
	return f, nil
}

// FindByIDs returns the folders present in the mock for the given ID set
// (missing IDs are skipped, mirroring an IN query).
func (m *MockDriveFolderRepository) FindByIDs(ids []string) ([]*model.DriveFolder, error) {
	out := make([]*model.DriveFolder, 0, len(ids))
	for _, id := range ids {
		if f, ok := m.Folders[id]; ok {
			out = append(out, f)
		}
	}
	return out, nil
}

func (m *MockDriveFolderRepository) Update(id string, fields map[string]any) error {
	f, ok := m.Folders[id]
	if !ok {
		return ErrNotFound
	}
	for k, v := range fields {
		switch k {
		case "name":
			if s, ok := v.(string); ok {
				f.Name = s
			}
		case "parentId":
			if s, ok := v.(*string); ok {
				f.ParentID = s
			}
		}
	}
	return nil
}

func (m *MockDriveFolderRepository) Delete(f *model.DriveFolder) error {
	delete(m.Folders, f.ID)
	return nil
}

func (m *MockDriveFolderRepository) ListByUser(userID string, parentID *string, untilID, sinceID string, limit int) ([]*model.DriveFolder, error) {
	var rows []*model.DriveFolder
	for _, f := range m.Folders {
		if f.UserID == nil || *f.UserID != userID {
			continue
		}
		if !matchesDriveFolder(f.ParentID, parentID) {
			continue
		}
		if untilID != "" && f.ID >= untilID {
			continue
		}
		if sinceID != "" && f.ID <= sinceID {
			continue
		}
		rows = append(rows, f)
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (m *MockDriveFolderRepository) FindByName(userID, name string, parentID *string) ([]*model.DriveFolder, error) {
	var result []*model.DriveFolder
	for _, f := range m.Folders {
		if f.UserID == nil || *f.UserID != userID || f.Name != name {
			continue
		}
		// production は parentID が nil なら `"parentId" IS NULL`。file 側の
		// FindByName と同形 (#2747)。
		if !matchesDriveFolder(f.ParentID, parentID) {
			continue
		}
		result = append(result, f)
	}
	return result, nil
}

func (m *MockDriveFolderRepository) HasChildren(folderID string) (bool, error) {
	for _, f := range m.Folders {
		if f.ParentID != nil && *f.ParentID == folderID {
			return true, nil
		}
	}
	if m.FilesRef != nil {
		for _, f := range m.FilesRef.Files {
			if f.FolderID != nil && *f.FolderID == folderID {
				return true, nil
			}
		}
	}
	return false, nil
}

func (m *MockDriveFolderRepository) CountChildFolders(parentID string) (int, error) {
	n := 0
	for _, f := range m.Folders {
		if f.ParentID != nil && *f.ParentID == parentID {
			n++
		}
	}
	return n, nil
}

func (m *MockDriveFileRepository) ListForAdmin(userID, origin, host, fileType, untilID, sinceID string, limit int) ([]*model.DriveFile, error) {
	rows := make([]*model.DriveFile, 0, len(m.Files))
	for _, f := range m.Files {
		if userID != "" {
			if f.UserID == nil || *f.UserID != userID {
				continue
			}
		} else {
			switch origin {
			case "local":
				if f.UserHost != nil {
					continue
				}
			case "remote":
				if f.UserHost == nil {
					continue
				}
			}
			if host != "" {
				if f.UserHost == nil || *f.UserHost != host {
					continue
				}
			}
		}
		if !matchesDriveFileType(f.Type, fileType) {
			continue
		}
		if untilID != "" && f.ID >= untilID {
			continue
		}
		if sinceID != "" && f.ID <= sinceID {
			continue
		}
		rows = append(rows, f)
	}
	// id DESC 固定。production は paginationOrder で sinceID 単独指定時のみ
	// ASC になるが、mock では省略している (#2747 で対象外とした)。順序だけで
	// なく limit 打ち切りで残る行が変わるので、sinceID を使う paging を
	// mock で検証しないこと。
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > len(rows) {
		limit = len(rows)
	}
	return rows[:limit], nil
}

func (m *MockDriveFileRepository) ListSystemFiles(fileType, untilID, sinceID string, limit int) ([]*model.DriveFile, error) {
	rows := make([]*model.DriveFile, 0, len(m.Files))
	for _, f := range m.Files {
		if f.UserID != nil {
			continue
		}
		if !matchesDriveFileType(f.Type, fileType) {
			continue
		}
		if untilID != "" && f.ID >= untilID {
			continue
		}
		if sinceID != "" && f.ID <= sinceID {
			continue
		}
		rows = append(rows, f)
	}
	// id DESC 固定。production は paginationOrder で sinceID 単独指定時のみ
	// ASC になるが、mock では省略している (#2747 で対象外とした)。順序だけで
	// なく limit 打ち切りで残る行が変わるので、sinceID を使う paging を
	// mock で検証しないこと。
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[i].ID < rows[j].ID {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > len(rows) {
		limit = len(rows)
	}
	return rows[:limit], nil
}

// DeleteOrphans の mock は production SQL の semantics を model 化する:
// userId NULL かつ userHost NULL かつ EmojiReferencedURLs に URL が
// 含まれない drive file を削除する (#722 / #2721)。
//
// `userHost` を見る条件は後から入ったもので、mock 側が追随していない間は
// 「リモートの owner 無し行が消える」という production では起きない挙動を
// 通していた。production の条件は internal/repository/drive_file.go の
// orphanWhere を参照。
func (m *MockDriveFileRepository) DeleteOrphans() (int64, error) {
	n := int64(0)
	for id, f := range m.Files {
		if f.UserID != nil {
			continue
		}
		if f.UserHost != nil {
			continue
		}
		if m.EmojiReferencedURLs[f.URL] {
			continue
		}
		delete(m.Files, id)
		n++
	}
	return n, nil
}

// ListOrphans mirrors DeleteOrphans's selection without deleting (#1724).
func (m *MockDriveFileRepository) ListOrphans(limit int) ([]*model.DriveFile, error) {
	if m.ListOrphansErr != nil {
		return nil, m.ListOrphansErr
	}
	if limit <= 0 {
		limit = 100
	}
	var out []*model.DriveFile
	for _, f := range m.Files {
		if f.UserID != nil {
			continue
		}
		// userHost 条件は #2721。理由は repository の orphanWhere の doc を参照。
		if f.UserHost != nil {
			continue
		}
		if m.EmojiReferencedURLs[f.URL] {
			continue
		}
		out = append(out, f)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *MockDriveFileRepository) DeleteRemoteCache() (int64, error) {
	n := int64(0)
	for id, f := range m.Files {
		// upstream は isLink=false (キャッシュ実体) を消す。
		if !f.IsLink && f.UserHost != nil {
			delete(m.Files, id)
			n++
		}
	}
	return n, nil
}

func (m *MockDriveFileRepository) ListRemoteCache(limit int) ([]*model.DriveFile, error) {
	if m.ListRemoteCacheErr != nil {
		return nil, m.ListRemoteCacheErr
	}
	if limit <= 0 {
		limit = 100
	}
	out := make([]*model.DriveFile, 0, limit)
	for _, f := range m.Files {
		if !f.IsLink && f.UserHost != nil {
			out = append(out, f)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *MockDriveFileRepository) ListByUserAll(userID string, limit int) ([]*model.DriveFile, error) {
	if m.ListByUserAllErr != nil {
		return nil, m.ListByUserAllErr
	}
	if userID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	out := make([]*model.DriveFile, 0, limit)
	for _, f := range m.Files {
		if f.UserID != nil && *f.UserID == userID {
			out = append(out, f)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *MockDriveFileRepository) DeleteByIDs(ids []string) (int64, error) {
	if m.DeleteByIDsErr != nil {
		return 0, m.DeleteByIDsErr
	}
	if m.DeleteByIDsNoOp {
		// 行を消さず成功扱い (= list が縮小しない degenerate ケース)。
		return 0, nil
	}
	n := int64(0)
	for _, id := range ids {
		if _, ok := m.Files[id]; ok {
			delete(m.Files, id)
			n++
		}
	}
	return n, nil
}

func (m *MockDriveFileRepository) DeleteByUser(userID string) (int64, error) {
	if userID == "" {
		return 0, nil
	}
	n := int64(0)
	for id, f := range m.Files {
		if f.UserID != nil && *f.UserID == userID {
			delete(m.Files, id)
			n++
		}
	}
	return n, nil
}

func (m *MockDriveFileRepository) DeleteByHost(host string) (int64, error) {
	if host == "" {
		return 0, nil
	}
	n := int64(0)
	for id, f := range m.Files {
		if f.UserHost != nil && *f.UserHost == host {
			delete(m.Files, id)
			n++
		}
	}
	return n, nil
}

func (m *MockDriveFileRepository) ListByHost(host string) ([]*model.DriveFile, error) {
	if host == "" {
		return nil, nil
	}
	var rows []*model.DriveFile
	for _, f := range m.Files {
		if f.UserHost != nil && *f.UserHost == host {
			rows = append(rows, f)
		}
	}
	return rows, nil
}
