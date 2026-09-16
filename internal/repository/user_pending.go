package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// UserPendingRepository manages pending user registrations (invite-based
// signup with email confirmation).
type UserPendingRepository interface {
	Create(p *model.UserPending) error
	FindByCode(code string) (*model.UserPending, error)
	Delete(id string) error
}

type userPendingRepository struct {
	db *gorm.DB
}

// NewUserPendingRepository creates a new UserPendingRepository.
func NewUserPendingRepository(db *gorm.DB) UserPendingRepository {
	return &userPendingRepository{db: db}
}

func (r *userPendingRepository) Create(p *model.UserPending) error {
	return r.db.Create(p).Error
}

func (r *userPendingRepository) FindByCode(code string) (*model.UserPending, error) {
	if !storable(code) {
		return nil, ErrNotFound
	}
	var row model.UserPending
	if err := r.db.Where("code = ?", code).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *userPendingRepository) Delete(id string) error {
	return r.db.Where("id = ?", id).Delete(&model.UserPending{}).Error
}
