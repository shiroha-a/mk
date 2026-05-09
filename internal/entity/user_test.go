package entity

import (
	"encoding/json"
	"testing"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func TestPackUserLite(t *testing.T) {
	name := "Test User"
	avatarURL := "https://example.com/avatar.png"
	blurhash := "LEHV6nWB2yk8"

	u := &model.User{
		ID:                "user1",
		Username:          "testuser",
		Name:              &name,
		AvatarURL:         &avatarURL,
		AvatarBlurhash:    &blurhash,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
		IsBot:             true,
		IsCat:             false,
	}

	lite := PackUserLite(u)

	assert.Equal(t, "user1", lite.ID)
	assert.Equal(t, "testuser", lite.Username)
	assert.Equal(t, &name, lite.Name)
	assert.Equal(t, avatarURL, lite.AvatarURL)
	assert.Equal(t, &blurhash, lite.AvatarBlurhash)
	assert.True(t, lite.IsBot)
	assert.False(t, lite.IsCat)
	assert.Equal(t, "unknown", lite.OnlineStatus)
	assert.NotNil(t, lite.Emojis)
	assert.Empty(t, lite.Emojis)
	assert.NotNil(t, lite.BadgeRoles)
	assert.Empty(t, lite.BadgeRoles)
}

func TestPackUserLite_NilFields(t *testing.T) {
	u := &model.User{
		ID:                "user2",
		Username:          "minimal",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}

	lite := PackUserLite(u)

	assert.Equal(t, "user2", lite.ID)
	assert.Nil(t, lite.Name)
	assert.Nil(t, lite.Host)
	// avatarUrlがnullの場合、identiconにフォールバック
	assert.Equal(t, "/identicon/minimal", lite.AvatarURL)
	assert.Nil(t, lite.AvatarBlurhash)
}

func TestPackUserLite_IdenticonWithHost(t *testing.T) {
	host := "remote.example"
	u := &model.User{
		ID:                "user3",
		Username:          "alice",
		Host:              &host,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	lite := PackUserLite(u)
	assert.Equal(t, "/identicon/alice@remote.example", lite.AvatarURL)
}

func TestPackUserLite_ExistingAvatarURL(t *testing.T) {
	avatar := "https://example.com/avatar.png"
	u := &model.User{
		ID:                "user4",
		Username:          "bob",
		AvatarURL:         &avatar,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	lite := PackUserLite(u)
	assert.Equal(t, "https://example.com/avatar.png", lite.AvatarURL)
}

func TestPackUserDetailed(t *testing.T) {
	name := "Detailed User"
	bannerURL := "https://example.com/banner.png"
	desc := "A test user"
	location := "Tokyo"
	birthday := "2000-01-01"
	lang := "ja-JP"

	u := &model.User{
		ID:                "user3",
		Username:          "detailed",
		Name:              &name,
		BannerURL:         &bannerURL,
		IsLocked:          true,
		IsSuspended:       false,
		FollowersCount:    100,
		FollowingCount:    50,
		NotesCount:        1000,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}

	profile := &model.UserProfile{
		UserID:      "user3",
		Description: &desc,
		Location:    &location,
		Birthday:    &birthday,
		Lang:        &lang,
		Fields:      datatypes.JSON([]byte("[]")),
	}

	detailed := PackUserDetailed(u, profile)

	assert.Equal(t, "user3", detailed.ID)
	assert.Equal(t, "detailed", detailed.Username)
	assert.Equal(t, &bannerURL, detailed.BannerURL)
	assert.True(t, detailed.IsLocked)
	assert.False(t, detailed.IsSuspended)
	assert.Equal(t, 100, detailed.FollowersCount)
	assert.Equal(t, 50, detailed.FollowingCount)
	assert.Equal(t, 1000, detailed.NotesCount)
	assert.Equal(t, &desc, detailed.Description)
	assert.Equal(t, &location, detailed.Location)
	assert.Equal(t, &birthday, detailed.Birthday)
	assert.Equal(t, &lang, detailed.Lang)
}

func TestPackUserDetailed_ProfileVisibility(t *testing.T) {
	u := &model.User{
		ID:                "user5",
		Username:          "visible",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}

	profile := &model.UserProfile{
		UserID:              "user5",
		FollowersVisibility: model.FollowingVisibilityFollowers,
		FollowingVisibility: model.FollowingVisibilityPrivate,
		Fields:              datatypes.JSON([]byte("[]")),
	}

	detailed := PackUserDetailed(u, profile)

	assert.Equal(t, "followers", detailed.FollowersVisibility)
	assert.Equal(t, "private", detailed.FollowingVisibility)
}

func TestPackUserDetailed_VerifiedLinks(t *testing.T) {
	u := &model.User{
		ID:                "user6",
		Username:          "verified",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}

	profile := &model.UserProfile{
		UserID:        "user6",
		VerifiedLinks: pq.StringArray{"https://example.com", "https://blog.example.com"},
		Fields:        datatypes.JSON([]byte("[]")),
	}

	detailed := PackUserDetailed(u, profile)

	assert.Equal(t, []string{"https://example.com", "https://blog.example.com"}, detailed.VerifiedLinks)
}

func TestPackUserDetailed_DefaultValues(t *testing.T) {
	u := &model.User{
		ID:                "user7",
		Username:          "defaults",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}

	// nilプロフィールのデフォルト
	detailed := PackUserDetailed(u, nil)

	assert.Equal(t, "public", detailed.FollowersVisibility)
	assert.Equal(t, "public", detailed.FollowingVisibility)
	assert.NotNil(t, detailed.VerifiedLinks)
	assert.Empty(t, detailed.VerifiedLinks)
}

// #692: chatScope を含めることで FE の /settings/privacy が `i/update` の
// レスポンスから現在値を取り戻せる。expose を忘れると DB は更新されている
// のに UI が "保存されない" 表示になる退行を防ぐ。
func TestPackUserDetailed_ExposesChatScope(t *testing.T) {
	cases := []string{"everyone", "followers", "following", "mutual", "none"}
	for _, scope := range cases {
		t.Run(scope, func(t *testing.T) {
			u := &model.User{
				ID:                "u",
				Username:          "u",
				ChatScope:         scope,
				AvatarDecorations: datatypes.JSON([]byte("[]")),
			}
			detailed := PackUserDetailed(u, nil)
			assert.Equal(t, scope, detailed.ChatScope)
		})
	}
}

func TestPackUserDetailed_NilProfile(t *testing.T) {
	u := &model.User{
		ID:                "user4",
		Username:          "noprofile",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}

	detailed := PackUserDetailed(u, nil)

	assert.Equal(t, "user4", detailed.ID)
	assert.Nil(t, detailed.Description)
	assert.Nil(t, detailed.Location)
	assert.Nil(t, detailed.Birthday)
	assert.Nil(t, detailed.Lang)
}

// --- Phase 7-5a (#247): UserLite 追加 optional フィールド ---

func TestPackUserLite_RequireSigninToViewContents_TrueExposed(t *testing.T) {
	u := &model.User{
		ID: "u1", Username: "x", AvatarDecorations: datatypes.JSON([]byte("[]")),
		RequireSigninToViewContents: true,
	}
	lite := PackUserLite(u)
	require.NotNil(t, lite.RequireSigninToViewContents)
	assert.Equal(t, true, *lite.RequireSigninToViewContents)
}

func TestPackUserLite_RequireSigninToViewContents_FalseOmitted(t *testing.T) {
	u := &model.User{
		ID: "u1", Username: "x", AvatarDecorations: datatypes.JSON([]byte("[]")),
		RequireSigninToViewContents: false,
	}
	lite := PackUserLite(u)
	// false → nil (TS で undefined、JSONはomit)
	assert.Nil(t, lite.RequireSigninToViewContents)

	// JSONマーシャル結果でもキーが出ないこと
	b, err := json.Marshal(lite)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "requireSigninToViewContents")
}

func TestPackUserLite_MakeNotesBefore(t *testing.T) {
	// makeNotes*Before は integer カラム (TS MiUser.ts:208) で「秒単位、
	// マイナスで相対時間」と定義されている。int32 (~2.1B) 範囲内の値を使う
	// こと (ms timestamp 等の大きい値は DB round-trip で overflow する)。
	followersSec := 1700000000 // 2023-11-14T22:13:20Z 相当の UNIX 秒
	hiddenRelSec := -86400     // 過去 1 日 (相対時間の例)
	u := &model.User{
		ID: "u1", Username: "x", AvatarDecorations: datatypes.JSON([]byte("[]")),
		MakeNotesFollowersOnlyBefore: &followersSec,
		MakeNotesHiddenBefore:        &hiddenRelSec,
	}
	lite := PackUserLite(u)
	require.NotNil(t, lite.MakeNotesFollowersOnlyBefore)
	assert.Equal(t, 1700000000, *lite.MakeNotesFollowersOnlyBefore)
	require.NotNil(t, lite.MakeNotesHiddenBefore)
	assert.Equal(t, -86400, *lite.MakeNotesHiddenBefore)
}

func TestPackUserLite_MakeNotesBefore_NilOmitted(t *testing.T) {
	u := &model.User{
		ID: "u1", Username: "x", AvatarDecorations: datatypes.JSON([]byte("[]")),
		// MakeNotes*Before は nil
	}
	lite := PackUserLite(u)
	assert.Nil(t, lite.MakeNotesFollowersOnlyBefore)
	assert.Nil(t, lite.MakeNotesHiddenBefore)

	b, err := json.Marshal(lite)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "makeNotesFollowersOnlyBefore")
	assert.NotContains(t, string(b), "makeNotesHiddenBefore")
}

func TestPackUserLite_Instance_NilByDefault(t *testing.T) {
	u := &model.User{
		ID: "u1", Username: "x", AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	lite := PackUserLite(u)
	// PackUserLite 自体は Instance を設定しない (caller がpre-fetchしてセット)
	assert.Nil(t, lite.Instance)

	b, err := json.Marshal(lite)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "\"instance\"")
}

func TestUserLite_Instance_WhenSet(t *testing.T) {
	u := &model.User{
		ID: "u1", Username: "x", AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	lite := PackUserLite(u)
	name := "example.com"
	iconURL := "https://example.com/icon.png"
	lite.Instance = &InstanceLite{Name: &name, IconURL: &iconURL}

	b, err := json.Marshal(lite)
	require.NoError(t, err)
	assert.Contains(t, string(b), "\"instance\"")
	assert.Contains(t, string(b), "example.com")
}

// UserDetailed は UserLite を埋め込むため、Instance 等もそこに含まれる
// ことを確認する (移動に伴う regression guard)。
func TestUserDetailed_EmbedsUserLiteOptionalFields(t *testing.T) {
	req := true
	u := &model.User{
		ID: "u1", Username: "x", AvatarDecorations: datatypes.JSON([]byte("[]")),
		RequireSigninToViewContents: req,
	}
	detailed := PackUserDetailed(u, nil)
	require.NotNil(t, detailed.RequireSigninToViewContents)
	assert.True(t, *detailed.RequireSigninToViewContents)

	// Instance 設定も embed 経由で動く
	host := "remote.example"
	detailed.Instance = &InstanceLite{Name: &host}
	b, err := json.Marshal(detailed)
	require.NoError(t, err)
	assert.Contains(t, string(b), "\"instance\"")
}

// profile が nil (リモートユーザーで user_profile レコードが未生成) の場合でも
// fields は空配列 `[]` として marshal されるべき。null だとフロントが
// user.fields.length で TypeError を起こし、ユーザーページの概要タブが
// 真っ白になる。
func TestPackUserDetailed_FieldsDefaultWhenProfileNil(t *testing.T) {
	u := &model.User{ID: "u1", Username: "x", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	detailed := PackUserDetailed(u, nil)
	b, err := json.Marshal(detailed)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"fields":[]`)
	assert.NotContains(t, string(b), `"fields":null`)
}

func TestPackUserForFollowStreamEvent(t *testing.T) {
	u := &model.User{ID: "u1", Username: "alice", AvatarDecorations: datatypes.JSON([]byte("[]"))}

	t.Run("follow event sets isFollowing=true and hasPending=false", func(t *testing.T) {
		out := PackUserForFollowStreamEvent(u, true, false)
		require.NotNil(t, out.IsFollowing)
		assert.True(t, *out.IsFollowing)
		require.NotNil(t, out.HasPendingFollowRequestFromYou)
		assert.False(t, *out.HasPendingFollowRequestFromYou)
		assert.Equal(t, "u1", out.ID)
		assert.Equal(t, "alice", out.Username)
	})

	t.Run("unfollow event sets isFollowing=false and hasPending=false", func(t *testing.T) {
		out := PackUserForFollowStreamEvent(u, false, false)
		require.NotNil(t, out.IsFollowing)
		assert.False(t, *out.IsFollowing)
		require.NotNil(t, out.HasPendingFollowRequestFromYou)
		assert.False(t, *out.HasPendingFollowRequestFromYou)
	})

	t.Run("pending request sets isFollowing=false and hasPending=true", func(t *testing.T) {
		out := PackUserForFollowStreamEvent(u, false, true)
		require.NotNil(t, out.IsFollowing)
		assert.False(t, *out.IsFollowing)
		require.NotNil(t, out.HasPendingFollowRequestFromYou)
		assert.True(t, *out.HasPendingFollowRequestFromYou)
	})

	t.Run("serialized JSON exposes the viewer fields", func(t *testing.T) {
		out := PackUserForFollowStreamEvent(u, true, false)
		b, err := json.Marshal(out)
		require.NoError(t, err)
		assert.Contains(t, string(b), `"isFollowing":true`)
		assert.Contains(t, string(b), `"hasPendingFollowRequestFromYou":false`)
	})
}

// #968: PackMeDetailed が User / UserProfile から self-view-only field を
// 全て transfer することを確認する。i/update の response はフロントの
// updateCurrentAccountPartial にそのまま流れ込むので、欠損すると session
// state が stale なまま残る。
func TestPackMeDetailed_TransfersSelfViewFields(t *testing.T) {
	u := &model.User{
		ID:                "me1",
		Username:          "me",
		IsExplorable:      false,
		IsDeleted:         true,
		HideOnlineStatus:  true,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	profile := &model.UserProfile{
		UserID:                   "me1",
		NoCrawle:                 true,
		PreventAiLearning:        false,
		AutoSensitive:            true,
		CarefulBot:               true,
		AutoAcceptFollowed:       true,
		AlwaysMarkNsfw:           true,
		ReceiveAnnouncementEmail: false,
		InjectFeaturedNote:       false,
		Fields:                   datatypes.JSON([]byte("[]")),
	}

	me := PackMeDetailed(u, profile)

	assert.False(t, me.IsExplorable)
	assert.True(t, me.IsDeleted)
	assert.True(t, me.HideOnlineStatus)
	assert.True(t, me.NoCrawle)
	assert.False(t, me.PreventAiLearning)
	assert.True(t, me.AutoSensitive)
	assert.True(t, me.CarefulBot)
	assert.True(t, me.AutoAcceptFollowed)
	assert.True(t, me.AlwaysMarkNsfw)
	assert.False(t, me.ReceiveAnnouncementEmail)
	assert.False(t, me.InjectFeaturedNote)

	// UserDetailed embed が機能していることも確認する。
	assert.Equal(t, "me1", me.ID)
	assert.Equal(t, "me", me.Username)
}

// #968: profile が nil でも panic せず、User 由来の self-view field のみ
// transfer されること。Profile fetch が失敗した fallback path で必要。
func TestPackMeDetailed_NilProfile(t *testing.T) {
	u := &model.User{
		ID:                "me2",
		Username:          "noprof",
		IsExplorable:      true,
		HideOnlineStatus:  true,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}

	me := PackMeDetailed(u, nil)

	assert.True(t, me.IsExplorable)
	assert.True(t, me.HideOnlineStatus)
	// profile が nil の fallback path では profile 由来 field は Go zero
	// (= bool false) になる。DB の column default (例: preventAiLearning は
	// `default:true`) とは一致しないが、これは Profile fetch が失敗した
	// 例外パスでのみ発火する。frontend は次の /api/i fetch で正しい値を
	// 取り直す前提なので、ここで DB default 相当に倒す必要はない。
	assert.False(t, me.NoCrawle)
	assert.False(t, me.PreventAiLearning)
	assert.False(t, me.InjectFeaturedNote)
	assert.False(t, me.ReceiveAnnouncementEmail)
}

// #968: serialized JSON が drop-in 互換 key 名 (isExplorable / noCrawle 等)
// を出すこと。フロントの updateCurrentAccountPartial が key 名で merge
// するので、key drift があると session には反映されない。
func TestPackMeDetailed_SerializedJSON(t *testing.T) {
	u := &model.User{
		ID:                "me3",
		Username:          "json",
		IsExplorable:      true,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	profile := &model.UserProfile{
		UserID:            "me3",
		NoCrawle:          true,
		PreventAiLearning: true,
		Fields:            datatypes.JSON([]byte("[]")),
	}

	me := PackMeDetailed(u, profile)
	b, err := json.Marshal(me)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"isExplorable":true`)
	assert.Contains(t, s, `"noCrawle":true`)
	assert.Contains(t, s, `"preventAiLearning":true`)
	assert.Contains(t, s, `"hideOnlineStatus":false`)
	assert.Contains(t, s, `"isDeleted":false`)
}
