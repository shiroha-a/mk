package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// ClipNoteRepository provides data access for the `clip_note` table.
type ClipNoteRepository interface {
	Create(cn *model.ClipNote) error
	Delete(cn *model.ClipNote) error
	FindByPair(clipID, noteID string) (*model.ClipNote, error)
	ListByClip(clipID string, untilID, sinceID string, limit int) ([]*model.ClipNote, error)
	// CountByClip returns the number of notes in the given clip. Used by
	// noteEachClipsLimit policy gate (#1029 PR-1 follow-up).
	CountByClip(clipID string) (int64, error)
}

type clipNoteRepository struct {
	db *gorm.DB
}

// NewClipNoteRepository creates a new ClipNoteRepository.
func NewClipNoteRepository(db *gorm.DB) ClipNoteRepository {
	return &clipNoteRepository{db: db}
}

func (r *clipNoteRepository) Create(cn *model.ClipNote) error {
	return r.db.Create(cn).Error
}

func (r *clipNoteRepository) Delete(cn *model.ClipNote) error {
	return r.db.Delete(cn).Error
}

func (r *clipNoteRepository) FindByPair(clipID, noteID string) (*model.ClipNote, error) {
	var cn model.ClipNote
	if err := r.db.Where("\"clipId\" = ? AND \"noteId\" = ?", clipID, noteID).First(&cn).Error; err != nil {
		return nil, err
	}
	return &cn, nil
}

// CountByClip returns the number of notes in the given clip.
func (r *clipNoteRepository) CountByClip(clipID string) (int64, error) {
	var count int64
	if err := r.db.Model(&model.ClipNote{}).
		Where(`"clipId" = ?`, clipID).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// ListByClip returns the entries for a clip with since/until pagination on
// the clip_note id. Order flips to ASC when only sinceID is supplied,
// matching paginationOrder (upstream QueryService.makePaginationQuery parity).
func (r *clipNoteRepository) ListByClip(clipID string, untilID, sinceID string, limit int) ([]*model.ClipNote, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	q := r.db.Where("\"clipId\" = ?", clipID)
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	var rows []*model.ClipNote
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
