package repository

import (
	"context"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cleanupClip(t *testing.T, id string) {
	t.Helper()
	testDB.Exec(`DELETE FROM "clip_note" WHERE "clipId" = ?`, id)
	testDB.Exec(`DELETE FROM "clip" WHERE id = ?`, id)
}

func newTestClip(id, ownerID, name string) *model.Clip {
	return &model.Clip{
		ID:     id,
		UserID: ownerID,
		Name:   name,
	}
}

func TestClipRepository_CreateAndFindByID(t *testing.T) {
	repo := NewClipRepository(testDB)
	user := insertTestUser(t, "u_clr_1", "clipuser1")
	defer cleanupUser(t, user.ID)

	c := newTestClip("clp_cr_1", user.ID, "alpha clip")
	require.NoError(t, repo.Create(c))
	defer cleanupClip(t, c.ID)

	got, err := repo.FindByID(c.ID)
	require.NoError(t, err)
	assert.Equal(t, "alpha clip", got.Name)
}

func TestClipRepository_FindByID_NotFound(t *testing.T) {
	repo := NewClipRepository(testDB)
	_, err := repo.FindByID("missing")
	assert.Error(t, err)
}

func TestClipRepository_UpdateFields(t *testing.T) {
	repo := NewClipRepository(testDB)
	user := insertTestUser(t, "u_clr_2", "clipuser2")
	defer cleanupUser(t, user.ID)

	c := newTestClip("clp_cr_2", user.ID, "beta")
	require.NoError(t, repo.Create(c))
	defer cleanupClip(t, c.ID)

	desc := "new description"
	require.NoError(t, repo.UpdateFields(c.ID, map[string]any{
		"name":        "beta updated",
		"description": &desc,
		"isPublic":    true,
	}))
	got, err := repo.FindByID(c.ID)
	require.NoError(t, err)
	assert.Equal(t, "beta updated", got.Name)
	assert.True(t, got.IsPublic)
}

func TestClipRepository_UpdateFields_NoOp(t *testing.T) {
	repo := NewClipRepository(testDB)
	require.NoError(t, repo.UpdateFields("any", nil))
}

func TestClipRepository_Delete(t *testing.T) {
	repo := NewClipRepository(testDB)
	user := insertTestUser(t, "u_clr_3", "clipuser3")
	defer cleanupUser(t, user.ID)

	c := newTestClip("clp_cr_3", user.ID, "gamma")
	require.NoError(t, repo.Create(c))
	require.NoError(t, repo.Delete(c))
	_, err := repo.FindByID(c.ID)
	assert.Error(t, err)
}

func TestClipRepository_IncrementCount(t *testing.T) {
	repo := NewClipRepository(testDB)
	user := insertTestUser(t, "u_clr_4", "clipuser4")
	defer cleanupUser(t, user.ID)

	c := newTestClip("clp_cr_4", user.ID, "delta")
	require.NoError(t, repo.Create(c))
	defer cleanupClip(t, c.ID)

	require.NoError(t, repo.IncrementCount(c.ID, "notesCount", 3))
	got, err := repo.FindByID(c.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, got.NotesCount)
}

func TestClipRepository_ListByUser(t *testing.T) {
	repo := NewClipRepository(testDB)
	user := insertTestUser(t, "u_clr_5", "clipuser5")
	defer cleanupUser(t, user.ID)

	for _, id := range []string{"clp_lst_1", "clp_lst_2", "clp_lst_3"} {
		c := newTestClip(id, user.ID, id)
		require.NoError(t, repo.Create(c))
		defer cleanupClip(t, c.ID)
	}

	rows, err := repo.ListByUser(user.ID, "", "", 10, 0)
	require.NoError(t, err)
	assert.Len(t, rows, 3)

	count, err := repo.CountByUser(user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestClipRepository_ListByUser_LimitClamp(t *testing.T) {
	repo := NewClipRepository(testDB)
	rows, err := repo.ListByUser("nobody", "", "", 9999, 0)
	require.NoError(t, err)
	assert.Empty(t, rows)
	rows, err = repo.ListByUser("nobody", "", "", -1, 0)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestClipRepository_ListByUser_QueryError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := testDB.WithContext(ctx)
	repo := NewClipRepository(db)
	_, err := repo.ListByUser("nobody", "", "", 10, 0)
	assert.Error(t, err)
	_, err = repo.CountByUser("nobody")
	assert.Error(t, err)
}

// frontend Paginator (cursor mode) との互換性確認: untilID < clip ID 群
// のうち、id < untilID で DESC で limit 件返ること、sinceID 単独だと ASC
// (paginationOrder の挙動)。#487 回帰防止。
func TestClipRepository_ListByUser_CursorPagination(t *testing.T) {
	repo := NewClipRepository(testDB)
	user := insertTestUser(t, "u_clr_pag", "clippag")
	defer cleanupUser(t, user.ID)

	for _, id := range []string{"clp_pg_a", "clp_pg_b", "clp_pg_c", "clp_pg_d"} {
		c := newTestClip(id, user.ID, id)
		require.NoError(t, repo.Create(c))
		defer cleanupClip(t, c.ID)
	}

	// untilID="clp_pg_c" → id < "clp_pg_c" を DESC 2 件 = [b, a]
	rows, err := repo.ListByUser(user.ID, "", "clp_pg_c", 2, 0)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "clp_pg_b", rows[0].ID)
	assert.Equal(t, "clp_pg_a", rows[1].ID)

	// sinceID="clp_pg_b" → id > "clp_pg_b" を ASC 2 件 = [c, d]
	rows, err = repo.ListByUser(user.ID, "clp_pg_b", "", 2, 0)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "clp_pg_c", rows[0].ID)
	assert.Equal(t, "clp_pg_d", rows[1].ID)
}
