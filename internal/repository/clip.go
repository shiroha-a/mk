package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// ClipRepository provides data access for the `clip` table.
type ClipRepository interface {
	Create(c *model.Clip) error
	FindByID(id string) (*model.Clip, error)
	UpdateFields(clipID string, fields map[string]any) error
	Delete(c *model.Clip) error
	// ListByUser returns clips owned by userID with cursor (sinceID/untilID)
	// or offset pagination. Empty cursors fall back to offset.
	ListByUser(userID string, sinceID, untilID string, limit, offset int) ([]*model.Clip, error)
	// CountByUser returns the number of clips owned by userID. Used by
	// clipLimit policy gate (#1029 PR-1 follow-up).
	CountByUser(userID string) (int64, error)
	// ListPublicByUser returns only public clips owned by userID. Used by
	// users/clips when the viewer is not the owner so LIMIT applies to the
	// already-filtered set. Same cursor semantics as ListByUser.
	ListPublicByUser(userID string, sinceID, untilID string, limit, offset int) ([]*model.Clip, error)
	IncrementCount(clipID, column string, delta int) error
}

type clipRepository struct {
	db *gorm.DB
}

// NewClipRepository creates a new ClipRepository.
func NewClipRepository(db *gorm.DB) ClipRepository {
	return &clipRepository{db: db}
}

func (r *clipRepository) Create(c *model.Clip) error {
	return r.db.Create(c).Error
}

func (r *clipRepository) FindByID(id string) (*model.Clip, error) {
	var c model.Clip
	if err := r.db.First(&c, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *clipRepository) UpdateFields(clipID string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.Clip{}).Where("id = ?", clipID).Updates(fields).Error
}

func (r *clipRepository) Delete(c *model.Clip) error {
	return r.db.Delete(c).Error
}

// ListByUser returns clips owned by userID with cursor or offset pagination.
func (r *clipRepository) ListByUser(userID, sinceID, untilID string, limit, offset int) ([]*model.Clip, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	q := r.db.Where(`"userId" = ?`, userID)
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	q = q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit)
	// cursor 指定時は offset 無視 (本家 makePaginationQuery と同じ)
	if sinceID == "" && untilID == "" && offset > 0 {
		q = q.Offset(offset)
	}
	var rows []*model.Clip
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CountByUser returns the number of clips owned by the given user.
func (r *clipRepository) CountByUser(userID string) (int64, error) {
	var count int64
	if err := r.db.Model(&model.Clip{}).
		Where(`"userId" = ?`, userID).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *clipRepository) ListPublicByUser(userID, sinceID, untilID string, limit, offset int) ([]*model.Clip, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	q := r.db.Where(`"userId" = ? AND "isPublic" = true`, userID)
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
	var rows []*model.Clip
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// IncrementCount adjusts a counter column on the clip row by delta.
func (r *clipRepository) IncrementCount(clipID, column string, delta int) error {
	return r.db.Model(&model.Clip{}).
		Where("id = ?", clipID).
		UpdateColumn(column, gorm.Expr("\""+column+"\" + ?", delta)).Error
}
