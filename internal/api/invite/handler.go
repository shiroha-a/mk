// Package invite implements the user-scope /api/invite/* endpoints used by
// users who hold the canInvite role policy to issue / list / delete / inspect
// invite codes. Admin-scope invite endpoints live under
// internal/api/admin/invite.go and have separate semantics.
package invite

import (
	cryptorand "crypto/rand"
	"io"
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

// RolePolicyProvider returns the merged role policy map for a user. The
// handler consumes inviteLimit / inviteLimitCycle / inviteExpirationTime from
// the returned map to enforce upstream Misskey TS semantics (#1029 PR-2).
// Nil provider disables the gate (= legacy unrestricted behaviour kept for
// test fixtures).
type RolePolicyProvider interface {
	GetUserPolicies(userID string) map[string]any
}

// Handler exposes /api/invite/* handler methods.
type Handler struct {
	repo               repository.RegistrationTicketRepository
	idGen              id.Generator
	rolePolicyProvider RolePolicyProvider
}

// NewHandler returns a Handler wired with the given repository + id generator.
func NewHandler(repo repository.RegistrationTicketRepository, idGen id.Generator) *Handler {
	return &Handler{repo: repo, idGen: idGen}
}

// SetRolePolicyProvider wires the policy provider used by Create / Limit.
// Nil keeps the gate disabled (= test fixture / wire 未配線).
func (h *Handler) SetRolePolicyProvider(p RolePolicyProvider) {
	h.rolePolicyProvider = p
}

// Create implements POST /api/invite/create. Upstream Misskey TS
// `invite/create.ts` semantics: enforce inviteLimit / inviteLimitCycle as a
// rolling time-window count, set expiresAt from inviteExpirationTime (0 / unset
// = null = 無期限), then persist the ticket.
func (h *Handler) Create(c echo.Context) error {
	user := middleware.GetUser(c)
	now := time.Now()

	policies := h.policies(user.ID)
	if invLimit, ok := policies["inviteLimit"].(int); ok && invLimit > 0 {
		cycleMin := 0
		if v, ok2 := policies["inviteLimitCycle"].(int); ok2 {
			cycleMin = v
		}
		sinceID := h.idGen.Generate(now.Add(-time.Duration(cycleMin) * time.Minute))
		count, err := h.repo.CountByCreatorSince(user.ID, sinceID)
		if err != nil {
			return apierr.JSONInternalError(c)
		}
		if count >= int64(invLimit) {
			return apierr.JSONExceededLimitOfCreateInviteCode(c)
		}
	}

	// inviteExpirationTime (= 分単位) が truthy なら expiresAt を計算する。
	// 0 / 未設定なら null (= 無期限) を維持し、upstream の falsy skip と一致する。
	var expiresAt *time.Time
	if v, ok := policies["inviteExpirationTime"].(int); ok && v > 0 {
		exp := now.Add(time.Duration(v) * time.Minute)
		expiresAt = &exp
	}

	ticket := &model.RegistrationTicket{
		ID:          h.idGen.Generate(now),
		Code:        generateInviteCode(),
		CreatedByID: &user.ID,
		ExpiresAt:   expiresAt,
	}
	if err := h.repo.Create(ticket); err != nil {
		return apierr.JSONInternalError(c)
	}

	resp := map[string]any{
		"id":        ticket.ID,
		"code":      ticket.Code,
		"expiresAt": ticket.ExpiresAt,
		"createdBy": entity.PackUserLite(user),
		"usedBy":    nil,
		"usedAt":    nil,
		"used":      false,
	}
	if t, err := h.idGen.ParseTime(ticket.ID); err == nil {
		resp["createdAt"] = t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return c.JSON(http.StatusOK, resp)
}

// List implements POST /api/invite/list.
func (h *Handler) List(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Limit   int    `json:"limit"`
		SinceID string `json:"sinceId"`
		UntilID string `json:"untilId"`
	}
	_ = c.Bind(&req)

	tickets, err := h.repo.ListByCreator(user.ID, req.SinceID, req.UntilID, req.Limit)
	if err != nil {
		// upstream は handler 内で DB error を listing 404 相当ではなく empty
		// array で返している。caller (frontend invite 管理画面) は array length
		// で空チェックするので 500 を返すと UI が壊れる、空 array で graceful
		// degrade する方が drop-in 互換性が高い。
		return c.JSON(http.StatusOK, []any{})
	}
	out := make([]map[string]any, 0, len(tickets))
	for _, t := range tickets {
		entry := map[string]any{
			"id":        t.ID,
			"code":      t.Code,
			"expiresAt": t.ExpiresAt,
			"createdBy": nil,
			"usedBy":    nil,
			"usedAt":    t.UsedAt,
			"used":      t.UsedByID != nil,
		}
		if ts, err := h.idGen.ParseTime(t.ID); err == nil {
			entry["createdAt"] = ts.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		out = append(out, entry)
	}
	return c.JSON(http.StatusOK, out)
}

// Delete implements POST /api/invite/delete. Only the original creator can
// delete their own ticket (= upstream `invite/delete.ts` access check)。
func (h *Handler) Delete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		InviteID string `json:"inviteId"`
	}
	if err := c.Bind(&req); err != nil || req.InviteID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "inviteId is required.", apierr.UUIDInvalidParam))
	}
	ticket, err := h.repo.FindByID(req.InviteID)
	if err != nil {
		// 既に削除済 / 存在しない場合は upstream と同様に 204 を返す (= idempotent)。
		return c.NoContent(http.StatusNoContent)
	}
	if ticket.CreatedByID == nil || *ticket.CreatedByID != user.ID {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", apierr.UUIDAccessDenied))
	}
	if err := h.repo.Delete(req.InviteID); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// Limit implements POST /api/invite/limit. Returns the remaining slots within
// the current inviteLimitCycle window. Upstream `invite/limit.ts` returns
// `remaining: null` when policies.inviteLimit is falsy (= 無制限), otherwise
// `max(0, inviteLimit - count)`.
func (h *Handler) Limit(c echo.Context) error {
	user := middleware.GetUser(c)
	policies := h.policies(user.ID)
	invLimit, hasLimit := policies["inviteLimit"].(int)
	if !hasLimit || invLimit <= 0 {
		return c.JSON(http.StatusOK, map[string]any{"remaining": nil})
	}
	cycleMin := 0
	if v, ok := policies["inviteLimitCycle"].(int); ok {
		cycleMin = v
	}
	sinceID := h.idGen.Generate(time.Now().Add(-time.Duration(cycleMin) * time.Minute))
	count, err := h.repo.CountByCreatorSince(user.ID, sinceID)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	remaining := int64(invLimit) - count
	if remaining < 0 {
		remaining = 0
	}
	return c.JSON(http.StatusOK, map[string]any{"remaining": remaining})
}

// policies returns the merged role policy map for userID, or an empty map
// when no provider is wired so callers can safely type-assert keys.
func (h *Handler) policies(userID string) map[string]any {
	if h.rolePolicyProvider == nil {
		return map[string]any{}
	}
	if p := h.rolePolicyProvider.GetUserPolicies(userID); p != nil {
		return p
	}
	return map[string]any{}
}

// generateInviteCode creates an 8-character random invite code over
// `[a-z0-9]`. mk-go 既存実装 (旧 router.go 内 helper) と同 charset / 長さで、
// 移植後も既発行コードの長さ / 文字種分布を維持する。upstream Misskey TS
// は別 charset (`23456789ABCDEFGHJKLMNPQRSTUVWXYZ` + timestamp suffix) を
// 使うので drop-in 互換の観点では別 issue で揃える余地あり。
func generateInviteCode() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	if _, err := io.ReadFull(cryptorand.Reader, b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	for i := range b {
		b[i] = chars[b[i]%byte(len(chars))]
	}
	return string(b)
}
