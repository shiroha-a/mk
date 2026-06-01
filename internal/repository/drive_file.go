package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// DriveFileRepository provides data access for the `drive_file` table.
type DriveFileRepository interface {
	Create(f *model.DriveFile) error
	FindByID(id string) (*model.DriveFile, error)
	FindByIDs(ids []string) ([]*model.DriveFile, error)
	FindByMD5(userID, md5 string) (*model.DriveFile, error)
	FindByAccessKey(accessKey string) (*model.DriveFile, error)
	// FindByAnyAccessKey looks up a DriveFile whose primary / thumbnail /
	// webpublic access key matches. Used by `/files/:accessKey` to resolve
	// storedInternal=true rows that the configured storage backend (S3)
	// does not hold (#1414).
	FindByAnyAccessKey(accessKey string) (*model.DriveFile, error)
	// FindByURI looks up a drive_file by its AP `uri` field. Used for
	// deduping remote attachments on inbound Note ingest (#378).
	FindByURI(uri string) (*model.DriveFile, error)
	Update(id string, fields map[string]any) error
	Delete(f *model.DriveFile) error
	ListByUser(userID string, folderID *string, untilID, sinceID string, limit int) ([]*model.DriveFile, error)
	// ListForAdmin returns drive files across all users with optional
	// userID / origin / host / type filters. When userID is non-empty,
	// origin / host are ignored (upstream Misskey の semantics と一致)。
	// origin is "local" / "remote" / "combined" ("combined" is the
	// default). Empty host / type are no-ops.
	ListForAdmin(userID, origin, host, fileType, untilID, sinceID string, limit int) ([]*model.DriveFile, error)
	// ListSystemFiles returns drive files that are not owned by any user
	// (userId IS NULL). #670 で導入した emoji copy / import zip の保管先
	// system file を admin UI から可視化するための一覧 API (#686)。
	// fileType は MIME prefix match (空なら無制約)、ID 範囲は他の admin
	// listing と同一の semantics。
	ListSystemFiles(fileType, untilID, sinceID string, limit int) ([]*model.DriveFile, error)
	FindByName(userID, name string, folderID *string) ([]*model.DriveFile, error)
	// CountByFolder は drive/folders/show の detail mode pack 用に、
	// 指定 folder 内に直下で属する file 数を返す (#845)。
	CountByFolder(folderID string) (int, error)
	ExistsByMD5(userID, md5 string) (bool, error)
	ListByFileIDs(fileIDs []string) ([]*model.DriveFile, error)
	UsageByUser(userID string) (int64, error)
	UpdateBulkFolder(fileIDs []string, folderID *string) error
	// DeleteOrphans removes rows whose userId is NULL. Returns affected count.
	DeleteOrphans() (int64, error)
	// DeleteRemoteCache removes remote cache rows (isLink=true with host set).
	// Returns affected count.
	DeleteRemoteCache() (int64, error)
	// DeleteByUser removes every drive_file owned by userID. Returns affected
	// count. Used by admin/delete-all-files-of-a-user.
	DeleteByUser(userID string) (int64, error)
	// DeleteByHost removes every drive_file whose userHost matches the given
	// remote instance host. Returns affected count. Used by
	// admin/federation/delete-all-files (#587)。
	DeleteByHost(host string) (int64, error)
}

type driveFileRepository struct {
	db *gorm.DB
}

// NewDriveFileRepository creates a new DriveFileRepository.
func NewDriveFileRepository(db *gorm.DB) DriveFileRepository {
	return &driveFileRepository{db: db}
}

func (r *driveFileRepository) Create(f *model.DriveFile) error {
	return r.db.Create(f).Error
}

func (r *driveFileRepository) FindByID(id string) (*model.DriveFile, error) {
	var f model.DriveFile
	if err := r.db.First(&f, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &f, nil
}

func (r *driveFileRepository) FindByIDs(ids []string) ([]*model.DriveFile, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var files []*model.DriveFile
	if err := r.db.Where("id IN ?", ids).Find(&files).Error; err != nil {
		return nil, err
	}
	return files, nil
}

// FindByMD5 returns the user's most recent file with the given md5 hash.
func (r *driveFileRepository) FindByMD5(userID, md5 string) (*model.DriveFile, error) {
	var f model.DriveFile
	if err := r.db.
		Where("\"userId\" = ? AND md5 = ?", userID, md5).
		Order("id DESC").
		First(&f).Error; err != nil {
		return nil, err
	}
	return &f, nil
}

func (r *driveFileRepository) FindByURI(uri string) (*model.DriveFile, error) {
	if uri == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var f model.DriveFile
	if err := r.db.Where("uri = ?", uri).First(&f).Error; err != nil {
		return nil, err
	}
	return &f, nil
}

// FindByAccessKey looks up a DriveFile whose primary access key matches.
// Returns gorm.ErrRecordNotFound when none match. Used by the media proxy
// to swap a `?preview` request for the cached webpublic / thumbnail variant
// when serving a local file (#637 M1)。
//
// 旧実装は thumbnail/webpublic access key も OR で引いていたが、呼び出し
// 側 (mediaproxy.swapToVariant) は primary key 一致のときしか swap しない
// ため、それらの match は dead clause だった。primary 単独 + unique index
// で planner も最短経路に落とせる (#637 review UR-014)。
func (r *driveFileRepository) FindByAccessKey(accessKey string) (*model.DriveFile, error) {
	if accessKey == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var f model.DriveFile
	if err := r.db.Where("\"accessKey\" = ?", accessKey).First(&f).Error; err != nil {
		return nil, err
	}
	return &f, nil
}

// FindByAnyAccessKey resolves an access key by matching the primary,
// thumbnail, or webpublic column. `/files/:accessKey` 経由でアクセスされる
// access key は元データ・thumbnail・webpublic のいずれかなので、3 列を OR で
// 引く必要がある (#1414)。3 列とも unique index 付きで planner は bitmap-or
// に落とせる。primary のみで充足する mediaproxy.swapToVariant とは別経路。
func (r *driveFileRepository) FindByAnyAccessKey(accessKey string) (*model.DriveFile, error) {
	if accessKey == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var f model.DriveFile
	if err := r.db.Where(
		`"accessKey" = ? OR "thumbnailAccessKey" = ? OR "webpublicAccessKey" = ?`,
		accessKey, accessKey, accessKey,
	).First(&f).Error; err != nil {
		return nil, err
	}
	return &f, nil
}

func (r *driveFileRepository) Update(id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.DriveFile{}).Where("id = ?", id).Updates(fields).Error
}

func (r *driveFileRepository) Delete(f *model.DriveFile) error {
	return r.db.Delete(f).Error
}

// ListByUser returns the user's drive files. folderID=nil means root files
// (folderId IS NULL); otherwise restricts to that folder.
func (r *driveFileRepository) ListByUser(userID string, folderID *string, untilID, sinceID string, limit int) ([]*model.DriveFile, error) {
	var rows []*model.DriveFile
	q := r.db.Where("\"userId\" = ?", userID)
	if folderID == nil {
		q = q.Where("\"folderId\" IS NULL")
	} else {
		q = q.Where("\"folderId\" = ?", *folderID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *driveFileRepository) FindByName(userID, name string, folderID *string) ([]*model.DriveFile, error) {
	q := r.db.Where(`"userId" = ? AND "name" = ?`, userID, name)
	if folderID != nil {
		q = q.Where(`"folderId" = ?`, *folderID)
	} else {
		q = q.Where(`"folderId" IS NULL`)
	}
	var files []*model.DriveFile
	if err := q.Find(&files).Error; err != nil {
		return nil, err
	}
	return files, nil
}

func (r *driveFileRepository) ExistsByMD5(userID, md5 string) (bool, error) {
	var count int64
	if err := r.db.Model(&model.DriveFile{}).Where(`"userId" = ? AND "md5" = ?`, userID, md5).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// CountByFolder returns the number of files directly in the given folder.
func (r *driveFileRepository) CountByFolder(folderID string) (int, error) {
	var n int64
	if err := r.db.Model(&model.DriveFile{}).Where(`"folderId" = ?`, folderID).Count(&n).Error; err != nil {
		return 0, err
	}
	return int(n), nil
}

func (r *driveFileRepository) ListByFileIDs(fileIDs []string) ([]*model.DriveFile, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	var files []*model.DriveFile
	if err := r.db.Where("id IN ?", fileIDs).Find(&files).Error; err != nil {
		return nil, err
	}
	return files, nil
}

func (r *driveFileRepository) UsageByUser(userID string) (int64, error) {
	var total int64
	if err := r.db.Model(&model.DriveFile{}).Where(`"userId" = ?`, userID).Select("COALESCE(SUM(size), 0)").Scan(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

func (r *driveFileRepository) UpdateBulkFolder(fileIDs []string, folderID *string) error {
	return r.db.Model(&model.DriveFile{}).Where("id IN ?", fileIDs).Update("folderId", folderID).Error
}

func (r *driveFileRepository) ListForAdmin(userID, origin, host, fileType, untilID, sinceID string, limit int) ([]*model.DriveFile, error) {
	q := r.db.Model(&model.DriveFile{})
	if userID != "" {
		// upstream は userId 指定時に origin / hostname を読まないので
		// それに合わせる。`/admin/user/<id>` のドライブタブが userId 単独で
		// 引いてくるが、handler 経由で origin="combined" が落ちてくると
		// 全 remote が混ざるバグになるため (#471)。
		q = q.Where(`"userId" = ?`, userID)
	} else {
		switch origin {
		case "local":
			q = q.Where(`"userHost" IS NULL`)
		case "remote":
			q = q.Where(`"userHost" IS NOT NULL`)
		}
		if host != "" {
			q = q.Where(`"userHost" = ?`, host)
		}
	}
	if fileType != "" {
		// type is mimetype prefix match (e.g. "image/" matches image/png)
		q = q.Where(`"type" LIKE ?`, fileType+"%")
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	var rows []*model.DriveFile
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *driveFileRepository) ListSystemFiles(fileType, untilID, sinceID string, limit int) ([]*model.DriveFile, error) {
	q := r.db.Model(&model.DriveFile{}).Where(`"userId" IS NULL`)
	if fileType != "" {
		// MIME prefix match: ListForAdmin と semantics 統一 (e.g. "image/" で
		// image/* を全部拾う)。
		q = q.Where(`"type" LIKE ?`, fileType+"%")
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	var rows []*model.DriveFile
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// DeleteOrphans removes drive_file rows whose userId is NULL **and** are not
// referenced by any custom emoji. Pure orphan files (例: 取り込み中断で残った
// 中間 zip) は削除対象だが、#670 で導入された system 所有 emoji 画像 file
// (emoji.originalUrl = drive_file.url で結ばれている) は cleanup で巻き込ま
// れないように除外する (#722)。
//
// upstream Misskey TS の cleanup は単純に userId NULL を全消ししており、
// 同様に emoji 画像も巻き込む構造的バグを内包しているが、mk-go では本
// guard で local emoji asset を保護する。
func (r *driveFileRepository) DeleteOrphans() (int64, error) {
	tx := r.db.Where(
		`"userId" IS NULL AND NOT EXISTS (
			SELECT 1 FROM "emoji" e
			WHERE e."originalUrl" = "drive_file"."url"
			   OR e."publicUrl"   = "drive_file"."url"
		)`,
	).Delete(&model.DriveFile{})
	return tx.RowsAffected, tx.Error
}

func (r *driveFileRepository) DeleteRemoteCache() (int64, error) {
	tx := r.db.Where(`"isLink" = true AND "userHost" IS NOT NULL`).Delete(&model.DriveFile{})
	return tx.RowsAffected, tx.Error
}

func (r *driveFileRepository) DeleteByUser(userID string) (int64, error) {
	if userID == "" {
		return 0, nil
	}
	tx := r.db.Where(`"userId" = ?`, userID).Delete(&model.DriveFile{})
	return tx.RowsAffected, tx.Error
}

func (r *driveFileRepository) DeleteByHost(host string) (int64, error) {
	if host == "" {
		return 0, nil
	}
	// userHost が remote のリモートユーザーアップロード分のみ削除。
	// ローカル user (userHost IS NULL) は誤って巻き込まないよう明示一致のみ。
	tx := r.db.Where(`"userHost" = ?`, host).Delete(&model.DriveFile{})
	return tx.RowsAffected, tx.Error
}
