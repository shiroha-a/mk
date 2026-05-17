package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// AbuseReportRepository provides data access for the `abuse_user_report` table.
type AbuseReportRepository interface {
	Create(r *model.AbuseUserReport) error
	FindByID(id string) (*model.AbuseUserReport, error)
	// List returns abuse reports filtered by state and reporter/target user
	// origin, paginated via cursor (sinceID / untilID) ordered by id DESC.
	// reporterOrigin / targetUserOrigin accept "local" / "remote" /
	// "combined" (or empty = combined). origin filter は対応する Host 列の
	// IS NULL / IS NOT NULL で判定する (= upstream Misskey TS と同じ pattern)。
	List(resolved *bool, reporterOrigin, targetUserOrigin, sinceID, untilID string, limit int) ([]*model.AbuseUserReport, error)
	UpdateFields(id string, fields map[string]any) error
}

type abuseReportRepository struct {
	db *gorm.DB
}

func NewAbuseReportRepository(db *gorm.DB) AbuseReportRepository {
	return &abuseReportRepository{db: db}
}

func (r *abuseReportRepository) Create(report *model.AbuseUserReport) error {
	return r.db.Create(report).Error
}

func (r *abuseReportRepository) FindByID(id string) (*model.AbuseUserReport, error) {
	var report model.AbuseUserReport
	if err := r.db.Preload("TargetUser").Preload("Reporter").Preload("Assignee").
		Where("id = ?", id).First(&report).Error; err != nil {
		return nil, err
	}
	return &report, nil
}

func (r *abuseReportRepository) List(resolved *bool, reporterOrigin, targetUserOrigin, sinceID, untilID string, limit int) ([]*model.AbuseUserReport, error) {
	q := r.db.Preload("TargetUser").Preload("Reporter").Preload("Assignee").
		Order("id DESC")
	if resolved != nil {
		q = q.Where("resolved = ?", *resolved)
	}
	// origin filter: "local" → Host が NULL (= local user)、"remote" → Host が
	// NOT NULL (= remote user)、"combined" / 空文字列は条件追加なし (= all)。
	// upstream Misskey TS と同じ semantics。reporterOrigin と targetUserOrigin
	// は独立に AND される。
	switch reporterOrigin {
	case "local":
		q = q.Where(`"reporterHost" IS NULL`)
	case "remote":
		q = q.Where(`"reporterHost" IS NOT NULL`)
	}
	switch targetUserOrigin {
	case "local":
		q = q.Where(`"targetUserHost" IS NULL`)
	case "remote":
		q = q.Where(`"targetUserHost" IS NOT NULL`)
	}
	// cursor pagination (keyset): id DESC 順なので untilID は「より古い id」、
	// sinceID は「より新しい id」を取り出す方向。frontend MkPagination の
	// untilId 経路がメインの fix 対象 (#1114)。
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	q = q.Limit(limit)
	var reports []*model.AbuseUserReport
	if err := q.Find(&reports).Error; err != nil {
		return nil, err
	}
	return reports, nil
}

func (r *abuseReportRepository) UpdateFields(id string, fields map[string]any) error {
	return r.db.Model(&model.AbuseUserReport{}).Where("id = ?", id).Updates(fields).Error
}

// ModerationLogRepository provides data access for the `moderation_log` table.
type ModerationLogRepository interface {
	Create(log *model.ModerationLog) error
	// CreateMany batch-inserts logs in one SQL statement (GORM slice-insert
	// uses a single multi-row INSERT). Used by Service.LogMany when bulk
	// admin operations want to record N entries without spawning N
	// goroutines + N round-trips (#671).
	CreateMany(logs []*model.ModerationLog) error
	List(limit, offset int) ([]*model.ModerationLog, error)
}

type moderationLogRepository struct {
	db *gorm.DB
}

func NewModerationLogRepository(db *gorm.DB) ModerationLogRepository {
	return &moderationLogRepository{db: db}
}

func (r *moderationLogRepository) Create(log *model.ModerationLog) error {
	return r.db.Create(log).Error
}

func (r *moderationLogRepository) CreateMany(logs []*model.ModerationLog) error {
	if len(logs) == 0 {
		return nil
	}
	return r.db.Create(&logs).Error
}

func (r *moderationLogRepository) List(limit, offset int) ([]*model.ModerationLog, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	q := r.db.Preload("User").Order("id DESC").Limit(limit)
	if offset > 0 {
		q = q.Offset(offset)
	}
	var logs []*model.ModerationLog
	if err := q.Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}
