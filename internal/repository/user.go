package repository

import (
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// UserRepository provides data access for users.
type UserRepository interface {
	Create(u *model.User) error
	FindByID(id string) (*model.User, error)
	FindByToken(token string) (*model.User, error)
	FindByURI(uri string) (*model.User, error)
	FindByUsernameLower(username string, host *string) (*model.User, error)
	FindProfileByUserID(userID string) (*model.UserProfile, error)
	// FindManyByIDs returns users for the given ID set in a single query.
	// Order is unspecified; missing rows are skipped silently. Pair with
	// FindProfilesByUserIDs to replace the user/show bulk N+1 (#503).
	FindManyByIDs(ids []string) ([]*model.User, error)
	// FindProfilesByUserIDs returns user_profile rows for the given userId
	// set in a single query. Order is unspecified; missing rows are skipped.
	FindProfilesByUserIDs(userIDs []string) ([]*model.UserProfile, error)
	// FindManyByUsernamesAndHost returns users matching any of the given
	// (case-insensitive) usernames within a single host scope. host=nil
	// targets local users (host IS NULL); otherwise host must equal the
	// supplied value. Used by note-create mention resolution to batch the
	// FindByUsernameLower per-mention loop into one query per host (#300 1-5).
	FindManyByUsernamesAndHost(usernames []string, host *string) ([]*model.User, error)
	IncrementFollowingCount(userID string, delta int) error
	IncrementFollowersCount(userID string, delta int) error
	// IncrementNotesCount atomically adjusts user.notesCount by delta.
	// 旧来は noteRepository.IncrementUserNotesCount が直接 user 行を
	// UPDATE していたが、CachedUserRepository wrapper の invalidate を
	// 通すため userRepo 経由に統一する (Devin review #552 BUG-2)。
	IncrementNotesCount(userID string, delta int) error
	// SearchByUsername は usernameLower の prefix で user を検索する。
	// origin は upstream Misskey TS と同じ semantics:
	//   "local"    → host IS NULL のみ
	//   "remote"   → host IS NOT NULL のみ
	//   "combined" / 空文字列 / その他 → filter なし
	// (#763)。
	SearchByUsername(query string, limit, offset int, origin string) ([]*model.User, error)
	UpdateUser(userID string, fields map[string]any) error
	UpdateProfile(userID string, fields map[string]any) error
	CreateProfile(profile *model.UserProfile) error
	ListUsers(filter model.UserListFilter) ([]*model.User, error)
	ListRemoteInboxes() ([]string, error)
	FindProfileByVerifyCode(code string) (*model.UserProfile, error)
	FindProfileByEmail(email string) (*model.UserProfile, error)
	CountOnlineUsers() (int64, error)
	// CountLocalUsers returns the number of non-deleted local users, used by
	// nodeinfo `usage.users.total` (#403).
	CountLocalUsers() (int64, error)
	// CountLocalUsersActiveSince returns the number of local users whose
	// lastActiveDate falls on or after `since`. Used by nodeinfo
	// `usage.users.activeMonth / activeHalfyear` (#403).
	CountLocalUsersActiveSince(since time.Time) (int64, error)
	// ListLocalUserIDsRegisteredAfter returns the IDs of local users whose
	// id is greater than `idCursor`. Used by retention aggregation to
	// resolve the "registered since cutoff" cohort (#421).
	ListLocalUserIDsRegisteredAfter(idCursor string) ([]string, error)
	// ListLocalUserIDsActiveSince returns the IDs of local users whose
	// lastActiveDate is at or after `since`. Used by retention aggregation
	// to compute the "active today" intersection (#421).
	ListLocalUserIDsActiveSince(since time.Time) ([]string, error)
	// ListUserRecommendations returns locally-active explorable users the
	// viewer does not already follow, ordered by followersCount descending.
	// viewerID is excluded from results. Used by users/recommendation.
	ListUserRecommendations(viewerID string, activeSince time.Time, limit, offset int) ([]*model.User, error)
}

type userRepository struct {
	db *gorm.DB
}

// NewUserRepository creates a new UserRepository.
func NewUserRepository(db *gorm.DB) UserRepository {
	return &userRepository{db: db}
}

func (r *userRepository) Create(u *model.User) error {
	return r.db.Create(u).Error
}

func (r *userRepository) FindByID(id string) (*model.User, error) {
	var user model.User
	if err := r.db.First(&user, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) FindByURI(uri string) (*model.User, error) {
	var user model.User
	if err := r.db.Where("uri = ?", uri).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) FindByToken(token string) (*model.User, error) {
	var user model.User
	if err := r.db.Where("token = ?", token).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) FindByUsernameLower(username string, host *string) (*model.User, error) {
	var user model.User
	q := r.db.Where("\"usernameLower\" = lower(?)", username)
	if host != nil {
		q = q.Where("host = ?", *host)
	} else {
		q = q.Where("host IS NULL")
	}
	if err := q.First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// FindManyByUsernamesAndHost batches the case-insensitive username lookup
// for one host scope. Empty input returns nil. PostgreSQL の `lower(unnest)`
// 比較で IN(?) 相当を 1 query にまとめる (#300 1-5)。
func (r *userRepository) FindManyByUsernamesAndHost(usernames []string, host *string) ([]*model.User, error) {
	if len(usernames) == 0 {
		return nil, nil
	}
	// usernameLower 列に格納されている値と比較したいので、引数側を lower
	// 化して IN マッチさせる。usernames が大文字混在でも一括で正規化される。
	lowered := make([]string, len(usernames))
	for i, u := range usernames {
		lowered[i] = strings.ToLower(u)
	}
	q := r.db.Where(`"usernameLower" IN ?`, lowered)
	if host != nil {
		q = q.Where("host = ?", *host)
	} else {
		q = q.Where("host IS NULL")
	}
	var users []*model.User
	if err := q.Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

func (r *userRepository) FindProfileByUserID(userID string) (*model.UserProfile, error) {
	var profile model.UserProfile
	if err := r.db.First(&profile, "\"userId\" = ?", userID).Error; err != nil {
		return nil, err
	}
	return &profile, nil
}

// FindManyByIDs returns users for the given ID set with a single IN query.
// Used together with FindProfilesByUserIDs by core/user.Service.ShowManyByIDs
// to eliminate the user/show bulk N+1 (#503).
func (r *userRepository) FindManyByIDs(ids []string) ([]*model.User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var users []*model.User
	if err := r.db.Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// FindProfilesByUserIDs returns user_profile rows for the given userId set
// with a single IN query. Missing profiles are skipped (the DB allows users
// without a profile, and ShowByID treats profile lookup as best-effort).
func (r *userRepository) FindProfilesByUserIDs(userIDs []string) ([]*model.UserProfile, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	var profiles []*model.UserProfile
	if err := r.db.Where(`"userId" IN ?`, userIDs).Find(&profiles).Error; err != nil {
		return nil, err
	}
	return profiles, nil
}

func (r *userRepository) IncrementFollowingCount(userID string, delta int) error {
	return r.db.Model(&model.User{}).
		Where("id = ?", userID).
		UpdateColumn("followingCount", gorm.Expr("\"followingCount\" + ?", delta)).Error
}

func (r *userRepository) IncrementFollowersCount(userID string, delta int) error {
	return r.db.Model(&model.User{}).
		Where("id = ?", userID).
		UpdateColumn("followersCount", gorm.Expr("\"followersCount\" + ?", delta)).Error
}

func (r *userRepository) IncrementNotesCount(userID string, delta int) error {
	return r.db.Model(&model.User{}).
		Where("id = ?", userID).
		UpdateColumn("notesCount", gorm.Expr("\"notesCount\" + ?", delta)).Error
}

// SearchByUsername returns users whose usernameLower starts with the given query.
// Phase 4でMeilisearch統合予定だが、現状は単純なLIKE検索のみ。
//
// origin で host filter を切り替える (#763)。
func (r *userRepository) SearchByUsername(query string, limit, offset int, origin string) ([]*model.User, error) {
	var users []*model.User
	q := r.db.Where("\"usernameLower\" LIKE ?", query+"%")
	switch origin {
	case "local":
		q = q.Where("\"host\" IS NULL")
	case "remote":
		q = q.Where("\"host\" IS NOT NULL")
	}
	if err := q.
		Order("\"followersCount\" DESC, id ASC").
		Limit(limit).
		Offset(offset).
		Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// UpdateUser updates the given columns on the user table.
func (r *userRepository) UpdateUser(userID string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.User{}).Where("id = ?", userID).Updates(fields).Error
}

// UpdateProfile updates the given columns on the user_profile table.
func (r *userRepository) UpdateProfile(userID string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.UserProfile{}).Where("\"userId\" = ?", userID).Updates(fields).Error
}

// CreateProfile inserts a new user_profile row.
func (r *userRepository) CreateProfile(profile *model.UserProfile) error {
	return r.db.Create(profile).Error
}

// FindProfileByVerifyCode looks up a user_profile by emailVerifyCode.
func (r *userRepository) FindProfileByVerifyCode(code string) (*model.UserProfile, error) {
	var p model.UserProfile
	if err := r.db.Where(`"emailVerifyCode" = ?`, code).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// FindProfileByEmail looks up a user_profile by email. admin/accounts/
// find-by-email で使う (本家 Misskey の accounts/find-by-email 相当)。
// email 列は nullable + case-insensitive 検索にしたいが、本家 DB は
// unique index を張っていないので「最初に見つかった 1 件」を返す。
func (r *userRepository) FindProfileByEmail(email string) (*model.UserProfile, error) {
	var p model.UserProfile
	if err := r.db.Where(`"email" = ?`, email).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// ListRemoteInboxes returns a deduplicated list of inbox URLs belonging to
// every known remote user. sharedInbox を優先し、無ければ個別 inbox を採用
// する。Public なアクティビティ (Delete 等) の broadcast に使う。
//
// SELECT DISTINCT で PostgreSQL 側 dedup させるのでリモートユーザー数が
// 数十万規模でも Go 側でマップを持たずに済む。空文字は NULLIF で NULL 化して
// WHERE inbox IS NOT NULL で除外している。
func (r *userRepository) ListRemoteInboxes() ([]string, error) {
	const query = `SELECT DISTINCT COALESCE(NULLIF("sharedInbox", ''), inbox) AS inbox
FROM "user"
WHERE host IS NOT NULL
  AND (COALESCE(NULLIF("sharedInbox", ''), inbox)) IS NOT NULL`
	var out []string
	if err := r.db.Raw(query).Scan(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListUsers returns users matching the filter.
func (r *userRepository) ListUsers(filter model.UserListFilter) ([]*model.User, error) {
	q := r.db.Model(&model.User{})

	switch filter.Origin {
	case "local":
		q = q.Where("host IS NULL")
	case "remote":
		q = q.Where("host IS NOT NULL")
	}
	if filter.Hostname != "" {
		q = q.Where("host = ?", filter.Hostname)
	}

	switch filter.State {
	case "suspended":
		q = q.Where("\"isSuspended\" = true")
	case "alive", "available":
		// `available` は本家 admin/show-users で `alive` と同義 (suspended で
		// ない = アクティブ)。`alive` だけ受け付けると admin UI が空返却に
		// なるので両方カバーする。
		q = q.Where("\"isSuspended\" = false")
	case "admin", "moderator", "adminOrModerator":
		// 本家 RoleService.getModeratorIds と等価な条件を SQL に落とす。
		// expiresAt が過ぎている assignment は除外、role の
		// isAdministrator / isModerator フラグで絞る。host は問わない方が
		// 上位表示で柔軟だが、admin/overview の moderator カードはローカル
		// だけを期待するので host IS NULL も付ける (#421)。
		//
		// root user (meta.rootUserId) は role_assignment 行を持たない
		// 暗黙の administrator なので admin / adminOrModerator では OR 条件
		// で必ず含める。本家 getModeratorIds の rootUserIds union と同じ
		// (#421 Devin review)。pure moderator フィルタは root を含まない。
		var roleCond string
		includeRoot := false
		switch filter.State {
		case "admin":
			roleCond = `r."isAdministrator" = true`
			includeRoot = true
		case "moderator":
			roleCond = `r."isModerator" = true`
		default: // adminOrModerator
			roleCond = `(r."isAdministrator" = true OR r."isModerator" = true)`
			includeRoot = true
		}
		idCond := `id IN (
			SELECT ra."userId" FROM role_assignment ra
			JOIN role r ON ra."roleId" = r.id
			WHERE ` + roleCond + `
			  AND (ra."expiresAt" IS NULL OR ra."expiresAt" > now())
		)`
		if includeRoot {
			idCond = "(" + idCond + ` OR id = (SELECT "rootUserId" FROM meta WHERE "rootUserId" IS NOT NULL LIMIT 1))`
		}
		q = q.Where("host IS NULL").Where(idCond)
	}

	switch filter.Sort {
	case "+createdAt":
		q = q.Order("id ASC")
	case "-createdAt":
		q = q.Order("id DESC")
	case "+updatedAt":
		q = q.Order("\"updatedAt\" ASC NULLS LAST")
	case "-updatedAt":
		q = q.Order("\"updatedAt\" DESC NULLS LAST")
	case "+lastActiveDate", "-lastActiveDate":
		// 本家 admin/show-users が moderator 一覧に使う sort key (#421)。
		// NULLS は最後に固定して accidental ASC で空アクティビティ user が
		// 先頭に来ないようにする。
		dir := "DESC"
		if filter.Sort == "+lastActiveDate" {
			dir = "ASC"
		}
		q = q.Order(`"lastActiveDate" ` + dir + ` NULLS LAST`)
	case "+followersCount":
		q = q.Order("\"followersCount\" ASC")
	case "-followersCount":
		q = q.Order("\"followersCount\" DESC")
	default:
		q = q.Order("id DESC")
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	q = q.Limit(limit)
	if filter.Offset > 0 {
		q = q.Offset(filter.Offset)
	}

	var users []*model.User
	if err := q.Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// ListUserRecommendations returns explorable, unlocked, active local users the
// viewer is not yet following. Misskey 本家互換: isExplorable AND NOT isLocked
// AND host IS NULL AND updatedAt >= activeSince AND id NOT IN (自分のfollowee)。
func (r *userRepository) ListUserRecommendations(viewerID string, activeSince time.Time, limit, offset int) ([]*model.User, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	var users []*model.User
	err := r.db.Model(&model.User{}).
		Where(`"isLocked" = FALSE AND "isExplorable" = TRUE AND host IS NULL AND "updatedAt" >= ? AND id <> ?`, activeSince, viewerID).
		Where(`id NOT IN (SELECT "followeeId" FROM "following" WHERE "followerId" = ?)`, viewerID).
		Order(`"followersCount" DESC`).
		Limit(limit).
		Offset(offset).
		Find(&users).Error
	if err != nil {
		return nil, err
	}
	return users, nil
}

// CountOnlineUsers returns the number of local users active within the last 10 minutes.
func (r *userRepository) CountOnlineUsers() (int64, error) {
	var count int64
	threshold := time.Now().Add(-10 * time.Minute)
	err := r.db.Model(&model.User{}).
		Where("host IS NULL").
		Where(`"lastActiveDate" > ?`, threshold).
		Count(&count).Error
	return count, err
}

// ListLocalUserIDsRegisteredAfter returns the IDs of local users whose
// id is lexicographically greater than `idCursor`. aidx の id は
// timestamp-prefixed なので、`idGen.Generate(cutoff)` で作った合成 id を
// 渡せば「`cutoff` 以降に作成されたユーザー」と等価になる (#421
// retention aggregation で使用)。
//
// `isDeleted = false` フィルタは意図的に省いている。retention の cohort は
// 「その日に登録したユーザー集合」であり、後から削除されたユーザーも cohort
// 母数からは外さない方が正しい (定着率の分母は登録時点で固定)。
func (r *userRepository) ListLocalUserIDsRegisteredAfter(idCursor string) ([]string, error) {
	var ids []string
	err := r.db.Model(&model.User{}).
		Where("host IS NULL").
		Where("id > ?", idCursor).
		Pluck("id", &ids).Error
	return ids, err
}

// ListLocalUserIDsActiveSince returns the IDs of local users whose
// lastActiveDate is at or after `since`. Used by the retention aggregation
// job to determine the "active today" cohort (#421)。
//
// 定着 (active) 側は削除済みユーザーを除外する。退会したアカウントを「定着」
// と数えると分子が膨らんで誤った retention 率になるため。`CountLocalUsers`
// /`CountLocalUsersActiveSince` と同じフィルタ方針。
func (r *userRepository) ListLocalUserIDsActiveSince(since time.Time) ([]string, error) {
	var ids []string
	err := r.db.Model(&model.User{}).
		Where("host IS NULL").
		Where(`"isDeleted" = false`).
		Where(`"lastActiveDate" >= ?`, since).
		Pluck("id", &ids).Error
	return ids, err
}

// CountLocalUsers returns the number of non-deleted local users.
func (r *userRepository) CountLocalUsers() (int64, error) {
	var count int64
	err := r.db.Model(&model.User{}).
		Where("host IS NULL").
		Where(`"isDeleted" = false`).
		Count(&count).Error
	return count, err
}

// CountLocalUsersActiveSince returns the number of local users whose
// lastActiveDate >= since. nodeinfo's activeMonth / activeHalfyear metrics.
func (r *userRepository) CountLocalUsersActiveSince(since time.Time) (int64, error) {
	var count int64
	err := r.db.Model(&model.User{}).
		Where("host IS NULL").
		Where(`"isDeleted" = false`).
		Where(`"lastActiveDate" >= ?`, since).
		Count(&count).Error
	return count, err
}
