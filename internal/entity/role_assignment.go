package entity

import (
	"time"

	"github.com/shiroha-a/mk/internal/model"
)

// PackRoleAssignmentLookup builds the exact-assignment response shared by the
// self and administrator endpoints.
func PackRoleAssignmentLookup(assignment *model.RoleAssignment, r *model.Role) map[string]any {
	assigned := assignment != nil
	var expiresAt *time.Time
	if assigned {
		expiresAt = assignment.ExpiresAt
	}
	return map[string]any{
		"assigned":  assigned,
		"expiresAt": ISOMillisPtr(expiresAt),
		"role": map[string]any{
			"id":                        r.ID,
			"isPublic":                  r.IsPublic,
			"canEditMembersByModerator": r.CanEditMembersByModerator,
		},
	}
}
