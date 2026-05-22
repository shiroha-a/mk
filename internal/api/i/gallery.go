package i

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// paginationFromRequest reads optional limit / offset / sinceId / untilId
// JSON fields, with Misskey-friendly defaults (limit=10, offset=0,
// limit clamped to 100). Cursor 指定時は frontend Paginator が untilId
// / sinceId を投げてくる経路 (#493)。
func paginationFromRequest(c echo.Context) (limit, offset int, sinceID, untilID string) {
	var req struct {
		Limit     *int   `json:"limit"`
		Offset    *int   `json:"offset"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
	}
	_ = c.Bind(&req)
	limit = 10
	if req.Limit != nil {
		limit = *req.Limit
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	if req.Offset != nil && *req.Offset > 0 {
		offset = *req.Offset
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID = id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	return limit, offset, sinceID, untilID
}

// GalleryLikes handles POST /api/i/gallery/likes.
// 自分が like した gallery_post を返却する。
//
// upstream Misskey TS は `{id, post: GalleryPost}` を返し、frontend は
// `item.post` で MkGalleryPostPreview を描画する。mk-go は元々 `postId`
// しか返しておらず frontend が空表示になっていた (#493 関連)。
// 同じ shape に揃えるため like 行ごとに対応する gallery_post を fetch
// して埋める。
func (h *Handler) GalleryLikes(c echo.Context) error {
	if h.galleryRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	u := middleware.GetUser(c)
	limit, offset, sinceID, untilID := paginationFromRequest(c)
	likes, err := h.galleryRepo.ListLikesByUser(u.ID, sinceID, untilID, limit, offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	postIDs := make([]string, 0, len(likes))
	seen := make(map[string]struct{}, len(likes))
	for _, l := range likes {
		if _, ok := seen[l.PostID]; ok {
			continue
		}
		seen[l.PostID] = struct{}{}
		postIDs = append(postIDs, l.PostID)
	}
	posts, err := h.galleryRepo.FindPostsByIDs(postIDs)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	postByID := make(map[string]*model.GalleryPost, len(posts))
	for _, p := range posts {
		postByID[p.ID] = p
	}
	out := make([]map[string]any, 0, len(likes))
	for _, l := range likes {
		// upstream Misskey TS は post が削除済の like row を Promise.all
		// + pack の null フィルタで弾く。frontend の MkGalleryPostPreview
		// も it.post 必須なので、ここで skip して整合させる。
		p, ok := postByID[l.PostID]
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"id":   l.ID,
			"post": packGalleryPost(p, h.idGen),
		})
	}
	return c.JSON(http.StatusOK, out)
}

// packGalleryPost は upstream の GalleryPost shape (createdAt / user 含む)
// に揃えてレスポンス用の map を返す。
func packGalleryPost(p *model.GalleryPost, idGen id.Generator) map[string]any {
	const tsFormat = "2006-01-02T15:04:05.000Z"
	createdAt := ""
	if idGen != nil {
		if t, err := idGen.ParseTime(p.ID); err == nil {
			createdAt = t.UTC().Format(tsFormat)
		}
	}
	resp := map[string]any{
		"id":          p.ID,
		"createdAt":   createdAt,
		"updatedAt":   p.UpdatedAt.UTC().Format(tsFormat),
		"title":       p.Title,
		"description": p.Description,
		"userId":      p.UserID,
		"fileIds":     []string(p.FileIDs),
		"files":       []any{},
		"isSensitive": p.IsSensitive,
		"likedCount":  p.LikedCount,
		"tags":        []string(p.Tags),
	}
	if p.User != nil {
		resp["user"] = entity.PackUserLite(p.User)
	}
	return resp
}

// GalleryPosts handles POST /api/i/gallery/posts.
// 自分が投稿した gallery_post を返却する。
func (h *Handler) GalleryPosts(c echo.Context) error {
	if h.galleryRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	u := middleware.GetUser(c)
	limit, offset, sinceID, untilID := paginationFromRequest(c)
	posts, err := h.galleryRepo.ListByUser(u.ID, sinceID, untilID, limit, offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(posts))
	for _, p := range posts {
		out = append(out, packGalleryPost(p, h.idGen))
	}
	return c.JSON(http.StatusOK, out)
}
