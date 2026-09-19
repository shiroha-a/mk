package repository

import (
	"fmt"
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

// UserIPAccountRow is one local account observed from a searched IP (#3104).
type UserIPAccountRow struct {
	UserID string
	// FirstSeen / LastSeen は `createdAt` / `lastSeenAt` (#3103)。
	FirstSeen        time.Time
	LastSeen         time.Time
	ObservationCount int
}

// UserIPSearchRepository looks up which accounts were seen from an IP (#3104).
//
// **`UserIPRepository` と分けてある。** あちらは記録と保持 (書き込み側) で、
// こちらはモデレーション用の読み取り。#3105 / #3106 が足す口もこちら側に付く。
type UserIPSearchRepository interface {
	// ListAccountsByIP returns accounts whose last observation of ip is at or
	// after since, newest last-seen first.
	//
	// **ip は `ipnorm.Normalize` を通した正規形であること。** 記録側は正規形しか
	// 書かないので、揺れたまま渡すと当たらない。正規化は NUL も落とすので、
	// 一覧系が nulparam gate の対象外 (#3025) でも列に入らない値は届かない。
	//
	// limit+1 件まで引いて、呼び出し側が「次がある」を判断できるようにする。
	ListAccountsByIP(ip string, since time.Time, limit, offset int) ([]UserIPAccountRow, error)
	// HasAnyHistory reports whether user_ip holds any row at all.
	//
	// **「一致なし」と「そもそも記録が無い」を区別するために要る** (#3066 §7)。
	// 記録が無効だった / 保持期間を過ぎた構成で「関連アカウントなし」と断定しない。
	HasAnyHistory() (bool, error)
}

type userIPSearchRepository struct {
	db *gorm.DB
}

// NewUserIPSearchRepository constructs the default UserIPSearchRepository.
func NewUserIPSearchRepository(db *gorm.DB) UserIPSearchRepository {
	return &userIPSearchRepository{db: db}
}

// userIPAccountsSQL ranks accounts by their last observation of the IP.
//
// **`(ip, lastSeenAt DESC)` の index に乗る形** (migration 000094)。並びは
// 最終観測の降順 → 初回観測の降順 → userId 昇順。`userId` を最後に置くのは、
// 同じ時刻の行で順序が実行ごとに変わらないようにするため (ページングが破綻する)。
//
// **クエリで host を絞っていないが、それで正しい。** `user_ip` は upstream /
// mk-go とも自インスタンスで認証を通した利用者しか書かないので、行そのものが
// ローカル分に閉じている (リモート利用者の行は生じない)。
const userIPAccountsSQL = `
SELECT "userId" AS user_id, "createdAt" AS first_seen, "lastSeenAt" AS last_seen,
	"observationCount" AS observation_count
FROM "user_ip"
WHERE ip = ? AND "lastSeenAt" >= ?
ORDER BY "lastSeenAt" DESC, "createdAt" DESC, "userId" ASC
LIMIT ? OFFSET ?`

func (r *userIPSearchRepository) ListAccountsByIP(ip string, since time.Time, limit, offset int) ([]UserIPAccountRow, error) {
	if limit <= 0 {
		limit = 1
	}
	if offset < 0 {
		offset = 0
	}
	type scan struct {
		UserID           string    `gorm:"column:user_id"`
		FirstSeen        time.Time `gorm:"column:first_seen"`
		LastSeen         time.Time `gorm:"column:last_seen"`
		ObservationCount int       `gorm:"column:observation_count"`
	}
	var rows []scan
	if err := r.db.Raw(userIPAccountsSQL, ip, since, limit, offset).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("user_ip accounts by ip: %w", err)
	}
	out := make([]UserIPAccountRow, 0, len(rows))
	for _, s := range rows {
		out = append(out, UserIPAccountRow{
			UserID: s.UserID, FirstSeen: s.FirstSeen,
			LastSeen: s.LastSeen, ObservationCount: s.ObservationCount,
		})
	}
	return out, nil
}

func (r *userIPSearchRepository) HasAnyHistory() (bool, error) {
	var exists bool
	if err := r.db.Raw(`SELECT EXISTS (SELECT 1 FROM "user_ip")`).Scan(&exists).Error; err != nil {
		return false, fmt.Errorf("user_ip has any history: %w", err)
	}
	return exists, nil
}
