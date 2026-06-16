package repository

import (
	"strings"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// DriveFileRepository provides data access for the `drive_file` table.
type DriveFileRepository interface {
	Create(f *model.DriveFile) error
	FindByID(id string) (*model.DriveFile, error)
	FindByIDs(ids []string) ([]*model.DriveFile, error)
	FindByMD5(userID, md5 string) (*model.DriveFile, error)
	// FindAllByMD5 returns every file of the user with the given md5 hash.
	// upstream drive/files/find-by-hash は findBy({md5, userId}) で一致
	// 全件を返すため、最新 1 件の FindByMD5 (upload dedup 用) と別に持つ
	// (#1564)。
	FindAllByMD5(userID, md5 string) ([]*model.DriveFile, error)
	FindByAccessKey(accessKey string) (*model.DriveFile, error)
	// FindByAnyURL looks up a DriveFile whose url / webpublicUrl /
	// thumbnailUrl matches. upstream drive/files/show の url 指定検索
	// (anyOf param) で使う (#1564)。3 列の index は migration
	// 000059-000061 で追加済み (#1625、BitmapOr で結合される)。
	FindByAnyURL(url string) (*model.DriveFile, error)
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
	// ListByUser returns the user's drive files (#1564 で filter/sort 対応):
	//   - folderID: anyFolder=false のとき nil は root (folderId IS NULL)
	//   - anyFolder: true で folder 条件を付けない (upstream drive/stream は
	//     全 folder 横断で返す)
	//   - fileType: MIME filter。"image/*" 形式は prefix match、それ以外は
	//     完全一致 (upstream drive/files.ts と同 semantics)。空は無条件
	//   - sort: "+createdAt"|"-createdAt"|"+name"|"-name"|"+size"|"-size"|""
	//     (空は sinceID/untilID 由来の id 順。createdAt は id 列で代理)
	ListByUser(userID string, folderID *string, anyFolder bool, fileType, sort, untilID, sinceID string, limit int) ([]*model.DriveFile, error)
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
	// UpdateBulkFolder moves the given files into folderID, scoped to the
	// owning user. The userID predicate prevents a caller from moving drive
	// files they do not own (IDOR): upstream drive/files/move-bulk only ever
	// touches the caller's own rows.
	UpdateBulkFolder(userID string, fileIDs []string, folderID *string) error
	// DeleteOrphans removes rows whose userId is NULL. Returns affected count.
	DeleteOrphans() (int64, error)
	// ListOrphans returns up to limit rows DeleteOrphans would delete (userId
	// IS NULL, not referenced by any emoji) so the caller can delete each
	// orphan's object-storage bytes before the DB rows (admin/drive/cleanup,
	// #1724)。Order is unspecified; mirrors ListRemoteCache's batched shape.
	ListOrphans(limit int) ([]*model.DriveFile, error)
	// DeleteRemoteCache removes cached remote files (isLink=false with host set)
	// — the rows whose actual bytes are cached locally / in object storage.
	// Returns affected count. Used as the DB-only fallback for
	// admin/drive/clean-remote-files when no storage backend is wired.
	DeleteRemoteCache() (int64, error)
	// ListRemoteCache returns up to limit cached remote files (isLink=false with
	// host set) so the caller can delete their object-storage objects before the
	// DB rows. Order is unspecified.
	ListRemoteCache(limit int) ([]*model.DriveFile, error)
	// ListByUserAll returns up to limit files owned by userID across all folders
	// (admin/delete-all-files-of-a-user storage cleanup).
	ListByUserAll(userID string, limit int) ([]*model.DriveFile, error)
	// DeleteByIDs removes the given rows in one statement. Returns affected count.
	DeleteByIDs(ids []string) (int64, error)
	// DeleteByUser removes every drive_file owned by userID. Returns affected
	// count. Used by admin/delete-all-files-of-a-user.
	DeleteByUser(userID string) (int64, error)
	// DeleteByHost removes every drive_file whose userHost matches the given
	// remote instance host. Returns affected count. Used by
	// admin/federation/delete-all-files (#587)。
	DeleteByHost(host string) (int64, error)
	// ListByHost returns every drive_file whose userHost matches the given
	// remote instance host. Used by the drive Service to physically delete each
	// file's storage objects before the bulk row delete (#1772)。
	ListByHost(host string) ([]*model.DriveFile, error)
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

// FindAllByMD5 returns every file of the user with the given md5 hash,
// oldest first (id ASC). upstream find-by-hash の findBy({md5, userId}) は
// order 未指定だが、決定的な応答のため id 昇順に固定する。
func (r *driveFileRepository) FindAllByMD5(userID, md5 string) ([]*model.DriveFile, error) {
	var files []*model.DriveFile
	if err := r.db.
		Where("\"userId\" = ? AND md5 = ?", userID, md5).
		Order("id ASC").
		Find(&files).Error; err != nil {
		return nil, err
	}
	return files, nil
}

// FindByAnyURL resolves a file by matching url, webpublicUrl, or
// thumbnailUrl (upstream drive/files/show の url anyOf 検索、#1564)。
// OR 3 条件は migration 000059-000061 (#1625) の各列 index を BitmapOr で
// 束ねて解決される (seq scan 回避)。
func (r *driveFileRepository) FindByAnyURL(url string) (*model.DriveFile, error) {
	if url == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var f model.DriveFile
	if err := r.db.Where(
		`"url" = ? OR "webpublicUrl" = ? OR "thumbnailUrl" = ?`,
		url, url, url,
	).First(&f).Error; err != nil {
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

// ListByUser returns the user's drive files filtered/sorted per the
// interface doc (#1564).
func (r *driveFileRepository) ListByUser(userID string, folderID *string, anyFolder bool, fileType, sort, untilID, sinceID string, limit int) ([]*model.DriveFile, error) {
	var rows []*model.DriveFile
	q := r.db.Where("\"userId\" = ?", userID)
	if !anyFolder {
		if folderID == nil {
			q = q.Where("\"folderId\" IS NULL")
		} else {
			q = q.Where("\"folderId\" = ?", *folderID)
		}
	}
	if fileType != "" {
		// upstream files.ts: `/*` 終端は `type.replace('/*','/') + '%'` の
		// prefix LIKE、それ以外は完全一致。
		if strings.HasSuffix(fileType, "/*") {
			q = q.Where(`"type" LIKE ?`, strings.TrimSuffix(fileType, "*")+"%")
		} else {
			q = q.Where(`"type" = ?`, fileType)
		}
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	// upstream files.ts は sort 指定時に makePaginationQuery の orderBy を
	// 上書きする (cursor の WHERE 条件はそのまま残る)。createdAt は id 列で
	// 代理 (= aidx は時系列順)。
	order := ""
	switch sort {
	case "+createdAt":
		order = "id DESC"
	case "-createdAt":
		order = "id ASC"
	case "+name":
		order = "name DESC"
	case "-name":
		order = "name ASC"
	case "+size":
		order = "size DESC"
	case "-size":
		order = "size ASC"
	default:
		order = paginationOrder(sinceID, untilID, "id")
	}
	if err := q.Order(order).Limit(limit).Find(&rows).Error; err != nil {
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

func (r *driveFileRepository) UpdateBulkFolder(userID string, fileIDs []string, folderID *string) error {
	// userId で絞ることで他人の DriveFile を移動できる IDOR を防ぐ (#parity)。
	return r.db.Model(&model.DriveFile{}).
		Where("id IN ?", fileIDs).
		Where(`"userId" = ?`, userID).
		Update("folderId", folderID).Error
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
		// upstream admin/drive/files.ts:78-84: `/*` 終端は prefix LIKE
		// (type.replace('/*','/')+'%')、それ以外は完全一致 (#1772、ListByUser と
		// 同 semantics)。以前は無条件 prefix LIKE で image/* がほぼ 0 件、
		// image/png が過剰マッチしていた。
		if strings.HasSuffix(fileType, "/*") {
			q = q.Where(`"type" LIKE ?`, strings.TrimSuffix(fileType, "*")+"%")
		} else {
			q = q.Where(`"type" = ?`, fileType)
		}
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
		// upstream files.ts と同じく `/*` 終端は prefix LIKE、それ以外は完全一致
		// (#1772、ListForAdmin と semantics 統一)。
		if strings.HasSuffix(fileType, "/*") {
			q = q.Where(`"type" LIKE ?`, strings.TrimSuffix(fileType, "*")+"%")
		} else {
			q = q.Where(`"type" = ?`, fileType)
		}
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
	tx := r.db.Where(orphanWhere).Delete(&model.DriveFile{})
	return tx.RowsAffected, tx.Error
}

// orphanWhere は orphan file (userId IS NULL かつ emoji 非参照) を選ぶ条件。
// DeleteOrphans / ListOrphans で同一 guard を共有する (#1724)。
const orphanWhere = `"userId" IS NULL AND NOT EXISTS (
	SELECT 1 FROM "emoji" e
	WHERE e."originalUrl" = "drive_file"."url"
	   OR e."publicUrl"   = "drive_file"."url"
)`

func (r *driveFileRepository) ListOrphans(limit int) ([]*model.DriveFile, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []*model.DriveFile
	if err := r.db.Where(orphanWhere).Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *driveFileRepository) DeleteRemoteCache() (int64, error) {
	// upstream CleanRemoteFilesProcessorService は userHost IS NOT NULL AND
	// isLink=false (= 実体をキャッシュしているリモートファイル) を消す。旧実装は
	// isLink=true (= 実体を持たない link-only proxy) を消しており条件が逆だった。
	tx := r.db.Where(`"isLink" = false AND "userHost" IS NOT NULL`).Delete(&model.DriveFile{})
	return tx.RowsAffected, tx.Error
}

func (r *driveFileRepository) ListRemoteCache(limit int) ([]*model.DriveFile, error) {
	if limit <= 0 {
		limit = 100
	}
	var files []*model.DriveFile
	err := r.db.Where(`"isLink" = false AND "userHost" IS NOT NULL`).
		Order("id DESC").Limit(limit).Find(&files).Error
	return files, err
}

func (r *driveFileRepository) ListByUserAll(userID string, limit int) ([]*model.DriveFile, error) {
	if userID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	var files []*model.DriveFile
	err := r.db.Where(`"userId" = ?`, userID).
		Order("id DESC").Limit(limit).Find(&files).Error
	return files, err
}

func (r *driveFileRepository) DeleteByIDs(ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx := r.db.Where("id IN ?", ids).Delete(&model.DriveFile{})
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

func (r *driveFileRepository) ListByHost(host string) ([]*model.DriveFile, error) {
	if host == "" {
		return nil, nil
	}
	var rows []*model.DriveFile
	if err := r.db.Where(`"userHost" = ?`, host).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
