package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// PageRepository provides data access for the `page` table.
type PageRepository interface {
	Create(p *model.Page) error
	FindByID(id string) (*model.Page, error)
	// FindManyByIDs returns pages for the given ID set in a single query.
	// Used by /api/i/page-likes to batch-resolve `like.pageId → page` and
	// avoid an N+1 lookup over the like list (#1136).
	FindManyByIDs(ids []string) ([]*model.Page, error)
	FindByUserAndName(userID, name string) (*model.Page, error)
	UpdateFields(pageID string, fields map[string]any) error
	Delete(p *model.Page) error
	// ListByUser returns pages owned by userID with cursor (sinceID/untilID)
	// or offset pagination. Cursor 指定時は offset 無視。
	ListByUser(userID, sinceID, untilID string, limit, offset int) ([]*model.Page, error)
	// ListPublicByUser returns only public pages owned by userID, used by
	// users/pages when viewer is not the owner. Same cursor semantics.
	ListPublicByUser(userID, sinceID, untilID string, limit, offset int) ([]*model.Page, error)
	// ListFeatured returns the most-liked public pages with cursor or
	// offset pagination.
	ListFeatured(sinceID, untilID string, limit, offset int) ([]*model.Page, error)
	IncrementCount(pageID, column string, delta int) error
}

type pageRepository struct {
	db *gorm.DB
}

// NewPageRepository creates a new PageRepository.
func NewPageRepository(db *gorm.DB) PageRepository {
	return &pageRepository{db: db}
}

func (r *pageRepository) Create(p *model.Page) error {
	return r.db.Create(p).Error
}

func (r *pageRepository) FindByID(id string) (*model.Page, error) {
	var p model.Page
	if err := r.db.First(&p, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *pageRepository) FindManyByIDs(ids []string) ([]*model.Page, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var pages []*model.Page
	if err := r.db.Where("id IN ?", ids).Find(&pages).Error; err != nil {
		return nil, err
	}
	return pages, nil
}

// FindByUserAndName looks up a page by the (userId, name) pair which is the
// primary user-facing identity for a Page in Misskey.
func (r *pageRepository) FindByUserAndName(userID, name string) (*model.Page, error) {
	var p model.Page
	if err := r.db.Where("\"userId\" = ? AND name = ?", userID, name).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *pageRepository) UpdateFields(pageID string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.Page{}).Where("id = ?", pageID).Updates(fields).Error
}

func (r *pageRepository) Delete(p *model.Page) error {
	return r.db.Delete(p).Error
}

// ListByUser returns the pages owned by userID with cursor (sinceID/untilID)
// or offset pagination. cursor mode はupstream `makePaginationQuery` 同様
// `id` で sort + filter する (元実装の `updatedAt DESC` は frontend
// Paginator の untilId/sinceId と整合しないため変更)。
func (r *pageRepository) ListByUser(userID, sinceID, untilID string, limit, offset int) ([]*model.Page, error) {
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
	if sinceID == "" && untilID == "" && offset > 0 {
		q = q.Offset(offset)
	}
	var rows []*model.Page
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *pageRepository) ListPublicByUser(userID, sinceID, untilID string, limit, offset int) ([]*model.Page, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	q := r.db.Where(`"userId" = ? AND visibility = ?`, userID, string(model.PageVisibilityPublic))
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
	var rows []*model.Page
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListFeatured returns the most-liked public pages. cursor (sinceID/untilID)
// 指定時はそれを優先。指定なしは likedCount DESC + offset で従来通り。
func (r *pageRepository) ListFeatured(sinceID, untilID string, limit, offset int) ([]*model.Page, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	q := r.db.Where("visibility = ?", string(model.PageVisibilityPublic))
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" || untilID != "" {
		q = q.Order(paginationOrder(sinceID, untilID, "id"))
	} else {
		q = q.Order(`"likedCount" DESC`)
	}
	q = q.Limit(limit)
	if sinceID == "" && untilID == "" && offset > 0 {
		q = q.Offset(offset)
	}
	var rows []*model.Page
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// IncrementCount adjusts a counter column on the page row by delta.
func (r *pageRepository) IncrementCount(pageID, column string, delta int) error {
	return r.db.Model(&model.Page{}).
		Where("id = ?", pageID).
		UpdateColumn(column, gorm.Expr("\""+column+"\" + ?", delta)).Error
}
