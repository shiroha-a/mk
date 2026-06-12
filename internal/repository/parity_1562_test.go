package repository

import (
	"sort"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// TestClipNoteRepository_SearchWords は clips/notes の search push-down
// (#1562) を検証する。text / cw への ILIKE 部分一致 (語内 OR、語間 AND) と
// LIKE メタ文字 (%) の literal 扱いを実 SQL で確認する。
func TestClipNoteRepository_SearchWords(t *testing.T) {
	clipRepo := NewClipRepository(testDB)
	repo := NewClipNoteRepository(testDB)
	noteRepo := NewNoteRepository(testDB)

	author := insertTestUser(t, "u_cns_a", "cnsauthor")
	t.Cleanup(func() { cleanupUser(t, author.ID) })

	c := newTestClip("clp_cns", author.ID, "search")
	require.NoError(t, clipRepo.Create(c))
	defer cleanupClip(t, c.ID)

	mkNote := func(id string, text, cw *string) {
		n := &model.Note{ID: id, UserID: author.ID, Visibility: model.NoteVisibilityPublic,
			Text: text, CW: cw, Reactions: datatypes.JSON([]byte("{}"))}
		require.NoError(t, noteRepo.Create(n))
		t.Cleanup(func() { cleanupNote(t, id) })
		cn := &model.ClipNote{ID: "cns_" + id, ClipID: c.ID, NoteID: id}
		require.NoError(t, repo.Create(cn))
		t.Cleanup(func() { testDB.Exec(`DELETE FROM "clip_note" WHERE id = ?`, cn.ID) })
	}
	s := func(v string) *string { return &v }
	mkNote("n_cns_1", s("Golang ROCKS here"), nil)
	mkNote("n_cns_2", s("unrelated text"), nil)
	mkNote("n_cns_3", nil, s("cw mentions golang"))
	mkNote("n_cns_4", s("100% literal percent"), nil)

	ids := func(rows []*model.ClipNote) []string {
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.NoteID)
		}
		sort.Strings(out)
		return out
	}

	// ILIKE は case-insensitive、text / cw のどちらかに当たれば良い
	rows, err := repo.ListByClipVisible(c.ID, "", "", "", 50, []string{"golang"})
	require.NoError(t, err)
	assert.Equal(t, []string{"n_cns_1", "n_cns_3"}, ids(rows))

	// 複数語は AND
	rows, err = repo.ListByClipVisible(c.ID, "", "", "", 50, []string{"golang", "rocks"})
	require.NoError(t, err)
	assert.Equal(t, []string{"n_cns_1"}, ids(rows))

	// LIKE メタ文字はエスケープされ literal 一致になる ("100%" は n_cns_4 のみ)
	rows, err = repo.ListByClipVisible(c.ID, "", "", "", 50, []string{"100%"})
	require.NoError(t, err)
	assert.Equal(t, []string{"n_cns_4"}, ids(rows))

	// "_" も literal ("100_" は wildcard 解釈なら n_cns_4 に当たるが、escape
	// 済みなので 0 件)
	rows, err = repo.ListByClipVisible(c.ID, "", "", "", 50, []string{"100_"})
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// TestClipFavoriteRepository_CountByClip は favoritedCount 実カウント (#1562)
// の repository 実装を検証する。
func TestClipFavoriteRepository_CountByClip(t *testing.T) {
	clipRepo := NewClipRepository(testDB)
	repo := NewClipFavoriteRepository(testDB)

	owner := insertTestUser(t, "u_cfc_o", "cfcowner")
	t.Cleanup(func() { cleanupUser(t, owner.ID) })
	fan1 := insertTestUser(t, "u_cfc_1", "cfcfan1")
	t.Cleanup(func() { cleanupUser(t, fan1.ID) })
	fan2 := insertTestUser(t, "u_cfc_2", "cfcfan2")
	t.Cleanup(func() { cleanupUser(t, fan2.ID) })

	c := newTestClip("clp_cfc", owner.ID, "counted")
	require.NoError(t, clipRepo.Create(c))
	defer cleanupClip(t, c.ID)

	n, err := repo.CountByClip(c.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, n)

	require.NoError(t, repo.Create(&model.ClipFavorite{ID: "cf_cfc_1", UserID: fan1.ID, ClipID: c.ID}))
	t.Cleanup(func() { testDB.Exec(`DELETE FROM "clip_favorite" WHERE id = ?`, "cf_cfc_1") })
	require.NoError(t, repo.Create(&model.ClipFavorite{ID: "cf_cfc_2", UserID: fan2.ID, ClipID: c.ID}))
	t.Cleanup(func() { testDB.Exec(`DELETE FROM "clip_favorite" WHERE id = ?`, "cf_cfc_2") })

	n, err = repo.CountByClip(c.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
}
