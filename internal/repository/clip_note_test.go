package repository

import (
	"context"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func TestClipNoteRepository_LifeCycle(t *testing.T) {
	clipRepo := NewClipRepository(testDB)
	repo := NewClipNoteRepository(testDB)
	noteRepo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_cn_1", "cnuser1")
	defer cleanupUser(t, user.ID)

	c := newTestClip("clp_cn_1", user.ID, "alpha")
	require.NoError(t, clipRepo.Create(c))
	defer cleanupClip(t, c.ID)

	n := &model.Note{
		ID:         "n_cn_1",
		UserID:     user.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, noteRepo.Create(n))
	defer cleanupNote(t, n.ID)

	cn := &model.ClipNote{ID: "cn_pair_1", ClipID: c.ID, NoteID: n.ID}
	require.NoError(t, repo.Create(cn))
	defer testDB.Exec(`DELETE FROM "clip_note" WHERE id = ?`, cn.ID)

	got, err := repo.FindByPair(c.ID, n.ID)
	require.NoError(t, err)
	assert.Equal(t, cn.ID, got.ID)

	rows, err := repo.ListByClip(c.ID, "", "", 10)
	require.NoError(t, err)
	assert.Len(t, rows, 1)

	count, err := repo.CountByClip(c.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	require.NoError(t, repo.Delete(cn))
	_, err = repo.FindByPair(c.ID, n.ID)
	assert.Error(t, err)
}

func TestClipNoteRepository_FindByPair_NotFound(t *testing.T) {
	repo := NewClipNoteRepository(testDB)
	_, err := repo.FindByPair("nope", "nope")
	assert.Error(t, err)
}

func TestClipNoteRepository_ListByClip_LimitClampAndPagination(t *testing.T) {
	clipRepo := NewClipRepository(testDB)
	repo := NewClipNoteRepository(testDB)
	noteRepo := NewNoteRepository(testDB)
	user := insertTestUser(t, "u_cn_2", "cnuser2")
	defer cleanupUser(t, user.ID)

	c := newTestClip("clp_cn_2", user.ID, "beta")
	require.NoError(t, clipRepo.Create(c))
	defer cleanupClip(t, c.ID)

	for _, id := range []string{"n_cn_2a", "n_cn_2b", "n_cn_2c"} {
		n := &model.Note{
			ID: id, UserID: user.ID, Visibility: model.NoteVisibilityPublic,
			Reactions: datatypes.JSON([]byte("{}")),
		}
		require.NoError(t, noteRepo.Create(n))
		defer cleanupNote(t, n.ID)
		cn := &model.ClipNote{ID: "cn_" + id, ClipID: c.ID, NoteID: n.ID}
		require.NoError(t, repo.Create(cn))
		defer testDB.Exec(`DELETE FROM "clip_note" WHERE id = ?`, cn.ID)
	}

	rows, err := repo.ListByClip(c.ID, "", "", 9999) // limit clamp
	require.NoError(t, err)
	assert.Len(t, rows, 3)

	rows, err = repo.ListByClip(c.ID, "", "", -1)
	require.NoError(t, err)
	assert.Len(t, rows, 3)

	rows, err = repo.ListByClip(c.ID, "cn_n_cn_2c", "", 10)
	require.NoError(t, err)
	assert.Len(t, rows, 2)

	rows, err = repo.ListByClip(c.ID, "", "cn_n_cn_2a", 10)
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

func TestClipNoteRepository_ListByClip_QueryError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := testDB.WithContext(ctx)
	repo := NewClipNoteRepository(db)
	_, err := repo.ListByClip("any", "", "", 10)
	assert.Error(t, err)
	_, err = repo.CountByClip("any")
	assert.Error(t, err)
}
