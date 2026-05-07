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
	repo  repository.UserListRepository
	idGen id.Generator
}

// NewHandler creates a new userlists Handler.
func NewHandler(repo repository.UserListRepository, idGen id.Generator) *Handler {
	return &Handler{repo: repo, idGen: idGen}
}

// List handles POST /api/users/lists/list.
//
// upstream Misskey TS と同じ packed shape ({id, createdAt, name, userIds,
// isPublic}) で返す (#871)。userIds は ListMembers で fetch して埋める。
// 旧 mk-go は model.UserList の生 JSON を返しており createdAt / userIds が
// 欠落していた。
func (h *Handler) List(c echo.Context) error {
	user := middleware.GetUser(c)
	lists, err := h.repo.ListByUser(user.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	out := make([]entity.UserList, 0, len(lists))
	for _, l := range lists {
		out = append(out, entity.PackUserList(l, h.memberIDs(l.ID), h.idGen))
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
// NOTE (#876): users/lists/list は N list 持つ user に対し本 helper を N 回
// 呼んでおり N+1 query になる。perf 改善は ListMembersByListIDs batch fetch
// で別 issue (#876) として追跡。
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
	if _, err := h.repo.FindByID(req.ListID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_LIST", "No such list.", "7bc05c21-1d7a-41ae-88f1-66820f4dc686"))
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
