package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// RelayRepository provides data access for the `relay` table.
type RelayRepository interface {
	Create(r *model.Relay) error
	FindByID(id string) (*model.Relay, error)
	List() ([]*model.Relay, error)
	ListByStatus(status string) ([]*model.Relay, error)
	UpdateStatus(id, status string) error
	Delete(id string) error
}

type relayRepository struct {
	db *gorm.DB
}

// NewRelayRepository creates a RelayRepository backed by gorm.
func NewRelayRepository(db *gorm.DB) RelayRepository {
	return &relayRepository{db: db}
}

func (r *relayRepository) Create(rel *model.Relay) error {
	return r.db.Create(rel).Error
}

func (r *relayRepository) FindByID(id string) (*model.Relay, error) {
	if !storable(id) {
		return nil, ErrNotFound
	}
	var rel model.Relay
	if err := r.db.Where(`"id" = ?`, id).First(&rel).Error; err != nil {
		return nil, err
	}
	return &rel, nil
}

func (r *relayRepository) List() ([]*model.Relay, error) {
	var rels []*model.Relay
	if err := r.db.Order(`"id" DESC`).Find(&rels).Error; err != nil {
		return nil, err
	}
	return rels, nil
}

func (r *relayRepository) ListByStatus(status string) ([]*model.Relay, error) {
	var rels []*model.Relay
	if err := r.db.Where(`"status" = ?`, status).Find(&rels).Error; err != nil {
		return nil, err
	}
	return rels, nil
}

func (r *relayRepository) UpdateStatus(id, status string) error {
	return r.db.Model(&model.Relay{}).
		Where(`"id" = ?`, id).
		Update("status", status).Error
}

func (r *relayRepository) Delete(id string) error {
	return r.db.Where(`"id" = ?`, id).Delete(&model.Relay{}).Error
}
