package users

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/notesfilter"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"gorm.io/gorm"
)

// SetAbuseRepo attaches the abuse report repository.
func (h *Handler) SetAbuseRepo(r repository.AbuseReportRepository) {
	h.abuseRepo = r
}

// Relation handles POST /api/users/relation.
// upstream Misskey TS `users/relation.ts` (= getRelation in
// server/api/common/getRelation.ts) と同 semantics で viewer と target の
// follow / follow-request / block / mute / renote-mute の関係性を返す。
//
// 認証は router.go の `middleware.RequireAuth()` で前段強制されているため
// production では viewer == nil は到達不可。下の `viewer == nil` branch は
// defensive guard + 単体テスト用 (= handler を直接叩く test path)。
//
// repo が wired されていない (= legacy handler test) では該当 field を false
// に保つので production runtime には影響しない (router で必ず wired される)。
func (h *Handler) Relation(c echo.Context) error {
	viewer := middleware.GetUser(c)
	var req struct {
		UserID string `json:"userId"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	out := map[string]any{
		"id":                             req.UserID,
		"isFollowing":                    false,
		"isFollowed":                     false,
		"hasPendingFollowRequestFromYou": false,
		"hasPendingFollowRequestToYou":   false,
		"isBlocking":                     false,
		"isBlocked":                      false,
		"isMuted":                        false,
		"isRenoteMuted":                  false,
	}

	if viewer == nil {
		return c.JSON(http.StatusOK, out)
	}

	// FindByPair は (rec, err) で返す契約 (gorm.ErrRecordNotFound は relation
	// 無し)。row 不在は default の false に倒すが、transient な DB error
	// (timeout / connection 切断等) は運用 debug のため slog.Warn で残す
	// (frontend には false で fallback、次の API call で正される程度の影響)。
	warnRelErr := func(label string, err error) {
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			slog.Warn("users/relation lookup failed", "rel", label, "viewer", viewer.ID, "target", req.UserID, "err", err)
		}
	}

	if h.followingRepo != nil {
		if rec, err := h.followingRepo.FindByPair(viewer.ID, req.UserID); rec != nil {
			out["isFollowing"] = true
		} else {
			warnRelErr("isFollowing", err)
		}
		if rec, err := h.followingRepo.FindByPair(req.UserID, viewer.ID); rec != nil {
			out["isFollowed"] = true
		} else {
			warnRelErr("isFollowed", err)
		}
	}
	if h.followRequestRepo != nil {
		if rec, err := h.followRequestRepo.FindByPair(viewer.ID, req.UserID); rec != nil {
			out["hasPendingFollowRequestFromYou"] = true
		} else {
			warnRelErr("hasPendingFollowRequestFromYou", err)
		}
		if rec, err := h.followRequestRepo.FindByPair(req.UserID, viewer.ID); rec != nil {
			out["hasPendingFollowRequestToYou"] = true
		} else {
			warnRelErr("hasPendingFollowRequestToYou", err)
		}
	}
	if h.blockingRepo != nil {
		if rec, err := h.blockingRepo.FindByPair(viewer.ID, req.UserID); rec != nil {
			out["isBlocking"] = true
		} else {
			warnRelErr("isBlocking", err)
		}
		if rec, err := h.blockingRepo.FindByPair(req.UserID, viewer.ID); rec != nil {
			out["isBlocked"] = true
		} else {
			warnRelErr("isBlocked", err)
		}
	}
	if h.mutingRepo != nil {
		if rec, err := h.mutingRepo.FindByPair(viewer.ID, req.UserID); rec != nil {
			out["isMuted"] = true
		} else {
			warnRelErr("isMuted", err)
		}
	}
	if h.renoteMutingRepo != nil {
		if rec, err := h.renoteMutingRepo.FindByPair(viewer.ID, req.UserID); rec != nil {
			out["isRenoteMuted"] = true
		} else {
			warnRelErr("isRenoteMuted", err)
		}
	}

	return c.JSON(http.StatusOK, out)
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
//
// upstream Misskey TS の users/reactions と同 semantics:
//   - target user の publicReactions が false で viewer ≠ target なら
//     403 REACTIONS_NOT_PUBLIC (= reaction history は private)。
//   - target user が remote なら 400 IS_REMOTE_USER (mk-go では現状 local
//     のみ対応、upstream と同じ制限)。
//   - それ以外 (public か self view) なら reactor の reaction list を
//     id / createdAt / type / user / note の minimal shape で返す。
//
// note pack は upstream の packManyWithNote (= 完全 note shape) ではなく
// 最小 shape (id / userId / text) で start。drop-in 互換性は両 backend で
// 同 endpoint が動くことを spec で担保する (#821 PR-D)。
func (h *Handler) Reactions(c echo.Context) error {
	viewer := middleware.GetUser(c)
	var req struct {
		UserID    string `json:"userId"`
		Limit     int    `json:"limit"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
	}
	if err := c.Bind(&req); err != nil || req.UserID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "userId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	req.SinceID, req.UntilID = id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 100 {
		req.Limit = 100
	}

	// target user lookup + remote / publicReactions check.
	// production では userRepo が必ず wire されるが、既存の handler test
	// (TestReactions_Success 等) は userRepo を wire しないので nil guard で
	// fall-through する (= test compat、production 影響なし)。
	if h.userRepo != nil {
		target, err := h.userRepo.FindByID(req.UserID)
		if err != nil || target == nil {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "27e494ba-2ac2-48e8-893b-10d4d8c2387b"))
		}
		// remote user は upstream でも display を制限している。
		// upstream TS は `host !== null` で判定するので mk-go も nil 判定に
		// 揃える (host="" の row は実際には DB に書かれないが、揺らぎ抑止)。
		if target.Host != nil {
			return c.JSON(http.StatusBadRequest, apierr.Error("IS_REMOTE_USER", "Currently unavailable to display reactions of remote users.", "6b95fa98-8cf9-2350-e284-f0ffdb54a805"))
		}
		// self view 以外は publicReactions check。
		// profile 行が無い (= nil) 場合は DB 列 default の `true` 扱いで
		// fall-through する (upstream TS の getUserPolicies と同 semantics)。
		if viewer == nil || viewer.ID != req.UserID {
			profile := h.userService.GetProfile(req.UserID)
			if profile != nil && !profile.PublicReactions {
				return c.JSON(http.StatusForbidden, apierr.Error("REACTIONS_NOT_PUBLIC", "Reactions of the user is not public.", "673a7dd2-6924-1093-e0c0-e68456ceae5c"))
			}
		}
	}

	// reactor の reaction list を取得 (User / Note を Preload 済み)。
	// userRepo と同じく test stub では noteReactionRepo を wire しないので、
	// nil guard で空配列を返して既存 handler test の挙動を維持する
	// (= 本 PR で stub から本実装に書き換えたが test 互換は保つ、#821 PR-D)。
	if h.noteReactionRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	rows, err := h.noteReactionRepo.ListByUserID(req.UserID, req.UntilID, req.SinceID, req.Limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		entry := map[string]any{
			"id":   r.ID,
			"type": r.Reaction,
		}
		if t, err := h.idGen.ParseTime(r.ID); err == nil {
			entry["createdAt"] = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		if r.User != nil {
			entry["user"] = entity.PackUserLite(r.User)
		}
		if r.Note != nil {
			noteEntry := map[string]any{
				"id":     r.Note.ID,
				"userId": r.Note.UserID,
			}
			if r.Note.Text != nil {
				noteEntry["text"] = *r.Note.Text
			}
			entry["note"] = noteEntry
		}
		out = append(out, entry)
	}
	return c.JSON(http.StatusOK, out)
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
	notes = notesfilter.ApplyHardMute(h.userRepo, viewer, notes)
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
