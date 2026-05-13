package notes

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/timeline"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// TimelineRequest is the common request body for the four timeline endpoints.
type TimelineRequest struct {
	Limit                 int    `json:"limit"`
	SinceID               string `json:"sinceId"`
	UntilID               string `json:"untilId"`
	SinceDate             *int64 `json:"sinceDate"`
	UntilDate             *int64 `json:"untilDate"`
	WithFiles             bool   `json:"withFiles"`
	WithRenotes           *bool  `json:"withRenotes"`
	WithReplies           *bool  `json:"withReplies"`
	IncludeMyRenotes      *bool  `json:"includeMyRenotes"`
	IncludeRenotedMyNotes *bool  `json:"includeRenotedMyNotes"`
	IncludeLocalRenotes   *bool  `json:"includeLocalRenotes"`
	AllowPartial          bool   `json:"allowPartial"`
}

func (r *TimelineRequest) normalize() {
	if r.Limit <= 0 {
		r.Limit = 10
	}
	if r.Limit > 100 {
		r.Limit = 100
	}
}

// Policy keys consumed by timeline gates. notes package 内 private const に
// 留めて core/role への依存を増やさない (= TimelinePolicyProvider interface の
// narrow design と整合)。値は role package の Policy* 定数と一致させる必要が
// あり、ずれると gate が動かなくなるので doc コメントで参照を明記する。
const (
	// policyKeyLtlAvailable = role.PolicyLtlAvailable。
	policyKeyLtlAvailable = "ltlAvailable"
	// policyKeyGtlAvailable = role.PolicyGtlAvailable。
	policyKeyGtlAvailable = "gtlAvailable"
)

// timelineAvailable reports whether the timeline endpoint gated by the
// given policy key (= "ltlAvailable" / "gtlAvailable") is enabled for the
// current viewer. upstream Misskey TS handler は `getUserPolicies(me ?
// me.id : null)` で匿名でも base policies (DefaultPolicies + meta.policies
// merge) を返す pattern なので、mk-go も viewer が nil なら userID="" を
// 渡して base policies を引く (= core/role.Service.GetUserPolicies の
// 匿名経路と同 semantics、#1026)。
//
// policyProvider 未配線時は gate skip (test fixture / 旧挙動互換)。policy
// が bool true でない場合は **fail-closed で reject** する: upstream の
// `if (!policies.ltlAvailable)` 評価 (= undefined / false どちらも falsy
// 扱い) と挙動を揃える。production 経路では DefaultPolicies が常に bool を
// 返すのでこの差は発火しないが、厳密互換性のため fail-closed で揃える
// (#1038 review)。
func (h *Handler) timelineAvailable(c echo.Context, policyKey string) bool {
	if h.policyProvider == nil {
		return true
	}
	var userID string
	if viewer := middleware.GetUser(c); viewer != nil {
		userID = viewer.ID
	}
	policies := h.policyProvider.GetUserPolicies(userID)
	v, ok := policies[policyKey].(bool)
	return ok && v
}

// Timeline handles POST /api/notes/timeline (home timeline).
func (h *Handler) Timeline(c echo.Context) error {
	return h.serveTimeline(c, func(viewer *model.User, req TimelineRequest) ([]*model.Note, error) {
		f := timeline.TimelineFilter{
			WithFiles:             req.WithFiles,
			WithRenotes:           req.WithRenotes,
			IncludeMyRenotes:      req.IncludeMyRenotes,
			IncludeRenotedMyNotes: req.IncludeRenotedMyNotes,
			IncludeLocalRenotes:   req.IncludeLocalRenotes,
			AllowPartial:          req.AllowPartial,
			MutedChannelIDs:       h.loadMutedChannelIDs(viewer),
			MutedUserIDs:          h.loadMutedUserIDs(viewer),
			RenoteMutedUserIDs:    h.loadRenoteMutedUserIDs(viewer),
		}
		return h.timelineService.HomeTimeline(c.Request().Context(), viewer, req.UntilID, req.SinceID, req.Limit, f)
	}, true)
}

// LocalTimeline handles POST /api/notes/local-timeline.
func (h *Handler) LocalTimeline(c echo.Context) error {
	if !h.timelineAvailable(c, policyKeyLtlAvailable) {
		return apierr.JSONLtlDisabled(c)
	}
	return h.serveTimeline(c, func(viewer *model.User, req TimelineRequest) ([]*model.Note, error) {
		f := timeline.TimelineFilter{
			WithFiles:          req.WithFiles,
			WithRenotes:        req.WithRenotes,
			WithReplies:        req.WithReplies,
			AllowPartial:       req.AllowPartial,
			MutedChannelIDs:    h.loadMutedChannelIDs(viewer),
			MutedUserIDs:       h.loadMutedUserIDs(viewer),
			RenoteMutedUserIDs: h.loadRenoteMutedUserIDs(viewer),
		}
		return h.timelineService.LocalTimeline(c.Request().Context(), viewer, req.UntilID, req.SinceID, req.Limit, f)
	}, false)
}

// GlobalTimeline handles POST /api/notes/global-timeline.
func (h *Handler) GlobalTimeline(c echo.Context) error {
	if !h.timelineAvailable(c, policyKeyGtlAvailable) {
		return apierr.JSONGtlDisabled(c)
	}
	return h.serveTimeline(c, func(viewer *model.User, req TimelineRequest) ([]*model.Note, error) {
		f := timeline.TimelineFilter{
			WithFiles:          req.WithFiles,
			WithRenotes:        req.WithRenotes,
			AllowPartial:       req.AllowPartial,
			MutedChannelIDs:    h.loadMutedChannelIDs(viewer),
			MutedUserIDs:       h.loadMutedUserIDs(viewer),
			RenoteMutedUserIDs: h.loadRenoteMutedUserIDs(viewer),
		}
		return h.timelineService.GlobalTimeline(c.Request().Context(), viewer, req.UntilID, req.SinceID, req.Limit, f)
	}, false)
}

// HybridTimeline handles POST /api/notes/hybrid-timeline.
func (h *Handler) HybridTimeline(c echo.Context) error {
	// upstream: hybrid-timeline は ltlAvailable で gate する (gtl ではなく
	// ltl 側 policy を見るのは「ローカルタイムライン + social の hybrid」だから)。
	if !h.timelineAvailable(c, policyKeyLtlAvailable) {
		return apierr.JSONLtlDisabled(c)
	}
	return h.serveTimeline(c, func(viewer *model.User, req TimelineRequest) ([]*model.Note, error) {
		f := timeline.TimelineFilter{
			WithFiles:             req.WithFiles,
			WithRenotes:           req.WithRenotes,
			WithReplies:           req.WithReplies,
			IncludeMyRenotes:      req.IncludeMyRenotes,
			IncludeRenotedMyNotes: req.IncludeRenotedMyNotes,
			IncludeLocalRenotes:   req.IncludeLocalRenotes,
			AllowPartial:          req.AllowPartial,
			MutedChannelIDs:       h.loadMutedChannelIDs(viewer),
			MutedUserIDs:          h.loadMutedUserIDs(viewer),
			RenoteMutedUserIDs:    h.loadRenoteMutedUserIDs(viewer),
		}
		return h.timelineService.HybridTimeline(c.Request().Context(), viewer, req.UntilID, req.SinceID, req.Limit, f)
	}, true)
}

// loadMutedChannelIDs returns the channel IDs that viewer has muted. Returns
// nil for anonymous viewers or when the repo is not wired. On repository
// error the mute list is skipped (best-effort) and a warning is logged so
// operators can see silent filter failures.
func (h *Handler) loadMutedChannelIDs(viewer *model.User) []string {
	if viewer == nil || h.channelMutingRepo == nil {
		return nil
	}
	rows, err := h.channelMutingRepo.ListByUser(viewer.ID)
	if err != nil {
		slog.Warn("timeline: failed to load muted channels", "userId", viewer.ID, "err", err)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	ids := make([]string, 0, len(rows))
	for _, m := range rows {
		ids = append(ids, m.ChannelID)
	}
	return ids
}

// loadMutedUserIDs returns userIDs that viewer has muted (active mutes only,
// non-expired). nil for anonymous viewers or when the repo is not wired.
// repository error は best-effort で nil 扱いにし slog.Warn で観測する
// (= channel mute 同様の pattern、#874)。
func (h *Handler) loadMutedUserIDs(viewer *model.User) []string {
	if viewer == nil || h.mutingRepo == nil {
		return nil
	}
	ids, err := h.mutingRepo.ListMuteeIDs(viewer.ID)
	if err != nil {
		slog.Warn("timeline: failed to load muted users", "userId", viewer.ID, "err", err)
		return nil
	}
	return ids
}

// loadRenoteMutedUserIDs returns userIDs that viewer has renote-muted.
// nil for anonymous viewers or when the repo is not wired (#903)。
// MutedUserIDs と異なり投稿者の plain note は除外せず、pure renote のみ
// filter する (logic は ApplyFilter / applyTimelineFilter 側で実装)。
func (h *Handler) loadRenoteMutedUserIDs(viewer *model.User) []string {
	if viewer == nil || h.renoteMutingRepo == nil {
		return nil
	}
	ids, err := h.renoteMutingRepo.ListMuteeIDs(viewer.ID)
	if err != nil {
		slog.Warn("timeline: failed to load renote-muted users", "userId", viewer.ID, "err", err)
		return nil
	}
	return ids
}

// serveTimeline factors out the common parsing and error handling for the four
// timeline endpoints. requireAuthがtrueのときviewer==nilで401相当を返す。
func (h *Handler) serveTimeline(
	c echo.Context,
	fn func(*model.User, TimelineRequest) ([]*model.Note, error),
	requireAuth bool,
) error {
	var req TimelineRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	req.normalize()

	// sinceDate/untilDate → sinceID/untilID 変換 (TS互換)
	if req.SinceDate != nil && req.SinceID == "" {
		req.SinceID = h.idGen.Generate(time.UnixMilli(*req.SinceDate))
	}
	if req.UntilDate != nil && req.UntilID == "" {
		req.UntilID = h.idGen.Generate(time.UnixMilli(*req.UntilDate))
	}

	viewer := middleware.GetUser(c)
	if requireAuth && viewer == nil {
		return c.JSON(http.StatusUnauthorized, apierr.Error("CREDENTIAL_REQUIRED", "Credential required.", "1384574d-a912-4b81-8601-c7b1c4085df1"))
	}

	// UGC visibility: 未ログインユーザーの閲覧を制限する (meta.ugcVisibilityForVisitor)。
	// "none" → 空リスト、"local" → local timeline のみ許可 (global はブロック)。
	if viewer == nil && h.ugcVisibility == "none" {
		return c.JSON(http.StatusOK, []any{})
	}

	notes, err := fn(viewer, req)
	if err != nil {
		// requireAuthでviewer nilチェックは事前に行っているので、Service層からの
		// ErrUnauthenticatedはここには到達しない。残りはRedis等の障害のみ。
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.packMany(c.Request().Context(), notes, viewer))
}
