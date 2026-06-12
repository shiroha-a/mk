package repository

import (
	"context"
	"sort"
	"testing"

	"github.com/lib/pq"
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

// TestClipNoteRepository_ListByClipVisible は clips/notes の visibility
// push-down (#1418 review) を検証する。clip に public / followers / specified
// の note を混在させ、viewer ごとに SQL 段階で見える clip_note が変わること、
// および ListByClip (export 経路) は全件返す (= フィルタしない) ことを確認する。
func TestClipNoteRepository_ListByClipVisible(t *testing.T) {
	clipRepo := NewClipRepository(testDB)
	repo := NewClipNoteRepository(testDB)
	noteRepo := NewNoteRepository(testDB)
	followingRepo := NewFollowingRepository(testDB)

	mkUser := func(id, username string) *model.User {
		u := insertTestUser(t, id, username)
		t.Cleanup(func() { cleanupUser(t, u.ID) })
		return u
	}
	author := mkUser("u_cnv_a", "cnvauthor")
	follower := mkUser("u_cnv_f", "cnvfollower")
	allowed := mkUser("u_cnv_al", "cnvallowed")

	c := newTestClip("clp_cnv", author.ID, "vis")
	require.NoError(t, clipRepo.Create(c))
	defer cleanupClip(t, c.ID)

	notes := []*model.Note{
		{ID: "n_cnv_pub", UserID: author.ID, Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}"))},
		{ID: "n_cnv_fol", UserID: author.ID, Visibility: model.NoteVisibilityFollowers, Reactions: datatypes.JSON([]byte("{}"))},
		{ID: "n_cnv_spec", UserID: author.ID, Visibility: model.NoteVisibilitySpecified, VisibleUserIDs: pq.StringArray{allowed.ID}, Reactions: datatypes.JSON([]byte("{}"))},
	}
	for _, n := range notes {
		require.NoError(t, noteRepo.Create(n))
		defer cleanupNote(t, n.ID)
		cn := &model.ClipNote{ID: "cnv_" + n.ID, ClipID: c.ID, NoteID: n.ID}
		require.NoError(t, repo.Create(cn))
		defer testDB.Exec(`DELETE FROM "clip_note" WHERE id = ?`, cn.ID)
	}

	f := &model.Following{ID: "fl_cnv_1", FollowerID: follower.ID, FolloweeID: author.ID}
	require.NoError(t, followingRepo.Create(f))
	defer testDB.Exec(`DELETE FROM "following" WHERE id = ?`, f.ID)

	noteIDsOf := func(rows []*model.ClipNote) []string {
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.NoteID)
		}
		sort.Strings(out)
		return out
	}

	// ListByClip (export 経路) はフィルタせず全件返す。
	rows, err := repo.ListByClip(c.ID, "", "", 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_cnv_fol", "n_cnv_pub", "n_cnv_spec"}, noteIDsOf(rows))

	// 匿名 viewer は public のみ。
	rows, err = repo.ListByClipVisible(c.ID, "", "", "", 50, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_cnv_pub"}, noteIDsOf(rows))

	// follower は public + followers。
	rows, err = repo.ListByClipVisible(c.ID, follower.ID, "", "", 50, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_cnv_fol", "n_cnv_pub"}, noteIDsOf(rows))

	// specified 対象 viewer は public + specified。
	rows, err = repo.ListByClipVisible(c.ID, allowed.ID, "", "", 50, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_cnv_pub", "n_cnv_spec"}, noteIDsOf(rows))

	// author 本人は全 visibility を閲覧可。
	rows, err = repo.ListByClipVisible(c.ID, author.ID, "", "", 50, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"n_cnv_fol", "n_cnv_pub", "n_cnv_spec"}, noteIDsOf(rows))
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
