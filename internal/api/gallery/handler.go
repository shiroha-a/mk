package gallery

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/pagination"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"gorm.io/gorm"
)

// Handler handles gallery-related API endpoints.
type Handler struct {
	db    *gorm.DB
	idGen id.Generator
}

// NewHandler creates a new gallery Handler.
func NewHandler(db *gorm.DB, idGen id.Generator) *Handler {
	return &Handler{db: db, idGen: idGen}
}

// Featured handles POST /api/gallery/featured.
func (h *Handler) Featured(c echo.Context) error {
	var posts []*model.GalleryPost
	if err := h.db.Preload("User").Order("\"likedCount\" DESC").Limit(10).Find(&posts).Error; err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, h.packMany(posts))
}

// Popular handles POST /api/gallery/popular.
func (h *Handler) Popular(c echo.Context) error {
	return h.Featured(c) // 同じロジック
}

// Posts handles POST /api/gallery/posts.
//
// frontend Paginator (cursor mode) は untilId / sinceId を投げてくる。
// id 範囲フィルタ + paginationOrder 同等の ASC/DESC を直接組み立てる。
func (h *Handler) Posts(c echo.Context) error {
	var req struct {
		Limit     int    `json:"limit"`
		Offset    int    `json:"offset"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	q := h.db.Preload("User")
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" && untilID == "" {
		q = q.Order("id ASC")
	} else {
		q = q.Order("id DESC")
	}
	q = q.Limit(req.Limit)
	if sinceID == "" && untilID == "" && req.Offset > 0 {
		q = q.Offset(req.Offset)
	}
	var posts []*model.GalleryPost
	if err := q.Find(&posts).Error; err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, h.packMany(posts))
}

// PostsCreate handles POST /api/gallery/posts/create.
func (h *Handler) PostsCreate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Title       string   `json:"title"`
		Description *string  `json:"description"`
		FileIDs     []string `json:"fileIds"`
		IsSensitive bool     `json:"isSensitive"`
	}
	if err := c.Bind(&req); err != nil || req.Title == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "title is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if req.FileIDs == nil {
		req.FileIDs = []string{}
	}
	now := time.Now()
	post := &model.GalleryPost{
		ID:          h.idGen.Generate(now),
		UpdatedAt:   now,
		Title:       req.Title,
		Description: req.Description,
		UserID:      user.ID,
		FileIDs:     req.FileIDs,
		IsSensitive: req.IsSensitive,
		Tags:        []string{},
	}
	if err := h.db.Create(post).Error; err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	post.User = user
	return c.JSON(http.StatusOK, h.packOne(post))
}

// PostsShow handles POST /api/gallery/posts/show.
func (h *Handler) PostsShow(c echo.Context) error {
	var req struct {
		PostID string `json:"postId"`
	}
	if err := c.Bind(&req); err != nil || req.PostID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "postId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	var post model.GalleryPost
	if err := h.db.Preload("User").Where("id = ?", req.PostID).First(&post).Error; err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_POST", "No such post.", "1137bf14-c5b0-4604-85bb-5b5371b1cd45"))
	}
	return c.JSON(http.StatusOK, h.packOne(&post))
}

// PostsDelete handles POST /api/gallery/posts/delete.
func (h *Handler) PostsDelete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		PostID string `json:"postId"`
	}
	if err := c.Bind(&req); err != nil || req.PostID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "postId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	result := h.db.Where("id = ? AND \"userId\" = ?", req.PostID, user.ID).Delete(&model.GalleryPost{})
	if result.RowsAffected == 0 {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_POST", "No such post.", "ae52f367-4bd7-4ecd-afc6-5672fff427f5"))
	}
	return c.NoContent(http.StatusNoContent)
}

// PostsUpdate handles POST /api/gallery/posts/update.
func (h *Handler) PostsUpdate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		PostID      string  `json:"postId"`
		Title       *string `json:"title"`
		Description *string `json:"description"`
	}
	if err := c.Bind(&req); err != nil || req.PostID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "postId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	fields := map[string]any{}
	if req.Title != nil {
		fields["title"] = *req.Title
	}
	if req.Description != nil {
		fields["description"] = *req.Description
	}
	result := h.db.Model(&model.GalleryPost{}).Where("id = ? AND \"userId\" = ?", req.PostID, user.ID).Updates(fields)
	if result.RowsAffected == 0 {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_POST", "No such post.", "1137bf14-c27b-46e0-86d3-0cbbf8b0aca5"))
	}
	return c.NoContent(http.StatusNoContent)
}

// PostsLike handles POST /api/gallery/posts/like.
func (h *Handler) PostsLike(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		PostID string `json:"postId"`
	}
	if err := c.Bind(&req); err != nil || req.PostID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "postId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	like := &model.GalleryLike{
		ID:     h.idGen.Generate(time.Now()),
		UserID: user.ID,
		PostID: req.PostID,
	}
	if err := h.db.Create(like).Error; err != nil {
		return c.JSON(http.StatusConflict, apierr.Error("ALREADY_LIKED", "Already liked.", "40e9ed56-a59c-473a-bf3f-f289c54fb5a7"))
	}
	h.db.Model(&model.GalleryPost{}).Where("id = ?", req.PostID).UpdateColumn("\"likedCount\"", gorm.Expr("\"likedCount\" + 1"))
	return c.NoContent(http.StatusNoContent)
}

// PostsUnlike handles POST /api/gallery/posts/unlike.
func (h *Handler) PostsUnlike(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		PostID string `json:"postId"`
	}
	if err := c.Bind(&req); err != nil || req.PostID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "postId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	h.db.Where("\"userId\" = ? AND \"postId\" = ?", user.ID, req.PostID).Delete(&model.GalleryLike{})
	h.db.Model(&model.GalleryPost{}).Where("id = ?", req.PostID).UpdateColumn("\"likedCount\"", gorm.Expr("GREATEST(\"likedCount\" - 1, 0)"))
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) packMany(posts []*model.GalleryPost) []map[string]any {
	result := make([]map[string]any, 0, len(posts))
	for _, p := range posts {
		result = append(result, h.packOne(p))
	}
	return result
}

func (h *Handler) packOne(p *model.GalleryPost) map[string]any {
	createdAt := ""
	if t, err := h.idGen.ParseTime(p.ID); err == nil {
		createdAt = t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	resp := map[string]any{
		"id":          p.ID,
		"createdAt":   createdAt,
		"updatedAt":   p.UpdatedAt,
		"title":       p.Title,
		"description": p.Description,
		"userId":      p.UserID,
		"fileIds":     p.FileIDs,
		"files":       []any{},
		"isSensitive": p.IsSensitive,
		"likedCount":  p.LikedCount,
		"tags":        p.Tags,
	}
	if p.User != nil {
		resp["user"] = entity.PackUserLite(p.User)
	}
	return resp
}
