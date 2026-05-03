package repository

import (
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func TestPollRepository_Create(t *testing.T) {
	repo := NewPollRepository(testDB)
	user := insertTestUser(t, "u_pc_1", "polluser")
	defer cleanupUser(t, user.ID)

	note := &model.Note{
		ID:         "n_pc_1",
		UserID:     user.ID,
		Visibility: model.NoteVisibilityPublic,
		HasPoll:    true,
		Reactions:  datatypes.JSON([]byte("{}")),
	}
	require.NoError(t, testDB.Create(note).Error)
	defer cleanupNote(t, note.ID)

	poll := &model.Poll{
		NoteID:         note.ID,
		Multiple:       false,
		Choices:        pq.StringArray{"A", "B", "C"},
		Votes:          pq.Int64Array{0, 0, 0},
		NoteVisibility: model.NoteVisibilityPublic,
		UserID:         user.ID,
	}
	require.NoError(t, repo.Create(poll))

	// 作成されたことを確認
	var found model.Poll
	err := testDB.First(&found, "\"noteId\" = ?", note.ID).Error
	require.NoError(t, err)
	assert.Equal(t, note.ID, found.NoteID)
	assert.Len(t, found.Choices, 3)
	assert.False(t, found.Multiple)
}

// TestPollRepository_ListExpiredUnnotified covers the ticker scan path
// added in #690 (ExpiryWorker)。partial index 経由のクエリ条件
// (expiresAt < now AND notifiedAt IS NULL) が正しく適用され、limit が効く
// ことを guard する。
func TestPollRepository_ListExpiredUnnotified(t *testing.T) {
	repo := NewPollRepository(testDB)
	user := insertTestUser(t, "u_lex_1", "lexuser")
	defer cleanupUser(t, user.ID)

	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	// 3 種類の poll を seed: 期限切れ未通知 / 期限切れ通知済 / 未満了
	mk := func(id string, expires time.Time, notified *time.Time) {
		note := &model.Note{ID: id, UserID: user.ID, Visibility: model.NoteVisibilityPublic, HasPoll: true, Reactions: datatypes.JSON([]byte("{}"))}
		require.NoError(t, testDB.Create(note).Error)
		t.Cleanup(func() { cleanupNote(t, note.ID) })
		p := &model.Poll{
			NoteID: id, Multiple: false,
			Choices: pq.StringArray{"a"}, Votes: pq.Int64Array{0},
			NoteVisibility: model.NoteVisibilityPublic, UserID: user.ID,
			ExpiresAt:  &expires,
			NotifiedAt: notified,
		}
		require.NoError(t, repo.Create(p))
	}
	mk("p_lex_expired1", past, nil)
	mk("p_lex_expired2", past.Add(-time.Minute), nil)
	notified := past.Add(time.Minute)
	mk("p_lex_already", past, &notified)
	mk("p_lex_future", future, nil)

	rows, err := repo.ListExpiredUnnotified(now, 10)
	require.NoError(t, err)
	ids := []string{}
	for _, r := range rows {
		ids = append(ids, r.NoteID)
	}
	// 期限切れ未通知の 2 件のみ、ASC で並ぶ (古い方が先)
	assert.Equal(t, []string{"p_lex_expired2", "p_lex_expired1"}, ids)
}

func TestPollRepository_ListExpiredUnnotified_LimitCap(t *testing.T) {
	repo := NewPollRepository(testDB)
	user := insertTestUser(t, "u_lim_1", "limuser")
	defer cleanupUser(t, user.ID)

	now := time.Now()
	past := now.Add(-time.Hour)

	// limit=0 / 負数のとき内部で 100 にクランプされる
	for i := 0; i < 3; i++ {
		nid := "p_lim_" + string(rune('a'+i))
		note := &model.Note{ID: nid, UserID: user.ID, Visibility: model.NoteVisibilityPublic, HasPoll: true, Reactions: datatypes.JSON([]byte("{}"))}
		require.NoError(t, testDB.Create(note).Error)
		t.Cleanup(func() { cleanupNote(t, nid) })
		p := &model.Poll{NoteID: nid, Choices: pq.StringArray{"x"}, Votes: pq.Int64Array{0}, NoteVisibility: model.NoteVisibilityPublic, UserID: user.ID, ExpiresAt: &past}
		require.NoError(t, repo.Create(p))
	}

	rows, err := repo.ListExpiredUnnotified(now, 1)
	require.NoError(t, err)
	assert.Len(t, rows, 1)

	rows, err = repo.ListExpiredUnnotified(now, 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(rows), 3)
}

func TestPollRepository_MarkNotified(t *testing.T) {
	repo := NewPollRepository(testDB)
	user := insertTestUser(t, "u_mn_1", "mnuser")
	defer cleanupUser(t, user.ID)

	noteID := "n_mn_1"
	note := &model.Note{ID: noteID, UserID: user.ID, Visibility: model.NoteVisibilityPublic, HasPoll: true, Reactions: datatypes.JSON([]byte("{}"))}
	require.NoError(t, testDB.Create(note).Error)
	defer cleanupNote(t, noteID)

	expires := time.Now().Add(-time.Hour)
	require.NoError(t, repo.Create(&model.Poll{
		NoteID: noteID, Choices: pq.StringArray{"a"}, Votes: pq.Int64Array{0},
		NoteVisibility: model.NoteVisibilityPublic, UserID: user.ID,
		ExpiresAt: &expires,
	}))

	// 未通知状態
	pre, err := repo.FindByNoteID(noteID)
	require.NoError(t, err)
	assert.Nil(t, pre.NotifiedAt)

	now := time.Now()
	require.NoError(t, repo.MarkNotified(noteID, now))

	post, err := repo.FindByNoteID(noteID)
	require.NoError(t, err)
	require.NotNil(t, post.NotifiedAt)
	assert.WithinDuration(t, now, *post.NotifiedAt, time.Second)

	// 不在 noteID でも no-op (= no error)
	require.NoError(t, repo.MarkNotified("nonexistent", now))
}
