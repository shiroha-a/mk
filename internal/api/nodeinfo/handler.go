// Package nodeinfo provides /nodeinfo/* endpoints.
package nodeinfo

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/config"
	corereversi "github.com/shiroha-a/mk/internal/core/reversi"
	"github.com/shiroha-a/mk/internal/repository"
)

// Handler handles nodeinfo endpoints.
type Handler struct {
	cfg      *config.Config
	metaRepo repository.MetaRepository
	userRepo repository.UserRepository
	noteRepo repository.NoteRepository
	clock    func() time.Time
}

// NewHandler constructs a Handler.
func NewHandler(cfg *config.Config) *Handler {
	return &Handler{cfg: cfg, clock: time.Now}
}

// SetMetaRepo injects a MetaRepository so that the nodeName / nodeDescription
// fields reflect the live admin settings instead of the config default.
// 未配線のまま呼ばれると cfg.Host fallback になる (#348)。
func (h *Handler) SetMetaRepo(r repository.MetaRepository) {
	h.metaRepo = r
}

// SetUsageRepos injects repositories used to populate the usage statistics
// (users.total / activeMonth / activeHalfyear / localPosts / localComments).
// 未配線のまま呼ばれると対応 field は 0 のままになる (#403)。
func (h *Handler) SetUsageRepos(userRepo repository.UserRepository, noteRepo repository.NoteRepository) {
	h.userRepo = userRepo
	h.noteRepo = noteRepo
}

// SetClock overrides the clock source. Intended for tests.
func (h *Handler) SetClock(now func() time.Time) {
	if now != nil {
		h.clock = now
	}
}

// Version2_1 handles GET /nodeinfo/2.1.
func (h *Handler) Version2_1(c echo.Context) error {
	nodeName := h.cfg.Host
	var nodeDescription string
	var maintainerName, maintainerEmail string
	openRegistrations := false
	if h.metaRepo != nil {
		if m, err := h.metaRepo.Fetch(); err == nil && m != nil {
			if m.Name != nil && *m.Name != "" {
				nodeName = *m.Name
			}
			if m.Description != nil {
				nodeDescription = *m.Description
			}
			if m.MaintainerName != nil {
				maintainerName = *m.MaintainerName
			}
			if m.MaintainerEmail != nil {
				maintainerEmail = *m.MaintainerEmail
			}
			// DisableRegistration=true が登録無効、openRegistrations はその反対。
			openRegistrations = !m.DisableRegistration
		}
	}
	metadata := map[string]any{
		"nodeName":        nodeName,
		"nodeDescription": nodeDescription,
		"maintainer": map[string]any{
			"name":  maintainerName,
			"email": maintainerEmail,
		},
		// CherryPick 本家の reversi 連合拡張と互換性を示すバージョン。
		// 相手側 (CherryPick) はこの値のメジャーバージョン一致で連合可否を
		// 判定するので、破壊的変更が無い限り 1.1.x を維持する (#417 P3)。
		// corereversi.ReversiVersion と drift しないよう定数参照する。
		"reversiVersion": corereversi.ReversiVersion,
	}
	// 統計値は repo 経由で集計。未配線なら 0 (#403)。DB error は nodeinfo を
	// 丸ごと failさせるより partial 値で返す方が federation crawler に優しい
	// ので slog.Warn でログだけ残して 0 fallback する。
	var (
		usersTotal, usersMonth, usersHalf, localPosts, localComments int64
	)
	if h.userRepo != nil {
		now := h.clock()
		if v, err := h.userRepo.CountLocalUsers(); err != nil {
			slog.Warn("nodeinfo: CountLocalUsers failed", "err", err)
		} else {
			usersTotal = v
		}
		if v, err := h.userRepo.CountLocalUsersActiveSince(now.AddDate(0, -1, 0)); err != nil {
			slog.Warn("nodeinfo: CountLocalUsersActiveSince(month) failed", "err", err)
		} else {
			usersMonth = v
		}
		if v, err := h.userRepo.CountLocalUsersActiveSince(now.AddDate(0, -6, 0)); err != nil {
			slog.Warn("nodeinfo: CountLocalUsersActiveSince(halfyear) failed", "err", err)
		} else {
			usersHalf = v
		}
	}
	if h.noteRepo != nil {
		if v, err := h.noteRepo.CountLocalNotes(); err != nil {
			slog.Warn("nodeinfo: CountLocalNotes failed", "err", err)
		} else {
			localPosts = v
		}
		if v, err := h.noteRepo.CountLocalComments(); err != nil {
			slog.Warn("nodeinfo: CountLocalComments failed", "err", err)
		} else {
			localComments = v
		}
	}

	resp := map[string]any{
		"version": "2.1",
		"software": map[string]any{
			"name":       "mk-go",
			"version":    config.MkGoVersion,
			"repository": "https://github.com/shiroha-a/mk",
		},
		"protocols": []string{"activitypub"},
		"services": map[string]any{
			"inbound":  []string{},
			"outbound": []string{"atom1.0", "rss2.0"},
		},
		"openRegistrations": openRegistrations,
		"usage": map[string]any{
			"users": map[string]any{
				"total":          usersTotal,
				"activeMonth":    usersMonth,
				"activeHalfyear": usersHalf,
			},
			"localPosts":    localPosts,
			"localComments": localComments,
		},
		"metadata": metadata,
	}
	return c.JSON(http.StatusOK, resp)
}
