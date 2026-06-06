package federation

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/pagination"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
)

// hostPageRequest is the common request body for the three per-host listing
// endpoints (federation/followers, federation/following, federation/users).
type hostPageRequest struct {
	Host   string `json:"host"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// Followers handles POST /api/federation/followers.
//
// 指定リモートホストに属するユーザーが、このインスタンスのローカルユーザーを
// フォローしている関係の一覧を返す。Misskey 本家互換。
func (h *Handler) Followers(c echo.Context) error {
	req, ok := parseHostPage(c)
	if !ok {
		return apierr.JSONInvalidParam(c)
	}
	if h.followingRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	rows, err := h.followingRepo.ListFollowersByHost(req.Host, req.Limit, req.Offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, packFollowings(rows))
}

// Following handles POST /api/federation/following.
//
// このインスタンスのローカルユーザーが、指定リモートホストに属するユーザーを
// フォローしている関係の一覧を返す。
func (h *Handler) Following(c echo.Context) error {
	req, ok := parseHostPage(c)
	if !ok {
		return apierr.JSONInvalidParam(c)
	}
	if h.followingRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	rows, err := h.followingRepo.ListFollowingByHost(req.Host, req.Limit, req.Offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, packFollowings(rows))
}

// Users handles POST /api/federation/users.
//
// 指定リモートホストに属するユーザーの一覧を返す。
func (h *Handler) Users(c echo.Context) error {
	var req hostPageRequest
	if err := c.Bind(&req); err != nil || req.Host == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.userRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	limit := pagination.ClampLimit(req.Limit, 10, 100)
	users, err := h.userRepo.ListUsers(model.UserListFilter{
		Origin:   "remote",
		Hostname: req.Host,
		Limit:    limit,
		Offset:   req.Offset,
	})
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		out = append(out, map[string]any{
			"id":       u.ID,
			"username": u.Username,
			"host":     u.Host,
			"name":     u.Name,
			// リモートユーザーのアバターは proxy 経由にする (issue #1529)。
			// IdenticonURL が avatar mode proxy 化と identicon fallback を担う。
			"avatarUrl": entity.IdenticonURL(u),
		})
	}
	return c.JSON(http.StatusOK, out)
}

// parseHostPage parses + validates the common per-host page request. Returns
// (req, true) on success; on failure the caller must write the 400 response
// itself to avoid double-writing the body.
func parseHostPage(c echo.Context) (hostPageRequest, bool) {
	var req hostPageRequest
	if err := c.Bind(&req); err != nil || req.Host == "" {
		return req, false
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	return req, true
}

func packFollowings(rows []*model.Following) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, f := range rows {
		out = append(out, map[string]any{
			"id":           f.ID,
			"followerId":   f.FollowerID,
			"followeeId":   f.FolloweeID,
			"followerHost": f.FollowerHost,
			"followeeHost": f.FolloweeHost,
			"withReplies":  f.WithReplies,
		})
	}
	return out
}
