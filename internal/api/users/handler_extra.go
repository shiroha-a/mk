package users

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// SetAbuseRepo attaches the abuse report repository.
func (h *Handler) SetAbuseRepo(r repository.AbuseReportRepository) {
	h.abuseRepo = r
}

// Relation handles POST /api/users/relation.
// ユーザー間の関係 (フォロー/ブロック/ミュート等) を返す。
func (h *Handler) Relation(c echo.Context) error {
	_ = middleware.GetUser(c) // 認証チェック
	var req struct {
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	return c.JSON(http.StatusOK, map[string]any{
		"id":                             req.UserID,
		"isFollowing":                    false,
		"isFollowed":                     false,
		"hasPendingFollowRequestFromYou": false,
		"hasPendingFollowRequestToYou":   false,
		"isBlocking":                     false,
		"isBlocked":                      false,
		"isMuted":                        false,
		"isRenoteMuted":                  false,
	})
}

// ReportAbuse handles POST /api/users/report-abuse.
func (h *Handler) ReportAbuse(c echo.Context) error {
	me := middleware.GetUser(c)
	var req struct {
		UserID  string `json:"userId"`
		Comment string `json:"comment"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" || req.Comment == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId and comment are required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.abuseRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	report := &model.AbuseUserReport{
		ID:           h.idGen.Generate(time.Now()),
		TargetUserID: req.UserID,
		ReporterID:   me.ID,
		Comment:      req.Comment,
	}
	if err := h.abuseRepo.Create(report); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// Reactions handles POST /api/users/reactions.
// ユーザーのリアクション一覧。
func (h *Handler) Reactions(c echo.Context) error {
	var req struct {
		UserID string `json:"userId"`
		Limit  int    `json:"limit"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// 簡易版: 空配列を返す (リアクション履歴クエリは重いため後続で最適化)
	return c.JSON(http.StatusOK, []any{})
}

// FeaturedNotes handles POST /api/users/featured-notes.
func (h *Handler) FeaturedNotes(c echo.Context) error {
	var req struct {
		UserID string `json:"userId"`
		Limit  int    `json:"limit"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	notes, err := h.noteRepo.ListByUserID(req.UserID, "", "", req.Limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	viewer := middleware.GetUser(c)
	result := entity.PackNotes(c.Request().Context(), notes, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
	h.fieldRes.Apply(result, viewer)
	return c.JSON(http.StatusOK, result)
}

// SearchByUsernameAndHost handles POST /api/users/search-by-username-and-host.
func (h *Handler) SearchByUsernameAndHost(c echo.Context) error {
	var req struct {
		Username string  `json:"username"`
		Host     *string `json:"host"`
		Limit    int     `json:"limit"`
	}
	if err := c.Bind(&req); err != nil || req.Username == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "username is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	// host=nil → local user、host=*string → 当該 host のみで絞り込む
	// (#766 fix、upstream Misskey TS の同 endpoint と同じ semantics)。
	users, err := h.userService.SearchByUsernameAndHost(req.Username, req.Host, req.Limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	resolver := entity.NewInstanceResolver(h.instanceLookup(), users...)
	result := make([]entity.UserLite, 0, len(users))
	for _, u := range users {
		lite := entity.PackUserLite(u)
		resolver.FillUserLite(&lite)
		h.populateUserEmojis(u, &lite)
		result = append(result, lite)
	}
	return c.JSON(http.StatusOK, result)
}

// UpdateMemo handles POST /api/users/update-memo.
// memo が null / 空文字なら削除、それ以外なら upsert する。
func (h *Handler) UpdateMemo(c echo.Context) error {
	me := middleware.GetUser(c)
	var req struct {
		UserID string  `json:"userId"`
		Memo   *string `json:"memo"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	if h.memoRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}

	if req.Memo == nil || *req.Memo == "" {
		_ = h.memoRepo.Delete(me.ID, req.UserID)
	} else {
		memo := &model.UserMemo{
			ID:           h.idGen.Generate(time.Now()),
			UserID:       me.ID,
			TargetUserID: req.UserID,
			Memo:         *req.Memo,
		}
		if err := h.memoRepo.CreateOrUpdate(memo); err != nil {
			return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
	}
	return c.NoContent(http.StatusNoContent)
}
