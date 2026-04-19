package repository

import (
	"context"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cleanupAnnouncement(t *testing.T, id string) {
	t.Helper()
	testDB.Exec(`DELETE FROM "announcement_read" WHERE "announcementId" = ?`, id)
	testDB.Exec(`DELETE FROM "announcement" WHERE id = ?`, id)
}

func TestAnnouncementRepository_CRUD(t *testing.T) {
	repo := NewAnnouncementRepository(testDB)

	a := &model.Announcement{ID: "ann_1", Title: "Test", Text: "Hello", Icon: "info", Display: "normal", IsActive: true}
	require.NoError(t, repo.Create(a))
	defer cleanupAnnouncement(t, a.ID)

	found, err := repo.FindByID(a.ID)
	require.NoError(t, err)
	assert.Equal(t, "Test", found.Title)

	// List (active)
	items, err := repo.List(true, 10, 0, "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, items)

	// List (all)
	items, err = repo.List(false, 10, 0, "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, items)

	// UpdateFields
	require.NoError(t, repo.UpdateFields(a.ID, map[string]any{"title": "Updated"}))
	found, _ = repo.FindByID(a.ID)
	assert.Equal(t, "Updated", found.Title)

	// Delete
	require.NoError(t, repo.Delete(a.ID))
	_, err = repo.FindByID(a.ID)
	assert.Error(t, err)
}

func TestAnnouncementRepository_FindByID_NotFound(t *testing.T) {
	repo := NewAnnouncementRepository(testDB)
	_, err := repo.FindByID("ghost")
	assert.Error(t, err)
}

func TestAnnouncementRepository_List_Pagination(t *testing.T) {
	repo := NewAnnouncementRepository(testDB)
	a1 := &model.Announcement{ID: "ann_p1", Title: "A", Text: "t", Icon: "info", Display: "normal", IsActive: true}
	a2 := &model.Announcement{ID: "ann_p2", Title: "B", Text: "t", Icon: "info", Display: "normal", IsActive: true}
	require.NoError(t, repo.Create(a1))
	require.NoError(t, repo.Create(a2))
	defer cleanupAnnouncement(t, a1.ID)
	defer cleanupAnnouncement(t, a2.ID)

	items, err := repo.List(true, 1, 0, "", "")
	require.NoError(t, err)
	assert.Len(t, items, 1)

	items, err = repo.List(true, 10, 1, "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, items)

	// default limit
	items, err = repo.List(true, 0, 0, "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, items)

	// limit cap
	items, err = repo.List(true, 999, 0, "", "")
	require.NoError(t, err)
	assert.LessOrEqual(t, len(items), 100)
}

func TestAnnouncementRepository_List_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewAnnouncementRepository(testDB.WithContext(ctx))
	_, err := repo.List(true, 10, 0, "", "")
	assert.Error(t, err)
}

func TestAnnouncementRepository_ListGlobal_ExcludesPerUser(t *testing.T) {
	repo := NewAnnouncementRepository(testDB)
	otherID := "u_lg_other"
	seedUser(t, otherID) // FK制約があるので先にuser行を作る
	other := otherID
	global := &model.Announcement{ID: "ann_lg_g", Title: "G", Text: "t", Icon: "info", Display: "normal", IsActive: true}
	targeted := &model.Announcement{ID: "ann_lg_t", Title: "T", Text: "t", Icon: "info", Display: "normal", IsActive: true, UserID: &other}
	// defer は Create の前に登録して、Create 途中失敗時も確実にcleanupする。
	defer cleanupAnnouncement(t, global.ID)
	defer cleanupAnnouncement(t, targeted.ID)
	require.NoError(t, repo.Create(global))
	require.NoError(t, repo.Create(targeted))

	items, err := repo.ListGlobal(true, 100, 0, "", "")
	require.NoError(t, err)
	ids := make(map[string]bool, len(items))
	for _, a := range items {
		ids[a.ID] = true
	}
	assert.True(t, ids[global.ID])
	assert.False(t, ids[targeted.ID])
}

func TestAnnouncementRepository_ListGlobal_Pagination(t *testing.T) {
	repo := NewAnnouncementRepository(testDB)
	a1 := &model.Announcement{ID: "ann_gp1", Title: "A", Text: "t", Icon: "info", Display: "normal", IsActive: true}
	a2 := &model.Announcement{ID: "ann_gp2", Title: "B", Text: "t", Icon: "info", Display: "normal", IsActive: true}
	defer cleanupAnnouncement(t, a1.ID)
	defer cleanupAnnouncement(t, a2.ID)
	require.NoError(t, repo.Create(a1))
	require.NoError(t, repo.Create(a2))

	items, err := repo.ListGlobal(true, 1, 0, "", "")
	require.NoError(t, err)
	assert.Len(t, items, 1)
	// default limitとcap経路
	items, err = repo.ListGlobal(true, 0, 0, "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, items)
	items, err = repo.ListGlobal(true, 999, 0, "", "")
	require.NoError(t, err)
	assert.LessOrEqual(t, len(items), 100)
	// offset
	items, err = repo.ListGlobal(true, 10, 1, "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, items)
}

func TestAnnouncementRepository_ListGlobal_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewAnnouncementRepository(testDB.WithContext(ctx))
	_, err := repo.ListGlobal(true, 10, 0, "", "")
	assert.Error(t, err)
}

func TestAnnouncementRepository_ListForUser_IncludesGlobalAndOwnTargeted(t *testing.T) {
	repo := NewAnnouncementRepository(testDB)
	me := "u_lfu_me"
	other := "u_lfu_oth"
	seedUser(t, me)
	seedUser(t, other)
	global := &model.Announcement{ID: "ann_lfu_g", Title: "G", Text: "t", Icon: "info", Display: "normal", IsActive: true}
	mine := &model.Announcement{ID: "ann_lfu_m", Title: "Mine", Text: "t", Icon: "info", Display: "normal", IsActive: true, UserID: &me}
	others := &model.Announcement{ID: "ann_lfu_o", Title: "Other", Text: "t", Icon: "info", Display: "normal", IsActive: true, UserID: &other}
	defer cleanupAnnouncement(t, global.ID)
	defer cleanupAnnouncement(t, mine.ID)
	defer cleanupAnnouncement(t, others.ID)
	require.NoError(t, repo.Create(global))
	require.NoError(t, repo.Create(mine))
	require.NoError(t, repo.Create(others))

	items, err := repo.ListForUser(me, true, 100, 0, "", "")
	require.NoError(t, err)
	ids := make(map[string]bool, len(items))
	for _, a := range items {
		ids[a.ID] = true
	}
	assert.True(t, ids[global.ID])
	assert.True(t, ids[mine.ID])
	assert.False(t, ids[others.ID])
}

func TestAnnouncementRepository_ListForUser_Pagination(t *testing.T) {
	repo := NewAnnouncementRepository(testDB)
	a1 := &model.Announcement{ID: "ann_fp1", Title: "A", Text: "t", Icon: "info", Display: "normal", IsActive: true}
	a2 := &model.Announcement{ID: "ann_fp2", Title: "B", Text: "t", Icon: "info", Display: "normal", IsActive: true}
	defer cleanupAnnouncement(t, a1.ID)
	defer cleanupAnnouncement(t, a2.ID)
	require.NoError(t, repo.Create(a1))
	require.NoError(t, repo.Create(a2))

	items, err := repo.ListForUser("u", true, 1, 0, "", "")
	require.NoError(t, err)
	assert.Len(t, items, 1)
	items, err = repo.ListForUser("u", true, 0, 0, "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, items)
	items, err = repo.ListForUser("u", true, 999, 0, "", "")
	require.NoError(t, err)
	assert.LessOrEqual(t, len(items), 100)
	items, err = repo.ListForUser("u", true, 10, 1, "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, items)
}

func TestAnnouncementRepository_ListForUser_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewAnnouncementRepository(testDB.WithContext(ctx))
	_, err := repo.ListForUser("u", true, 10, 0, "", "")
	assert.Error(t, err)
}

// OR の括弧漏れで isActive filter が効かなくなる GORM gotcha
// (SQL優先順位 AND > OR) の回帰防止テスト。
func TestAnnouncementRepository_ListForUser_ActiveOnlyAppliesToGlobal(t *testing.T) {
	repo := NewAnnouncementRepository(testDB)
	// model.Announcement の gorm tag に default:true があり、Goのbool zero
	// valueをGORMが「未指定」と見なしてDB defaultを適用してしまうため、
	// 一度IsActive=trueで作ってからUpdateFieldsでfalseに落とす。
	inactive := &model.Announcement{ID: "ann_ra_g", Title: "G-off", Text: "t", Icon: "info", Display: "normal", IsActive: true}
	defer cleanupAnnouncement(t, inactive.ID)
	require.NoError(t, repo.Create(inactive))
	require.NoError(t, repo.UpdateFields(inactive.ID, map[string]any{"isActive": false}))

	// activeOnly=true の場合、inactive な global announcement も除外される
	// はず(括弧無しだと isActive filter が global に適用されず漏れる)。
	items, err := repo.ListForUser("some_user", true, 100, 0, "", "")
	require.NoError(t, err)
	for _, a := range items {
		assert.NotEqual(t, inactive.ID, a.ID)
	}
}

func TestAnnouncementRepository_ReadManagement(t *testing.T) {
	repo := NewAnnouncementRepository(testDB)
	createTestUser(t, "ann_reader")

	a := &model.Announcement{ID: "ann_r1", Title: "R", Text: "t", Icon: "info", Display: "normal", IsActive: true}
	require.NoError(t, repo.Create(a))
	defer cleanupAnnouncement(t, a.ID)

	// Not read yet
	read, err := repo.IsRead("ann_reader", a.ID)
	require.NoError(t, err)
	assert.False(t, read)

	// Unread list
	unread, err := repo.UnreadForUser("ann_reader")
	require.NoError(t, err)
	assert.NotEmpty(t, unread)

	// Mark read
	require.NoError(t, repo.MarkRead(&model.AnnouncementRead{ID: "ar_1", UserID: "ann_reader", AnnouncementID: a.ID}))

	read, err = repo.IsRead("ann_reader", a.ID)
	require.NoError(t, err)
	assert.True(t, read)

	unread, err = repo.UnreadForUser("ann_reader")
	require.NoError(t, err)
	assert.Empty(t, unread)
}

func TestAnnouncementRepository_UnreadForUser_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewAnnouncementRepository(testDB.WithContext(ctx))
	_, err := repo.UnreadForUser("x")
	assert.Error(t, err)
}

func TestAnnouncementRepository_IsRead_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewAnnouncementRepository(testDB.WithContext(ctx))
	_, err := repo.IsRead("x", "y")
	assert.Error(t, err)
}
