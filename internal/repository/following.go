package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// FollowingRepository provides data access for the `following` table.
type FollowingRepository interface {
	Create(f *model.Following) error
	Delete(f *model.Following) error
	FindByPair(followerID, followeeID string) (*model.Following, error)
	Exists(followerID, followeeID string) (bool, error)
	// FilterFollowingsFromAnchor returns the subset of candidateIDs that
	// anchorID follows (anchor → candidate direction, = rows where
	// followerId=anchorID AND followeeId IN candidates). Used to batch-
	// compute `isFollowing` across a user list (e.g. users/following /
	// users/followers) without N+1 Exists round-trips (#1144).
	FilterFollowingsFromAnchor(anchorID string, candidateIDs []string) ([]string, error)
	// FilterFollowingsToAnchor returns the subset of candidateIDs that
	// follow anchorID (candidate → anchor direction, = rows where
	// followerId IN candidates AND followeeId=anchorID). Used to batch-
	// compute `isFollowed` across a user list (#1144).
	FilterFollowingsToAnchor(anchorID string, candidateIDs []string) ([]string, error)
	ListFollowers(userID string, limit, offset int) ([]*model.Following, error)
	// ListFollowersToNotify returns Following rows where followeeId matches and
	// notify='normal'. Used by note.CreateService to fan out 'note'
	// notifications to followers who opted into post notifications (upstream
	// NoteCreateService の findBy({followeeId, notify:'normal'}))。
	ListFollowersToNotify(userID string) ([]*model.Following, error)
	ListFollowing(userID string, limit, offset int) ([]*model.Following, error)
	// ListFollowingForList returns Following rows where followerID matches,
	// optionally filtered by `notify IS NOT NULL` (= notification=true) and
	// cursor-paginated by sinceID / untilID. Used by /api/following/list
	// (upstream 2026.5.2 #17385 + #17416).
	ListFollowingForList(followerID, sinceID, untilID string, notification bool, limit int) ([]*model.Following, error)
	// ListFollowersByHost returns Following rows whose followeeHost matches
	// host. Used by federation/followers. 本家 followers.ts は
	// `following.followeeHost = :host` で絞る (= 指定リモートホストの
	// ユーザーがフォローされている = followee 側がそのホスト) ので、列は
	// followeeHost。
	ListFollowersByHost(host string, limit, offset int) ([]*model.Following, error)
	// ListFollowingByHost returns Following rows whose followerHost matches
	// host. Used by federation/following. 本家 following.ts は
	// `following.followerHost = :host` で絞る (= 指定リモートホストの
	// ユーザーがフォローしている = follower 側がそのホスト) ので、列は
	// followerHost。
	ListFollowingByHost(host string, limit, offset int) ([]*model.Following, error)
	// ListFollowersByHostCursor is ListFollowersByHost with id cursor pagination
	// (sinceID/untilID) instead of offset. Used by federation/followers to match
	// upstream makePaginationQuery (#1732)。sinceID のみ指定時は id ASC、
	// それ以外は id DESC。
	ListFollowersByHostCursor(host, sinceID, untilID string, limit int) ([]*model.Following, error)
	// ListFollowingByHostCursor is ListFollowingByHost with id cursor pagination.
	// Used by federation/following (#1732)。
	ListFollowingByHostCursor(host, sinceID, untilID string, limit int) ([]*model.Following, error)
	// CountRemoteFollowees returns the number of Following rows whose
	// followeeHost is non-NULL (= remote users being followed by anyone).
	// federation/stats の allSubCount (upstream count where followeeHost is
	// not null) に対応 (#1544)。
	CountRemoteFollowees() (int64, error)
	// CountRemoteFollowers returns the number of Following rows whose
	// followerHost is non-NULL (= remote users following anyone). stats の
	// allPubCount (upstream count where followerHost is not null) に対応 (#1544)。
	CountRemoteFollowers() (int64, error)
	// UpdateRelation applies partial updates to a Following row identified
	// by (followerID, followeeID). Used by following/update.
	UpdateRelation(followerID, followeeID string, fields map[string]any) error
	// UpdateAllByFollower applies partial updates to every Following row
	// with the given follower. Used by following/update-all.
	UpdateAllByFollower(followerID string, fields map[string]any) error
	// DeleteAllByUser removes every Following row that involves userID as
	// either the follower or the followee. Returns the total affected rows.
	// Used by cascade account deletion.
	DeleteAllByUser(userID string) (int64, error)
	// ListRemoteFollowerInboxes returns the deduplicated inboxes for remote
	// followers of userID. sharedInbox を持つフォロワーは sharedInbox に集約され
	// (Shared=true)、無いフォロワーは個別 inbox (Shared=false)。Shared フラグは
	// deliver の goneSuspended 判定 (shared inbox の 410) に使う (#1811)。
	ListRemoteFollowerInboxes(userID string) ([]model.RemoteInbox, error)
	// ListFollowingByBirthday returns the followees of followerID whose
	// birthday (mmdd) falls in [beginMMDD, endMMDD] inclusive. Passing
	// beginMMDD > endMMDD treats the range as year-wrapping (e.g. 1225..0105).
	// Results are ordered by mmdd ascending. Used by
	// users/get-following-users-by-birthday.
	ListFollowingByBirthday(followerID string, beginMMDD, endMMDD, limit, offset int) ([]model.FollowingBirthday, error)
}

type followingRepository struct {
	db *gorm.DB
}

// NewFollowingRepository creates a new FollowingRepository.
func NewFollowingRepository(db *gorm.DB) FollowingRepository {
	return &followingRepository{db: db}
}

func (r *followingRepository) Create(f *model.Following) error {
	return r.db.Create(f).Error
}

func (r *followingRepository) Delete(f *model.Following) error {
	return r.db.Delete(f).Error
}

func (r *followingRepository) FindByPair(followerID, followeeID string) (*model.Following, error) {
	var f model.Following
	if err := r.db.Where("\"followerId\" = ? AND \"followeeId\" = ?", followerID, followeeID).First(&f).Error; err != nil {
		return nil, err
	}
	return &f, nil
}

func (r *followingRepository) Exists(followerID, followeeID string) (bool, error) {
	var count int64
	if err := r.db.Model(&model.Following{}).
		Where("\"followerId\" = ? AND \"followeeId\" = ?", followerID, followeeID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *followingRepository) FilterFollowingsFromAnchor(anchorID string, candidateIDs []string) ([]string, error) {
	if anchorID == "" || len(candidateIDs) == 0 {
		return nil, nil
	}
	var ids []string
	if err := r.db.Model(&model.Following{}).
		Where(`"followerId" = ? AND "followeeId" IN ?`, anchorID, candidateIDs).
		Pluck(`"followeeId"`, &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *followingRepository) FilterFollowingsToAnchor(anchorID string, candidateIDs []string) ([]string, error) {
	if anchorID == "" || len(candidateIDs) == 0 {
		return nil, nil
	}
	var ids []string
	if err := r.db.Model(&model.Following{}).
		Where(`"followerId" IN ? AND "followeeId" = ?`, candidateIDs, anchorID).
		Pluck(`"followerId"`, &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *followingRepository) ListFollowers(userID string, limit, offset int) ([]*model.Following, error) {
	var rows []*model.Following
	if err := r.db.Where("\"followeeId\" = ?", userID).
		Order("id DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListFollowersToNotify returns Following rows where followeeId matches and
// notify='normal'. note 通知の fan-out 対象を絞る (#1559)。`notify` 列は
// null=OFF / 'normal'=ON という Misskey 互換 semantics で、TS NoteCreateService は
// findBy({followeeId, notify:'normal'}) と完全一致で絞るため値も 'normal' に揃える。
func (r *followingRepository) ListFollowersToNotify(userID string) ([]*model.Following, error) {
	var rows []*model.Following
	if err := r.db.Where(`"followeeId" = ? AND notify = ?`, userID, "normal").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *followingRepository) ListFollowing(userID string, limit, offset int) ([]*model.Following, error) {
	var rows []*model.Following
	if err := r.db.Where("\"followerId\" = ?", userID).
		Order("id DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListFollowingForList implements the cursor + notification-filter variant
// used by /api/following/list (upstream 2026.5.2 #17385 + #17416)。
//
// notification=true で `notify IS NOT NULL` 絞り込み。`notify` field 自体は
// 投稿通知の音声 / バイブ等の preference を保持する varchar(32) で、null
// なら通知 OFF / 非 null なら ON という Misskey 互換の semantics。
func (r *followingRepository) ListFollowingForList(followerID, sinceID, untilID string, notification bool, limit int) ([]*model.Following, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	q := r.db.Where(`"followerId" = ?`, followerID)
	if notification {
		q = q.Where(`notify IS NOT NULL`)
	}
	if sinceID != "" {
		q = q.Where(`id > ?`, sinceID)
	}
	if untilID != "" {
		q = q.Where(`id < ?`, untilID)
	}
	var rows []*model.Following
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListFollowersByHost returns Following rows whose followeeHost matches the
// given remote host. 本家 federation/followers.ts と同じく followeeHost で絞る。
func (r *followingRepository) ListFollowersByHost(host string, limit, offset int) ([]*model.Following, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	var rows []*model.Following
	if err := r.db.Where(`"followeeHost" = ?`, host).
		Order("id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListFollowingByHost returns Following rows whose followerHost matches the
// given remote host. 本家 federation/following.ts と同じく followerHost で絞る。
func (r *followingRepository) ListFollowingByHost(host string, limit, offset int) ([]*model.Following, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	var rows []*model.Following
	if err := r.db.Where(`"followerHost" = ?`, host).
		Order("id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListFollowersByHostCursor is the cursor-paginated variant of
// ListFollowersByHost (federation/followers, #1732)。
func (r *followingRepository) ListFollowersByHostCursor(host, sinceID, untilID string, limit int) ([]*model.Following, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	q := r.db.Where(`"followeeHost" = ?`, host)
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	var rows []*model.Following
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListFollowingByHostCursor is the cursor-paginated variant of
// ListFollowingByHost (federation/following, #1732)。
func (r *followingRepository) ListFollowingByHostCursor(host, sinceID, untilID string, limit int) ([]*model.Following, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	q := r.db.Where(`"followerHost" = ?`, host)
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	var rows []*model.Following
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *followingRepository) CountRemoteFollowees() (int64, error) {
	var n int64
	if err := r.db.Model(&model.Following{}).
		Where(`"followeeHost" IS NOT NULL`).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

func (r *followingRepository) CountRemoteFollowers() (int64, error) {
	var n int64
	if err := r.db.Model(&model.Following{}).
		Where(`"followerHost" IS NOT NULL`).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// UpdateRelation applies a partial field update to a single (follower, followee)
// Following row.
func (r *followingRepository) UpdateRelation(followerID, followeeID string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.Following{}).
		Where(`"followerId" = ? AND "followeeId" = ?`, followerID, followeeID).
		Updates(fields).Error
}

// UpdateAllByFollower applies a partial field update to every Following row
// authored by the given follower.
func (r *followingRepository) UpdateAllByFollower(followerID string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.Following{}).
		Where(`"followerId" = ?`, followerID).
		Updates(fields).Error
}

// DeleteAllByUser removes every Following row whose follower or followee is
// userID.
func (r *followingRepository) DeleteAllByUser(userID string) (int64, error) {
	tx := r.db.Where(`"followerId" = ? OR "followeeId" = ?`, userID, userID).
		Delete(&model.Following{})
	return tx.RowsAffected, tx.Error
}

// ListRemoteFollowerInboxes returns deduplicated inbox URLs for remote
// followers. sharedInbox を優先し、無い場合のみ個別 inbox を使う。
//
// SQLは以下の流れ:
//  1. follower がリモート (host IS NOT NULL) のフォロワーをjoin
//  2. sharedInbox があれば sharedInbox、無ければ inbox を選択
//  3. NULL/空文字列を除外し DISTINCT で重複排除
func (r *followingRepository) ListRemoteFollowerInboxes(userID string) ([]model.RemoteInbox, error) {
	var rows []struct {
		Inbox  string
		Shared bool
	}
	// shared フラグは sharedInbox が非空のとき true (= COALESCE で sharedInbox 側を
	// 選んだ行)。同 shared inbox を共有する複数フォロワーは DISTINCT で 1 行に集約。
	err := r.db.
		Table(`"following" AS f`).
		Select(`DISTINCT COALESCE(NULLIF(u."sharedInbox", ''), u.inbox) AS inbox, (u."sharedInbox" IS NOT NULL AND u."sharedInbox" != '') AS shared`).
		Joins(`JOIN "user" u ON u.id = f."followerId"`).
		Where(`f."followeeId" = ? AND u.host IS NOT NULL AND (u."sharedInbox" IS NOT NULL OR u.inbox IS NOT NULL)`, userID).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	// COALESCE が NULL を返す可能性があるため、空文字を除去
	out := make([]model.RemoteInbox, 0, len(rows))
	for _, row := range rows {
		if row.Inbox != "" {
			out = append(out, model.RemoteInbox{Inbox: row.Inbox, Shared: row.Shared})
		}
	}
	return out, nil
}

// ListFollowingByBirthday returns followees whose birthday mmdd falls within
// the given range. user_profile.birthday は "YYYY-MM-DD" の char(10) として
// 保存されているため、SUBSTRING で月日を切り出して整数化する。
func (r *followingRepository) ListFollowingByBirthday(followerID string, beginMMDD, endMMDD, limit, offset int) ([]model.FollowingBirthday, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	// PostgreSQL SUBSTRING(text FROM start FOR length) は 1-indexed。
	// "YYYY-MM-DD" のうち MM は 6-7 文字目、DD は 9-10 文字目。
	const mmddExpr = `(CAST(SUBSTRING(p.birthday FROM 6 FOR 2) AS INTEGER) * 100 + CAST(SUBSTRING(p.birthday FROM 9 FOR 2) AS INTEGER))`
	q := r.db.
		Table(`"following" AS f`).
		Select(`f."followeeId", p.birthday`).
		Joins(`JOIN "user_profile" p ON p."userId" = f."followeeId"`).
		Where(`f."followerId" = ? AND p.birthday IS NOT NULL`, followerID)
	if beginMMDD <= endMMDD {
		q = q.Where(mmddExpr+` BETWEEN ? AND ?`, beginMMDD, endMMDD)
	} else {
		// 年跨ぎ (例: 12/25 -> 1/5) の場合は begin..1231 と 101..end を OR。
		q = q.Where(`(`+mmddExpr+` BETWEEN ? AND 1231 OR `+mmddExpr+` BETWEEN 101 AND ?)`, beginMMDD, endMMDD)
	}
	var rows []model.FollowingBirthday
	if err := q.Order(mmddExpr + ` ASC`).Limit(limit).Offset(offset).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
