package userlists

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Handler handles users/lists API endpoints.
type Handler struct {
	repo               repository.UserListRepository
	idGen              id.Generator
	rolePolicyProvider RolePolicyProvider
}

// RolePolicyProvider abstracts role-policy lookup for `userListLimit` /
// `userEachUserListsLimit` enforcement (#1029)。実装は core/role.Service。
type RolePolicyProvider interface {
	GetUserPolicies(userID string) map[string]any
}

// NewHandler creates a new userlists Handler.
func NewHandler(repo repository.UserListRepository, idGen id.Generator) *Handler {
	return &Handler{repo: repo, idGen: idGen}
}

// SetRolePolicyProvider wires a RolePolicyProvider so Create / Push enforce
// the userListLimit / userEachUserListsLimit role policies (#1029).
func (h *Handler) SetRolePolicyProvider(p RolePolicyProvider) {
	h.rolePolicyProvider = p
}

// List handles POST /api/users/lists/list.
//
// upstream Misskey TS と同じ packed shape ({id, createdAt, name, userIds,
// isPublic}) で返す (#871)。userIds は ListMembersByListIDs で 1 query batch
// fetch する (= per-list N+1 を回避、#876)。
func (h *Handler) List(c echo.Context) error {
	user := middleware.GetUser(c)
	lists, err := h.repo.ListByUser(user.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	listIDs := make([]string, 0, len(lists))
	for _, l := range lists {
		listIDs = append(listIDs, l.ID)
	}
	membersByList, err := h.repo.ListMembersByListIDs(listIDs)
	if err != nil {
		// repo error は best-effort で空 map にフォールバックして shape を保つ
		// (= memberIDs helper と同 pattern、entity.PackUserList が [] で
		// serialize する)。観測のため slog.Warn は残す。
		slog.Warn("users/lists/list: failed to batch fetch members", "userId", user.ID, "error", err)
		membersByList = map[string][]string{}
	}
	out := make([]entity.UserList, 0, len(lists))
	for _, l := range lists {
		out = append(out, entity.PackUserList(l, membersByList[l.ID], h.idGen))
	}
	return c.JSON(http.StatusOK, out)
}

// Create handles POST /api/users/lists/create.
//
// 新規作成時は member 0 件なので userIds は []。upstream と同じ packed
// shape を返す (#871)。
func (h *Handler) Create(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Name string `json:"name"`
	}
	if err := c.Bind(&req); err != nil || req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "name is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// userListLimit role policy gate (#1029)。
	if h.rolePolicyProvider != nil {
		if limit, ok := h.rolePolicyProvider.GetUserPolicies(user.ID)["userListLimit"].(int); ok && limit >= 0 {
			count, err := h.repo.CountByUser(user.ID)
			if err != nil {
				return apierr.JSONInternalError(c)
			}
			if count >= int64(limit) {
				return apierr.JSONTooManyUserLists(c)
			}
		}
	}
	list := &model.UserList{
		ID:     h.idGen.Generate(time.Now()),
		UserID: user.ID,
		Name:   req.Name,
	}
	if err := h.repo.Create(list); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, entity.PackUserList(list, nil, h.idGen))
}

// Show handles POST /api/users/lists/show.
//
// userIds を含む packed shape を返す (#871)。
func (h *Handler) Show(c echo.Context) error {
	var req struct {
		ListID string `json:"listId"`
	}
	if err := c.Bind(&req); err != nil || req.ListID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "listId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	list, err := h.repo.FindByID(req.ListID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_LIST", "No such list.", "7bc05c21-1d7a-41ae-88f1-66820f4dc686"))
	}
	return c.JSON(http.StatusOK, entity.PackUserList(list, h.memberIDs(list.ID), h.idGen))
}

// memberIDs returns the userId list of members of the given list.
// repo error は best-effort で nil を返し、entity.PackUserList 側で空配列
// として serialize される (= 部分結果でも shape は保つ、production では
// router で必ず repo wire 済)。transient error は運用で観測したいので
// slog.Warn で記録する (= blocking handler の fetchBlockeeMap と同 pattern)。
//
// 本 helper は `Show` (`/api/users/lists/show`) からのみ呼ばれる単発 1 query
// 経路。list endpoint (= `/api/users/lists/list`) は line 57 で
// `ListMembersByListIDs` 経由の 1 batch query を使うので N+1 にならない。
func (h *Handler) memberIDs(listID string) []string {
	members, err := h.repo.ListMembers(listID)
	if err != nil {
		slog.Warn("users/lists: failed to fetch members", "listId", listID, "error", err)
		return nil
	}
	if len(members) == 0 {
		return nil
	}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.UserID)
	}
	return ids
}

// Push handles POST /api/users/lists/push (add member).
func (h *Handler) Push(c echo.Context) error {
	var req struct {
		ListID string `json:"listId"`
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.ListID == "" || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "listId and userId are required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	list, err := h.repo.FindByID(req.ListID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_LIST", "No such list.", "2214501d-ac96-4049-b717-91e42272a711"))
	}
	// userEachUserListsLimit role policy gate (#1029)。list owner の policy
	// で評価する (upstream は owner = me 経路、mk-go も owner.UserID と me 一致
	// を確認する access gate は別途必要だが本 PR scope 外、limit 単独で gate)。
	if h.rolePolicyProvider != nil {
		if limit, ok := h.rolePolicyProvider.GetUserPolicies(list.UserID)["userEachUserListsLimit"].(int); ok && limit >= 0 {
			count, err := h.repo.CountMembers(list.ID)
			if err != nil {
				return apierr.JSONInternalError(c)
			}
			if count >= int64(limit) {
				return apierr.JSONTooManyUsers(c)
			}
		}
	}
	m := &model.UserListMembership{
		ID:         h.idGen.Generate(time.Now()),
		UserListID: req.ListID,
		UserID:     req.UserID,
	}
	if err := h.repo.AddMember(m); err != nil {
		// 既 member への push は TS 互換の ALREADY_ADDED (HTTP 400) で
		// 返す (#396)。UUID は misskey/.../users/lists/push.ts と一致。
		if errors.Is(err, repository.ErrUserListDuplicateMember) {
			return c.JSON(http.StatusBadRequest, apierr.Error("ALREADY_ADDED", "That user has already been added to that list.", "1de7c884-1595-49e9-857e-61f12f4d4fc5"))
		}
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// Pull handles POST /api/users/lists/pull (remove member).
func (h *Handler) Pull(c echo.Context) error {
	var req struct {
		ListID string `json:"listId"`
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.ListID == "" || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "listId and userId are required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if err := h.repo.RemoveMember(req.ListID, req.UserID); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// Delete handles POST /api/users/lists/delete.
func (h *Handler) Delete(c echo.Context) error {
	var req struct {
		ListID string `json:"listId"`
	}
	if err := c.Bind(&req); err != nil || req.ListID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "listId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if err := h.repo.Delete(req.ListID); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}
