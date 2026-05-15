package repository

import (
	"context"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cleanupUserKeypairExtra(t *testing.T, userID string) {
	t.Helper()
	testDB.Exec(`DELETE FROM "user_keypair_extra" WHERE "userId" = ?`, userID)
}

func TestUserKeypairExtraRepository_UpsertAndFind(t *testing.T) {
	repo := NewUserKeypairExtraRepository(testDB)
	user := insertTestUser(t, "u_kpx_1", "kpx1")
	defer cleanupUser(t, user.ID)
	defer cleanupUserKeypairExtra(t, user.ID)

	k := &model.UserKeypairExtra{
		UserID:            user.ID,
		Ed25519PublicKey:  "PUBKEY_v1",
		Ed25519PrivateKey: "PRIVKEY_v1",
	}
	require.NoError(t, repo.Upsert(k))

	got, err := repo.FindByUserID(user.ID)
	require.NoError(t, err)
	assert.Equal(t, "PUBKEY_v1", got.Ed25519PublicKey)
	assert.Equal(t, "PRIVKEY_v1", got.Ed25519PrivateKey)

	// Upsert は同一 userId で更新できる
	k.Ed25519PublicKey = "PUBKEY_v2"
	k.Ed25519PrivateKey = "PRIVKEY_v2"
	require.NoError(t, repo.Upsert(k))

	got, err = repo.FindByUserID(user.ID)
	require.NoError(t, err)
	assert.Equal(t, "PUBKEY_v2", got.Ed25519PublicKey)
	assert.Equal(t, "PRIVKEY_v2", got.Ed25519PrivateKey)
}

func TestUserKeypairExtraRepository_Delete(t *testing.T) {
	repo := NewUserKeypairExtraRepository(testDB)
	user := insertTestUser(t, "u_kpx_2", "kpx2")
	defer cleanupUser(t, user.ID)
	defer cleanupUserKeypairExtra(t, user.ID)

	require.NoError(t, repo.Upsert(&model.UserKeypairExtra{
		UserID:            user.ID,
		Ed25519PublicKey:  "PUB",
		Ed25519PrivateKey: "PRIV",
	}))
	require.NoError(t, repo.Delete(user.ID))

	_, err := repo.FindByUserID(user.ID)
	assert.Error(t, err)
}

func TestUserKeypairExtraRepository_FindByUserID_NotFound(t *testing.T) {
	repo := NewUserKeypairExtraRepository(testDB)
	_, err := repo.FindByUserID("u_kpx_nonexistent")
	assert.Error(t, err)
}

func TestUserKeypairExtraRepository_QueryErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewUserKeypairExtraRepository(testDB.WithContext(ctx))

	err := repo.Upsert(&model.UserKeypairExtra{UserID: "x", Ed25519PublicKey: "p", Ed25519PrivateKey: "k"})
	assert.Error(t, err)
	_, err = repo.FindByUserID("x")
	assert.Error(t, err)
	err = repo.Delete("x")
	assert.Error(t, err)
}
