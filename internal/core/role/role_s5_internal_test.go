package role

import (
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #2106 S5: time-limited assignment を持つ user の role cache entry は固定 TTL (5分)
// ではなく assignment の expiresAt で失効する (期限切れ role の延命防止)。
func TestGetUserRoles_CacheExpiryCappedAtAssignmentExpiry(t *testing.T) {
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := testutil.NewMockRoleAssignmentRepository(roleRepo)
	metaRepo := testutil.NewMockMetaRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := NewService(roleRepo, assignRepo, metaRepo, idGen)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Temp"}

	// time-limited (90s < 5min TTL) → entry は soon で失効する。
	soon := time.Now().Add(90 * time.Second)
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1", ExpiresAt: &soon, Role: roleRepo.Roles["r1"],
	}
	_, err := svc.GetUserRoles("user1")
	require.NoError(t, err)
	v, ok := svc.userRoleCache.Load("user1")
	require.True(t, ok)
	assert.False(t, v.(*roleCacheEntry).expiresAt.After(soon),
		"cache entry must expire by the assignment expiresAt, not the 5min TTL")

	// 期限なし assignment は従来通り full TTL (~5min)。
	assignRepo.Assignments["user2:r1"] = &model.RoleAssignment{
		ID: "a2", UserID: "user2", RoleID: "r1", Role: roleRepo.Roles["r1"],
	}
	_, err = svc.GetUserRoles("user2")
	require.NoError(t, err)
	v2, ok := svc.userRoleCache.Load("user2")
	require.True(t, ok)
	assert.True(t, v2.(*roleCacheEntry).expiresAt.After(time.Now().Add(4*time.Minute)),
		"non-expiring assignment keeps the full TTL")
}
