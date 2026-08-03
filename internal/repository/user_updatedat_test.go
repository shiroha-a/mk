package repository

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// upstream が user.updatedAt を書くのは NoteCreateService.incNotesCountOfUser
// (= ノート投稿時) の 1 箇所だけ。GORM は `UpdatedAt` という名前の field を
// 規約で自動更新するため、放置すると全 user 行書き込み (auth middleware が
// 5 分おきに書く lastActiveDate を含む) で bump され、意味が「最終アクセス
// 時刻」に化ける。model 側で autoUpdateTime/autoCreateTime を切ってあることを
// 実 DB で固定する (#2285)。
func TestUserUpdatedAt_OnlyBumpedByNoteCreation(t *testing.T) {
	insertTestUser(t, "uat1", "uat1")
	defer testDB.Exec(`DELETE FROM "user" WHERE id = 'uat1'`)
	repo := NewUserRepository(testDB)

	// signup 相当の Create では書かれない (upstream も NULL のまま)。
	u, err := repo.FindByID("uat1")
	require.NoError(t, err)
	require.Nil(t, u.UpdatedAt, "Create では updatedAt を書かない")

	// lastActiveDate の更新でも書かれない。
	require.NoError(t, repo.UpdateUser("uat1", map[string]any{"lastActiveDate": time.Now()}))
	u, err = repo.FindByID("uat1")
	require.NoError(t, err)
	require.Nil(t, u.UpdatedAt, "他 column の更新で updatedAt を bump しない")

	// ノート投稿 (notesCount 増分) でのみ書かれる。
	require.NoError(t, repo.IncrementNotesCount("uat1", 1))
	u, err = repo.FindByID("uat1")
	require.NoError(t, err)
	require.NotNil(t, u.UpdatedAt, "ノート投稿では updatedAt を書く")
	afterNote := *u.UpdatedAt

	// ノート削除 (notesCount 減算) では触らない (upstream も触らない)。
	require.NoError(t, repo.IncrementNotesCount("uat1", -1))
	u, err = repo.FindByID("uat1")
	require.NoError(t, err)
	require.NotNil(t, u.UpdatedAt)
	assert.True(t, u.UpdatedAt.Equal(afterNote), "ノート削除では updatedAt を触らない")
}
