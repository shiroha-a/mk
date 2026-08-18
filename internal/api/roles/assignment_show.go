package roles

import (
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	corerole "github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// AssignmentShow handles POST /api/roles/assignment-show.
func (h *Handler) AssignmentShow(c echo.Context) error {
	var req struct {
		RoleID string `json:"roleId"`
	}
	if err := c.Bind(&req); err != nil || req.RoleID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "roleId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	viewerID := ""
	if viewer := middleware.GetUser(c); viewer != nil {
		viewerID = viewer.ID
	}
	// private roleの存在を未付与userへ明かさないため、self側はroleより先に
	// assignmentを確認する。admin側のrole-first lookupとは意図的に非対称。
	assignment, err := h.roleService.GetUserAssign(viewerID, req.RoleID, time.Now())
	if err != nil {
		return apierr.JSONInternalError(c)
	}

	r, err := h.roleService.Show(req.RoleID)
	if err != nil {
		if errors.Is(err, corerole.ErrRoleNotFound) {
			return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_ROLE", "No such role.", "4a3b21e8-9c54-4d7a-b1e2-8f6c9d0e5a71"))
		}
		return apierr.JSONInternalError(c)
	}

	assigned := assignment != nil
	if !r.IsPublic && !assigned {
		return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_ROLE", "No such role.", "4a3b21e8-9c54-4d7a-b1e2-8f6c9d0e5a71"))
	}
	return c.JSON(http.StatusOK, entity.PackRoleAssignmentLookup(assignment, r))
}
