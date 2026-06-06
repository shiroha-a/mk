package gallery

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
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
	return c.JSON(http.StatusOK, h.packMany(posts, middleware.GetUser(c)))
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
	return c.JSON(http.StatusOK, h.packMany(posts, middleware.GetUser(c)))
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
	// upstream paramDef: fileIds required, minItems:1。所有する drive file のみ
	// を残し、1 件も残らなければ reject (upstream は filter→0 で throw)。
	if len(req.FileIDs) == 0 || len(req.FileIDs) > galleryMaxFiles {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "fileIds must contain 1-32 items.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	owned, err := h.ownedFileIDs(req.FileIDs, user.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	if len(owned) == 0 {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "No valid files.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	now := time.Now()
	post := &model.GalleryPost{
		ID:          h.idGen.Generate(now),
		UpdatedAt:   now,
		Title:       req.Title,
		Description: req.Description,
		UserID:      user.ID,
		FileIDs:     pq.StringArray(owned),
		IsSensitive: req.IsSensitive,
		Tags:        []string{},
	}
	if err := h.db.Create(post).Error; err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	post.User = user
	return c.JSON(http.StatusOK, h.packOne(post, user))
}

// galleryMaxFiles mirrors upstream paramDef fileIds.maxItems.
const galleryMaxFiles = 32

// ownedFileIDs returns the subset of fileIDs that belong to userID, preserving
// input order and deduplicating (upstream fileIds.uniqueItems). A DB error is
// propagated (not swallowed) so the handler returns 500 rather than masking it
// as a "no valid files" 400.
func (h *Handler) ownedFileIDs(fileIDs []string, userID string) ([]string, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	var rows []*model.DriveFile
	if err := h.db.Select("id").Where("id IN ? AND \"userId\" = ?", fileIDs, userID).Find(&rows).Error; err != nil {
		return nil, err
	}
	ownedSet := make(map[string]bool, len(rows))
	for _, f := range rows {
		ownedSet[f.ID] = true
	}
	seen := make(map[string]bool, len(fileIDs))
	out := make([]string, 0, len(fileIDs))
	for _, fid := range fileIDs {
		if ownedSet[fid] && !seen[fid] {
			seen[fid] = true
			out = append(out, fid)
		}
	}
	return out, nil
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
	return c.JSON(http.StatusOK, h.packOne(&post, middleware.GetUser(c)))
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
		PostID      string    `json:"postId"`
		Title       *string   `json:"title"`
		Description *string   `json:"description"`
		FileIDs     *[]string `json:"fileIds"`
		// upstream paramDef は isSensitive: default false。省略時も false で
		// 上書きされる (TypeORM は undefined だけ skip)。bool で受け常に書く。
		IsSensitive bool `json:"isSensitive"`
	}
	if err := c.Bind(&req); err != nil || req.PostID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "postId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// title は minLength:1 (空文字は 400)。
	if req.Title != nil && *req.Title == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "title must not be empty.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// upstream は updatedAt と isSensitive(default false) を必ず更新する。fields が
	// 空にならないので RowsAffected==0 (= 行不在) を NO_SUCH_POST 判定に使える。
	fields := map[string]any{"updatedAt": time.Now(), "isSensitive": req.IsSensitive}
	if req.Title != nil {
		fields["title"] = *req.Title
	}
	if req.Description != nil {
		fields["description"] = *req.Description
	}
	if req.FileIDs != nil {
		if len(*req.FileIDs) == 0 || len(*req.FileIDs) > galleryMaxFiles {
			return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "fileIds must contain 1-32 items.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
		}
		// fileIds 指定時は所有 file のみに絞り、1 件も無ければ reject。
		owned, err := h.ownedFileIDs(*req.FileIDs, user.ID)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
		if len(owned) == 0 {
			return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "No valid files.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
		}
		fields["fileIds"] = pq.StringArray(owned)
	}
	result := h.db.Model(&model.GalleryPost{}).Where("id = ? AND \"userId\" = ?", req.PostID, user.ID).Updates(fields)
	if result.RowsAffected == 0 {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_POST", "No such post.", "1137bf14-c27b-46e0-86d3-0cbbf8b0aca5"))
	}
	// upstream は更新後の GalleryPost を返す (204 ではない)。
	var post model.GalleryPost
	if err := h.db.Preload("User").Where("id = ?", req.PostID).First(&post).Error; err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_POST", "No such post.", "1137bf14-c27b-46e0-86d3-0cbbf8b0aca5"))
	}
	return c.JSON(http.StatusOK, h.packOne(&post, user))
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

// resolveFiles fetches the DriveFiles for fileIDs and packs them in the same
// order as fileIDs (upstream GalleryPost.files is the resolved DriveFile[]).
// Missing files are skipped. Returns a non-nil slice so JSON is [] not null.
func (h *Handler) resolveFiles(fileIDs []string) []entity.DriveFileEntity {
	out := make([]entity.DriveFileEntity, 0, len(fileIDs))
	if len(fileIDs) == 0 {
		return out
	}
	var rows []*model.DriveFile
	if err := h.db.Where("id IN ?", fileIDs).Find(&rows).Error; err != nil {
		return out
	}
	byID := make(map[string]*model.DriveFile, len(rows))
	for _, f := range rows {
		byID[f.ID] = f
	}
	for _, fid := range fileIDs {
		if f := byID[fid]; f != nil {
			out = append(out, entity.PackDriveFile(f, h.idGen))
		}
	}
	return out
}

// likedPostIDs returns the set of postIDs (from the given list) that viewerID
// has liked, in one query. Empty when viewer is nil.
func (h *Handler) likedPostIDs(viewerID string, postIDs []string) map[string]bool {
	set := map[string]bool{}
	if viewerID == "" || len(postIDs) == 0 {
		return set
	}
	var liked []string
	h.db.Model(&model.GalleryLike{}).
		Where("\"userId\" = ? AND \"postId\" IN ?", viewerID, postIDs).
		Pluck("\"postId\"", &liked)
	for _, id := range liked {
		set[id] = true
	}
	return set
}

func (h *Handler) packMany(posts []*model.GalleryPost, viewer *model.User) []map[string]any {
	// batch file resolution (N+1 回避): 全 post の fileIds をまとめて 1 query。
	idSet := map[string]struct{}{}
	postIDs := make([]string, 0, len(posts))
	for _, p := range posts {
		postIDs = append(postIDs, p.ID)
		for _, fid := range p.FileIDs {
			idSet[fid] = struct{}{}
		}
	}
	allFileIDs := make([]string, 0, len(idSet))
	for fid := range idSet {
		allFileIDs = append(allFileIDs, fid)
	}
	fileByID := map[string]entity.DriveFileEntity{}
	if len(allFileIDs) > 0 {
		var rows []*model.DriveFile
		if err := h.db.Where("id IN ?", allFileIDs).Find(&rows).Error; err == nil {
			for _, f := range rows {
				fileByID[f.ID] = entity.PackDriveFile(f, h.idGen)
			}
		}
	}
	var likedSet map[string]bool
	if viewer != nil {
		likedSet = h.likedPostIDs(viewer.ID, postIDs)
	}

	result := make([]map[string]any, 0, len(posts))
	for _, p := range posts {
		files := make([]entity.DriveFileEntity, 0, len(p.FileIDs))
		for _, fid := range p.FileIDs {
			if f, ok := fileByID[fid]; ok {
				files = append(files, f)
			}
		}
		var isLiked *bool
		if viewer != nil {
			v := likedSet[p.ID]
			isLiked = &v
		}
		result = append(result, h.buildPostMap(p, files, isLiked))
	}
	return result
}

// packOne packs a single post, resolving its files + the viewer's like state.
// viewer may be nil (isLiked is then omitted, matching upstream optional field).
func (h *Handler) packOne(p *model.GalleryPost, viewer *model.User) map[string]any {
	files := h.resolveFiles(p.FileIDs)
	var isLiked *bool
	if viewer != nil {
		liked := h.likedPostIDs(viewer.ID, []string{p.ID})
		v := liked[p.ID]
		isLiked = &v
	}
	return h.buildPostMap(p, files, isLiked)
}

// buildPostMap assembles the GalleryPost response shape from precomputed files
// and isLiked. isLiked nil omits the field (upstream `isLiked` is optional).
func (h *Handler) buildPostMap(p *model.GalleryPost, files []entity.DriveFileEntity, isLiked *bool) map[string]any {
	createdAt := ""
	if t, err := h.idGen.ParseTime(p.ID); err == nil {
		createdAt = t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	fileIDs := []string(p.FileIDs)
	if fileIDs == nil {
		fileIDs = []string{}
	}
	tags := []string(p.Tags)
	if tags == nil {
		tags = []string{}
	}
	resp := map[string]any{
		"id":          p.ID,
		"createdAt":   createdAt,
		"updatedAt":   p.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		"title":       p.Title,
		"description": p.Description,
		"userId":      p.UserID,
		"fileIds":     fileIDs,
		"files":       files,
		"isSensitive": p.IsSensitive,
		"likedCount":  p.LikedCount,
		"tags":        tags,
	}
	if p.User != nil {
		resp["user"] = entity.PackUserLite(p.User)
	}
	if isLiked != nil {
		resp["isLiked"] = *isLiked
	}
	return resp
}
