package testutil

import (
	"strings"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// applyUserFieldsはmock内部関数だが、productionのresolver/move双方が
// 渡す型(string/*string, time.Time/*time.Time)を両方正しく反映する
// ことをissue #357の回帰防止として検証する。
func TestApplyUserFields_MovedAndAlsoKnownAs_BothForms(t *testing.T) {
	repo := NewMockUserRepository()
	const uid = "u_applytest"
	require.NoError(t, repo.Create(&model.User{ID: uid, Username: "alice"}))

	// resolver.refreshActor が渡す *string / *time.Time 形式
	movedToPtr := "https://remote.example/users/new"
	nowPtr := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	akaPtr := "https://remote.example/users/aka"
	require.NoError(t, repo.UpdateUser(uid, map[string]any{
		"movedToUri":  &movedToPtr,
		"movedAt":     &nowPtr,
		"alsoKnownAs": &akaPtr,
	}))
	got, err := repo.FindByID(uid)
	require.NoError(t, err)
	require.NotNil(t, got.MovedToURI)
	assert.Equal(t, movedToPtr, *got.MovedToURI)
	require.NotNil(t, got.MovedAt)
	assert.Equal(t, nowPtr, *got.MovedAt)
	require.NotNil(t, got.AlsoKnownAs)
	assert.Equal(t, akaPtr, *got.AlsoKnownAs)

	// core/move が渡す string / time.Time (非ポインタ) 形式
	movedTo2 := "https://remote.example/users/newer"
	now2 := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	aka2 := "https://remote.example/users/aka2"
	require.NoError(t, repo.UpdateUser(uid, map[string]any{
		"movedToUri":  movedTo2,
		"movedAt":     now2,
		"alsoKnownAs": aka2,
	}))
	got, err = repo.FindByID(uid)
	require.NoError(t, err)
	require.NotNil(t, got.MovedToURI)
	assert.Equal(t, movedTo2, *got.MovedToURI)
	require.NotNil(t, got.MovedAt)
	assert.Equal(t, now2, *got.MovedAt)
	require.NotNil(t, got.AlsoKnownAs)
	assert.Equal(t, aka2, *got.AlsoKnownAs)
}

// MockMetaRepository.Update は real repo の挙動に近づけるため、varchar[]
// 列に nil / 非 string element 含む []any / 不明型を渡すと error を返す
// (#590 review #4)。これにより handler 側の coerce 漏れが mock テストでも
// 検出できるようになり、real DB を使わない handler テストでも regression
// detection が効く。
func TestMockMeta_Update_RejectsInvalidArrayValue(t *testing.T) {
	t.Run("nil for varchar[] column errors", func(t *testing.T) {
		repo := NewMockMetaRepository()
		err := repo.Update(map[string]any{"blockedHosts": nil})
		require.Error(t, err)
	})
	t.Run("non-string element in []any errors", func(t *testing.T) {
		repo := NewMockMetaRepository()
		err := repo.Update(map[string]any{"blockedHosts": []any{"ok.example", 42}})
		require.Error(t, err)
	})
	t.Run("unsupported type errors", func(t *testing.T) {
		repo := NewMockMetaRepository()
		err := repo.Update(map[string]any{"blockedHosts": "not-an-array"})
		require.Error(t, err)
	})
	t.Run("model.StringArray succeeds", func(t *testing.T) {
		repo := NewMockMetaRepository()
		err := repo.Update(map[string]any{"blockedHosts": []string{"ok.example"}})
		require.NoError(t, err)
		assert.Equal(t, []string{"ok.example"}, []string(repo.Meta.BlockedHosts))
	})
	t.Run("[]any of strings succeeds", func(t *testing.T) {
		repo := NewMockMetaRepository()
		err := repo.Update(map[string]any{"blockedHosts": []any{"ok.example"}})
		require.NoError(t, err)
		assert.Equal(t, []string{"ok.example"}, []string(repo.Meta.BlockedHosts))
	})
}

// admin/federation/update-instance handler は #715 / #724 で
// boolean (isSuspended) → enum (suspensionState) 変換を service 層で
// 行うようになった。mock の applyInstanceFields も string で渡された
// suspensionState を受け取れることを確認する (production handler の
// `out["suspensionState"] = string(...)` 経路と一致)。
func TestApplyInstanceFields_SuspensionStateString(t *testing.T) {
	repo := NewMockInstanceRepository()
	const host = "remote.example"
	require.NoError(t, repo.Create(&model.Instance{
		ID:              "inst1",
		Host:            host,
		SuspensionState: model.SuspensionStateNone,
	}))

	t.Run("string manuallySuspended", func(t *testing.T) {
		require.NoError(t, repo.UpdateFields(host, map[string]any{
			"suspensionState": "manuallySuspended",
		}))
		got, err := repo.FindByHost(host)
		require.NoError(t, err)
		assert.Equal(t, model.SuspensionStateManuallySuspended, got.SuspensionState)
	})

	t.Run("string none clears state", func(t *testing.T) {
		require.NoError(t, repo.UpdateFields(host, map[string]any{
			"suspensionState": "none",
		}))
		got, err := repo.FindByHost(host)
		require.NoError(t, err)
		assert.Equal(t, model.SuspensionStateNone, got.SuspensionState)
	})

	t.Run("typed enum value still works", func(t *testing.T) {
		require.NoError(t, repo.UpdateFields(host, map[string]any{
			"suspensionState": model.SuspensionStateGoneSuspended,
		}))
		got, err := repo.FindByHost(host)
		require.NoError(t, err)
		assert.Equal(t, model.SuspensionStateGoneSuspended, got.SuspensionState)
	})
}

// mock の varchar guard 自体をゲートする。production の呼び出し元
// (federation の refreshActor) は戻り値を捨てるので、guard が壊れても
// resolver 側の test では気付けない (#2662)。
func TestMockUserRepository_VarcharGuards(t *testing.T) {
	long := strings.Repeat("a", 129)
	nul := "al\x00ice"

	t.Run("create rejects over-long name", func(t *testing.T) {
		repo := NewMockUserRepository()
		require.Error(t, repo.Create(&model.User{ID: "u1", Username: "a", Name: &long}))
		assert.NotContains(t, repo.Users, "u1", "実 DB と同じく行を作らない")
	})
	t.Run("create rejects NUL in username", func(t *testing.T) {
		repo := NewMockUserRepository()
		require.Error(t, repo.Create(&model.User{ID: "u1", Username: nul, UsernameLower: nul}))
	})
	t.Run("create accepts 128 code points", func(t *testing.T) {
		repo := NewMockUserRepository()
		ok := strings.Repeat("あ", 128)
		require.NoError(t, repo.Create(&model.User{ID: "u1", Username: "a", Name: &ok}),
			"varchar はコードポイントで数えるのでバイト数では弾かない")
	})
	t.Run("update rejects and leaves the row untouched", func(t *testing.T) {
		repo := NewMockUserRepository()
		orig := "Alice"
		require.NoError(t, repo.Create(&model.User{ID: "u1", Username: "a", Name: &orig}))
		require.Error(t, repo.UpdateUser("u1", map[string]any{"name": &long}))
		// 実 DB は UPDATE ごと落とすので 1 列も変わらない。
		require.NotNil(t, repo.Users["u1"].Name)
		assert.Equal(t, "Alice", *repo.Users["u1"].Name)
	})

	t.Run("profile create rejects over-long location", func(t *testing.T) {
		repo := NewMockUserRepository()
		loc := strings.Repeat("b", 129)
		require.Error(t, repo.CreateProfile(&model.UserProfile{UserID: "u1", Location: &loc}))
		assert.NotContains(t, repo.Profiles, "u1")
	})
	t.Run("profile create rejects over-long description", func(t *testing.T) {
		repo := NewMockUserRepository()
		desc := strings.Repeat("c", 2049)
		require.Error(t, repo.CreateProfile(&model.UserProfile{UserID: "u1", Description: &desc}))
	})
	t.Run("profile update rejects and leaves the row untouched", func(t *testing.T) {
		repo := NewMockUserRepository()
		orig := "Kyoto"
		require.NoError(t, repo.CreateProfile(&model.UserProfile{UserID: "u1", Location: &orig}))
		bad := strings.Repeat("b", 129)
		require.Error(t, repo.UpdateProfile("u1", map[string]any{"location": &bad}))
		require.NotNil(t, repo.Profiles["u1"].Location)
		assert.Equal(t, "Kyoto", *repo.Profiles["u1"].Location)
	})
	t.Run("profile update rejects NUL in jsonb fields", func(t *testing.T) {
		repo := NewMockUserRepository()
		require.NoError(t, repo.CreateProfile(&model.UserProfile{UserID: "u1"}))
		err := repo.UpdateProfile("u1", map[string]any{"fields": "[{\"name\":\"a\\u0000b\"}]"})
		require.Error(t, err, "jsonb は \\u0000 エスケープを SQLSTATE 22P05 で拒否する")
	})
	// CreateProfile 経路は applyProfileFields を通らないので、
	// assertProfileColumns の jsonb 検査だけが頼り。
	t.Run("profile create rejects NUL in jsonb fields", func(t *testing.T) {
		repo := NewMockUserRepository()
		require.Error(t, repo.CreateProfile(&model.UserProfile{
			UserID: "u1",
			Fields: datatypes.JSON([]byte(`[{"name":"a\u0000b"}]`)),
		}))
		assert.NotContains(t, repo.Profiles, "u1")
	})
	t.Run("profile create rejects invalid JSON in jsonb fields", func(t *testing.T) {
		repo := NewMockUserRepository()
		require.Error(t, repo.CreateProfile(&model.UserProfile{
			UserID: "u1",
			Fields: datatypes.JSON([]byte("{")),
		}))
	})
	t.Run("profile create accepts valid fields", func(t *testing.T) {
		repo := NewMockUserRepository()
		require.NoError(t, repo.CreateProfile(&model.UserProfile{
			UserID: "u1",
			Fields: datatypes.JSON([]byte(`[{"name":"a","value":"b"}]`)),
		}))
	})
	t.Run("profile update rejects raw []byte for jsonb fields", func(t *testing.T) {
		repo := NewMockUserRepository()
		require.NoError(t, repo.CreateProfile(&model.UserProfile{UserID: "u1"}))
		require.Error(t, repo.UpdateProfile("u1", map[string]any{"fields": []byte("[]")}))
	})
	t.Run("profile update applies nothing when a later field fails", func(t *testing.T) {
		// 実 DB は UPDATE ごと落とすので、同じ書き込みに乗った他の列も残らない。
		// map の反復順に依存せず決定的であること。
		repo := NewMockUserRepository()
		require.NoError(t, repo.CreateProfile(&model.UserProfile{UserID: "u1"}))
		bad := strings.Repeat("b", 129)
		desc := "new description"
		require.Error(t, repo.UpdateProfile("u1", map[string]any{
			"location":    &bad,
			"description": &desc,
		}))
		assert.Nil(t, repo.Profiles["u1"].Description, "巻き添えの列も適用しない")
	})
}
