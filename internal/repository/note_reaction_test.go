package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func cleanupReaction(t *testing.T, id string) {
	t.Helper()
	testDB.Exec(`DELETE FROM "note_reaction" WHERE id = ?`, id)
}

func TestNoteReactionRepository_CreateAndFind(t *testing.T) {
	repo := NewNoteReactionRepository(testDB)
	user := insertTestUser(t, "u_nr_1", "rxuser1")
	defer cleanupUser(t, user.ID)

	noteRepo := NewNoteRepository(testDB)
	note := &model.Note{
		ID:         "n_nr_1",
		UserID:     user.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, noteRepo.Create(note))
	defer cleanupNote(t, note.ID)

	rec := &model.NoteReaction{ID: "rx_1", UserID: user.ID, NoteID: note.ID, Reaction: "👍"}
	require.NoError(t, repo.Create(rec))
	defer cleanupReaction(t, rec.ID)

	found, err := repo.FindByPair(user.ID, note.ID)
	require.NoError(t, err)
	assert.Equal(t, "👍", found.Reaction)
}

func TestNoteReactionRepository_DuplicateError(t *testing.T) {
	repo := NewNoteReactionRepository(testDB)
	user := insertTestUser(t, "u_nr_2", "rxuser2")
	defer cleanupUser(t, user.ID)

	noteRepo := NewNoteRepository(testDB)
	note := &model.Note{
		ID:         "n_nr_2",
		UserID:     user.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, noteRepo.Create(note))
	defer cleanupNote(t, note.ID)

	rec1 := &model.NoteReaction{ID: "rx_d1", UserID: user.ID, NoteID: note.ID, Reaction: "👍"}
	require.NoError(t, repo.Create(rec1))
	defer cleanupReaction(t, rec1.ID)

	// 同じ (userId, noteId) で再度作成すると一意制約違反
	rec2 := &model.NoteReaction{ID: "rx_d2", UserID: user.ID, NoteID: note.ID, Reaction: "❤"}
	err := repo.Create(rec2)
	assert.ErrorIs(t, err, ErrDuplicateReaction)
}

func TestNoteReactionRepository_Delete(t *testing.T) {
	repo := NewNoteReactionRepository(testDB)
	user := insertTestUser(t, "u_nr_3", "rxuser3")
	defer cleanupUser(t, user.ID)

	noteRepo := NewNoteRepository(testDB)
	note := &model.Note{
		ID:         "n_nr_3",
		UserID:     user.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, noteRepo.Create(note))
	defer cleanupNote(t, note.ID)

	rec := &model.NoteReaction{ID: "rx_del_1", UserID: user.ID, NoteID: note.ID, Reaction: "👍"}
	require.NoError(t, repo.Create(rec))
	require.NoError(t, repo.Delete(rec))

	_, err := repo.FindByPair(user.ID, note.ID)
	assert.Error(t, err)
}

func TestNoteReactionRepository_ListByNoteID(t *testing.T) {
	repo := NewNoteReactionRepository(testDB)
	user1 := insertTestUser(t, "u_nr_4", "rxuser4")
	user2 := insertTestUser(t, "u_nr_5", "rxuser5")
	defer cleanupUser(t, user1.ID)
	defer cleanupUser(t, user2.ID)

	noteRepo := NewNoteRepository(testDB)
	note := &model.Note{
		ID:         "n_nr_4",
		UserID:     user1.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, noteRepo.Create(note))
	defer cleanupNote(t, note.ID)

	for _, r := range []struct {
		id, uid, rx string
	}{
		{"rx_l1", user1.ID, "👍"},
		{"rx_l2", user2.ID, "❤"},
	} {
		rec := &model.NoteReaction{ID: r.id, UserID: r.uid, NoteID: note.ID, Reaction: r.rx}
		require.NoError(t, repo.Create(rec))
		defer cleanupReaction(t, rec.ID)
	}

	out, err := repo.ListByNoteID(note.ID, "", "", 10, nil)
	require.NoError(t, err)
	assert.Len(t, out, 2)

	// 種類絞り込み
	out, err = repo.ListByNoteID(note.ID, "", "", 10, []string{"👍"})
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "👍", out[0].Reaction)

	// untilID で絞り込み
	out, err = repo.ListByNoteID(note.ID, "rx_l2", "", 10, nil)
	require.NoError(t, err)
	assert.Len(t, out, 1)

	// sinceID で絞り込み
	out, err = repo.ListByNoteID(note.ID, "", "rx_l1", 10, nil)
	require.NoError(t, err)
	assert.Len(t, out, 1)
}

// TestNoteReactionRepository_ListByUserID covers the #821 PR-D path used by
// users/reactions endpoint: reactor 視点の reaction list を取得し、User /
// Note を Preload して pack 時の N+1 を avoid する。
func TestNoteReactionRepository_ListByUserID(t *testing.T) {
	repo := NewNoteReactionRepository(testDB)
	user1 := insertTestUser(t, "u_nr_6", "rxuser6")
	user2 := insertTestUser(t, "u_nr_7", "rxuser7")
	defer cleanupUser(t, user1.ID)
	defer cleanupUser(t, user2.ID)

	noteRepo := NewNoteRepository(testDB)
	note1 := &model.Note{
		ID:         "n_nr_5",
		UserID:     user1.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	note2 := &model.Note{
		ID:         "n_nr_6",
		UserID:     user1.ID,
		Visibility: model.NoteVisibilityPublic,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, noteRepo.Create(note1))
	require.NoError(t, noteRepo.Create(note2))
	defer cleanupNote(t, note1.ID)
	defer cleanupNote(t, note2.ID)

	// user2 が note1 / note2 に reaction、user1 は note2 のみに reaction。
	// ListByUserID(user2) で 2 件、ListByUserID(user1) で 1 件返ることを check。
	for _, r := range []struct {
		id, uid, nid, rx string
	}{
		{"rx_u1", user2.ID, note1.ID, "👍"},
		{"rx_u2", user2.ID, note2.ID, "❤"},
		{"rx_u3", user1.ID, note2.ID, "🎉"},
	} {
		rec := &model.NoteReaction{ID: r.id, UserID: r.uid, NoteID: r.nid, Reaction: r.rx}
		require.NoError(t, repo.Create(rec))
		defer cleanupReaction(t, rec.ID)
	}

	// user2 視点で 2 件、Preload("User") / Preload("Note") が効いていることも
	// 確認する。
	out, err := repo.ListByUserID(user2.ID, "", "", 10)
	require.NoError(t, err)
	require.Len(t, out, 2)
	for _, rec := range out {
		assert.Equal(t, user2.ID, rec.UserID)
		require.NotNil(t, rec.User, "User should be preloaded")
		assert.Equal(t, user2.ID, rec.User.ID)
		require.NotNil(t, rec.Note, "Note should be preloaded")
	}

	// user1 視点で 1 件 (= note2 のみ)。
	out, err = repo.ListByUserID(user1.ID, "", "", 10)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "rx_u3", out[0].ID)

	// untilID で絞り込み (rx_u2 より前 → rx_u1 の 1 件)。
	out, err = repo.ListByUserID(user2.ID, "rx_u2", "", 10)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "rx_u1", out[0].ID)

	// sinceID で絞り込み (rx_u1 より後 → rx_u2 の 1 件)。
	out, err = repo.ListByUserID(user2.ID, "", "rx_u1", 10)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "rx_u2", out[0].ID)
}

func TestNoteReactionRepository_QueryErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := testDB.WithContext(ctx)
	repo := NewNoteReactionRepository(db)

	err := repo.Create(&model.NoteReaction{ID: "x", UserID: "u", NoteID: "n", Reaction: "a"})
	assert.Error(t, err)
	// キャンセル済みコンテキストはErrDuplicateReactionではなく素のエラー
	assert.False(t, errors.Is(err, ErrDuplicateReaction))

	_, err = repo.FindByPair("u", "n")
	assert.Error(t, err)

	_, err = repo.ListByNoteID("n", "", "", 10, nil)
	assert.Error(t, err)
}
