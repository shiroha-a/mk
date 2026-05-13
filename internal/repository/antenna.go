package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// AntennaRepository provides data access for the `antenna` table.
type AntennaRepository interface {
	Create(a *model.Antenna) error
	FindByID(id string) (*model.Antenna, error)
	UpdateFields(antennaID string, fields map[string]any) error
	Delete(a *model.Antenna) error
	ListByUser(userID string) ([]*model.Antenna, error)
	// CountByUser returns the number of antennas owned by userID. Used by
	// antennaLimit policy gate so we don't have to fetch all rows just to
	// count them (#1029 PR-1 follow-up).
	CountByUser(userID string) (int64, error)
	ListAllActive() ([]*model.Antenna, error)
}

type antennaRepository struct {
	db *gorm.DB
}

// NewAntennaRepository creates a new AntennaRepository.
func NewAntennaRepository(db *gorm.DB) AntennaRepository {
	return &antennaRepository{db: db}
}

func (r *antennaRepository) Create(a *model.Antenna) error {
	return r.db.Create(a).Error
}

func (r *antennaRepository) FindByID(id string) (*model.Antenna, error) {
	var a model.Antenna
	if err := r.db.First(&a, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// UpdateFields applies a map of column → value updates to the antenna row.
func (r *antennaRepository) UpdateFields(antennaID string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.Antenna{}).Where("id = ?", antennaID).Updates(fields).Error
}

func (r *antennaRepository) Delete(a *model.Antenna) error {
	return r.db.Delete(a).Error
}

// ListByUser returns the antennas owned by the given user, ordered by id desc.
func (r *antennaRepository) ListByUser(userID string) ([]*model.Antenna, error) {
	var rows []*model.Antenna
	if err := r.db.Where("\"userId\" = ?", userID).
		Order("id DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CountByUser returns the number of antennas owned by the given user.
func (r *antennaRepository) CountByUser(userID string) (int64, error) {
	var count int64
	if err := r.db.Model(&model.Antenna{}).
		Where(`"userId" = ?`, userID).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// ListAllActive returns every antenna with isActive=true. NoteCreate イベント
// ごとに走査するため、antenna 数が膨大な場合は将来 page 単位での走査に切り替え
// ることになる。Phase 4.3 ではシンプルに all を返す。
func (r *antennaRepository) ListAllActive() ([]*model.Antenna, error) {
	var rows []*model.Antenna
	if err := r.db.Where("\"isActive\" = ?", true).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
