package repository

import (
	"regexp"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// localUsernameSearchPattern mirrors upstream localUsernameSchema (^\w{1,20}$),
// used by SearchUsers to decide whether a bare (non-@) query should also match
// usernames by partial (#1939)。`\w` = [a-zA-Z0-9_]。
var localUsernameSearchPattern = regexp.MustCompile(`^[a-zA-Z0-9_]{1,20}$`)

// SearchOrigin は users/search の origin 引数の enum。upstream Misskey TS の
// paramDef enum (`local` / `remote` / `combined`) に揃える (#763)。
const (
	SearchOriginLocal    = "local"
	SearchOriginRemote   = "remote"
	SearchOriginCombined = "combined"
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
	// SearchUsers は display name / username / description で user を検索する
	// (upstream UserSearchService 相当、#1939)。origin は SearchOriginLocal /
	// SearchOriginRemote / SearchOriginCombined。空文字列 / 未知値は Combined 扱い。
	SearchUsers(query, meID string, limit, offset int, origin string) ([]*model.User, error)
	// SearchByUsernameAndHost は username prefix と host を SQL で絞り込む
	// (#766 / #1054 / #1064)。upstream Misskey TS の同名 endpoint と同じ
	// semantics:
	//   - localOnly=true       → host IS NULL (= local 限定)
	//   - localOnly=false / host=nil → host filter なし (= local + remote 両方返す)
	//   - localOnly=false / host=*string → lower("host") LIKE lower(?) ESCAPE '\'
	//     (prefix match)
	// caller は username / host を pre-lowercase して渡すこと。self-hostname
	// や "." の取り扱い (= local 限定への remap) は Service レイヤで行う。
	SearchByUsernameAndHost(query string, host *string, localOnly bool, limit int) ([]*model.User, error)
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
	// HardDeleteUser permanently removes the user row. FK ON DELETE CASCADE purges
	// dependent rows (profile, keypair, following, blocking, ...). Used by the
	// delete-account cascade for local users (Soft=false) so the account fully
	// disappears instead of lingering as a suspended tombstone (#2230).
	HardDeleteUser(userID string) error
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
	q := r.db.Model(&model.User{}).Where("id = ?", userID)
	if delta > 0 {
		// upstream incNotesCountOfUser は notesCount の増分と同時に
		// updatedAt を現在時刻で書く。削除側 (delta < 0) では触らない
		// ので、ここも増分時のみ更新する (#2285)。
		return q.UpdateColumns(map[string]any{
			"notesCount": gorm.Expr("\"notesCount\" + ?", delta),
			"updatedAt":  time.Now(),
		}).Error
	}
	return q.UpdateColumn("notesCount", gorm.Expr("\"notesCount\" + ?", delta)).Error
}

// SearchUsers searches users by display name (ILIKE partial) OR username (prefix
// for an @handle query, partial for a username-shaped query), with a
// userProfile.description ILIKE fallback appended when fewer than `limit` rows
// match. Mirrors upstream UserSearchService.search (#1939): only active users
// (updatedAt within 30 days or NULL), isSuspended excluded, muted users excluded
// when meID is set, ordered by updatedAt DESC NULLS LAST. query is the raw user
// input (with any leading @ and original case preserved).
func (r *userRepository) SearchUsers(query, meID string, limit, offset int, origin string) ([]*model.User, error) {
	if limit <= 0 {
		limit = 10
	}
	activeThreshold := time.Now().Add(-30 * 24 * time.Hour)
	escaped := escapeSQLLikePattern(query)

	// name ILIKE '%query%' OR username 一致 (upstream nameQuery の Brackets)。
	// @handle (空白なし・@ が先頭のみ) は usernameLower 前方一致、username 形式の
	// query は usernameLower 部分一致。それ以外は name のみ。
	nameOrUsername := r.db.Where(`"name" ILIKE ? ESCAPE '\'`, "%"+escaped+"%")
	isUsername := strings.HasPrefix(query, "@") && !strings.Contains(query, " ") && !strings.Contains(query[1:], "@")
	if isUsername {
		uname := escapeSQLLikePattern(strings.ToLower(strings.ReplaceAll(query, "@", "")))
		nameOrUsername = nameOrUsername.Or(`"usernameLower" LIKE ? ESCAPE '\'`, uname+"%")
	} else if localUsernameSearchPattern.MatchString(query) {
		nameOrUsername = nameOrUsername.Or(`"usernameLower" LIKE ? ESCAPE '\'`, "%"+escapeSQLLikePattern(strings.ToLower(query))+"%")
	}

	mutingSub := func() *gorm.DB {
		return r.db.Table("muting").Select(`"muteeId"`).Where(`"muterId" = ?`, meID)
	}

	q := r.db.Model(&model.User{}).
		Where(nameOrUsername).
		Where(`("updatedAt" IS NULL OR "updatedAt" > ?)`, activeThreshold).
		Where(`"isSuspended" = false`)
	q = applyUserSearchOrigin(q, origin)
	if meID != "" {
		q = q.Where(`"id" NOT IN (?)`, mutingSub())
	}

	var users []*model.User
	if err := q.Order(`"updatedAt" DESC NULLS LAST`).Limit(limit).Offset(offset).Find(&users).Error; err != nil {
		return nil, err
	}
	if len(users) >= limit {
		return users, nil
	}

	// description fallback (upstream profQuery)。既に name/username で hit した ID は
	// 除外して重複を避ける (upstream は重複しうるが mk-go では dedup する)。
	foundIDs := make([]string, 0, len(users))
	for _, u := range users {
		foundIDs = append(foundIDs, u.ID)
	}
	profSub := r.db.Table("user_profile").Select(`"userId"`).Where(`"description" ILIKE ? ESCAPE '\'`, "%"+escaped+"%")
	switch origin {
	case SearchOriginLocal:
		profSub = profSub.Where(`"userHost" IS NULL`)
	case SearchOriginRemote:
		profSub = profSub.Where(`"userHost" IS NOT NULL`)
	}
	if meID != "" {
		profSub = profSub.Where(`"userId" NOT IN (?)`, mutingSub())
	}
	dq := r.db.Model(&model.User{}).
		Where(`"id" IN (?)`, profSub).
		Where(`("updatedAt" IS NULL OR "updatedAt" > ?)`, activeThreshold).
		Where(`"isSuspended" = false`)
	if len(foundIDs) > 0 {
		dq = dq.Where(`"id" NOT IN ?`, foundIDs)
	}
	var descUsers []*model.User
	if err := dq.Order(`"updatedAt" DESC NULLS LAST`).Limit(limit).Offset(offset).Find(&descUsers).Error; err != nil {
		return nil, err
	}
	return append(users, descUsers...), nil
}

// applyUserSearchOrigin adds the local/remote host filter shared by the name and
// description user-search queries.
func applyUserSearchOrigin(q *gorm.DB, origin string) *gorm.DB {
	switch origin {
	case SearchOriginLocal:
		return q.Where(`"host" IS NULL`)
	case SearchOriginRemote:
		return q.Where(`"host" IS NOT NULL`)
	}
	return q
}

// SearchByUsernameAndHost narrows username prefix matches with a host filter.
// #766 fix: SearchByUsername の origin 軸では「特定 host への絞り込み」が
// できないので分離した sibling method。
//
// 3-state semantics (upstream Misskey TS の UserSearchService と一致):
//   - localOnly=true            → "host" IS NULL (= local 限定)
//   - localOnly=false, host=nil → host filter なし (= local + remote 両方)
//   - localOnly=false, host=ptr → lower("host") LIKE lower(?) ESCAPE '\'
//     (prefix match)
//
// #1064 fix: 旧来 host=nil で local 強制していたが、upstream
// `generateUserQueryBuilder` は `params.host` falsy 時に host filter を一切
// 付けない (= local + remote 両方返す) ため drop-in 互換性違反だった。
// frontend MkAutocomplete は `@alice` だけ入力したとき host=undefined を送る
// ので、host=nil でも remote user を候補に含める必要がある。upstream で
// local 限定にしたい場合は `host === config.hostname` か `host === '.'` で
// gate する設計なので、その判定は Service 側 (= self-hostname を知っている
// 層) で行い、ここには `localOnly` フラグで降ろす。
//
// host comparison は upstream Misskey TS の UserSearchService と一致させて
// `lower(host) LIKE lower(?) || '%'` の prefix match (#1054)。frontend の
// MkAutocomplete は `@alice@rem` のように host を途中まで入力した状態で
// API を叩くため、完全一致だと remote user 候補が一切出ない。
//
// upstream Misskey TS の UserSearchService.searchByUsernameAndHost も同様に
// usernameLower prefix match + host case-insensitive prefix match を適用し、
// `isSuspended = FALSE` で suspended user を除外する (#878)。mk-go も同 filter
// を共有して drop-in 互換を維持する。
//
// LIKE wildcard (`%` / `_`) の escape:
//   - host 側: escapeSQLLikePattern で escape (#1054)。
//   - username 側: 同じく escapeSQLLikePattern で escape (#1061)。username 仕様
//     で `_` が許容されるため、escape 無しだと `alice_bob` を search すると
//     `_` を 1 文字 wildcard と解釈して `alice1bob` 等の関係ない user も
//     hit してしまう。upstream Misskey TS UserSearchService.ts:197 と一致。
//
// なお upstream TS は logged-in caller に対して updatedAt > / <= threshold の
// 4-query 優先順位 (followee active/inactive + non-followee active/inactive)
// を適用する。これは新規 signup user (updatedAt = NULL) を search 結果から
// 除外する quirk があり、brand-new user の discoverability を悪化させる。
// mk-go では本 quirk を意図的に採用せず、followersCount DESC + id ASC の
// 単純な sort を維持する (= 新規 user も即 search hit する)。詳細は #878。
func (r *userRepository) SearchByUsernameAndHost(query string, host *string, localOnly bool, limit int) ([]*model.User, error) {
	var users []*model.User
	q := r.db.
		Where(`"usernameLower" LIKE ? ESCAPE '\'`, escapeSQLLikePattern(query)+"%").
		Where("\"isSuspended\" = false")
	switch {
	case localOnly:
		q = q.Where("\"host\" IS NULL")
	case host != nil:
		q = q.Where(`lower("host") LIKE lower(?) ESCAPE '\'`, escapeSQLLikePattern(*host)+"%")
	}
	if err := q.
		Order("\"followersCount\" DESC, id ASC").
		Limit(limit).
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

// HardDeleteUser permanently removes the user row, relying on FK ON DELETE
// CASCADE to purge dependent rows (profile/keypair/following/...) (#2230).
func (r *userRepository) HardDeleteUser(userID string) error {
	if userID == "" {
		return nil
	}
	return r.db.Exec(`DELETE FROM "user" WHERE "id" = ?`, userID).Error
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
		// upstream show-users.ts:97 / users.ts は hostname を lowercase 化して
		// 突合する。host は lowercase 保存なので大文字混在でも match させる。
		q = q.Where("host = ?", strings.ToLower(filter.Hostname))
	}
	// public /users endpoint の base filter (upstream users.ts:58-59、#1957-b)。
	if filter.ExplorableOnly {
		q = q.Where(`"isExplorable" = true`).Where(`"isSuspended" = false`)
	}
	// 認証 caller が mute / block している user、および caller を block している
	// user を除外する (upstream generateMutedUserQueryForUsers /
	// generateBlockQueryForUsers、#1957-b)。block は双方向、mute は片方向。
	if filter.ExcludeRelatedTo != "" {
		vid := filter.ExcludeRelatedTo
		q = q.Where(`id NOT IN (SELECT "muteeId" FROM muting WHERE "muterId" = ?)`, vid).
			Where(`id NOT IN (SELECT "blockeeId" FROM blocking WHERE "blockerId" = ?)`, vid).
			Where(`id NOT IN (SELECT "blockerId" FROM blocking WHERE "blockeeId" = ?)`, vid)
	}
	// username prefix フィルタ (upstream show-users.ts:92-94 の
	// usernameLower LIKE sqlLikeEscape(lower)+'%')。LIKE メタ文字を escape し
	// ESCAPE '\' を明示して prefix match させる (sibling query と統一)。
	if filter.Username != "" {
		q = q.Where(`"usernameLower" LIKE ? ESCAPE '\'`, escapeSQLLikePattern(strings.ToLower(filter.Username))+"%")
	}

	switch filter.State {
	case "suspended":
		q = q.Where("\"isSuspended\" = true")
	case "available":
		// upstream show-users.ts:64 — available = isSuspended = FALSE (#1948-12)。
		q = q.Where("\"isSuspended\" = false")
	case "alive":
		// upstream show-users.ts:65 — alive は updatedAt > now-5d の「直近アクティブ」
		// window で、available (= 未 suspend) とは別 filter。以前は alive を available
		// の synonym 扱いにしていたが upstream と乖離していた (#1948-12)。
		q = q.Where(`"updatedAt" > ?`, time.Now().Add(-5*24*time.Hour))
	case "admin", "moderator", "adminOrModerator":
		// upstream show-users.ts は getAdministratorIds / getModeratorIds で role 由来の
		// id 集合に絞る。これらは (1) host を問わない (origin param が別途 host を filter
		// する)、(2) expiresAt を filter しない (getModeratorIds の excludeExpire は既定
		// false、getAdministratorIds は expiry 非考慮)、(3) root を含めない
		// (getAdministratorIds は TODO で未対応 / getModeratorIds の includeRoot 既定
		// false) — の 3 点を満たす。以前の mk-go は host IS NULL / expiresAt filter /
		// root union を付けており upstream と乖離していた (#1948-12)。
		//
		// upstream #17334 の「複数 admin role を持つ user の id 重複」は、本 SQL の
		// `id IN (SELECT ra."userId" ...)` が PostgreSQL の IN semantics で自動 dedup
		// するため等価に解消される。
		var roleCond string
		switch filter.State {
		case "admin":
			roleCond = `r."isAdministrator" = true`
		case "moderator":
			roleCond = `r."isModerator" = true`
		default: // adminOrModerator
			roleCond = `(r."isAdministrator" = true OR r."isModerator" = true)`
		}
		q = q.Where(`id IN (
			SELECT ra."userId" FROM role_assignment ra
			JOIN role r ON ra."roleId" = r.id
			WHERE ` + roleCond + `
		)`)
	}

	// sort 方向は upstream admin/show-users.ts:99-110 に一致させる。'+' は
	// 降順 (新しい/多い順)、'-' は昇順という Misskey の慣習で、key 名も
	// follower 系は +follower/-follower (followersCount 列) を使う。
	switch filter.Sort {
	case "+createdAt":
		q = q.Order("id DESC")
	case "-createdAt":
		q = q.Order("id ASC")
	case "+updatedAt":
		// public users.ts は updatedAt sort 時 IS NOT NULL を andWhere して NULL
		// updatedAt user を除外する (#1975)。admin/show-users は NULLS LAST で
		// 保持するため UpdatedAtSortNonNull は public /users のみ true。
		if filter.UpdatedAtSortNonNull {
			q = q.Where(`"updatedAt" IS NOT NULL`)
		}
		q = q.Order(`"updatedAt" DESC NULLS LAST`)
	case "-updatedAt":
		if filter.UpdatedAtSortNonNull {
			q = q.Where(`"updatedAt" IS NOT NULL`)
		}
		q = q.Order(`"updatedAt" ASC NULLS FIRST`)
	case "+lastActiveDate":
		q = q.Order(`"lastActiveDate" DESC NULLS LAST`)
	case "-lastActiveDate":
		q = q.Order(`"lastActiveDate" ASC NULLS FIRST`)
	case "+follower":
		q = q.Order(`"followersCount" DESC`)
	case "-follower":
		q = q.Order(`"followersCount" ASC`)
	default:
		// cursor 指定時は下で id order を付けるので default order は付けない。
		if filter.SinceID == "" && filter.UntilID == "" {
			q = q.Order("id ASC")
		}
	}

	// cursor pagination (federation/users 等、#1732)。非空のときは Sort/Offset を
	// 無視して id cursor で絞る (makePaginationQuery 互換)。
	cursor := filter.SinceID != "" || filter.UntilID != ""
	if filter.SinceID != "" {
		q = q.Where(`"id" > ?`, filter.SinceID)
	}
	if filter.UntilID != "" {
		q = q.Where(`"id" < ?`, filter.UntilID)
	}
	if cursor {
		// sinceId のみ指定時は ASC、それ以外は DESC (Misskey keyset 互換)。
		if filter.SinceID != "" && filter.UntilID == "" {
			q = q.Order("id ASC")
		} else {
			q = q.Order("id DESC")
		}
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	q = q.Limit(limit)
	if !cursor && filter.Offset > 0 {
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
		// upstream recommendation.ts:64-67 と同じく viewer が mute / block している
		// user、および viewer を block している user を除外する (#1547)。
		Where(`id NOT IN (SELECT "muteeId" FROM "muting" WHERE "muterId" = ?)`, viewerID).
		Where(`id NOT IN (SELECT "blockeeId" FROM "blocking" WHERE "blockerId" = ?)`, viewerID).
		Where(`id NOT IN (SELECT "blockerId" FROM "blocking" WHERE "blockeeId" = ?)`, viewerID).
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
