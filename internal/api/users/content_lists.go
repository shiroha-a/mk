package users

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Clips handles POST /api/users/clips.
//
// 非所有者視点では public のみ返す。LIMIT は絞り込み後に適用されるよう SQL
// 側で WHERE isPublic = true を通す (ListPublicByUser) — 古い post-fetch
// filter 方式だと private が多いユーザーで空の結果を返してしまう bug になる。
//
// frontend Paginator が cursor mode で投げてくる sinceId / untilId を
// 拾って repo に渡し、Paginator が次ページ取得で同じ row 集合を返さない
// ようにする (#487 兄弟ケース)。
func (h *Handler) Clips(c echo.Context) error {
	var req struct {
		UserID    string `json:"userId"`
		Limit     int    `json:"limit"`
		Offset    int    `json:"offset"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.clipRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	clampListLimit(&req.Limit)
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	req.SinceID, req.UntilID = id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	viewer := middleware.GetUser(c)
	isSelf := viewer != nil && viewer.ID == req.UserID
	var rows []*model.Clip
	var err error
	if isSelf {
		rows, err = h.clipRepo.ListByUser(req.UserID, req.SinceID, req.UntilID, req.Limit, req.Offset)
	} else {
		rows, err = h.clipRepo.ListPublicByUser(req.UserID, req.SinceID, req.UntilID, req.Limit, req.Offset)
	}
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	// clip の owner は全行で同一 (req.UserID) なので 1 回だけ lookup して
	// PackClip の user フィールドに使う。misskey_dart の Clip.fromJson は
	// user を非null Map として要求するため owner なしでは落ちる (#1237)。
	var owner *model.User
	if h.userRepo != nil {
		owner, _ = h.userRepo.FindByID(req.UserID)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, cl := range rows {
		// favoritedCount は常時実カウント、isFavorited は認証 viewer のみ、
		// notesCount は owner 閲覧時のみ (#1562、upstream ClipEntityService.pack)。
		extras := entity.ClipExtras{ShowNotesCount: isSelf}
		if viewer != nil {
			fav := false
			extras.IsFavorited = &fav
		}
		if h.clipFavoriteRepo != nil {
			if n, err := h.clipFavoriteRepo.CountByClip(cl.ID); err == nil {
				extras.FavoritedCount = n
			}
			if viewer != nil {
				if ok, err := h.clipFavoriteRepo.Exists(viewer.ID, cl.ID); err == nil {
					extras.IsFavorited = &ok
				}
			}
		}
		out = append(out, entity.PackClip(cl, h.idGen, owner, extras))
	}
	return c.JSON(http.StatusOK, out)
}

// Flashs handles POST /api/users/flashs.
//
// frontend Paginator (cursor mode) は untilId / sinceId を forward する (#493)。
func (h *Handler) Flashs(c echo.Context) error {
	var req struct {
		UserID    string `json:"userId"`
		Limit     int    `json:"limit"`
		Offset    int    `json:"offset"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.flashRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	clampListLimit(&req.Limit)
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	req.SinceID, req.UntilID = id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	viewer := middleware.GetUser(c)
	isSelf := viewer != nil && viewer.ID == req.UserID
	var rows []*model.Flash
	var err error
	if isSelf {
		rows, err = h.flashRepo.ListByUser(req.UserID, req.SinceID, req.UntilID, req.Limit, req.Offset)
	} else {
		rows, err = h.flashRepo.ListPublicByUser(req.UserID, req.SinceID, req.UntilID, req.Limit, req.Offset)
	}
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	// flash/* endpoint と shape を揃える: createdAt / updatedAt は
	// ISO 8601 (.000Z) 固定、user (UserLite) を embed する (#520 review)。
	// 全 row が同一 user (= req.UserID) なので fetch は 1 回で済む。
	const tsFormat = "2006-01-02T15:04:05.000Z"
	var packedUser any
	if bundle, err := h.userService.ShowByID(req.UserID); err == nil && bundle != nil {
		packedUser = entity.PackUserLite(bundle.User)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, f := range rows {
		createdAt := ""
		if t, err := h.idGen.ParseTime(f.ID); err == nil {
			createdAt = t.UTC().Format(tsFormat)
		}
		entry := map[string]any{
			"id":          f.ID,
			"createdAt":   createdAt,
			"updatedAt":   f.UpdatedAt.UTC().Format(tsFormat),
			"title":       f.Title,
			"summary":     f.Summary,
			"userId":      f.UserID,
			"script":      f.Script,
			"permissions": []string(f.Permissions),
			"likedCount":  f.LikedCount,
			"visibility":  f.Visibility,
		}
		if packedUser != nil {
			entry["user"] = packedUser
		}
		out = append(out, entry)
	}
	return c.JSON(http.StatusOK, out)
}

// GalleryPosts handles POST /api/users/gallery/posts.
//
// frontend Paginator (cursor mode) は untilId / sinceId を forward する (#493)。
func (h *Handler) GalleryPosts(c echo.Context) error {
	var req struct {
		UserID    string `json:"userId"`
		Limit     int    `json:"limit"`
		Offset    int    `json:"offset"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.galleryRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	clampListLimit(&req.Limit)
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	req.SinceID, req.UntilID = id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	rows, err := h.galleryRepo.ListByUser(req.UserID, req.SinceID, req.UntilID, req.Limit, req.Offset)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	// GalleryPost に visibility 概念はなく常に公開扱い。
	// updatedAt / createdAt は i/gallery/posts と format を揃える (#520 review)。
	// galleryRepo.ListByUser が Preload("User") を行うので g.User を読んで
	// user (UserLite) を embed できる。i/gallery/posts と同 shape。
	const tsFormat = "2006-01-02T15:04:05.000Z"
	out := make([]map[string]any, 0, len(rows))
	for _, g := range rows {
		createdAt := ""
		if t, err := h.idGen.ParseTime(g.ID); err == nil {
			createdAt = t.UTC().Format(tsFormat)
		}
		entry := map[string]any{
			"id":          g.ID,
			"createdAt":   createdAt,
			"updatedAt":   g.UpdatedAt.UTC().Format(tsFormat),
			"userId":      g.UserID,
			"title":       g.Title,
			"description": g.Description,
			"fileIds":     []string(g.FileIDs),
			"tags":        []string(g.Tags),
			"isSensitive": g.IsSensitive,
			"likedCount":  g.LikedCount,
			"files":       []any{},
		}
		if g.User != nil {
			entry["user"] = entity.PackUserLite(g.User)
		}
		out = append(out, entry)
	}
	return c.JSON(http.StatusOK, out)
}

// Pages handles POST /api/users/pages.
//
// frontend Paginator (cursor mode) は untilId / sinceId を forward する (#493)。
// upstream PageEntityService.pack 同様 `user` (UserLite) を含む full Page entity
// shape で返す (#1134)。旧 inline map 6 field では frontend MkPagePreview の
// `page.user.username` template が落ちて list が空になる drop-in regression
// を起こしていた。
func (h *Handler) Pages(c echo.Context) error {
	var req struct {
		UserID    string `json:"userId"`
		Limit     int    `json:"limit"`
		Offset    int    `json:"offset"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.pageRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	clampListLimit(&req.Limit)
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	req.SinceID, req.UntilID = id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	viewer := middleware.GetUser(c)
	isSelf := viewer != nil && viewer.ID == req.UserID
	var rows []*model.Page
	var err error
	if isSelf {
		rows, err = h.pageRepo.ListByUser(req.UserID, req.SinceID, req.UntilID, req.Limit, req.Offset)
	} else {
		rows, err = h.pageRepo.ListPublicByUser(req.UserID, req.SinceID, req.UntilID, req.Limit, req.Offset)
	}
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	// owner は req.UserID で全 row 同一なので 1 度だけ fetch して全 row に attach。
	// ShowByID 失敗時は empty list を返す (= upstream は user lookup 失敗時に
	// 該当 row を skip するため、最終結果は同等)。
	owner, oerr := h.userService.ShowByID(req.UserID)
	if oerr != nil || owner == nil || owner.User == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		// content の image block / eyeCatchingImageId から drive file を解決する
		// (#1662)。driveFileRepo 未配線なら default ([] / null)。page list なので
		// per-page lookup (N+1) だが list は小さい前提で許容する。
		attachedFiles, eyeCatchingImage := entity.ResolvePageDriveFiles(p, h.driveFileRepo, h.idGen)
		out = append(out, entity.PackPageWithContext(p, entity.PackPageContext{
			IDGen:            h.idGen,
			Owner:            owner.User,
			EyeCatchingImage: eyeCatchingImage,
			AttachedFiles:    attachedFiles,
		}))
	}
	return c.JSON(http.StatusOK, out)
}

// clampListLimit normalises a user-supplied limit to 1..100 with a default of 10.
func clampListLimit(limit *int) {
	if *limit <= 0 {
		*limit = 10
	}
	if *limit > 100 {
		*limit = 100
	}
}
