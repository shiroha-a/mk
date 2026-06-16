package repository

import (
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSwSubscriptionRepository_Full(t *testing.T) {
	repo := NewSwSubscriptionRepository(testDB)
	user := insertTestUser(t, "u_sw_1", "swuser")
	defer cleanupUser(t, user.ID)

	// Create
	sub := &model.SwSubscription{
		ID:              "sw_1",
		UserID:          user.ID,
		Endpoint:        "https://push.example/test",
		Auth:            "authdata",
		PublicKey:       "pubkeydata",
		SendReadMessage: false,
	}
	require.NoError(t, repo.Create(sub))
	defer testDB.Exec(`DELETE FROM "sw_subscription" WHERE id = ?`, sub.ID)

	// FindByUserAndEndpoint
	found, err := repo.FindByUserAndEndpoint(user.ID, "https://push.example/test")
	require.NoError(t, err)
	assert.Equal(t, "sw_1", found.ID)
	assert.Equal(t, false, found.SendReadMessage)

	// FindByUserAndEndpoint - not found
	_, err = repo.FindByUserAndEndpoint(user.ID, "https://ghost")
	assert.Error(t, err)

	// FindByUserEndpointAuthKey - 4-tuple full match (#1775)
	found4, err := repo.FindByUserEndpointAuthKey(user.ID, "https://push.example/test", "authdata", "pubkeydata")
	require.NoError(t, err)
	assert.Equal(t, "sw_1", found4.ID)

	// FindByUserEndpointAuthKey - key rotation (auth/publickey mismatch) must NOT match
	_, err = repo.FindByUserEndpointAuthKey(user.ID, "https://push.example/test", "rotated-auth", "rotated-pk")
	assert.Error(t, err)

	// Update
	found.SendReadMessage = true
	require.NoError(t, repo.Update(found))
	updated, err := repo.FindByUserAndEndpoint(user.ID, "https://push.example/test")
	require.NoError(t, err)
	assert.Equal(t, true, updated.SendReadMessage)

	// DeleteByUserAndEndpoint
	require.NoError(t, repo.DeleteByUserAndEndpoint(user.ID, "https://push.example/test"))
	_, err = repo.FindByUserAndEndpoint(user.ID, "https://push.example/test")
	assert.Error(t, err)

	// Re-create for DeleteByEndpoint test
	sub2 := &model.SwSubscription{
		ID:        "sw_2",
		UserID:    user.ID,
		Endpoint:  "https://push.example/test2",
		Auth:      "a2",
		PublicKey: "pk2",
	}
	require.NoError(t, repo.Create(sub2))
	require.NoError(t, repo.DeleteByEndpoint("https://push.example/test2"))
	_, err = repo.FindByUserAndEndpoint(user.ID, "https://push.example/test2")
	assert.Error(t, err)
}
