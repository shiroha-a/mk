package repository

import (
	"context"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cleanupFlash(t *testing.T, id string) {
	t.Helper()
	testDB.Exec(`DELETE FROM "flash_like" WHERE "flashId" = ?`, id)
	testDB.Exec(`DELETE FROM "flash" WHERE id = ?`, id)
}

func newTestFlash(id, ownerID, title string) *model.Flash {
	return &model.Flash{
		ID:          id,
		UpdatedAt:   time.Now(),
		Title:       title,
		Summary:     "summary-" + title,
		UserID:      ownerID,
		Script:      "<:0>",
		Permissions: pq.StringArray{},
		Visibility:  "public",
	}
}

func TestFlashRepository_CreateAndFindByID(t *testing.T) {
	repo := NewFlashRepository(testDB)
	user := insertTestUser(t, "u_fr_1", "flashuser1")
	defer cleanupUser(t, user.ID)

	f := newTestFlash("fl_cr_1", user.ID, "alpha")
	require.NoError(t, repo.Create(f))
	defer cleanupFlash(t, f.ID)

	got, err := repo.FindByID(f.ID)
	require.NoError(t, err)
	assert.Equal(t, "alpha", got.Title)
}

func TestFlashRepository_FindByID_NotFound(t *testing.T) {
	repo := NewFlashRepository(testDB)
	_, err := repo.FindByID("missing")
	assert.Error(t, err)
}

func TestFlashRepository_UpdateFields(t *testing.T) {
	repo := NewFlashRepository(testDB)
	user := insertTestUser(t, "u_fr_2", "flashuser2")
	defer cleanupUser(t, user.ID)

	f := newTestFlash("fl_cr_2", user.ID, "beta")
	require.NoError(t, repo.Create(f))
	defer cleanupFlash(t, f.ID)

	require.NoError(t, repo.UpdateFields(f.ID, map[string]any{
		"title":   "beta updated",
		"summary": "new summary",
		"script":  "<:1>",
	}))

	got, err := repo.FindByID(f.ID)
	require.NoError(t, err)
	assert.Equal(t, "beta updated", got.Title)
	assert.Equal(t, "new summary", got.Summary)
	assert.Equal(t, "<:1>", got.Script)
}

func TestFlashRepository_UpdateFields_NoOp(t *testing.T) {
	repo := NewFlashRepository(testDB)
	require.NoError(t, repo.UpdateFields("any", nil))
}

// UpdateFields で permissions に空 pq.StringArray を渡しても NOT NULL
// 制約違反を起こさないこと (#896)。core/flash.Service.Update が
// pq.StringArray() で wrap せずに plain []string を渡していた時、
// GORM が NULL に倒して 23502 を起こしていた regression guard。
func TestFlashRepository_UpdateFields_EmptyPermissions(t *testing.T) {
	repo := NewFlashRepository(testDB)
	user := insertTestUser(t, "u_fr_perms", "flashpermsuser")
	defer cleanupUser(t, user.ID)

	f := newTestFlash("fl_cr_perms", user.ID, "perms")
	f.Permissions = pq.StringArray{"read:account"}
	require.NoError(t, repo.Create(f))
	defer cleanupFlash(t, f.ID)

	// 空 permissions で update — pq.StringArray{} なら '{}' に serialize、
	// plain []string{} だと GORM が NULL に倒す drift があったので
	// pq.StringArray で wrap する pattern を guard する。
	require.NoError(t, repo.UpdateFields(f.ID, map[string]any{
		"permissions": pq.StringArray{},
	}))

	got, err := repo.FindByID(f.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.Permissions, "permissions は NULL にならず空配列で保存される")
	assert.Empty(t, got.Permissions)
}

func TestFlashRepository_Delete(t *testing.T) {
	repo := NewFlashRepository(testDB)
	user := insertTestUser(t, "u_fr_3", "flashuser3")
	defer cleanupUser(t, user.ID)

	f := newTestFlash("fl_cr_3", user.ID, "gamma")
	require.NoError(t, repo.Create(f))
	require.NoError(t, repo.Delete(f))
	_, err := repo.FindByID(f.ID)
	assert.Error(t, err)
}

func TestFlashRepository_IncrementCount(t *testing.T) {
	repo := NewFlashRepository(testDB)
	user := insertTestUser(t, "u_fr_4", "flashuser4")
	defer cleanupUser(t, user.ID)

	f := newTestFlash("fl_cr_4", user.ID, "delta")
	require.NoError(t, repo.Create(f))
	defer cleanupFlash(t, f.ID)

	require.NoError(t, repo.IncrementCount(f.ID, "likedCount", 4))
	got, err := repo.FindByID(f.ID)
	require.NoError(t, err)
	assert.Equal(t, 4, got.LikedCount)
}

func TestFlashRepository_ListByUser(t *testing.T) {
	repo := NewFlashRepository(testDB)
	user := insertTestUser(t, "u_fr_5", "flashuser5")
	defer cleanupUser(t, user.ID)

	for _, id := range []string{"fl_lst_1", "fl_lst_2", "fl_lst_3"} {
		f := newTestFlash(id, user.ID, id)
		require.NoError(t, repo.Create(f))
		defer cleanupFlash(t, f.ID)
	}

	rows, err := repo.ListByUser(user.ID, "", "", 10, 0)
	require.NoError(t, err)
	assert.Len(t, rows, 3)
}

func TestFlashRepository_ListByUser_LimitClamp(t *testing.T) {
	repo := NewFlashRepository(testDB)
	rows, err := repo.ListByUser("nobody", "", "", 9999, 0)
	require.NoError(t, err)
	assert.Empty(t, rows)
	rows, err = repo.ListByUser("nobody", "", "", -1, 0)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestFlashRepository_ListByUser_QueryError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := testDB.WithContext(ctx)
	repo := NewFlashRepository(db)
	_, err := repo.ListByUser("nobody", "", "", 10, 0)
	assert.Error(t, err)
}

func TestFlashRepository_ListFeatured(t *testing.T) {
	repo := NewFlashRepository(testDB)
	user := insertTestUser(t, "u_fr_6", "flashuser6")
	defer cleanupUser(t, user.ID)

	flashes := []*model.Flash{
		newTestFlash("fl_ft_1", user.ID, "ft1"),
		newTestFlash("fl_ft_2", user.ID, "ft2"),
	}
	flashes[0].LikedCount = 5
	flashes[1].LikedCount = 10
	for _, f := range flashes {
		require.NoError(t, repo.Create(f))
		defer cleanupFlash(t, f.ID)
	}
	rows, err := repo.ListFeatured("", "", 10, 0)
	require.NoError(t, err)
	var found []*model.Flash
	for _, f := range rows {
		if f.UserID == user.ID {
			found = append(found, f)
		}
	}
	require.Len(t, found, 2)
	assert.Equal(t, "fl_ft_2", found[0].ID)
	assert.Equal(t, "fl_ft_1", found[1].ID)
}

func TestFlashRepository_ListFeatured_LimitClamp(t *testing.T) {
	repo := NewFlashRepository(testDB)
	_, err := repo.ListFeatured("", "", 9999, 0)
	require.NoError(t, err)
	_, err = repo.ListFeatured("", "", -1, 0)
	require.NoError(t, err)
}

func TestFlashRepository_ListFeatured_QueryError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := testDB.WithContext(ctx)
	repo := NewFlashRepository(db)
	_, err := repo.ListFeatured("", "", 10, 0)
	assert.Error(t, err)
}

func TestFlashRepository_Search(t *testing.T) {
	repo := NewFlashRepository(testDB)
	user := insertTestUser(t, "u_fr_7", "flashuser7")
	defer cleanupUser(t, user.ID)

	titles := map[string]string{
		"fl_sr_1": "alpha calc tool",
		"fl_sr_2": "beta game",
		"fl_sr_3": "gamma calc",
	}
	for id, title := range titles {
		f := newTestFlash(id, user.ID, title)
		require.NoError(t, repo.Create(f))
		defer cleanupFlash(t, f.ID)
	}

	rows, err := repo.Search("calc", "", "", 10, 0)
	require.NoError(t, err)
	var found []*model.Flash
	for _, f := range rows {
		if f.UserID == user.ID {
			found = append(found, f)
		}
	}
	assert.Len(t, found, 2)
}

func TestFlashRepository_Search_LimitClamp(t *testing.T) {
	repo := NewFlashRepository(testDB)
	_, err := repo.Search("anything", "", "", 9999, 0)
	require.NoError(t, err)
	_, err = repo.Search("anything", "", "", -1, 0)
	require.NoError(t, err)
}

func TestFlashRepository_Search_QueryError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := testDB.WithContext(ctx)
	repo := NewFlashRepository(db)
	_, err := repo.Search("any", "", "", 10, 0)
	assert.Error(t, err)
}
