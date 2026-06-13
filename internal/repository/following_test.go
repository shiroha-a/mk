package repository

import (
	"context"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func insertFollowing(t *testing.T, id, followerID, followeeID string) *model.Following {
	t.Helper()
	f := &model.Following{
		ID:         id,
		FollowerID: followerID,
		FolloweeID: followeeID,
	}
	require.NoError(t, testDB.Create(f).Error)
	return f
}

func TestFollowingRepository_Create_FindByPair(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	follower := insertTestUser(t, "u_fl_1", "follower1")
	defer cleanupUser(t, follower.ID)
	followee := insertTestUser(t, "u_fl_2", "followee1")
	defer cleanupUser(t, followee.ID)

	f := &model.Following{
		ID:         "fl_1",
		FollowerID: follower.ID,
		FolloweeID: followee.ID,
	}
	require.NoError(t, repo.Create(f))
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, f.ID)

	found, err := repo.FindByPair(follower.ID, followee.ID)
	require.NoError(t, err)
	assert.Equal(t, f.ID, found.ID)
}

func TestFollowingRepository_FindByPair_NotFound(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	_, err := repo.FindByPair("ghost1", "ghost2")
	assert.Error(t, err)
}

func TestFollowingRepository_Exists(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	follower := insertTestUser(t, "u_fl_3", "follower3")
	defer cleanupUser(t, follower.ID)
	followee := insertTestUser(t, "u_fl_4", "followee4")
	defer cleanupUser(t, followee.ID)

	exists, err := repo.Exists(follower.ID, followee.ID)
	require.NoError(t, err)
	assert.False(t, exists)

	insertFollowing(t, "fl_2", follower.ID, followee.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "fl_2")

	exists, err = repo.Exists(follower.ID, followee.ID)
	require.NoError(t, err)
	assert.True(t, exists)
}

// TestFollowingRepository_FilterFollowingsFromAnchor + ToAnchor cover the
// batch lookup added for users/{followers,following} relation flag
// population (#1144). 空 input は短絡で nil 返却、通常 input は anchor が
// follow している (or anchor を follow している) candidate のみ返る。
// cancelled ctx の error path も別 test で cover。
func TestFollowingRepository_FilterFollowingsFromAnchor(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	anchor := insertTestUser(t, "u_ff_a", "anchorff")
	defer cleanupUser(t, anchor.ID)
	t1 := insertTestUser(t, "u_ff_t1", "targetff1")
	defer cleanupUser(t, t1.ID)
	t2 := insertTestUser(t, "u_ff_t2", "targetff2")
	defer cleanupUser(t, t2.ID)

	insertFollowing(t, "ff_1", anchor.ID, t1.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "ff_1")

	// 空入力は repo を叩かずに nil 返却。
	out, err := repo.FilterFollowingsFromAnchor("", []string{t1.ID})
	require.NoError(t, err)
	assert.Nil(t, out)
	out, err = repo.FilterFollowingsFromAnchor(anchor.ID, nil)
	require.NoError(t, err)
	assert.Nil(t, out)

	// 通常入力: anchor は t1 のみ follow → t1 だけ返る (t2 は除外、ghost も miss)。
	out, err = repo.FilterFollowingsFromAnchor(anchor.ID, []string{t1.ID, t2.ID, "ghost"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{t1.ID}, out)
}

func TestFollowingRepository_FilterFollowingsToAnchor(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	anchor := insertTestUser(t, "u_ft_a", "anchorft")
	defer cleanupUser(t, anchor.ID)
	c1 := insertTestUser(t, "u_ft_c1", "candft1")
	defer cleanupUser(t, c1.ID)
	c2 := insertTestUser(t, "u_ft_c2", "candft2")
	defer cleanupUser(t, c2.ID)

	insertFollowing(t, "ft_1", c1.ID, anchor.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "ft_1")

	out, err := repo.FilterFollowingsToAnchor("", []string{c1.ID})
	require.NoError(t, err)
	assert.Nil(t, out)
	out, err = repo.FilterFollowingsToAnchor(anchor.ID, nil)
	require.NoError(t, err)
	assert.Nil(t, out)

	out, err = repo.FilterFollowingsToAnchor(anchor.ID, []string{c1.ID, c2.ID, "ghost"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{c1.ID}, out)
}

// TestFollowingRepository_FilterFollowings_QueryError covers the DB error
// branch of both Filter helpers (cancelled ctx → driver error → bubble up).
func TestFollowingRepository_FilterFollowings_QueryError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := testDB.WithContext(ctx)
	repo := NewFollowingRepository(db)
	_, err := repo.FilterFollowingsFromAnchor("u", []string{"v"})
	assert.Error(t, err)
	_, err = repo.FilterFollowingsToAnchor("u", []string{"v"})
	assert.Error(t, err)
}

func TestFollowingRepository_Delete(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	follower := insertTestUser(t, "u_fl_5", "follower5")
	defer cleanupUser(t, follower.ID)
	followee := insertTestUser(t, "u_fl_6", "followee6")
	defer cleanupUser(t, followee.ID)

	f := insertFollowing(t, "fl_3", follower.ID, followee.ID)
	require.NoError(t, repo.Delete(f))

	exists, err := repo.Exists(follower.ID, followee.ID)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestFollowingRepository_ListFollowers_ListFollowing(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	a := insertTestUser(t, "u_fl_7", "user7")
	defer cleanupUser(t, a.ID)
	b := insertTestUser(t, "u_fl_8", "user8")
	defer cleanupUser(t, b.ID)
	c := insertTestUser(t, "u_fl_9", "user9")
	defer cleanupUser(t, c.ID)

	insertFollowing(t, "fl_4", a.ID, b.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "fl_4")
	insertFollowing(t, "fl_5", c.ID, b.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "fl_5")
	insertFollowing(t, "fl_6", a.ID, c.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "fl_6")

	followers, err := repo.ListFollowers(b.ID, 10, 0)
	require.NoError(t, err)
	assert.Len(t, followers, 2)

	followingsOfA, err := repo.ListFollowing(a.ID, 10, 0)
	require.NoError(t, err)
	assert.Len(t, followingsOfA, 2)
}

// ListFollowersToNotify は notify='normal' の follower row だけを返す (#1559)。
func TestFollowingRepository_ListFollowersToNotify(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	followee := insertTestUser(t, "u_ntf_fe", "ntffe")
	defer cleanupUser(t, followee.ID)
	notifyFollower := insertTestUser(t, "u_ntf_on", "ntfon")
	defer cleanupUser(t, notifyFollower.ID)
	silentFollower := insertTestUser(t, "u_ntf_off", "ntfoff")
	defer cleanupUser(t, silentFollower.ID)

	// notify='normal' の follow edge
	normal := "normal"
	on := &model.Following{ID: "ntf_on", FollowerID: notifyFollower.ID, FolloweeID: followee.ID, Notify: &normal}
	require.NoError(t, testDB.Create(on).Error)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, on.ID)
	// notify 無し (null) の follow edge は対象外
	insertFollowing(t, "ntf_off", silentFollower.ID, followee.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "ntf_off")

	rows, err := repo.ListFollowersToNotify(followee.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, notifyFollower.ID, rows[0].FollowerID)
}

func TestFollowingRepository_QueryErrors(t *testing.T) {
	// cancelledなcontextを使ってgorm経由でerrorを発生させる
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := testDB.WithContext(ctx)
	repo := NewFollowingRepository(db)

	_, err := repo.Exists("a", "b")
	assert.Error(t, err)

	_, err = repo.ListFollowers("a", 10, 0)
	assert.Error(t, err)

	_, err = repo.ListFollowersToNotify("a")
	assert.Error(t, err)

	_, err = repo.ListFollowing("a", 10, 0)
	assert.Error(t, err)

	_, err = repo.ListRemoteFollowerInboxes("a")
	assert.Error(t, err)
}

func TestFollowingRepository_ListRemoteFollowerInboxes(t *testing.T) {
	repo := NewFollowingRepository(testDB)

	// Local followee (id を短く保つ — token は char(16) なので "tok_"+id<=16)
	followee := insertTestUser(t, "u_inb_fe", "inbfe")
	defer cleanupUser(t, followee.ID)

	// Remote follower with sharedInbox
	host := "remote.example"
	sharedInbox1 := "https://remote.example/inbox"
	inbox1 := "https://remote.example/users/r1/inbox"
	uri1 := "https://remote.example/users/r1"
	require.NoError(t, testDB.Exec(`INSERT INTO "user" (id, username, "usernameLower", host, uri, inbox, "sharedInbox", "avatarDecorations") VALUES (?, ?, ?, ?, ?, ?, ?, '[]')`,
		"u_inb_r1", "r1", "r1", host, uri1, inbox1, sharedInbox1).Error)
	defer cleanupUser(t, "u_inb_r1")

	// Remote follower with same sharedInbox (集約対象)
	inbox2 := "https://remote.example/users/r2/inbox"
	uri2 := "https://remote.example/users/r2"
	require.NoError(t, testDB.Exec(`INSERT INTO "user" (id, username, "usernameLower", host, uri, inbox, "sharedInbox", "avatarDecorations") VALUES (?, ?, ?, ?, ?, ?, ?, '[]')`,
		"u_inb_r2", "r2", "r2", host, uri2, inbox2, sharedInbox1).Error)
	defer cleanupUser(t, "u_inb_r2")

	// Remote follower without sharedInbox (個別inboxを返す)
	host2 := "other.example"
	inbox3 := "https://other.example/users/r3/inbox"
	uri3 := "https://other.example/users/r3"
	require.NoError(t, testDB.Exec(`INSERT INTO "user" (id, username, "usernameLower", host, uri, inbox, "avatarDecorations") VALUES (?, ?, ?, ?, ?, ?, '[]')`,
		"u_inb_r3", "r3", "r3", host2, uri3, inbox3).Error)
	defer cleanupUser(t, "u_inb_r3")

	// Local follower (除外対象)
	local := insertTestUser(t, "u_inb_lo", "inblo")
	defer cleanupUser(t, local.ID)

	insertFollowing(t, "fl_inb_1", "u_inb_r1", followee.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "fl_inb_1")
	insertFollowing(t, "fl_inb_2", "u_inb_r2", followee.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "fl_inb_2")
	insertFollowing(t, "fl_inb_3", "u_inb_r3", followee.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "fl_inb_3")
	insertFollowing(t, "fl_inb_4", local.ID, followee.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "fl_inb_4")

	inboxes, err := repo.ListRemoteFollowerInboxes(followee.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{sharedInbox1, inbox3}, inboxes)
}

func TestUserRepository_IncrementFollowingCount(t *testing.T) {
	repo := NewUserRepository(testDB)
	user := insertTestUser(t, "u_f_inc_1", "fincuser1")
	defer cleanupUser(t, user.ID)

	require.NoError(t, repo.IncrementFollowingCount(user.ID, 1))
	found, _ := repo.FindByID(user.ID)
	assert.Equal(t, 1, found.FollowingCount)

	require.NoError(t, repo.IncrementFollowingCount(user.ID, -1))
	found, _ = repo.FindByID(user.ID)
	assert.Equal(t, 0, found.FollowingCount)
}

// DeleteAllByUser は target が follower か followee のどちらに入っていても
// 消え、target と無関係な行には触らないことを確認する。
func TestFollowingRepository_DeleteAllByUser(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	target := insertTestUser(t, "u_dab_target", "dabtarget")
	other := insertTestUser(t, "u_dab_other", "dabother")
	bystander := insertTestUser(t, "u_dab_by", "dabby")
	defer cleanupUser(t, target.ID)
	defer cleanupUser(t, other.ID)
	defer cleanupUser(t, bystander.ID)

	insertFollowing(t, "fl_dab_1", target.ID, other.ID)
	insertFollowing(t, "fl_dab_2", other.ID, target.ID)
	insertFollowing(t, "fl_dab_3", other.ID, bystander.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id IN (?, ?, ?)`, "fl_dab_1", "fl_dab_2", "fl_dab_3")

	n, err := repo.DeleteAllByUser(target.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)

	// target 絡みの 2 件は消え、無関係は残る
	_, err = repo.FindByPair(target.ID, other.ID)
	assert.Error(t, err)
	_, err = repo.FindByPair(other.ID, target.ID)
	assert.Error(t, err)
	found, err := repo.FindByPair(other.ID, bystander.ID)
	require.NoError(t, err)
	assert.Equal(t, "fl_dab_3", found.ID)
}

func TestUserRepository_IncrementFollowersCount(t *testing.T) {
	repo := NewUserRepository(testDB)
	user := insertTestUser(t, "u_inc_2", "incuser2")
	defer cleanupUser(t, user.ID)

	require.NoError(t, repo.IncrementFollowersCount(user.ID, 1))
	found, _ := repo.FindByID(user.ID)
	assert.Equal(t, 1, found.FollowersCount)

	require.NoError(t, repo.IncrementFollowersCount(user.ID, -1))
	found, _ = repo.FindByID(user.ID)
	assert.Equal(t, 0, found.FollowersCount)
}

// insertBirthdayProfile は user_profile 行を birthday 付きで直接 INSERT する。
// 他のテストで既存プロフィールが作られている場合は UPSERT になる。
func insertBirthdayProfile(t *testing.T, userID, birthday string) {
	t.Helper()
	require.NoError(t, testDB.Exec(
		`INSERT INTO "user_profile" ("userId", birthday) VALUES (?, ?)
         ON CONFLICT ("userId") DO UPDATE SET birthday = EXCLUDED.birthday`,
		userID, birthday).Error)
}

func TestFollowingRepository_ListFollowingByBirthday(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	me := insertTestUser(t, "u_bd_me", "bdme")
	defer cleanupUser(t, me.ID)
	fe1 := insertTestUser(t, "u_bd_fe1", "bdfe1")
	defer cleanupUser(t, fe1.ID)
	fe2 := insertTestUser(t, "u_bd_fe2", "bdfe2")
	defer cleanupUser(t, fe2.ID)
	fe3 := insertTestUser(t, "u_bd_fe3", "bdfe3")
	defer cleanupUser(t, fe3.ID)

	insertBirthdayProfile(t, fe1.ID, "1990-05-10")
	insertBirthdayProfile(t, fe2.ID, "1991-12-30")
	insertBirthdayProfile(t, fe3.ID, "1992-01-03")

	insertFollowing(t, "fl_bd_1", me.ID, fe1.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "fl_bd_1")
	insertFollowing(t, "fl_bd_2", me.ID, fe2.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "fl_bd_2")
	insertFollowing(t, "fl_bd_3", me.ID, fe3.ID)
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "fl_bd_3")

	// 単発: 5/10 のみ。
	rows, err := repo.ListFollowingByBirthday(me.ID, 510, 510, 10, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, fe1.ID, rows[0].FolloweeID)

	// 範囲 (年跨ぎ): 12/25..1/5 → fe2, fe3 (mmdd 昇順なので 103, 1230)。
	rows2, err := repo.ListFollowingByBirthday(me.ID, 1225, 105, 10, 0)
	require.NoError(t, err)
	require.Len(t, rows2, 2)
	// mmdd ASC なので fe3 (0103) が先、fe2 (1230) が後。
	assert.Equal(t, fe3.ID, rows2[0].FolloweeID)
	assert.Equal(t, fe2.ID, rows2[1].FolloweeID)

	// 範囲 (正常): 1/1..6/30 → fe1 (05-10), fe3 (01-03)。
	rows3, err := repo.ListFollowingByBirthday(me.ID, 101, 630, 10, 0)
	require.NoError(t, err)
	require.Len(t, rows3, 2)
}

func TestFollowingRepository_ListFollowingByBirthday_LimitDefaultsAndOffset(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	// limit <= 0 / > 100 のパスを通す。呼び出しが成功すれば良い。
	_, err := repo.ListFollowingByBirthday("nobody", 1, 1231, 0, 0)
	require.NoError(t, err)
	_, err = repo.ListFollowingByBirthday("nobody", 1, 1231, 500, 0)
	require.NoError(t, err)
}

func TestFollowingRepository_ListFollowingByBirthday_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewFollowingRepository(testDB.WithContext(ctx))
	_, err := repo.ListFollowingByBirthday("me", 1, 1231, 10, 0)
	assert.Error(t, err)
}

// --- 追加テスト (#260 repository coverage: 0%関数) ---

// TestFollowingRepository_ListFollowersByHost guards upstream parity: the
// federation/followers query filters by followeeHost (= followed users belong
// to the given remote host), matching 本家 followers.ts. A row whose
// followerHost (but not followeeHost) matches must NOT be returned.
func TestFollowingRepository_ListFollowersByHost(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	localr := insertTestUser(t, "flh_l1", "localfollower")
	defer cleanupUser(t, localr.ID)

	host := "remote.example"
	remoteFollowee := &model.User{
		ID:                "flh_e1",
		Username:          "remote_followee",
		UsernameLower:     "remote_followee",
		Host:              &host,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, testDB.Create(remoteFollowee).Error)
	defer cleanupUser(t, remoteFollowee.ID)

	// followeeHost=host のフォロー (local が remote をフォロー) が対象。
	match := &model.Following{
		ID:           "flh_match",
		FollowerID:   localr.ID,
		FolloweeID:   remoteFollowee.ID,
		FolloweeHost: &host,
	}
	require.NoError(t, repo.Create(match))
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, match.ID)

	// followerHost=host のみのフォローは federation/followers では対象外。
	remoteFollower := &model.User{
		ID:                "flh_r1",
		Username:          "remote_follower",
		UsernameLower:     "remote_follower",
		Host:              &host,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, testDB.Create(remoteFollower).Error)
	defer cleanupUser(t, remoteFollower.ID)
	miss := &model.Following{
		ID:           "flh_miss",
		FollowerID:   remoteFollower.ID,
		FolloweeID:   localr.ID,
		FollowerHost: &host,
	}
	require.NoError(t, repo.Create(miss))
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, miss.ID)

	rows, err := repo.ListFollowersByHost(host, 10, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, match.ID, rows[0].ID)

	// limit のデフォルト (0 → 30) と cap (>100 → 100) の分岐を通す
	_, err = repo.ListFollowersByHost(host, 0, 0)
	require.NoError(t, err)
	_, err = repo.ListFollowersByHost(host, 999, 0)
	require.NoError(t, err)
}

// TestFollowingRepository_ListFollowingByHost guards upstream parity: the
// federation/following query filters by followerHost (= followers belong to
// the given remote host), matching 本家 following.ts. A row whose followeeHost
// (but not followerHost) matches must NOT be returned.
func TestFollowingRepository_ListFollowingByHost(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	localee := insertTestUser(t, "flg_l1", "localfollowee")
	defer cleanupUser(t, localee.ID)

	host := "remote.example"
	remoteFollower := &model.User{
		ID:                "flg_r1",
		Username:          "remote_follower",
		UsernameLower:     "remote_follower",
		Host:              &host,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, testDB.Create(remoteFollower).Error)
	defer cleanupUser(t, remoteFollower.ID)

	// followerHost=host のフォロー (remote が local をフォロー) が対象。
	match := &model.Following{
		ID:           "flg_match",
		FollowerID:   remoteFollower.ID,
		FolloweeID:   localee.ID,
		FollowerHost: &host,
	}
	require.NoError(t, repo.Create(match))
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, match.ID)

	// followeeHost=host のみのフォローは federation/following では対象外。
	remoteFollowee := &model.User{
		ID:                "flg_e1",
		Username:          "remote_followee",
		UsernameLower:     "remote_followee",
		Host:              &host,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, testDB.Create(remoteFollowee).Error)
	defer cleanupUser(t, remoteFollowee.ID)
	miss := &model.Following{
		ID:           "flg_miss",
		FollowerID:   localee.ID,
		FolloweeID:   remoteFollowee.ID,
		FolloweeHost: &host,
	}
	require.NoError(t, repo.Create(miss))
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, miss.ID)

	rows, err := repo.ListFollowingByHost(host, 10, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, match.ID, rows[0].ID)

	_, err = repo.ListFollowingByHost(host, 0, 0)
	require.NoError(t, err)
	_, err = repo.ListFollowingByHost(host, 999, 0)
	require.NoError(t, err)
}

func TestFollowingRepository_UpdateRelation(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	u1 := insertTestUser(t, "fur_u1", "fur_u1")
	u2 := insertTestUser(t, "fur_u2", "fur_u2")
	defer cleanupUser(t, u1.ID)
	defer cleanupUser(t, u2.ID)

	f := &model.Following{ID: "fur_1", FollowerID: u1.ID, FolloweeID: u2.ID}
	require.NoError(t, repo.Create(f))
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, f.ID)

	// 空fieldsはno-op
	require.NoError(t, repo.UpdateRelation(u1.ID, u2.ID, map[string]any{}))

	// followerHostを更新
	newHost := "updated.example"
	require.NoError(t, repo.UpdateRelation(u1.ID, u2.ID, map[string]any{"followerHost": newHost}))

	found, err := repo.FindByPair(u1.ID, u2.ID)
	require.NoError(t, err)
	require.NotNil(t, found.FollowerHost)
	assert.Equal(t, newHost, *found.FollowerHost)
}

func TestFollowingRepository_UpdateAllByFollower(t *testing.T) {
	repo := NewFollowingRepository(testDB)
	u1 := insertTestUser(t, "uab_u1", "uab_u1")
	u2 := insertTestUser(t, "uab_u2", "uab_u2")
	u3 := insertTestUser(t, "uab_u3", "uab_u3")
	defer cleanupUser(t, u1.ID)
	defer cleanupUser(t, u2.ID)
	defer cleanupUser(t, u3.ID)

	for _, id := range []string{"uab_1", "uab_2"} {
		f := &model.Following{ID: id, FollowerID: u1.ID, FolloweeID: u2.ID}
		if id == "uab_2" {
			f.FolloweeID = u3.ID
		}
		require.NoError(t, repo.Create(f))
		defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, id)
	}

	// 空fieldsはno-op
	require.NoError(t, repo.UpdateAllByFollower(u1.ID, map[string]any{}))

	// 一括更新
	inbox := "https://remote.example/inbox"
	require.NoError(t, repo.UpdateAllByFollower(u1.ID, map[string]any{"followerInbox": inbox}))
}

var _ = context.Background // import guard (used elsewhere in file)
