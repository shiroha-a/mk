package entity

import (
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
)

func TestPackRoleAssignmentLookup(t *testing.T) {
	r := &model.Role{ID: "r1", IsPublic: true, CanEditMembersByModerator: true}
	assert.Equal(t, map[string]any{
		"assigned":  false,
		"expiresAt": nil,
		"role": map[string]any{
			"id":                        "r1",
			"isPublic":                  true,
			"canEditMembersByModerator": true,
		},
	}, PackRoleAssignmentLookup(nil, r))

	expiresAt := time.Date(2026, 1, 2, 12, 4, 5, 123000000, time.FixedZone("JST", 9*60*60))
	got := PackRoleAssignmentLookup(&model.RoleAssignment{ExpiresAt: &expiresAt}, r)
	assert.Equal(t, true, got["assigned"])
	assert.Equal(t, "2026-01-02T03:04:05.123Z", got["expiresAt"])
}
