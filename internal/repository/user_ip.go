package repository

import (
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserIPRepository provides data access for the `user_ip` table.
type UserIPRepository interface {
	// Upsert inserts or updates the (userId, ip) pair. UNIQUE constraint on
	// (userId, ip) なので同じ IP からの再ログインは createdAt だけ更新される。
	Upsert(userID, ip string) error
	// ListByUser returns IPs for the given user, newest first.
	ListByUser(userID string, limit int) ([]*model.UserIP, error)
	// DeleteOlderThan removes user_ip rows created before t. Run by the
	// daily clean cron to prune IP history older than 90 days (#1563).
	// Returns rows deleted.
	DeleteOlderThan(t time.Time) (int64, error)
}

type userIPRepository struct {
	db *gorm.DB
}

// NewUserIPRepository constructs the default UserIPRepository.
func NewUserIPRepository(db *gorm.DB) UserIPRepository {
	return &userIPRepository{db: db}
}

func (r *userIPRepository) Upsert(userID, ip string) error {
	record := &model.UserIP{UserID: userID, IP: ip}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "userId"}, {Name: "ip"}},
		DoUpdates: clause.AssignmentColumns([]string{"createdAt"}),
	}).Create(record).Error
}

func (r *userIPRepository) ListByUser(userID string, limit int) ([]*model.UserIP, error) {
	if limit <= 0 {
		limit = 30
	}
	var ips []*model.UserIP
	if err := r.db.Where(`"userId" = ?`, userID).Order(`"createdAt" DESC`).Limit(limit).Find(&ips).Error; err != nil {
		return nil, err
	}
	return ips, nil
}

func (r *userIPRepository) DeleteOlderThan(t time.Time) (int64, error) {
	res := r.db.Where(`"createdAt" < ?`, t).Delete(&model.UserIP{})
	return res.RowsAffected, res.Error
}
