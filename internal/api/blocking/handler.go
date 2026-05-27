// Package blocking provides /api/blocking/* endpoints.
package blocking

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/pagination"
	coreblocking "github.com/shiroha-a/mk/internal/core/blocking"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Handler handles blocking-related API endpoints.
type Handler struct {
	svc      *coreblocking.Service
	userRepo repository.UserRepository
	idGen    id.Generator
}

// NewHandler creates a new blocking Handler.
// userRepo / idGen are required by blocking/list to embed the
// `blockee` user object that the upstream Misskey frontend renders.
func NewHandler(svc *coreblocking.Service, userRepo repository.UserRepository, idGen id.Generator) *Handler {
	return &Handler{svc: svc, userRepo: userRepo, idGen: idGen}
}

// PairRequest is the request body for blocking/create and blocking/delete.
type PairRequest struct {
	UserID string `json:"userId"`
}

// Create handles POST /api/blocking/create.
//
// upstream Misskey TS と同じく成功時は 200 + 対象 blockee の UserDetailed
// (= isBlocking=true 反映) を返す。frontend (vue) が即時 redraw するため
// 必要 (#870)。userRepo が wire されない test stub では legacy 互換で
// 204 No Content にフォールバック。
func (h *Handler) Create(c echo.Context) error {
	user := middleware.GetUser(c)
	var req PairRequest
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return apierr.JSONInvalidParam(c)
	}

	if _, err := h.svc.Block(user.ID, req.UserID); err != nil {
		switch {
		case errors.Is(err, coreblocking.ErrSelfBlock):
			return c.JSON(http.StatusBadRequest, apierr.Error("BLOCKEE_IS_YOURSELF", "You cannot block yourself.", "88b19138-f28d-42c0-8499-6a31bbd0fdc6"))
		case errors.Is(err, coreblocking.ErrBlockeeNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "7cc4f851-e2f1-4621-9633-ec9e1d00c01e"))
		case errors.Is(err, coreblocking.ErrAlreadyBlocking):
			return c.JSON(http.StatusBadRequest, apierr.Error("ALREADY_BLOCKING", "You are already blocking that user.", "787fed64-acb9-464a-82eb-afbd745b9614"))
		}
		return apierr.JSONInternalError(c)
	}
	return h.respondPackedUser(c, req.UserID)
}

// Delete handles POST /api/blocking/delete.
//
// 同じく upstream TS は 200 + UserDetailed (isBlocking=false 反映) を返す。
// userRepo 未 wire 時は 204 No Content フォールバック (#870)。
func (h *Handler) Delete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req PairRequest
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.svc.Unblock(user.ID, req.UserID); err != nil {
		switch {
		case errors.Is(err, coreblocking.ErrSelfBlock):
			return c.JSON(http.StatusBadRequest, apierr.Error("BLOCKEE_IS_YOURSELF", "You cannot unblock yourself.", "06f6fac6-524b-473c-a354-e97a40ae6eac"))
		case errors.Is(err, coreblocking.ErrNotBlocking):
			return c.JSON(http.StatusBadRequest, apierr.Error("NOT_BLOCKING", "You are not blocking that user.", "291b2efa-60c6-45c0-9f6a-045c8f9b02cd"))
		}
		return apierr.JSONInternalError(c)
	}
	return h.respondPackedUser(c, req.UserID)
}

// respondPackedUser fetches the target user + profile via userRepo and writes
// a 200 + UserDetailed response. userRepo 未 wire (= 既存 test stub) なら
// legacy 204 を返す互換 path に落ちる (production では router で必ず wire
// されるので影響なし、#870)。lookup 失敗時も best-effort 204 にフォール
// バックして block / unblock の副作用 (= state 反映) を尊重する。
func (h *Handler) respondPackedUser(c echo.Context, userID string) error {
	if h.userRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	target, err := h.userRepo.FindByID(userID)
	if err != nil || target == nil {
		return c.NoContent(http.StatusNoContent)
	}
	profile, _ := h.userRepo.FindProfileByUserID(userID)
	return c.JSON(http.StatusOK, entity.PackUserDetailed(target, profile, h.idGen))
}

// ListRequest is the request body for blocking/list.
type ListRequest struct {
	Limit     int    `json:"limit"`
	Offset    int    `json:"offset"`
	SinceID   string `json:"sinceId"`
	UntilID   string `json:"untilId"`
	SinceDate *int64 `json:"sinceDate"`
	UntilDate *int64 `json:"untilDate"`
}

// List handles POST /api/blocking/list.
//
// frontend Paginator (cursor mode) は untilId / sinceId を forward する。
// 本 endpoint は #493 で cursor 対応に切り替えた。
func (h *Handler) List(c echo.Context) error {
	user := middleware.GetUser(c)
	var req ListRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	req.Limit = pagination.ClampLimit(req.Limit, 30, 100)
	// sinceDate / untilDate を aidx prefix に正規化 (#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	rows, err := h.svc.List(user.ID, sinceID, untilID, req.Limit, req.Offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	// upstream frontend が item.blockee で MkUserCardMini を描画するので
	// batch fetch で N+1 を回避しつつ user object を embed する。
	blockeeMap := h.fetchBlockeeMap(rows)
	const tsFormat = "2006-01-02T15:04:05.000Z"
	out := make([]map[string]any, 0, len(rows))
	for _, b := range rows {
		createdAt := ""
		if h.idGen != nil {
			if t, err := h.idGen.ParseTime(b.ID); err == nil {
				createdAt = t.UTC().Format(tsFormat)
			}
		}
		entry := map[string]any{
			"id":        b.ID,
			"createdAt": createdAt,
			"blockeeId": b.BlockeeID,
		}
		if blockee, ok := blockeeMap[b.BlockeeID]; ok {
			entry["blockee"] = blockee
		}
		out = append(out, entry)
	}
	return c.JSON(http.StatusOK, out)
}

// fetchBlockeeMap batches user + profile lookups for the blockee IDs in
// rows so the response build loop performs zero per-row DB queries.
func (h *Handler) fetchBlockeeMap(rows []*model.Blocking) map[string]entity.UserDetailed {
	if h.userRepo == nil || len(rows) == 0 {
		return nil
	}
	ids := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, b := range rows {
		if _, ok := seen[b.BlockeeID]; ok {
			continue
		}
		seen[b.BlockeeID] = struct{}{}
		ids = append(ids, b.BlockeeID)
	}
	users, err := h.userRepo.FindManyByIDs(ids)
	if err != nil {
		return nil
	}
	profiles, profErr := h.userRepo.FindProfilesByUserIDs(ids)
	if profErr != nil {
		// 部分結果を返す graceful degradation だが、log は残しておく。
		slog.Warn("blocking/list: failed to fetch profiles", "error", profErr)
	}
	profileByUser := make(map[string]*model.UserProfile, len(profiles))
	for _, p := range profiles {
		profileByUser[p.UserID] = p
	}
	out := make(map[string]entity.UserDetailed, len(users))
	for _, u := range users {
		out[u.ID] = entity.PackUserDetailed(u, profileByUser[u.ID], h.idGen)
	}
	return out
}
