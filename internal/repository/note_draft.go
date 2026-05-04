package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// NoteDraftRepository handles note_draft persistence.
type NoteDraftRepository interface {
	Create(draft *model.NoteDraft) error
	FindByIDAndUser(id, userID string) (*model.NoteDraft, error)
	ListByUser(userID string, limit int) ([]*model.NoteDraft, error)
	Update(draft *model.NoteDraft) error
	// Delete returns the number of rows affected so callers can detect
	// "no such draft" without a separate FindByIDAndUser pre-check (single
	// SQL round-trip + no TOCTOU race)。0 = 該当 draft なし、1 = 削除成功。
	Delete(id, userID string) (int64, error)
	CountByUser(userID string) (int64, error)
}

type noteDraftRepository struct {
	db *gorm.DB
}

// NewNoteDraftRepository creates a new NoteDraftRepository.
func NewNoteDraftRepository(db *gorm.DB) NoteDraftRepository {
	return &noteDraftRepository{db: db}
}

func (r *noteDraftRepository) Create(draft *model.NoteDraft) error {
	return r.db.Create(draft).Error
}

func (r *noteDraftRepository) FindByIDAndUser(id, userID string) (*model.NoteDraft, error) {
	var d model.NoteDraft
	if err := r.db.Where(`"id" = ? AND "userId" = ?`, id, userID).First(&d).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *noteDraftRepository) ListByUser(userID string, limit int) ([]*model.NoteDraft, error) {
	if limit <= 0 {
		limit = 20
	}
	var drafts []*model.NoteDraft
	if err := r.db.Where(`"userId" = ?`, userID).Order(`"id" DESC`).Limit(limit).Find(&drafts).Error; err != nil {
		return nil, err
	}
	return drafts, nil
}

func (r *noteDraftRepository) Update(draft *model.NoteDraft) error {
	return r.db.Save(draft).Error
}

func (r *noteDraftRepository) Delete(id, userID string) (int64, error) {
	tx := r.db.Where(`"id" = ? AND "userId" = ?`, id, userID).Delete(&model.NoteDraft{})
	return tx.RowsAffected, tx.Error
}

func (r *noteDraftRepository) CountByUser(userID string) (int64, error) {
	var count int64
	if err := r.db.Model(&model.NoteDraft{}).Where(`"userId" = ?`, userID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}
