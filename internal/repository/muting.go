package repository

import (
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// MutingRepository provides data access for the `muting` table.
type MutingRepository interface {
	Create(m *model.Muting) error
	Delete(m *model.Muting) error
	FindByPair(muterID, muteeID string) (*model.Muting, error)
	Exists(muterID, muteeID string) (bool, error)
	// ListByMuter supports cursor (sinceID/untilID) and offset pagination.
	// Cursor 指定時は offset を無視 (upstream makePaginationQuery と一致)。
	ListByMuter(muterID, sinceID, untilID string, limit, offset int) ([]*model.Muting, error)
	// ListMuteeIDs returns the muteeIDs of active (non-expired) mute rows
	// for muterID. timeline endpoint で muted user の note を除外する
	// filter 用 (#874)。
	ListMuteeIDs(muterID string) ([]string, error)
	// DeleteExpired physically removes muting rows whose expiresAt has
	// passed. Read filters (ListMuteeIDs/Exists) already exclude them; this
	// is the active prune run by the checkExpiredMutings cron (#1563).
	// Returns the number of rows deleted.
	DeleteExpired(now time.Time) (int64, error)
}

type mutingRepository struct {
	db *gorm.DB
}

// NewMutingRepository creates a new MutingRepository.
func NewMutingRepository(db *gorm.DB) MutingRepository {
	return &mutingRepository{db: db}
}

func (r *mutingRepository) Create(m *model.Muting) error {
	return r.db.Create(m).Error
}

func (r *mutingRepository) Delete(m *model.Muting) error {
	return r.db.Delete(m).Error
}

func (r *mutingRepository) FindByPair(muterID, muteeID string) (*model.Muting, error) {
	var m model.Muting
	if err := r.db.Where("\"muterId\" = ? AND \"muteeId\" = ?", muterID, muteeID).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// Exists reports whether muter has an active (non-expired) mute on mutee.
func (r *mutingRepository) Exists(muterID, muteeID string) (bool, error) {
	var count int64
	now := time.Now()
	if err := r.db.Model(&model.Muting{}).
		Where("\"muterId\" = ? AND \"muteeId\" = ?", muterID, muteeID).
		Where("\"expiresAt\" IS NULL OR \"expiresAt\" > ?", now).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *mutingRepository) ListByMuter(muterID, sinceID, untilID string, limit, offset int) ([]*model.Muting, error) {
	q := r.db.Where(`"muterId" = ?`, muterID)
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	q = q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit)
	if sinceID == "" && untilID == "" && offset > 0 {
		q = q.Offset(offset)
	}
	var rows []*model.Muting
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListMuteeIDs returns muteeIDs where muterID has an active (non-expired)
// mute. timeline endpoint で muted user の note を除外する filter 用 (#874)。
func (r *mutingRepository) ListMuteeIDs(muterID string) ([]string, error) {
	if muterID == "" {
		return nil, nil
	}
	var ids []string
	now := time.Now()
	if err := r.db.Model(&model.Muting{}).
		Where(`"muterId" = ?`, muterID).
		Where(`"expiresAt" IS NULL OR "expiresAt" > ?`, now).
		Pluck(`"muteeId"`, &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *mutingRepository) DeleteExpired(now time.Time) (int64, error) {
	res := r.db.Where(`"expiresAt" IS NOT NULL AND "expiresAt" < ?`, now).Delete(&model.Muting{})
	return res.RowsAffected, res.Error
}

// RenoteMutingRepository provides data access for the `renote_muting` table.
type RenoteMutingRepository interface {
	Create(m *model.RenoteMuting) error
	Delete(m *model.RenoteMuting) error
	FindByPair(muterID, muteeID string) (*model.RenoteMuting, error)
	Exists(muterID, muteeID string) (bool, error)
	// ListByMuter supports cursor (sinceID/untilID) and offset pagination.
	ListByMuter(muterID, sinceID, untilID string, limit, offset int) ([]*model.RenoteMuting, error)
	// ListMuteeIDs returns the muteeIDs of renote-mute rows for muterID.
	// timeline endpoint で renote-mute されたユーザーの pure renote を
	// 除外する filter 用 (#903)。MutingRepository.ListMuteeIDs と同 shape。
	ListMuteeIDs(muterID string) ([]string, error)
}

type renoteMutingRepository struct {
	db *gorm.DB
}

// NewRenoteMutingRepository creates a new RenoteMutingRepository.
func NewRenoteMutingRepository(db *gorm.DB) RenoteMutingRepository {
	return &renoteMutingRepository{db: db}
}

func (r *renoteMutingRepository) Create(m *model.RenoteMuting) error {
	return r.db.Create(m).Error
}

func (r *renoteMutingRepository) Delete(m *model.RenoteMuting) error {
	return r.db.Delete(m).Error
}

func (r *renoteMutingRepository) FindByPair(muterID, muteeID string) (*model.RenoteMuting, error) {
	var m model.RenoteMuting
	if err := r.db.Where("\"muterId\" = ? AND \"muteeId\" = ?", muterID, muteeID).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *renoteMutingRepository) Exists(muterID, muteeID string) (bool, error) {
	var count int64
	if err := r.db.Model(&model.RenoteMuting{}).
		Where("\"muterId\" = ? AND \"muteeId\" = ?", muterID, muteeID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListMuteeIDs returns muteeIDs of all renote-mute rows for muterID.
// renote_muting には expiresAt が無いので active filter は不要 (= 解除は
// row delete で完結)。MutingRepository.ListMuteeIDs と同 shape (#903)。
func (r *renoteMutingRepository) ListMuteeIDs(muterID string) ([]string, error) {
	if muterID == "" {
		return nil, nil
	}
	var ids []string
	if err := r.db.Model(&model.RenoteMuting{}).
		Where(`"muterId" = ?`, muterID).
		Pluck(`"muteeId"`, &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *renoteMutingRepository) ListByMuter(muterID, sinceID, untilID string, limit, offset int) ([]*model.RenoteMuting, error) {
	q := r.db.Where(`"muterId" = ?`, muterID)
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	q = q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit)
	if sinceID == "" && untilID == "" && offset > 0 {
		q = q.Offset(offset)
	}
	var rows []*model.RenoteMuting
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
