package repository

import (
	"context"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func cleanupPage(t *testing.T, id string) {
	t.Helper()
	testDB.Exec(`DELETE FROM "page_like" WHERE "pageId" = ?`, id)
	testDB.Exec(`DELETE FROM "page" WHERE id = ?`, id)
}

func newTestPage(id, ownerID, name string) *model.Page {
	return &model.Page{
		ID:         id,
		UpdatedAt:  time.Now(),
		Title:      "title-" + name,
		Name:       name,
		UserID:     ownerID,
		Content:    datatypes.JSON([]byte("[]")),
		Variables:  datatypes.JSON([]byte("[]")),
		Visibility: model.PageVisibilityPublic,
	}
}

func TestPageRepository_CreateAndFindByID(t *testing.T) {
	repo := NewPageRepository(testDB)
	user := insertTestUser(t, "u_pr_1", "pageuser1")
	defer cleanupUser(t, user.ID)

	p := newTestPage("pg_cr_1", user.ID, "alpha")
	require.NoError(t, repo.Create(p))
	defer cleanupPage(t, p.ID)

	got, err := repo.FindByID(p.ID)
	require.NoError(t, err)
	assert.Equal(t, "alpha", got.Name)
	assert.Equal(t, model.PageVisibilityPublic, got.Visibility)
}

func TestPageRepository_FindByID_NotFound(t *testing.T) {
	repo := NewPageRepository(testDB)
	_, err := repo.FindByID("missing")
	assert.Error(t, err)
}

// TestPageRepository_FindManyByIDs covers the batch helper added for
// /api/i/page-likes (#1136). 空 input は短絡で nil 返却、通常 input は
// 指定 ID set に該当する page だけ返り、欠落 ID は silent skip される。
func TestPageRepository_FindManyByIDs(t *testing.T) {
	repo := NewPageRepository(testDB)
	user := insertTestUser(t, "u_pr_fm", "pageuserfm")
	defer cleanupUser(t, user.ID)

	p1 := newTestPage("pg_fm_1", user.ID, "fm1")
	p2 := newTestPage("pg_fm_2", user.ID, "fm2")
	require.NoError(t, repo.Create(p1))
	defer cleanupPage(t, p1.ID)
	require.NoError(t, repo.Create(p2))
	defer cleanupPage(t, p2.ID)

	// 空 input は repo を叩かずに nil 返却。
	out, err := repo.FindManyByIDs(nil)
	require.NoError(t, err)
	assert.Nil(t, out)

	// 通常 input: 2 件 hit + 1 件 miss → 2 件のみ返る (順序は SQL 任せなので
	// length と ID set だけ確認)。
	out, err = repo.FindManyByIDs([]string{p1.ID, p2.ID, "ghost"})
	require.NoError(t, err)
	require.Len(t, out, 2)
	ids := map[string]bool{out[0].ID: true, out[1].ID: true}
	assert.True(t, ids[p1.ID])
	assert.True(t, ids[p2.ID])
}

func TestPageRepository_FindByUserAndName(t *testing.T) {
	repo := NewPageRepository(testDB)
	user := insertTestUser(t, "u_pr_2", "pageuser2")
	defer cleanupUser(t, user.ID)

	p := newTestPage("pg_cr_2", user.ID, "beta")
	require.NoError(t, repo.Create(p))
	defer cleanupPage(t, p.ID)

	got, err := repo.FindByUserAndName(user.ID, "beta")
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)

	_, err = repo.FindByUserAndName(user.ID, "nonexistent")
	assert.Error(t, err)
}

func TestPageRepository_UpdateFields(t *testing.T) {
	repo := NewPageRepository(testDB)
	user := insertTestUser(t, "u_pr_3", "pageuser3")
	defer cleanupUser(t, user.ID)

	p := newTestPage("pg_cr_3", user.ID, "gamma")
	require.NoError(t, repo.Create(p))
	defer cleanupPage(t, p.ID)

	summary := "summary text"
	require.NoError(t, repo.UpdateFields(p.ID, map[string]any{
		"title":       "gamma updated",
		"summary":     &summary,
		"alignCenter": true,
		"font":        "serif",
	}))

	got, err := repo.FindByID(p.ID)
	require.NoError(t, err)
	assert.Equal(t, "gamma updated", got.Title)
	require.NotNil(t, got.Summary)
	assert.Equal(t, "summary text", *got.Summary)
	assert.True(t, got.AlignCenter)
	assert.Equal(t, "serif", got.Font)
}

func TestPageRepository_UpdateFields_NoOp(t *testing.T) {
	repo := NewPageRepository(testDB)
	require.NoError(t, repo.UpdateFields("any", nil))
}

func TestPageRepository_Delete(t *testing.T) {
	repo := NewPageRepository(testDB)
	user := insertTestUser(t, "u_pr_4", "pageuser4")
	defer cleanupUser(t, user.ID)

	p := newTestPage("pg_cr_4", user.ID, "delta")
	require.NoError(t, repo.Create(p))
	require.NoError(t, repo.Delete(p))
	_, err := repo.FindByID(p.ID)
	assert.Error(t, err)
}

func TestPageRepository_IncrementCount(t *testing.T) {
	repo := NewPageRepository(testDB)
	user := insertTestUser(t, "u_pr_5", "pageuser5")
	defer cleanupUser(t, user.ID)

	p := newTestPage("pg_cr_5", user.ID, "epsilon")
	require.NoError(t, repo.Create(p))
	defer cleanupPage(t, p.ID)

	require.NoError(t, repo.IncrementCount(p.ID, "likedCount", 2))
	got, err := repo.FindByID(p.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, got.LikedCount)
}

func TestPageRepository_ListByUser(t *testing.T) {
	repo := NewPageRepository(testDB)
	user := insertTestUser(t, "u_pr_6", "pageuser6")
	defer cleanupUser(t, user.ID)

	for _, id := range []string{"pg_lst_1", "pg_lst_2", "pg_lst_3"} {
		p := newTestPage(id, user.ID, id)
		require.NoError(t, repo.Create(p))
		defer cleanupPage(t, p.ID)
	}

	rows, err := repo.ListByUser(user.ID, "", "", 10, 0)
	require.NoError(t, err)
	assert.Len(t, rows, 3)
}

func TestPageRepository_ListByUser_LimitClamp(t *testing.T) {
	repo := NewPageRepository(testDB)
	rows, err := repo.ListByUser("nobody", "", "", 9999, 0)
	require.NoError(t, err)
	assert.Empty(t, rows)
	rows, err = repo.ListByUser("nobody", "", "", -1, 0)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestPageRepository_ListByUser_QueryError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := testDB.WithContext(ctx)
	repo := NewPageRepository(db)
	_, err := repo.ListByUser("nobody", "", "", 10, 0)
	assert.Error(t, err)
}

// TestPageRepository_FindManyByIDs_QueryError covers the DB error branch
// of FindManyByIDs (#1136). cancelled ctx を渡して driver level で
// query error を発生させ、handler 側の fail-soft が依存する error path
// が想定通り bubble up することを確認する。
func TestPageRepository_FindManyByIDs_QueryError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := testDB.WithContext(ctx)
	repo := NewPageRepository(db)
	_, err := repo.FindManyByIDs([]string{"pg_x"})
	assert.Error(t, err)
}

func TestPageRepository_ListFeatured(t *testing.T) {
	repo := NewPageRepository(testDB)
	user := insertTestUser(t, "u_pr_7", "pageuser7")
	defer cleanupUser(t, user.ID)

	// Create three pages with different liked counts.
	pages := []*model.Page{
		newTestPage("pg_ft_1", user.ID, "ft1"),
		newTestPage("pg_ft_2", user.ID, "ft2"),
		newTestPage("pg_ft_3", user.ID, "ft3"),
	}
	pages[0].LikedCount = 5
	pages[1].LikedCount = 1
	pages[2].LikedCount = 10
	for _, p := range pages {
		require.NoError(t, repo.Create(p))
		defer cleanupPage(t, p.ID)
	}

	rows, err := repo.ListFeatured("", "", 10, 0)
	require.NoError(t, err)
	// 他テストの残骸が混ざる可能性があるので、この3件が降順で含まれることだけ検証する。
	var found []*model.Page
	for _, p := range rows {
		if p.UserID == user.ID {
			found = append(found, p)
		}
	}
	require.Len(t, found, 3)
	assert.Equal(t, "pg_ft_3", found[0].ID)
	assert.Equal(t, "pg_ft_1", found[1].ID)
	assert.Equal(t, "pg_ft_2", found[2].ID)
}

func TestPageRepository_ListFeatured_LimitClamp(t *testing.T) {
	repo := NewPageRepository(testDB)
	rows, err := repo.ListFeatured("", "", 9999, 0)
	require.NoError(t, err)
	_ = rows
	rows, err = repo.ListFeatured("", "", -1, 0)
	require.NoError(t, err)
	_ = rows
}

func TestPageRepository_ListFeatured_QueryError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := testDB.WithContext(ctx)
	repo := NewPageRepository(db)
	_, err := repo.ListFeatured("", "", 10, 0)
	assert.Error(t, err)
}
