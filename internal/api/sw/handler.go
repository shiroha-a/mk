package sw

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Handler handles Service Worker push notification endpoints.
type Handler struct {
	repo     repository.SwSubscriptionRepository
	metaRepo repository.MetaRepository
	idGen    id.Generator
}

// NewHandler creates a new SW handler.
func NewHandler(repo repository.SwSubscriptionRepository, metaRepo repository.MetaRepository, idGen id.Generator) *Handler {
	return &Handler{repo: repo, metaRepo: metaRepo, idGen: idGen}
}

// Register handles POST /api/sw/register.
func (h *Handler) Register(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Endpoint        string `json:"endpoint"`
		Auth            string `json:"auth"`
		PublicKey       string `json:"publickey"`
		SendReadMessage bool   `json:"sendReadMessage"`
	}
	if err := c.Bind(&req); err != nil || req.Endpoint == "" || req.Auth == "" || req.PublicKey == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "endpoint, auth, and publickey are required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	var swPublicKey *string
	if m, err := h.metaRepo.Fetch(); err == nil {
		swPublicKey = m.SwPublicKey
	}

	// 既存サブスクリプションの確認
	existing, err := h.repo.FindByUserAndEndpoint(user.ID, req.Endpoint)
	if err == nil && existing != nil {
		return c.JSON(http.StatusOK, map[string]any{
			"state":           "already-subscribed",
			"key":             swPublicKey,
			"userId":          user.ID,
			"endpoint":        existing.Endpoint,
			"sendReadMessage": existing.SendReadMessage,
		})
	}

	// 新規登録
	sub := &model.SwSubscription{
		ID:              h.idGen.Generate(time.Now()),
		UserID:          user.ID,
		Endpoint:        req.Endpoint,
		Auth:            req.Auth,
		PublicKey:       req.PublicKey,
		SendReadMessage: req.SendReadMessage,
	}
	if err := h.repo.Create(sub); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	return c.JSON(http.StatusOK, map[string]any{
		"state":           "subscribed",
		"key":             swPublicKey,
		"userId":          user.ID,
		"endpoint":        req.Endpoint,
		"sendReadMessage": req.SendReadMessage,
	})
}

// ShowRegistration handles POST /api/sw/show-registration.
func (h *Handler) ShowRegistration(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if err := c.Bind(&req); err != nil || req.Endpoint == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "endpoint is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	sub, err := h.repo.FindByUserAndEndpoint(user.ID, req.Endpoint)
	if err != nil {
		// upstream Misskey TS は handler が null を return すると Endpoint base
		// が 204 No Content に変換する。mk-go は明示的に 200 + JSON null を
		// 返していたため drop-in 互換性が崩れていた (#918)。204 に揃える。
		return c.NoContent(http.StatusNoContent)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"userId":          sub.UserID,
		"endpoint":        sub.Endpoint,
		"sendReadMessage": sub.SendReadMessage,
	})
}

// UpdateRegistration handles POST /api/sw/update-registration.
func (h *Handler) UpdateRegistration(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Endpoint        string `json:"endpoint"`
		SendReadMessage *bool  `json:"sendReadMessage"`
	}
	if err := c.Bind(&req); err != nil || req.Endpoint == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "endpoint is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	sub, err := h.repo.FindByUserAndEndpoint(user.ID, req.Endpoint)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_REGISTRATION", "No such registration.", "b09d8066-8064-5613-efb6-0e963b21d012"))
	}

	if req.SendReadMessage != nil {
		sub.SendReadMessage = *req.SendReadMessage
	}
	if err := h.repo.Update(sub); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	return c.JSON(http.StatusOK, map[string]any{
		"userId":          sub.UserID,
		"endpoint":        sub.Endpoint,
		"sendReadMessage": sub.SendReadMessage,
	})
}

// Unregister handles POST /api/sw/unregister.
func (h *Handler) Unregister(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if err := c.Bind(&req); err != nil || req.Endpoint == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "endpoint is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	if user != nil {
		_ = h.repo.DeleteByUserAndEndpoint(user.ID, req.Endpoint)
	} else {
		_ = h.repo.DeleteByEndpoint(req.Endpoint)
	}

	return c.NoContent(http.StatusNoContent)
}
