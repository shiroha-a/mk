// Package pages provides /api/pages/* endpoints.
package pages

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	corepage "github.com/shiroha-a/mk/internal/core/page"
	coreuser "github.com/shiroha-a/mk/internal/core/user"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// MainStreamPublisher emits events to a user's `main` WebSocket channel.
// Used here to publish `pageEvent` when /api/page-push is called so the
// page owner's UI can react to custom events emitted from the page script.
// Accepted as an interface to avoid importing internal/stream here
// (循環依存回避)。
type MainStreamPublisher interface {
	PublishMainEvent(userID, eventType string, body any)
}

// UserBundleSource fetches a user + profile bundle. ShowByID is used to pack
// the caller for the `pageEvent` body emitted from /api/page-push, and
// ShowByUsername is used to resolve the upstream-compatible
// {username, name} shape on /api/pages/show (#955). Satisfied by
// *core/user.Service.
type UserBundleSource interface {
	ShowByID(id string) (*coreuser.UserWithProfile, error)
	ShowByUsername(username string, host *string) (*coreuser.UserWithProfile, error)
}

// Handler handles page-related API endpoints.
type Handler struct {
	svc                 *corepage.Service
	idGen               id.Generator
	mainStreamPublisher MainStreamPublisher
	userSource          UserBundleSource
}

// NewHandler creates a new pages Handler. idGen is required so that
// responses include createdAt derived from the aidx-encoded page ID.
func NewHandler(svc *corepage.Service, idGen id.Generator) *Handler {
	return &Handler{svc: svc, idGen: idGen}
}

// SetMainStreamPublisher attaches a publisher used to emit `pageEvent` on
// /api/page-push. Optional — nil disables emit.
func (h *Handler) SetMainStreamPublisher(p MainStreamPublisher) {
	h.mainStreamPublisher = p
}

// SetUserSource attaches a user bundle fetcher used to pack the caller in
// `pageEvent` bodies. Optional — without it page-push returns 204 without
// emitting (caller info missing).
func (h *Handler) SetUserSource(s UserBundleSource) {
	h.userSource = s
}

// CreateRequest is the request body for pages/create. Content / Variables
// are stored verbatim into jsonb columns; we don't interpret them.
type CreateRequest struct {
	Title               string               `json:"title"`
	Name                string               `json:"name"`
	Summary             *string              `json:"summary"`
	Content             json.RawMessage      `json:"content"`
	Variables           json.RawMessage      `json:"variables"`
	Script              string               `json:"script"`
	EyeCatchingImageID  *string              `json:"eyeCatchingImageId"`
	Font                string               `json:"font"`
	AlignCenter         bool                 `json:"alignCenter"`
	HideTitleWhenPinned bool                 `json:"hideTitleWhenPinned"`
	Visibility          model.PageVisibility `json:"visibility"`
}

// Create handles POST /api/pages/create.
func (h *Handler) Create(c echo.Context) error {
	user := middleware.GetUser(c)
	var req CreateRequest
	if err := c.Bind(&req); err != nil || req.Title == "" || req.Name == "" {
		return apierr.JSONInvalidParam(c)
	}
	p, err := h.svc.Create(corepage.CreateInput{
		OwnerID:             user.ID,
		Title:               req.Title,
		Name:                req.Name,
		Summary:             req.Summary,
		AlignCenter:         req.AlignCenter,
		HideTitleWhenPinned: req.HideTitleWhenPinned,
		Font:                req.Font,
		EyeCatchingImageID:  req.EyeCatchingImageID,
		Content:             req.Content,
		Variables:           req.Variables,
		Script:              req.Script,
		Visibility:          req.Visibility,
	})
	if err != nil {
		if errors.Is(err, corepage.ErrPageNameConflict) {
			return nameConflict(c)
		}
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.pageToMap(p))
}

// ShowRequest is the request body for pages/show. upstream Misskey TS の
// paramDef は anyOf で 3 形を accept する: {pageId} / {userId, name} /
// {username, name} (#955)。frontend (page.vue) は 3 つ目の {username, name}
// を投げてくるので、本 struct も同 shape を受ける。
type ShowRequest struct {
	PageID   string `json:"pageId"`
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

// Show handles POST /api/pages/show.
func (h *Handler) Show(c echo.Context) error {
	user := middleware.GetUser(c)
	var req ShowRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	requesterID := ""
	if user != nil {
		requesterID = user.ID
	}
	var (
		p   *model.Page
		err error
	)
	switch {
	case req.PageID != "":
		p, err = h.svc.Show(requesterID, req.PageID)
	case req.UserID != "" && req.Name != "":
		p, err = h.svc.ShowByName(requesterID, req.UserID, req.Name)
	case req.Username != "" && req.Name != "":
		// {username, name} 経路 (#955): username で local user を引いて
		// その user.id + name で page を引く。upstream は host=NULL の
		// local user 縛り (= remote の page は連合経路で配信されない)。
		if h.userSource == nil {
			return apierr.JSONInternalError(c)
		}
		bundle, ulerr := h.userSource.ShowByUsername(req.Username, nil)
		if ulerr != nil || bundle == nil || bundle.User == nil {
			return notFound(c)
		}
		p, err = h.svc.ShowByName(requesterID, bundle.User.ID, req.Name)
	default:
		return apierr.JSONInvalidParam(c)
	}
	if err != nil {
		if errors.Is(err, corepage.ErrAccessDenied) {
			return apierr.JSONAccessDenied(c)
		}
		return notFound(c)
	}
	return c.JSON(http.StatusOK, h.pageToMap(p))
}

// UpdateRequest is the request body for pages/update.
type UpdateRequest struct {
	PageID              string                `json:"pageId"`
	Title               *string               `json:"title"`
	Name                *string               `json:"name"`
	Summary             *string               `json:"summary"`
	Content             json.RawMessage       `json:"content"`
	Variables           json.RawMessage       `json:"variables"`
	Script              *string               `json:"script"`
	EyeCatchingImageID  *string               `json:"eyeCatchingImageId"`
	Font                *string               `json:"font"`
	AlignCenter         *bool                 `json:"alignCenter"`
	HideTitleWhenPinned *bool                 `json:"hideTitleWhenPinned"`
	Visibility          *model.PageVisibility `json:"visibility"`
}

// Update handles POST /api/pages/update.
func (h *Handler) Update(c echo.Context) error {
	user := middleware.GetUser(c)
	var req UpdateRequest
	if err := c.Bind(&req); err != nil || req.PageID == "" {
		return apierr.JSONInvalidParam(c)
	}
	in := corepage.UpdateInput{
		Title:               req.Title,
		Name:                req.Name,
		AlignCenter:         req.AlignCenter,
		HideTitleWhenPinned: req.HideTitleWhenPinned,
		Font:                req.Font,
		Script:              req.Script,
		Visibility:          req.Visibility,
	}
	if req.Summary != nil {
		s := req.Summary
		in.Summary = &s
	}
	if req.EyeCatchingImageID != nil {
		s := req.EyeCatchingImageID
		in.EyeCatchingImageID = &s
	}
	if req.Content != nil {
		in.Content = req.Content
	}
	if req.Variables != nil {
		in.Variables = req.Variables
	}
	p, err := h.svc.Update(user.ID, req.PageID, in)
	if err != nil {
		switch {
		case errors.Is(err, corepage.ErrPageNotFound):
			return notFound(c)
		case errors.Is(err, corepage.ErrAccessDenied):
			return apierr.JSONAccessDenied(c)
		case errors.Is(err, corepage.ErrPageNameRequired),
			errors.Is(err, corepage.ErrPageTitleRequired):
			return apierr.JSONInvalidParam(c)
		case errors.Is(err, corepage.ErrPageNameConflict):
			return nameConflict(c)
		}
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.pageToMap(p))
}

// DeleteRequest is the request body for pages/delete.
type DeleteRequest struct {
	PageID string `json:"pageId"`
}

// Delete handles POST /api/pages/delete.
func (h *Handler) Delete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req DeleteRequest
	if err := c.Bind(&req); err != nil || req.PageID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.svc.Delete(user.ID, req.PageID); err != nil {
		switch {
		case errors.Is(err, corepage.ErrPageNotFound):
			return notFound(c)
		case errors.Is(err, corepage.ErrAccessDenied):
			return apierr.JSONAccessDenied(c)
		}
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// MyRequest is the request body for i/pages (own pages list).
type MyRequest struct {
	Limit   int    `json:"limit"`
	Offset  int    `json:"offset"`
	SinceID string `json:"sinceId"`
	UntilID string `json:"untilId"`
}

// My handles POST /api/i/pages.
//
// frontend Paginator (cursor mode) は untilId / sinceId を forward する (#493)。
func (h *Handler) My(c echo.Context) error {
	user := middleware.GetUser(c)
	var req MyRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	rows, err := h.svc.ListByUser(user.ID, req.SinceID, req.UntilID, req.Limit, req.Offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.pagesToList(rows))
}

// FeaturedRequest is the request body for pages/featured.
type FeaturedRequest struct {
	Limit   int    `json:"limit"`
	Offset  int    `json:"offset"`
	SinceID string `json:"sinceId"`
	UntilID string `json:"untilId"`
}

// Featured handles POST /api/pages/featured.
func (h *Handler) Featured(c echo.Context) error {
	var req FeaturedRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	rows, err := h.svc.Featured(req.SinceID, req.UntilID, req.Limit, req.Offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.pagesToList(rows))
}

// LikeRequest is the request body for pages/like and pages/unlike.
type LikeRequest struct {
	PageID string `json:"pageId"`
}

// Like handles POST /api/pages/like.
func (h *Handler) Like(c echo.Context) error {
	user := middleware.GetUser(c)
	var req LikeRequest
	if err := c.Bind(&req); err != nil || req.PageID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.svc.Like(user.ID, req.PageID); err != nil {
		switch {
		case errors.Is(err, corepage.ErrPageNotFound):
			return notFound(c)
		case errors.Is(err, corepage.ErrAccessDenied):
			return apierr.JSONAccessDenied(c)
		case errors.Is(err, corepage.ErrAlreadyLiked):
			return alreadyLiked(c)
		}
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// PagePushRequest is the request body for /api/page-push.
type PagePushRequest struct {
	PageID string          `json:"pageId"`
	Event  string          `json:"event"`
	Var    json.RawMessage `json:"var"`
}

// PagePush handles POST /api/page-push. Triggered by page scripts to emit
// a custom event to the page owner's `main` WebSocket channel. Body shape
// mirrors the Misskey original endpoints/page-push.ts
// (pageId / event / var / userId / user).
func (h *Handler) PagePush(c echo.Context) error {
	caller := middleware.GetUser(c)
	var req PagePushRequest
	if err := c.Bind(&req); err != nil || req.PageID == "" || req.Event == "" {
		return apierr.JSONInvalidParam(c)
	}
	// TS本家はfindOneByのみで可視性チェックをしないのでFindByIDを
	// 使って合わせる。見つからなければ404に丸め、存在するIDだけに
	// emitする。
	p, err := h.svc.FindByID(req.PageID)
	if err != nil {
		return notFound(c)
	}
	if h.mainStreamPublisher == nil || h.userSource == nil {
		// 配線未完了ならemitせず204を返す(API互換のため)。
		return c.NoContent(http.StatusNoContent)
	}
	bundle, err := h.userSource.ShowByID(caller.ID)
	if err != nil || bundle == nil || bundle.User == nil {
		// bundle.Userは現実装のShowByIDでは常にnon-nilだが、interface
		// 経由で将来実装が変わる可能性に備えて防御的にcheck。
		return c.NoContent(http.StatusNoContent)
	}
	body := map[string]any{
		"pageId": p.ID,
		"event":  req.Event,
		"var":    rawJSONBytes(req.Var),
		"userId": caller.ID,
		"user":   entity.PackUserDetailed(bundle.User, bundle.Profile, h.idGen),
	}
	h.mainStreamPublisher.PublishMainEvent(p.UserID, "pageEvent", body)
	return c.NoContent(http.StatusNoContent)
}

// rawJSONBytes returns b as json.RawMessage, or nil when empty. Empty
// bytes are mapped to nil so that json.Marshal serialises the field as
// null — matching the TS original's behaviour of sending `undefined`
// when the optional `var` parameter is omitted.
func rawJSONBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return json.RawMessage(b)
}

// Unlike handles POST /api/pages/unlike.
func (h *Handler) Unlike(c echo.Context) error {
	user := middleware.GetUser(c)
	var req LikeRequest
	if err := c.Bind(&req); err != nil || req.PageID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.svc.Unlike(user.ID, req.PageID); err != nil {
		switch {
		case errors.Is(err, corepage.ErrPageNotFound):
			return notFound(c)
		case errors.Is(err, corepage.ErrNotLiked):
			return notLiked(c)
		}
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// pagesToList packs a slice of pages using h.idGen so every element gets a
// consistent createdAt alongside the other fields.
func (h *Handler) pagesToList(rows []*model.Page) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		out = append(out, h.pageToMap(p))
	}
	return out
}

// pageToMap delegates to entity.PackPage so the JSON shape (timestamp
// format, field set) stays in sync between /api/pages/* and other places
// that embed a page (e.g. pinnedPage on /api/i and /api/users/show).
func (h *Handler) pageToMap(p *model.Page) map[string]any {
	return entity.PackPage(p, h.idGen)
}

func notFound(c echo.Context) error {
	return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_PAGE", "No such page.", "222120c0-3ead-4528-811b-b96f233388d7"))
}

func nameConflict(c echo.Context) error {
	return c.JSON(http.StatusBadRequest, apierr.Error("NAME_ALREADY_EXISTS", "The page name is already in use.", "4650348e-301c-499a-83c9-6aa988c66bc1"))
}

func alreadyLiked(c echo.Context) error {
	return c.JSON(http.StatusBadRequest, apierr.Error("ALREADY_LIKED", "You already liked that page.", "cc98a8a2-0dc3-4123-b198-62c71df18ed3"))
}

func notLiked(c echo.Context) error {
	return c.JSON(http.StatusBadRequest, apierr.Error("NOT_LIKED", "You have not liked that page.", "f5e586b0-ce93-4050-b0e3-7f31af5259ee"))
}
