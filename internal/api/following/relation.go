package following

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Invalidate handles POST /api/following/invalidate.
//
// 認証ユーザーが自分のフォロワーのうち userId 指定の相手を強制的に外す。
// Misskey 本家互換で RequireAuth レベル (moderator 不要) のため、嫌がらせ
// フォロワーに対して本人がいつでも解除できる。
func (h *Handler) Invalidate(c echo.Context) error {
	me := middleware.GetUser(c)
	var req struct {
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.followingService.Unfollow(req.UserID, me.ID); err != nil {
		if errors.Is(err, corefollowing.ErrNotFollowing) {
			return c.JSON(http.StatusBadRequest, apierr.Error("NOT_FOLLOWING", "You are not followed by that user.", "918faac3-074f-41ae-9c43-ed5d2946770d"))
		}
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// UpdateFollow handles POST /api/following/update.
//
// notify ("normal" / "none") / withReplies を呼び出しユーザーの指定 followee
// に対して更新する。
//
// upstream Misskey TS は `ps.notify === 'none' ? null : ps.notify` の三項で
// "none" を SQL NULL に変換する仕様 (#17385)。これがないと `following/list`
// の `notification=true` filter (= `notify IS NOT NULL`) が機能不全になる
// (mk-go では旧来 "none" 文字列がそのまま入って null 扱いされなかった)。
func (h *Handler) UpdateFollow(c echo.Context) error {
	me := middleware.GetUser(c)
	var req struct {
		UserID      string  `json:"userId"`
		Notify      *string `json:"notify"`
		WithReplies *bool   `json:"withReplies"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return apierr.JSONInvalidParam(c)
	}
	fields := map[string]any{}
	if req.Notify != nil {
		fields["notify"] = normalizeNotify(*req.Notify)
	}
	if req.WithReplies != nil {
		fields["withReplies"] = *req.WithReplies
	}
	if err := h.followingService.UpdateRelation(me.ID, req.UserID, fields); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// normalizeNotify converts the upstream `notify` enum ("normal" / "none") to
// the value stored in the DB column. "none" → nil (SQL NULL) so the
// `following/list` notification=true filter (= `notify IS NOT NULL`) correctly
// excludes users with notifications turned off (upstream Misskey TS 2026.5.2
// #17385 互換)。"normal" / other → そのまま文字列として保存。
func normalizeNotify(v string) any {
	if v == "none" {
		return nil
	}
	return v
}

// UpdateFollowAll handles POST /api/following/update-all.
//
// 呼び出しユーザーの全フォロー関係に対して同じ partial update を適用する。
// 旅行期間中 notify を一括で無効にする等の運用に使う。
func (h *Handler) UpdateFollowAll(c echo.Context) error {
	me := middleware.GetUser(c)
	var req struct {
		Notify      *string `json:"notify"`
		WithReplies *bool   `json:"withReplies"`
	}
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	fields := map[string]any{}
	if req.Notify != nil {
		// upstream #17385 互換: notify="none" は SQL NULL に正規化する。
		fields["notify"] = normalizeNotify(*req.Notify)
	}
	if req.WithReplies != nil {
		fields["withReplies"] = *req.WithReplies
	}
	if len(fields) == 0 {
		return c.NoContent(http.StatusNoContent)
	}
	if err := h.followingService.UpdateAllByFollower(me.ID, fields); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}
