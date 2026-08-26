package repository

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/model"
)

// emojiRemoteColumns は upsertEmojis が AP tag から書く列と、その上限。
// migration/000001_initial.up.sql と一致させる。
var emojiRemoteColumns = []struct {
	column string
	max    int
}{
	{"name", 128},
	{"originalUrl", 512},
	{"publicUrl", 512},
	{"uri", 512},
	{"license", 1024},
}

// 列の上限そのものを schema から固定する (#2726)。
func TestEmoji_RemoteColumnLimits(t *testing.T) {
	for _, tc := range emojiRemoteColumns {
		var n int
		require.NoError(t, testDB.Raw(`SELECT character_maximum_length FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'emoji' AND column_name = ?`,
			tc.column).Scan(&n).Error)
		assert.Equal(t, tc.max, n,
			"emoji.%s の列長が変わっている (internal/core/federation/resolver.go の定数も直すこと)", tc.column)
	}
}

// upsertEmojis が「収まる」と判断した長さで実際に書けること (#2726)。
// **全角で埋める** — byte で数える実装だと 3 倍になって入らない。
func TestEmoji_RemoteColumnLimitsAcceptMaxLengthValues(t *testing.T) {
	repo := NewEmojiRepository(testDB)
	host := "emojicol.example"
	url := "https://emojicol.example/" + strings.Repeat("あ", 487)
	require.Equal(t, 512, len([]rune(url)))
	license := strings.Repeat("い", 1024)

	emoji := &model.Emoji{
		ID:          "e_collimit_1",
		Name:        strings.Repeat("う", 128),
		Host:        &host,
		OriginalURL: url,
		PublicURL:   url,
		URI:         &url,
		License:     &license,
	}
	require.NoError(t, repo.Create(emoji))
	defer testDB.Exec(`DELETE FROM "emoji" WHERE id = ?`, emoji.ID)

	got, err := repo.FindByNameAndHost(emoji.Name, &host)
	require.NoError(t, err)
	assert.Equal(t, 128, len([]rune(got.Name)))
	assert.Equal(t, 512, len([]rune(got.OriginalURL)))
	require.NotNil(t, got.License)
	assert.Equal(t, 1024, len([]rune(*got.License)))
}
