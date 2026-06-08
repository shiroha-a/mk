package repository

import (
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserIPRepository_DeleteOlderThan(t *testing.T) {
	repo := NewUserIPRepository(testDB)
	u := insertTestUser(t, "ip_del", "ipdel")
	defer cleanupUser(t, u.ID)
	defer testDB.Exec(`DELETE FROM "user_ip" WHERE "userId" = ?`, u.ID)

	// 100 日前の行を直接 backdate insert。
	old := &model.UserIP{UserID: u.ID, IP: "1.1.1.1", CreatedAt: time.Now().Add(-100 * 24 * time.Hour)}
	require.NoError(t, testDB.Create(old).Error)
	// 直近の行 (now)。
	require.NoError(t, repo.Upsert(u.ID, "2.2.2.2"))

	n, err := repo.DeleteOlderThan(time.Now().Add(-90 * 24 * time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, n, "90 日より古い行だけ削除")
	rows, err := repo.ListByUser(u.ID, 10)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "直近の行のみ残る")
}

func TestUserIPRepository_Upsert(t *testing.T) {
	repo := NewUserIPRepository(testDB)
	u := insertTestUser(t, "ip_u1", "ipu1")
	defer cleanupUser(t, u.ID)

	require.NoError(t, repo.Upsert(u.ID, "192.168.1.1"))
	require.NoError(t, repo.Upsert(u.ID, "10.0.0.1"))

	ips, err := repo.ListByUser(u.ID, 10)
	require.NoError(t, err)
	assert.Len(t, ips, 2)

	// upsert same IP updates timestamp
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, repo.Upsert(u.ID, "192.168.1.1"))
	ips, _ = repo.ListByUser(u.ID, 10)
	assert.Len(t, ips, 2)

	// cleanup
	testDB.Exec(`DELETE FROM "user_ip" WHERE "userId" = ?`, u.ID)
}

func TestUserIPRepository_ListByUser_Empty(t *testing.T) {
	repo := NewUserIPRepository(testDB)
	ips, err := repo.ListByUser("ghost-ip-user", 10)
	require.NoError(t, err)
	assert.Empty(t, ips)
}

func TestUserIPRepository_ListByUser_DefaultLimit(t *testing.T) {
	repo := NewUserIPRepository(testDB)
	ips, err := repo.ListByUser("ghost", 0)
	require.NoError(t, err)
	assert.Empty(t, ips)
}
