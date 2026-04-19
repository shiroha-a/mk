package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// AnnouncementRepository provides data access for announcements.
type AnnouncementRepository interface {
	Create(a *model.Announcement) error
	FindByID(id string) (*model.Announcement, error)
	// List returns ALL announcements regardless of targeting. Use this for
	// admin-only surfaces; do not expose to unauthenticated users because
	// it returns per-user announcements targeted at other users.
	List(activeOnly bool, limit, offset int, sinceID, untilID string) ([]*model.Announcement, error)
	// ListForUser returns announcements visible to userID: global ones
	// (userId IS NULL) plus those targeted at this user. Used by
	// /api/announcements so per-user announcements do not leak to unrelated
	// users.
	ListForUser(userID string, activeOnly bool, limit, offset int, sinceID, untilID string) ([]*model.Announcement, error)
	// ListGlobal returns only global announcements (userId IS NULL). Used
	// for unauthenticated /api/announcements requests so targeted
	// announcements are never exposed.
	ListGlobal(activeOnly bool, limit, offset int, sinceID, untilID string) ([]*model.Announcement, error)
	UpdateFields(id string, fields map[string]any) error
	Delete(id string) error
	// Read management
	MarkRead(read *model.AnnouncementRead) error
	IsRead(userID, announcementID string) (bool, error)
	UnreadForUser(userID string) ([]*model.Announcement, error)
}

type announcementRepository struct {
	db *gorm.DB
}

func NewAnnouncementRepository(db *gorm.DB) AnnouncementRepository {
	return &announcementRepository{db: db}
}

func (r *announcementRepository) Create(a *model.Announcement) error {
	return r.db.Create(a).Error
}

func (r *announcementRepository) FindByID(id string) (*model.Announcement, error) {
	var a model.Announcement
	if err := r.db.Where("id = ?", id).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *announcementRepository) List(activeOnly bool, limit, offset int, sinceID, untilID string) ([]*model.Announcement, error) {
	q := r.db.Order("id DESC")
	if activeOnly {
		q = q.Where("\"isActive\" = true")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	q = q.Limit(limit)
	// カーソル指定時はoffsetを無視する（本家TS実装と同じ挙動）
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID == "" && untilID == "" && offset > 0 {
		q = q.Offset(offset)
	}
	var announcements []*model.Announcement
	if err := q.Find(&announcements).Error; err != nil {
		return nil, err
	}
	return announcements, nil
}

func (r *announcementRepository) ListGlobal(activeOnly bool, limit, offset int, sinceID, untilID string) ([]*model.Announcement, error) {
	q := r.db.Where(`"userId" IS NULL`).Order("id DESC")
	if activeOnly {
		q = q.Where("\"isActive\" = true")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	q = q.Limit(limit)
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID == "" && untilID == "" && offset > 0 {
		q = q.Offset(offset)
	}
	var announcements []*model.Announcement
	if err := q.Find(&announcements).Error; err != nil {
		return nil, err
	}
	return announcements, nil
}

func (r *announcementRepository) ListForUser(userID string, activeOnly bool, limit, offset int, sinceID, untilID string) ([]*model.Announcement, error) {
	// GORMは連続.WhereをANDで繋ぐがraw SQL側のORはデフォルトでは括弧で
	// 囲まれないので、明示的に囲まないとAND側の他フィルタ(isActive等)を
	// バイパスしてしまう(SQL優先順位: AND > OR)。note.go:423と同じ gotcha。
	q := r.db.Where(`("userId" IS NULL OR "userId" = ?)`, userID).Order("id DESC")
	if activeOnly {
		q = q.Where("\"isActive\" = true")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	q = q.Limit(limit)
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID == "" && untilID == "" && offset > 0 {
		q = q.Offset(offset)
	}
	var announcements []*model.Announcement
	if err := q.Find(&announcements).Error; err != nil {
		return nil, err
	}
	return announcements, nil
}

func (r *announcementRepository) UpdateFields(id string, fields map[string]any) error {
	return r.db.Model(&model.Announcement{}).Where("id = ?", id).Updates(fields).Error
}

func (r *announcementRepository) Delete(id string) error {
	return r.db.Where("id = ?", id).Delete(&model.Announcement{}).Error
}

func (r *announcementRepository) MarkRead(read *model.AnnouncementRead) error {
	return r.db.Create(read).Error
}

func (r *announcementRepository) IsRead(userID, announcementID string) (bool, error) {
	var count int64
	err := r.db.Model(&model.AnnouncementRead{}).
		Where("\"userId\" = ? AND \"announcementId\" = ?", userID, announcementID).
		Count(&count).Error
	return count > 0, err
}

func (r *announcementRepository) UnreadForUser(userID string) ([]*model.Announcement, error) {
	var announcements []*model.Announcement
	// per-user announcement(userId!=null)はその対象ユーザーのみ含める。
	// global announcement(userId IS NULL)は全ユーザーに見せる。
	err := r.db.Where(
		`"isActive" = true AND ("userId" IS NULL OR "userId" = ?) AND id NOT IN (?)`,
		userID,
		r.db.Model(&model.AnnouncementRead{}).Select("\"announcementId\"").Where("\"userId\" = ?", userID),
	).Order("id DESC").Find(&announcements).Error
	if err != nil {
		return nil, err
	}
	return announcements, nil
}
