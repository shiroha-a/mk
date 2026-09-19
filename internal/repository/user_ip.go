package repository

import (
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserIPRepository provides data access for the `user_ip` table.
type UserIPRepository interface {
	// Observe records that userID was seen from ip at the given time.
	//
	// **`createdAt` は衝突しても更新しない (#3103)。** upstream の
	// `ApiCallService.logIp` が `INSERT ... orIgnore` なので、あの列は初回観測を
	// 意味する。更新するのは mk-go 独自列の `lastSeenAt` / `observationCount`。
	//
	// ip は `ipnorm.Normalize` を通した正規形であることを前提にする。表記揺れの
	// まま入れると同じ端末の観測が別の行に分かれる。
	Observe(userID, ip string, at time.Time) error
	// ListByUser returns IPs for the given user, newest first.
	//
	// **並びは `id` の降順。** upstream `admin/get-user-ips` が
	// `order: { id: 'DESC' }` なのに揃えてある (`createdAt` ではない)。`id` は
	// 行を初めて作ったときに採番されるので、実質「初めて観測した順」になる。
	//
	// **最終観測では並べ替えない。** upstream と順序が変わるうえ、返している値
	// (`createdAt` = 初回観測) と順序の基準が食い違う。ただし上限 30 件なので、
	// 「初めて見たのは古いが今も使っている IP」は一覧から落ちうる (#3103 で
	// `createdAt` の意味を変えたことで、それ以前とは見え方が変わる)。
	ListByUser(userID string, limit int) ([]*model.UserIP, error)
	// DeleteLastSeenBefore removes user_ip rows whose **last** observation is
	// older than t. Run by the daily clean cron to prune IP history older than
	// 90 days (#1563). Returns rows deleted.
	//
	// **基準は `createdAt` ではない (#3103)。** あちらは初回観測なので、基準に
	// すると「初めて見たのは 1 年前だが今も使っている IP」まで消える。
	DeleteLastSeenBefore(t time.Time) (int64, error)
}

type userIPRepository struct {
	db *gorm.DB
}

// NewUserIPRepository constructs the default UserIPRepository.
func NewUserIPRepository(db *gorm.DB) UserIPRepository {
	return &userIPRepository{db: db}
}

func (r *userIPRepository) Observe(userID, ip string, at time.Time) error {
	record := &model.UserIP{
		UserID: userID, IP: ip,
		CreatedAt: at, LastSeenAt: at, ObservationCount: 1,
	}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "userId"}, {Name: "ip"}},
		DoUpdates: clause.Assignments(map[string]any{
			// **GREATEST で受ける。** 観測が前後して届くことがある (記録は
			// goroutine で走るので順序の保証が無い)。素直に代入すると古い時刻へ
			// 巻き戻り、保持の刈り取り基準まで古くなる。
			"lastSeenAt":       gorm.Expr(`GREATEST("user_ip"."lastSeenAt", EXCLUDED."lastSeenAt")`),
			"observationCount": gorm.Expr(`"user_ip"."observationCount" + 1`),
		}),
	}).Create(record).Error
}

func (r *userIPRepository) ListByUser(userID string, limit int) ([]*model.UserIP, error) {
	if limit <= 0 {
		limit = 30
	}
	var ips []*model.UserIP
	if err := r.db.Where(`"userId" = ?`, userID).Order(`id DESC`).Limit(limit).Find(&ips).Error; err != nil {
		return nil, err
	}
	return ips, nil
}

func (r *userIPRepository) DeleteLastSeenBefore(t time.Time) (int64, error) {
	res := r.db.Where(`"lastSeenAt" < ?`, t).Delete(&model.UserIP{})
	return res.RowsAffected, res.Error
}
