package repository

import (
	"context"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func insertTestUser(t *testing.T, id, username string) *model.User {
	t.Helper()
	token := "tok_" + id
	user := &model.User{
		ID:                id,
		Username:          username,
		UsernameLower:     username,
		Token:             &token,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, testDB.Create(user).Error)
	return user
}

// insertRemoteTestUser inserts a user record marked as belonging to the
// given remote host. SearchByFilter / federation 系のテスト用ヘルパ。
func insertRemoteTestUser(t *testing.T, id, username, host string) *model.User {
	t.Helper()
	user := &model.User{
		ID:                id,
		Username:          username,
		UsernameLower:     username,
		Host:              &host,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, testDB.Create(user).Error)
	return user
}

func TestUserRepository_CreateAndFindByURI(t *testing.T) {
	repo := NewUserRepository(testDB)
	uri := "https://remote.example/users/remote1"
	host := "remote.example"
	u := &model.User{
		ID:                "remote1",
		Username:          "remote1",
		UsernameLower:     "remote1",
		URI:               &uri,
		Host:              &host,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, repo.Create(u))
	defer cleanupUser(t, u.ID)

	got, err := repo.FindByURI(uri)
	require.NoError(t, err)
	assert.Equal(t, "remote1", got.ID)
}

func TestUserRepository_FindByURI_NotFound(t *testing.T) {
	repo := NewUserRepository(testDB)
	_, err := repo.FindByURI("https://nope.example/x")
	assert.Error(t, err)
}

func TestUserRepository_Create_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewUserRepository(testDB.WithContext(ctx))
	err := repo.Create(&model.User{ID: "x", Username: "x", UsernameLower: "x"})
	assert.Error(t, err)
}

func cleanupUser(t *testing.T, id string) {
	t.Helper()
	testDB.Exec(`DELETE FROM "user_profile" WHERE "userId" = ?`, id)
	testDB.Exec(`DELETE FROM "user" WHERE id = ?`, id)
}

func TestUserRepository_FindByID(t *testing.T) {
	repo := NewUserRepository(testDB)
	user := insertTestUser(t, "u_fbi_1", "findbyid_user")
	defer cleanupUser(t, user.ID)

	found, err := repo.FindByID(user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.ID, found.ID)
	assert.Equal(t, "findbyid_user", found.Username)
}

func TestUserRepository_FindByID_NotFound(t *testing.T) {
	repo := NewUserRepository(testDB)

	_, err := repo.FindByID("nonexistent_id")
	assert.Error(t, err)
}

func TestUserRepository_FindByToken(t *testing.T) {
	repo := NewUserRepository(testDB)
	user := insertTestUser(t, "u_fbt_1", "findbytoken_user")
	defer cleanupUser(t, user.ID)

	found, err := repo.FindByToken("tok_u_fbt_1")
	require.NoError(t, err)
	assert.Equal(t, user.ID, found.ID)
}

func TestUserRepository_FindByToken_NotFound(t *testing.T) {
	repo := NewUserRepository(testDB)

	_, err := repo.FindByToken("invalid_token")
	assert.Error(t, err)
}

func TestUserRepository_FindByUsernameLower_Local(t *testing.T) {
	repo := NewUserRepository(testDB)
	user := insertTestUser(t, "u_fun_1", "localuser")
	defer cleanupUser(t, user.ID)

	found, err := repo.FindByUsernameLower("localuser", nil)
	require.NoError(t, err)
	assert.Equal(t, user.ID, found.ID)
}

func TestUserRepository_FindByUsernameLower_Remote(t *testing.T) {
	repo := NewUserRepository(testDB)

	host := "remote.example.com"
	remoteUser := &model.User{
		ID:                "u_fun_2",
		Username:          "remoteuser",
		UsernameLower:     "remoteuser",
		Host:              &host,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, testDB.Create(remoteUser).Error)
	defer cleanupUser(t, remoteUser.ID)

	found, err := repo.FindByUsernameLower("remoteuser", &host)
	require.NoError(t, err)
	assert.Equal(t, remoteUser.ID, found.ID)
	assert.Equal(t, &host, found.Host)
}

func TestUserRepository_FindProfileByUserID(t *testing.T) {
	repo := NewUserRepository(testDB)
	user := insertTestUser(t, "u_fpb_1", "profileuser")
	defer cleanupUser(t, user.ID)

	desc := "test description"
	profile := &model.UserProfile{
		UserID:      user.ID,
		Description: &desc,
		Fields:      datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, testDB.Create(profile).Error)

	found, err := repo.FindProfileByUserID(user.ID)
	require.NoError(t, err)
	assert.Equal(t, &desc, found.Description)
}

func TestUserRepository_FindProfileByUserID_NotFound(t *testing.T) {
	repo := NewUserRepository(testDB)

	_, err := repo.FindProfileByUserID("nonexistent_user")
	assert.Error(t, err)
}

func TestUserRepository_FindManyByIDs(t *testing.T) {
	repo := NewUserRepository(testDB)
	u1 := insertTestUser(t, "u_fmid_1", "fmid1")
	u2 := insertTestUser(t, "u_fmid_2", "fmid2")
	defer cleanupUser(t, u1.ID)
	defer cleanupUser(t, u2.ID)

	out, err := repo.FindManyByIDs([]string{u1.ID, "ghost", u2.ID})
	require.NoError(t, err)
	got := map[string]bool{}
	for _, u := range out {
		got[u.ID] = true
	}
	assert.True(t, got[u1.ID], "u1 should be returned")
	assert.True(t, got[u2.ID], "u2 should be returned")
	assert.False(t, got["ghost"], "missing rows are skipped silently")
}

func TestUserRepository_FindManyByIDs_Empty(t *testing.T) {
	repo := NewUserRepository(testDB)
	out, err := repo.FindManyByIDs(nil)
	require.NoError(t, err)
	assert.Nil(t, out)
}

func TestUserRepository_FindProfilesByUserIDs(t *testing.T) {
	repo := NewUserRepository(testDB)
	u1 := insertTestUser(t, "u_fpu_1", "fpu1")
	u2 := insertTestUser(t, "u_fpu_2", "fpu2")
	defer cleanupUser(t, u1.ID)
	defer cleanupUser(t, u2.ID)

	desc1 := "p1"
	desc2 := "p2"
	require.NoError(t, testDB.Create(&model.UserProfile{
		UserID: u1.ID, Description: &desc1, Fields: datatypes.JSON([]byte("[]")),
	}).Error)
	require.NoError(t, testDB.Create(&model.UserProfile{
		UserID: u2.ID, Description: &desc2, Fields: datatypes.JSON([]byte("[]")),
	}).Error)

	out, err := repo.FindProfilesByUserIDs([]string{u1.ID, "ghost", u2.ID})
	require.NoError(t, err)
	got := map[string]string{}
	for _, p := range out {
		if p.Description != nil {
			got[p.UserID] = *p.Description
		}
	}
	assert.Equal(t, "p1", got[u1.ID])
	assert.Equal(t, "p2", got[u2.ID])
	assert.NotContains(t, got, "ghost")
}

func TestUserRepository_FindProfilesByUserIDs_Empty(t *testing.T) {
	repo := NewUserRepository(testDB)
	out, err := repo.FindProfilesByUserIDs(nil)
	require.NoError(t, err)
	assert.Nil(t, out)
}

func TestUserRepository_FindManyByIDs_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewUserRepository(testDB.WithContext(ctx))
	_, err := repo.FindManyByIDs([]string{"x"})
	assert.Error(t, err)
}

func TestUserRepository_FindProfilesByUserIDs_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewUserRepository(testDB.WithContext(ctx))
	_, err := repo.FindProfilesByUserIDs([]string{"x"})
	assert.Error(t, err)
}

func TestUserRepository_FindManyByUsernamesAndHost_Local(t *testing.T) {
	repo := NewUserRepository(testDB)
	u1 := insertTestUser(t, "u_fmu_1", "alice")
	u2 := insertTestUser(t, "u_fmu_2", "bob")
	defer cleanupUser(t, u1.ID)
	defer cleanupUser(t, u2.ID)

	out, err := repo.FindManyByUsernamesAndHost([]string{"alice", "bob", "ghost"}, nil)
	require.NoError(t, err)
	got := map[string]bool{}
	for _, u := range out {
		got[u.ID] = true
	}
	assert.True(t, got[u1.ID])
	assert.True(t, got[u2.ID])
	assert.False(t, got["ghost"], "missing rows are skipped silently")
}

func TestUserRepository_FindManyByUsernamesAndHost_CaseInsensitive(t *testing.T) {
	repo := NewUserRepository(testDB)
	u := insertTestUser(t, "u_fmu_3", "carol")
	defer cleanupUser(t, u.ID)

	// 入力が大文字混じりでも usernameLower 列で正規化マッチする (#300 1-5)。
	out, err := repo.FindManyByUsernamesAndHost([]string{"CAROL"}, nil)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, u.ID, out[0].ID)
}

func TestUserRepository_FindManyByUsernamesAndHost_RemoteScope(t *testing.T) {
	repo := NewUserRepository(testDB)
	hostA := "a.example.com"
	hostB := "b.example.com"
	uA := insertRemoteTestUser(t, "u_fmu_4", "dave", hostA)
	uB := insertRemoteTestUser(t, "u_fmu_5", "dave", hostB)
	uLocal := insertTestUser(t, "u_fmu_6", "dave")
	defer cleanupUser(t, uA.ID)
	defer cleanupUser(t, uB.ID)
	defer cleanupUser(t, uLocal.ID)

	// host=hostA のみ返る (同名だが host 違いの remote / local は除外)。
	out, err := repo.FindManyByUsernamesAndHost([]string{"dave"}, &hostA)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, uA.ID, out[0].ID)

	// host=nil のときは local のみ返る (remote 同名はマッチしない)。
	out, err = repo.FindManyByUsernamesAndHost([]string{"dave"}, nil)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, uLocal.ID, out[0].ID)
}

func TestUserRepository_FindManyByUsernamesAndHost_Empty(t *testing.T) {
	repo := NewUserRepository(testDB)
	out, err := repo.FindManyByUsernamesAndHost(nil, nil)
	require.NoError(t, err)
	assert.Nil(t, out)
}

func TestUserRepository_FindManyByUsernamesAndHost_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewUserRepository(testDB.WithContext(ctx))
	_, err := repo.FindManyByUsernamesAndHost([]string{"x"}, nil)
	assert.Error(t, err)
}

func TestUserRepository_FindByUsernameLower_NotFound(t *testing.T) {
	repo := NewUserRepository(testDB)

	_, err := repo.FindByUsernameLower("doesnotexist", nil)
	assert.Error(t, err)

	host := "nowhere.example.com"
	_, err = repo.FindByUsernameLower("doesnotexist", &host)
	assert.Error(t, err)
}

func TestUserRepository_SearchByUsername(t *testing.T) {
	repo := NewUserRepository(testDB)
	a := insertTestUser(t, "u_sb_1", "searchalpha")
	defer cleanupUser(t, a.ID)
	b := insertTestUser(t, "u_sb_2", "searchbeta")
	defer cleanupUser(t, b.ID)

	out, err := repo.SearchByUsername("search", 10, 0, "")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(out), 2)
}

// origin filter (#763): "local" は host IS NULL のみ、"remote" は IS NOT NULL
// のみ返す。"combined" / "" は両方返す。
func TestUserRepository_SearchByUsername_OriginFilter(t *testing.T) {
	repo := NewUserRepository(testDB)
	local := insertTestUser(t, "u_so_local", "originlocal")
	defer cleanupUser(t, local.ID)
	remoteHost := "remote.example"
	remote := insertTestUser(t, "u_so_remote", "originremote")
	// host を後付けで設定 (insertTestUser は host=nil で作る前提)。
	require.NoError(t, repo.UpdateUser(remote.ID, map[string]any{"host": remoteHost}))
	defer cleanupUser(t, remote.ID)

	t.Run("local only excludes remote", func(t *testing.T) {
		out, err := repo.SearchByUsername("origin", 10, 0, SearchOriginLocal)
		require.NoError(t, err)
		ids := make(map[string]bool, len(out))
		for _, u := range out {
			ids[u.ID] = true
		}
		assert.True(t, ids[local.ID], "local user should appear")
		assert.False(t, ids[remote.ID], "remote user should NOT appear in local-only search")
	})

	t.Run("remote only excludes local", func(t *testing.T) {
		out, err := repo.SearchByUsername("origin", 10, 0, SearchOriginRemote)
		require.NoError(t, err)
		ids := make(map[string]bool, len(out))
		for _, u := range out {
			ids[u.ID] = true
		}
		assert.False(t, ids[local.ID], "local user should NOT appear in remote-only search")
		assert.True(t, ids[remote.ID], "remote user should appear")
	})

	t.Run("combined returns both", func(t *testing.T) {
		out, err := repo.SearchByUsername("origin", 10, 0, SearchOriginCombined)
		require.NoError(t, err)
		ids := make(map[string]bool, len(out))
		for _, u := range out {
			ids[u.ID] = true
		}
		assert.True(t, ids[local.ID])
		assert.True(t, ids[remote.ID])
	})

	t.Run("empty origin treated as combined", func(t *testing.T) {
		out, err := repo.SearchByUsername("origin", 10, 0, "")
		require.NoError(t, err)
		ids := make(map[string]bool, len(out))
		for _, u := range out {
			ids[u.ID] = true
		}
		assert.True(t, ids[local.ID])
		assert.True(t, ids[remote.ID])
	})
}

// host filter (#766): host=nil は local (host IS NULL) のみ、host=*string
// は当該 host のみ返す。host comparison は case-insensitive。
// IDX_user_usernameLower_local_unique 制約があるので local user は 1 つに
// 留め、remote 同士は host が違えば同 username でも OK な前提で組む。
func TestUserRepository_SearchByUsernameAndHost(t *testing.T) {
	repo := NewUserRepository(testDB)
	local := insertTestUser(t, "u_sh_local", "hosttest")
	defer cleanupUser(t, local.ID)
	remoteHost := "remote.example"
	remote := insertTestUser(t, "u_sh_remote", "hosttest_r")
	require.NoError(t, repo.UpdateUser(remote.ID, map[string]any{"host": remoteHost}))
	defer cleanupUser(t, remote.ID)
	otherHost := "other.example"
	other := insertTestUser(t, "u_sh_other", "hosttest_o")
	require.NoError(t, repo.UpdateUser(other.ID, map[string]any{"host": otherHost}))
	defer cleanupUser(t, other.ID)

	t.Run("host=nil returns only local", func(t *testing.T) {
		out, err := repo.SearchByUsernameAndHost("hosttest", nil, 10)
		require.NoError(t, err)
		ids := make(map[string]bool, len(out))
		for _, u := range out {
			ids[u.ID] = true
		}
		assert.True(t, ids[local.ID])
		assert.False(t, ids[remote.ID])
		assert.False(t, ids[other.ID])
	})

	t.Run("host=remote returns only that host", func(t *testing.T) {
		out, err := repo.SearchByUsernameAndHost("hosttest", &remoteHost, 10)
		require.NoError(t, err)
		ids := make(map[string]bool, len(out))
		for _, u := range out {
			ids[u.ID] = true
		}
		assert.False(t, ids[local.ID])
		assert.True(t, ids[remote.ID])
		assert.False(t, ids[other.ID])
	})

	t.Run("host comparison is case-insensitive", func(t *testing.T) {
		upper := "REMOTE.EXAMPLE"
		out, err := repo.SearchByUsernameAndHost("hosttest", &upper, 10)
		require.NoError(t, err)
		ids := make(map[string]bool, len(out))
		for _, u := range out {
			ids[u.ID] = true
		}
		assert.True(t, ids[remote.ID])
	})

	// upstream Misskey TS は isSuspended=TRUE の user を search 結果から
	// 除外する。mk-go も #878 fix で同 filter を適用するので、suspend した
	// user が hit から消えること。
	t.Run("isSuspended user is filtered out", func(t *testing.T) {
		require.NoError(t, repo.UpdateUser(local.ID, map[string]any{"isSuspended": true}))
		t.Cleanup(func() {
			_ = repo.UpdateUser(local.ID, map[string]any{"isSuspended": false})
		})
		out, err := repo.SearchByUsernameAndHost("hosttest", nil, 10)
		require.NoError(t, err)
		ids := make(map[string]bool, len(out))
		for _, u := range out {
			ids[u.ID] = true
		}
		assert.False(t, ids[local.ID], "suspended user must not appear in search")
	})

	// #1054: host を途中まで入力した (= prefix) 状態でも remote user が hit する。
	// frontend MkAutocomplete が `@alice@rem` のような prefix で API を叩くので、
	// `host = "rem"` で `remote.example` の user が hit しないと autocomplete
	// で remote user 候補が一切出ない。
	t.Run("host prefix match hits remote users", func(t *testing.T) {
		prefix := "rem"
		out, err := repo.SearchByUsernameAndHost("hosttest", &prefix, 10)
		require.NoError(t, err)
		ids := make(map[string]bool, len(out))
		for _, u := range out {
			ids[u.ID] = true
		}
		assert.True(t, ids[remote.ID], "host=rem prefix should hit remote.example user")
		assert.False(t, ids[other.ID], "host=rem prefix should not hit other.example user")
		assert.False(t, ids[local.ID], "host prefix should not hit local user (host IS NULL)")
	})
}

// #1054: SQL LIKE wildcard (`%` / `_`) は host 引数中に含まれた場合 escape
// されて literal として扱われる。これにより悪意 / 偶発の wildcard 注入で
// 意図しない user が hit するのを防ぐ。
func TestUserRepository_SearchByUsernameAndHost_LikeWildcardEscape(t *testing.T) {
	repo := NewUserRepository(testDB)
	// `_` literal を含む host を持つ remote user
	withUnderscore := insertTestUser(t, "u_we_u", "wildtest_u")
	defer cleanupUser(t, withUnderscore.ID)
	require.NoError(t, repo.UpdateUser(withUnderscore.ID, map[string]any{"host": "with_underscore.example"}))

	// `a` を 1 文字目に持つ別 host (= SQL LIKE で `_` wildcard 1 文字 match の
	// 場合に意図せず hit してしまう candidate)
	withoutUnderscore := insertTestUser(t, "u_we_a", "wildtest_a")
	defer cleanupUser(t, withoutUnderscore.ID)
	require.NoError(t, repo.UpdateUser(withoutUnderscore.ID, map[string]any{"host": "witha.example"}))

	t.Run("underscore is escaped to literal", func(t *testing.T) {
		// `with_` を prefix として渡す。escape 無しだと `_` は 1 文字 wildcard
		// として扱われ `witha.example` (= `_` の位置に `a` がある) も hit する。
		// escape 有りなら literal `_` として扱われ、`with_underscore.example`
		// のみ hit する。
		prefix := "with_"
		out, err := repo.SearchByUsernameAndHost("wildtest", &prefix, 10)
		require.NoError(t, err)
		ids := make(map[string]bool, len(out))
		for _, u := range out {
			ids[u.ID] = true
		}
		assert.True(t, ids[withUnderscore.ID], "literal `_` should match `with_underscore.example`")
		assert.False(t, ids[withoutUnderscore.ID], "literal `_` should NOT match `witha.example` (= regression guard for `_` wildcard escape)")
	})

	t.Run("percent is escaped to literal", func(t *testing.T) {
		// `%` を含む host name は実運用ではほぼ無いが、escape されることを
		// confirm するため: `%` を渡しても全 host にマッチする wildcard と
		// しては解釈されず、literal `%` を含む host のみ hit する (= 0 件)。
		prefix := "%"
		out, err := repo.SearchByUsernameAndHost("wildtest", &prefix, 10)
		require.NoError(t, err)
		assert.Empty(t, out, "literal `%` should not match any actual host (= regression guard for `%` wildcard escape)")
	})
}

func TestUserRepository_UpdateUser(t *testing.T) {
	repo := NewUserRepository(testDB)
	user := insertTestUser(t, "u_up_1", "updateuser1")
	defer cleanupUser(t, user.ID)

	require.NoError(t, repo.UpdateUser(user.ID, map[string]any{"isLocked": true}))
	found, _ := repo.FindByID(user.ID)
	assert.True(t, found.IsLocked)

	// 空フィールドはnoop
	require.NoError(t, repo.UpdateUser(user.ID, map[string]any{}))
}

func TestUserRepository_SearchByUsername_QueryError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := testDB.WithContext(ctx)
	repo := NewUserRepository(db)

	_, err := repo.SearchByUsername("anything", 10, 0, "")
	assert.Error(t, err)
}

func TestUserRepository_UpdateProfile(t *testing.T) {
	repo := NewUserRepository(testDB)
	user := insertTestUser(t, "u_up_2", "updateuser2")
	defer cleanupUser(t, user.ID)

	desc := "initial"
	profile := &model.UserProfile{
		UserID:      user.ID,
		Description: &desc,
		Fields:      datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, testDB.Create(profile).Error)

	newDesc := "updated"
	require.NoError(t, repo.UpdateProfile(user.ID, map[string]any{"description": newDesc}))
	found, _ := repo.FindProfileByUserID(user.ID)
	assert.Equal(t, "updated", *found.Description)

	// 空フィールドはnoop
	require.NoError(t, repo.UpdateProfile(user.ID, map[string]any{}))
}

func TestUserRepository_CreateProfile(t *testing.T) {
	repo := NewUserRepository(testDB)
	user := insertTestUser(t, "cp_u1", "cpuser")
	defer cleanupUser(t, user.ID)

	pass := "$2a$10$test"
	profile := &model.UserProfile{
		UserID:             user.ID,
		Password:           &pass,
		AutoAcceptFollowed: true,
	}
	require.NoError(t, repo.CreateProfile(profile))

	found, err := repo.FindProfileByUserID(user.ID)
	require.NoError(t, err)
	assert.Equal(t, &pass, found.Password)
}

func TestUserRepository_CreateProfile_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewUserRepository(testDB.WithContext(ctx))
	_, err := repo.ListUsers(model.UserListFilter{Origin: "combined", State: "all", Sort: "invalid"})
	assert.Error(t, err)
}

func TestUserRepository_ListUsers_Default(t *testing.T) {
	repo := NewUserRepository(testDB)
	u1 := insertTestUser(t, "lu_u1", "listuser1")
	u2 := insertTestUser(t, "lu_u2", "listuser2")
	defer cleanupUser(t, u1.ID)
	defer cleanupUser(t, u2.ID)

	users, err := repo.ListUsers(model.UserListFilter{Limit: 100})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(users), 2)
}

func TestUserRepository_ListUsers_LocalOnly(t *testing.T) {
	repo := NewUserRepository(testDB)
	u := insertTestUser(t, "lu_loc", "localonly")
	defer cleanupUser(t, u.ID)

	users, err := repo.ListUsers(model.UserListFilter{Origin: "local", Limit: 100})
	require.NoError(t, err)
	for _, user := range users {
		assert.Nil(t, user.Host)
	}
}

func TestUserRepository_ListUsers_Suspended(t *testing.T) {
	repo := NewUserRepository(testDB)
	u := insertTestUser(t, "lu_sus", "suspended")
	defer cleanupUser(t, u.ID)
	require.NoError(t, repo.UpdateUser(u.ID, map[string]any{"isSuspended": true}))

	users, err := repo.ListUsers(model.UserListFilter{State: "suspended", Limit: 100})
	require.NoError(t, err)
	for _, user := range users {
		assert.True(t, user.IsSuspended)
	}
}

// adminOrModerator filter joins role_assignment + role and returns only
// users with isAdministrator/isModerator role and host IS NULL (#421)。
// 旧実装は state=adminOrModerator を黙って無視していたので admin/overview
// の moderator 一覧に観測した全リモートユーザーが流れ込んでいた。
func TestUserRepository_ListUsers_AdminOrModerator(t *testing.T) {
	repo := NewUserRepository(testDB)
	host := "remote.example"
	mod := insertTestUser(t, "lu_mod", "modlocal")
	defer cleanupUser(t, mod.ID)
	plain := insertTestUser(t, "lu_plain", "plainlocal")
	defer cleanupUser(t, plain.ID)
	remote := insertTestUser(t, "lu_remote", "remotemod")
	require.NoError(t, repo.UpdateUser(remote.ID, map[string]any{"host": host}))
	defer cleanupUser(t, remote.ID)

	// moderator role + assignment
	now := time.Now()
	role := &model.Role{
		ID:              "role_lu_mod",
		Name:            "test-mod",
		IsModerator:     true,
		IsAdministrator: false,
		UpdatedAt:       now,
		LastUsedAt:      now,
	}
	require.NoError(t, testDB.Create(role).Error)
	defer testDB.Exec(`DELETE FROM "role" WHERE id = ?`, role.ID)

	// local mod + remote (incorrectly) get the moderator role
	for _, uid := range []string{mod.ID, remote.ID} {
		ra := &model.RoleAssignment{ID: "ra_" + uid, UserID: uid, RoleID: role.ID}
		require.NoError(t, testDB.Create(ra).Error)
		defer testDB.Exec(`DELETE FROM "role_assignment" WHERE id = ?`, ra.ID)
	}

	users, err := repo.ListUsers(model.UserListFilter{State: "adminOrModerator", Limit: 100})
	require.NoError(t, err)
	ids := make(map[string]struct{}, len(users))
	for _, u := range users {
		ids[u.ID] = struct{}{}
	}
	assert.Contains(t, ids, mod.ID, "local moderator must be listed")
	assert.NotContains(t, ids, plain.ID, "non-moderator local user must be excluded")
	assert.NotContains(t, ids, remote.ID, "remote user must be excluded even if assigned the role")
}

// admin / adminOrModerator フィルタは meta.rootUserId を暗黙の admin として
// 含める必要がある。本家 RoleService.getModeratorIds の rootUserIds union と
// 同じ挙動で、これが無いと「初期 root のみ」のインスタンスでは moderator
// カードが空になる (#421 Devin review)。
func TestUserRepository_ListUsers_AdminIncludesRootUser(t *testing.T) {
	repo := NewUserRepository(testDB)
	root := insertTestUser(t, "lu_root", "rootuser")
	defer cleanupUser(t, root.ID)
	other := insertTestUser(t, "lu_other", "otheruser")
	defer cleanupUser(t, other.ID)

	// root を meta.rootUserId に設定する。テスト終了時に元の値へ戻す。
	// migration には meta シードが無いので、行が無ければ INSERT でブート
	// ストラップする。
	var rowCount int64
	require.NoError(t, testDB.Raw(`SELECT COUNT(*) FROM meta`).Scan(&rowCount).Error)
	if rowCount == 0 {
		require.NoError(t, testDB.Exec(`INSERT INTO meta (id, "rootUserId") VALUES ('x', ?)`, root.ID).Error)
		t.Cleanup(func() { testDB.Exec(`DELETE FROM meta WHERE id = 'x'`) })
	} else {
		var prev *string
		require.NoError(t, testDB.Raw(`SELECT "rootUserId" FROM meta LIMIT 1`).Scan(&prev).Error)
		require.NoError(t, testDB.Exec(`UPDATE meta SET "rootUserId" = ?`, root.ID).Error)
		t.Cleanup(func() {
			if prev != nil {
				testDB.Exec(`UPDATE meta SET "rootUserId" = ?`, *prev)
			} else {
				testDB.Exec(`UPDATE meta SET "rootUserId" = NULL`)
			}
		})
	}

	for _, state := range []string{"admin", "adminOrModerator"} {
		users, err := repo.ListUsers(model.UserListFilter{State: state, Limit: 100})
		require.NoError(t, err)
		ids := make(map[string]struct{}, len(users))
		for _, u := range users {
			ids[u.ID] = struct{}{}
		}
		assert.Contains(t, ids, root.ID, "root user must be in state=%s results", state)
		assert.NotContains(t, ids, other.ID, "non-admin local user must not be in state=%s results", state)
	}

	// pure moderator フィルタは root (admin) を含まない。
	users, err := repo.ListUsers(model.UserListFilter{State: "moderator", Limit: 100})
	require.NoError(t, err)
	for _, u := range users {
		assert.NotEqual(t, root.ID, u.ID, "root must NOT be in state=moderator (admin-only)")
	}
}

func TestUserRepository_ListUsers_SortAndPagination(t *testing.T) {
	repo := NewUserRepository(testDB)
	u1 := insertTestUser(t, "lu_s1", "sortuser1")
	u2 := insertTestUser(t, "lu_s2", "sortuser2")
	defer cleanupUser(t, u1.ID)
	defer cleanupUser(t, u2.ID)

	// Sort by createdAt ASC / DESC
	users, err := repo.ListUsers(model.UserListFilter{Sort: "+createdAt", Limit: 1})
	require.NoError(t, err)
	assert.Len(t, users, 1)

	users, err = repo.ListUsers(model.UserListFilter{Sort: "-createdAt", Limit: 1})
	require.NoError(t, err)
	assert.Len(t, users, 1)

	// Sort by updatedAt
	users, err = repo.ListUsers(model.UserListFilter{Sort: "+updatedAt", Limit: 100})
	require.NoError(t, err)
	assert.NotEmpty(t, users)

	// Sort by -updatedAt
	users, err = repo.ListUsers(model.UserListFilter{Sort: "-updatedAt", Limit: 100})
	require.NoError(t, err)
	assert.NotEmpty(t, users)

	// Sort by followersCount
	users, err = repo.ListUsers(model.UserListFilter{Sort: "+followersCount", Limit: 100})
	require.NoError(t, err)
	assert.NotEmpty(t, users)

	users, err = repo.ListUsers(model.UserListFilter{Sort: "-followersCount", Limit: 100})
	require.NoError(t, err)
	assert.NotEmpty(t, users)

	// Pagination offset
	users, err = repo.ListUsers(model.UserListFilter{Limit: 100, Offset: 1})
	require.NoError(t, err)
	assert.NotEmpty(t, users)
}

func TestUserRepository_ListUsers_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewUserRepository(testDB.WithContext(ctx))
	_, err := repo.ListUsers(model.UserListFilter{})
	assert.Error(t, err)
}

func TestUserRepository_ListUsers_RemoteOrigin(t *testing.T) {
	repo := NewUserRepository(testDB)
	users, err := repo.ListUsers(model.UserListFilter{Origin: "remote", Limit: 10})
	require.NoError(t, err)
	// リモートユーザーがいなくても空配列で返る
	assert.NotNil(t, users)
}

func TestUserRepository_ListUsers_AliveState(t *testing.T) {
	repo := NewUserRepository(testDB)
	users, err := repo.ListUsers(model.UserListFilter{State: "alive", Limit: 10})
	require.NoError(t, err)
	for _, u := range users {
		assert.False(t, u.IsSuspended)
	}
}

func TestUserRepository_ListUsers_LimitCap(t *testing.T) {
	repo := NewUserRepository(testDB)
	// limit > 100 は 100 にキャップされる
	users, err := repo.ListUsers(model.UserListFilter{Limit: 999})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(users), 100)
}

func TestUserRepository_ListRemoteInboxes(t *testing.T) {
	repo := NewUserRepository(testDB)

	// ローカルユーザー (inbox なし) は含まれない。
	local := insertTestUser(t, "lri_local", "lri_local")
	defer cleanupUser(t, local.ID)

	// リモートユーザー A: sharedInbox あり → sharedInbox が使われる。
	hostA := "remote-a.example"
	inboxA := "https://remote-a.example/users/a/inbox"
	sharedA := "https://remote-a.example/inbox"
	a := &model.User{
		ID:                "lri_a",
		Username:          "lri_a",
		UsernameLower:     "lri_a",
		Host:              &hostA,
		Inbox:             &inboxA,
		SharedInbox:       &sharedA,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, repo.Create(a))
	defer cleanupUser(t, a.ID)

	// リモートユーザー B: sharedInbox なし → inbox が使われる。
	hostB := "remote-b.example"
	inboxB := "https://remote-b.example/users/b/inbox"
	b := &model.User{
		ID:                "lri_b",
		Username:          "lri_b",
		UsernameLower:     "lri_b",
		Host:              &hostB,
		Inbox:             &inboxB,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, repo.Create(b))
	defer cleanupUser(t, b.ID)

	// リモートユーザー C: A と同じ sharedInbox → dedup される。
	c := &model.User{
		ID:                "lri_c",
		Username:          "lri_c",
		UsernameLower:     "lri_c",
		Host:              &hostA,
		SharedInbox:       &sharedA,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, repo.Create(c))
	defer cleanupUser(t, c.ID)

	// リモートユーザー D: inbox も sharedInbox も空 → スキップされる。
	hostD := "remote-d.example"
	d := &model.User{
		ID:                "lri_d",
		Username:          "lri_d",
		UsernameLower:     "lri_d",
		Host:              &hostD,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, repo.Create(d))
	defer cleanupUser(t, d.ID)

	inboxes, err := repo.ListRemoteInboxes()
	require.NoError(t, err)

	// sharedA と inboxB が含まれる (localは host=NULL で除外、D は空でスキップ、
	// C は A と同じ sharedInbox なので dedup)。
	assert.Contains(t, inboxes, sharedA)
	assert.Contains(t, inboxes, inboxB)
	// inboxA は shared が優先されるので出ない。
	assert.NotContains(t, inboxes, inboxA)
	// dedup 確認: sharedA は 1 回だけ。
	seen := 0
	for _, ib := range inboxes {
		if ib == sharedA {
			seen++
		}
	}
	assert.Equal(t, 1, seen)
}

func TestUserRepository_ListUserRecommendations(t *testing.T) {
	repo := NewUserRepository(testDB)
	now := time.Now()
	recent := now.Add(-time.Hour)
	old := now.Add(-30 * 24 * time.Hour)

	// me (viewer)、候補: r1 (高 followers), r2 (低 followers)
	// 除外対象: r3 (remote), r4 (locked), r5 (non-explorable), r6 (stale update)
	// さらに r1 をフォロー済みにすると除外されることを確認する。
	me := insertTestUser(t, "u_rec_me", "recme")
	defer cleanupUser(t, me.ID)
	r1 := &model.User{ID: "u_rec_r1", Username: "recr1", UsernameLower: "recr1",
		IsExplorable: true, FollowersCount: 100, UpdatedAt: &recent,
		AvatarDecorations: datatypes.JSON([]byte("[]"))}
	r2 := &model.User{ID: "u_rec_r2", Username: "recr2", UsernameLower: "recr2",
		IsExplorable: true, FollowersCount: 5, UpdatedAt: &recent,
		AvatarDecorations: datatypes.JSON([]byte("[]"))}
	host := "remote.example"
	r3 := &model.User{ID: "u_rec_r3", Username: "recr3", UsernameLower: "recr3", Host: &host,
		IsExplorable: true, FollowersCount: 999, UpdatedAt: &recent,
		AvatarDecorations: datatypes.JSON([]byte("[]"))}
	r4 := &model.User{ID: "u_rec_r4", Username: "recr4", UsernameLower: "recr4",
		IsExplorable: true, IsLocked: true, FollowersCount: 999, UpdatedAt: &recent,
		AvatarDecorations: datatypes.JSON([]byte("[]"))}
	r5 := &model.User{ID: "u_rec_r5", Username: "recr5", UsernameLower: "recr5",
		IsExplorable: true, FollowersCount: 999, UpdatedAt: &recent,
		AvatarDecorations: datatypes.JSON([]byte("[]"))}
	r6 := &model.User{ID: "u_rec_r6", Username: "recr6", UsernameLower: "recr6",
		IsExplorable: true, FollowersCount: 999, UpdatedAt: &old,
		AvatarDecorations: datatypes.JSON([]byte("[]"))}
	for _, u := range []*model.User{r1, r2, r3, r4, r5, r6} {
		require.NoError(t, repo.Create(u))
		defer cleanupUser(t, u.ID)
	}
	// GORMは bool の zero value (false) を default 句で上書きしてしまうため、
	// r5 の isExplorable = false は Create 後に明示的に UPDATE する。
	require.NoError(t, testDB.Exec(`UPDATE "user" SET "isExplorable" = FALSE WHERE id = ?`, r5.ID).Error)

	activeSince := now.Add(-7 * 24 * time.Hour)
	users, err := repo.ListUserRecommendations(me.ID, activeSince, 10, 0)
	require.NoError(t, err)
	got := make(map[string]*model.User, len(users))
	for _, u := range users {
		got[u.ID] = u
	}
	// r1, r2 のみ含まれる。
	_, hasR1 := got["u_rec_r1"]
	_, hasR2 := got["u_rec_r2"]
	assert.True(t, hasR1)
	assert.True(t, hasR2)
	assert.NotContains(t, got, "u_rec_r3")
	assert.NotContains(t, got, "u_rec_r4")
	assert.NotContains(t, got, "u_rec_r5")
	assert.NotContains(t, got, "u_rec_r6")
	// followersCount DESC なので r1 が先に来る。
	assert.Equal(t, "u_rec_r1", users[0].ID)

	// 既フォローは除外。
	fRepo := NewFollowingRepository(testDB)
	require.NoError(t, fRepo.Create(&model.Following{ID: "fl_rec_1", FollowerID: me.ID, FolloweeID: r1.ID}))
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, "fl_rec_1")
	users2, err := repo.ListUserRecommendations(me.ID, activeSince, 10, 0)
	require.NoError(t, err)
	for _, u := range users2 {
		assert.NotEqual(t, r1.ID, u.ID)
	}
}

func TestUserRepository_ListUserRecommendations_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewUserRepository(testDB.WithContext(ctx))
	_, err := repo.ListUserRecommendations("me", time.Now(), 10, 0)
	assert.Error(t, err)
}

func TestUserRepository_ListUserRecommendations_LimitCap(t *testing.T) {
	repo := NewUserRepository(testDB)
	// limit > 100 は 100 にキャップ。limit <= 0 は 10 にデフォルト。
	// 呼び出しが成功することのみ確認する (カバレッジのため)。
	_, err := repo.ListUserRecommendations("nobody", time.Now(), 500, 0)
	require.NoError(t, err)
	_, err = repo.ListUserRecommendations("nobody", time.Now(), 0, 0)
	require.NoError(t, err)
}

// #403: nodeinfo usage 統計の DB 層を実 PostgreSQL で検証する。
func TestUserRepository_CountLocalUsers(t *testing.T) {
	repo := NewUserRepository(testDB)
	// fixture: local 2 (うち1 deleted), remote 1
	localAlive := insertTestUser(t, "u_cnt_la", "cnt_la")
	defer cleanupUser(t, localAlive.ID)
	localDeleted := insertTestUser(t, "u_cnt_ld", "cnt_ld")
	require.NoError(t, testDB.Model(&model.User{}).Where("id = ?", localDeleted.ID).Update("isDeleted", true).Error)
	defer cleanupUser(t, localDeleted.ID)
	remote := insertRemoteTestUser(t, "u_cnt_rem", "cnt_rem", "remote.example")
	defer cleanupUser(t, remote.ID)

	// testcontainers の共有 DB で他テスト fixture 残存がありうるので、
	// 正確な count ではなく「少なくとも追加した localAlive 1人が含まれる」
	// ことだけ assert する。
	got, err := repo.CountLocalUsers()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, got, int64(1))
}

func TestUserRepository_CountLocalUsersActiveSince(t *testing.T) {
	repo := NewUserRepository(testDB)
	now := time.Now()
	active := insertTestUser(t, "ucntact1", "cntact1")
	recent := now.Add(-5 * time.Minute)
	require.NoError(t, testDB.Model(&model.User{}).Where("id = ?", active.ID).UpdateColumn("lastActiveDate", &recent).Error)
	defer cleanupUser(t, active.ID)

	stale := insertTestUser(t, "ucntstl1", "cntstl1")
	old := now.Add(-200 * 24 * time.Hour) // 200日前
	require.NoError(t, testDB.Model(&model.User{}).Where("id = ?", stale.ID).UpdateColumn("lastActiveDate", &old).Error)
	defer cleanupUser(t, stale.ID)

	// 1ヶ月以内の active user が含まれる。
	cnt, err := repo.CountLocalUsersActiveSince(now.AddDate(0, -1, 0))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, cnt, int64(1))
}

// retention aggregation (#421) で使う 2 メソッドを統合テストする。
// 削除済みフィルタが retention 側 (active) のみで効き、cohort 側
// (registered) では効かないことを確認する。
func TestUserRepository_ListLocalUserIDsRegisteredAfter(t *testing.T) {
	repo := NewUserRepository(testDB)

	// id 順に並ぶよう prefix を時系列順にする (aidx は timestamp prefixed
	// なので「id > cursor」 = 「cursor 以降に登録」と等価)。
	older := insertTestUser(t, "uregaaaa", "regaaaa")
	defer cleanupUser(t, older.ID)
	newer := insertTestUser(t, "uregzzzz", "regzzzz")
	defer cleanupUser(t, newer.ID)

	// 削除済み local user も cohort には含める (定着率の分母は登録時点で固定)。
	deleted := insertTestUser(t, "uregyyyy", "regyyyy")
	require.NoError(t, testDB.Model(&model.User{}).Where("id = ?", deleted.ID).Update("isDeleted", true).Error)
	defer cleanupUser(t, deleted.ID)

	// remote user は除外される。
	remote := insertRemoteTestUser(t, "uregremo", "regremo", "remote.example")
	defer cleanupUser(t, remote.ID)

	got, err := repo.ListLocalUserIDsRegisteredAfter(older.ID)
	require.NoError(t, err)
	// older 自身は ">"" なので含まれない。newer と deleted は含まれる。
	assert.Contains(t, got, newer.ID)
	assert.Contains(t, got, deleted.ID, "deleted users must remain in the registration cohort")
	assert.NotContains(t, got, older.ID)
	assert.NotContains(t, got, remote.ID, "remote users must be excluded")
}

func TestUserRepository_ListLocalUserIDsActiveSince(t *testing.T) {
	repo := NewUserRepository(testDB)
	now := time.Now()
	recent := now.Add(-5 * time.Minute)
	old := now.Add(-200 * 24 * time.Hour)

	active := insertTestUser(t, "ulistact", "listact")
	require.NoError(t, testDB.Model(&model.User{}).Where("id = ?", active.ID).UpdateColumn("lastActiveDate", &recent).Error)
	defer cleanupUser(t, active.ID)

	stale := insertTestUser(t, "uliststa", "liststa")
	require.NoError(t, testDB.Model(&model.User{}).Where("id = ?", stale.ID).UpdateColumn("lastActiveDate", &old).Error)
	defer cleanupUser(t, stale.ID)

	// 削除済み + 直近 active は除外する (退会者を「定着」と数えない)。
	deleted := insertTestUser(t, "ulistdel", "listdel")
	require.NoError(t, testDB.Model(&model.User{}).
		Where("id = ?", deleted.ID).
		Updates(map[string]any{"lastActiveDate": &recent, "isDeleted": true}).Error)
	defer cleanupUser(t, deleted.ID)

	got, err := repo.ListLocalUserIDsActiveSince(now.Add(-1 * time.Hour))
	require.NoError(t, err)
	assert.Contains(t, got, active.ID)
	assert.NotContains(t, got, stale.ID, "stale users must be excluded")
	assert.NotContains(t, got, deleted.ID, "deleted users must be excluded from the retained set")
}

func TestUserRepository_CountLocalUsers_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewUserRepository(testDB.WithContext(ctx))
	_, err := repo.CountLocalUsers()
	assert.Error(t, err)
	_, err = repo.CountLocalUsersActiveSince(time.Now())
	assert.Error(t, err)
}
