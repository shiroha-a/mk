package testutil

import (
	"strings"

	"github.com/shiroha-a/mk/internal/model"
)

// MockDriveFileRepository is a test double for repository.DriveFileRepository.
type MockDriveFileRepository struct {
	Files map[string]*model.DriveFile // keyed by ID
	// EmojiReferencedURLs は DeleteOrphans の guard を mock するための「emoji
	// が参照している drive_file.url 集合」。production の SQL では
	// `NOT EXISTS (SELECT 1 FROM emoji WHERE originalUrl = drive_file.url)`
	// で判定する経路を test 用に模倣する (#722)。空なら guard 無効 (= 旧来の
	// userId NULL 全削除) で既存テスト互換。
	EmojiReferencedURLs map[string]bool

	// BulkFolderUserID / BulkFolderFileIDs record the last UpdateBulkFolder
	// call so handler tests can assert the owning user is scoped (IDOR guard).
	BulkFolderUserID  string
	BulkFolderFileIDs []string

	// 以下は bulk cleanup (clean-remote-files / delete-all-files) のエラー経路を
	// テストするための注入フィールド。nil なら通常動作。
	ListRemoteCacheErr error
	ListByUserAllErr   error
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

func (m *MockDriveFileRepository) FindByURI(uri string) (*model.DriveFile, error) {
	if uri == "" {
		return nil, ErrNotFound
	}
	for _, f := range m.Files {
		if f.URI != nil && *f.URI == uri {
			return f, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockDriveFileRepository) FindByAccessKey(accessKey string) (*model.DriveFile, error) {
	if accessKey == "" {
		return nil, ErrNotFound
	}
	for _, f := range m.Files {
		if f.AccessKey != nil && *f.AccessKey == accessKey {
			return f, nil
		}
		if f.ThumbnailAccessKey != nil && *f.ThumbnailAccessKey == accessKey {
			return f, nil
		}
		if f.WebpublicAccessKey != nil && *f.WebpublicAccessKey == accessKey {
			return f, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MockDriveFileRepository) FindByAnyAccessKey(accessKey string) (*model.DriveFile, error) {
	return m.FindByAccessKey(accessKey)
}

func (m *MockDriveFileRepository) Update(id string, fields map[string]any) error {
	f, ok := m.Files[id]
	if !ok {
		return ErrNotFound
	}
	applyDriveFileFields(f, fields)
	return nil
}

func (m *MockDriveFileRepository) FindByName(userID, name string, _ *string) ([]*model.DriveFile, error) {
	var result []*model.DriveFile
	for _, f := range m.Files {
		if f.UserID != nil && *f.UserID == userID && f.Name == name {
			result = append(result, f)
		}
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

func (m *MockDriveFileRepository) ListByUser(userID string, folderID *string, untilID, sinceID string, limit int) ([]*model.DriveFile, error) {
	var rows []*model.DriveFile
	for _, f := range m.Files {
		if f.UserID == nil || *f.UserID != userID {
			continue
		}
		if folderID == nil {
			if f.FolderID != nil {
				continue
			}
		} else {
			if f.FolderID == nil || *f.FolderID != *folderID {
				continue
			}
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
		if parentID == nil {
			if f.ParentID != nil {
				continue
			}
		} else {
			if f.ParentID == nil || *f.ParentID != *parentID {
				continue
			}
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

func (m *MockDriveFolderRepository) FindByName(userID, name string, _ *string) ([]*model.DriveFolder, error) {
	var result []*model.DriveFolder
	for _, f := range m.Folders {
		if f.UserID != nil && *f.UserID == userID && f.Name == name {
			result = append(result, f)
		}
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
		if fileType != "" && !strings.HasPrefix(f.Type, fileType) {
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
	// id DESC
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
		if fileType != "" && !strings.HasPrefix(f.Type, fileType) {
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
	// id DESC
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
// userId NULL かつ EmojiReferencedURLs に URL が含まれない drive file を
// 削除する。`EmojiReferencedURLs` を空のままにすると元来の挙動 (userId
// NULL を全削除) と等価になるので、既存テストは無改修で通る (#722)。
func (m *MockDriveFileRepository) DeleteOrphans() (int64, error) {
	n := int64(0)
	for id, f := range m.Files {
		if f.UserID != nil {
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
