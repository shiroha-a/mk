package repository

import (
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoteDraftRepository_Full(t *testing.T) {
	repo := NewNoteDraftRepository(testDB)
	user := insertTestUser(t, "u_draft_1", "draftuser")
	defer cleanupUser(t, user.ID)

	text := "draft text"
	// Create
	draft := &model.NoteDraft{
		ID:         "draft_1",
		UserID:     user.ID,
		Text:       &text,
		Visibility: "public",
	}
	require.NoError(t, repo.Create(draft))
	defer testDB.Exec(`DELETE FROM "note_draft" WHERE id = ?`, draft.ID)

	// FindByIDAndUser
	found, err := repo.FindByIDAndUser("draft_1", user.ID)
	require.NoError(t, err)
	assert.Equal(t, "draft text", *found.Text)

	// FindByIDAndUser - not found
	_, err = repo.FindByIDAndUser("ghost", user.ID)
	assert.Error(t, err)

	// ListByUser
	list, err := repo.ListByUser(user.ID, 10)
	require.NoError(t, err)
	assert.Len(t, list, 1)

	// ListByUser - default limit
	list2, err := repo.ListByUser(user.ID, 0)
	require.NoError(t, err)
	assert.Len(t, list2, 1)

	// Update
	newText := "updated text"
	found.Text = &newText
	require.NoError(t, repo.Update(found))
	updated, err := repo.FindByIDAndUser("draft_1", user.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated text", *updated.Text)

	// CountByUser
	count, err := repo.CountByUser(user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// Delete: 該当行が存在するので RowsAffected = 1
	rows, err := repo.Delete("draft_1", user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)
	_, err = repo.FindByIDAndUser("draft_1", user.ID)
	assert.Error(t, err)

	// CountByUser after delete
	count, err = repo.CountByUser(user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	// Delete on already-removed draft: 0 rows affected, no error。
	// handler 側の "no such draft" 判定はこの 0 を見て 404 を返す。
	rows, err = repo.Delete("draft_1", user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), rows)
}
