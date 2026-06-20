// Package nodeinfo provides /nodeinfo/* endpoints.
package nodeinfo

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/config"
	corereversi "github.com/shiroha-a/mk/internal/core/reversi"
	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/repository"
)

// maxNoteTextLength mirrors upstream MAX_NOTE_TEXT_LENGTH advertised in nodeinfo
// metadata。実際の投稿長 limit は別経路だが nodeinfo は upstream と同じ定数を出す。
const maxNoteTextLength = 3000

// defaultThemeColor mirrors upstream NodeinfoServerService の
// `meta.themeColor ?? '#86b300'` fallback。
const defaultThemeColor = "#86b300"

// softwareRepository is the mk-go source repository URL advertised in the
// nodeinfo software block. Upstream advertises homepage/repository in the same
// object (2.0: homepage only; 2.1: homepage=repository=repositoryUrl); mk-go has
// no separate hub, so both point at the repository (#1925)。
const softwareRepository = "https://github.com/shiroha-a/mk"

// Handler handles nodeinfo endpoints.
type Handler struct {
	cfg          *config.Config
	metaRepo     repository.MetaRepository
	userRepo     repository.UserRepository
	noteRepo     repository.NoteRepository
	proxyAccount func() (string, bool)
	clock        func() time.Time
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

// SetProxyAccountResolver wires the resolver used to populate the
// metadata.proxyAccountName field (#1777)。未配線なら proxyAccountName は null。
func (h *Handler) SetProxyAccountResolver(r func() (string, bool)) {
	h.proxyAccount = r
}

// Version2_1 handles GET /nodeinfo/2.1.
func (h *Handler) Version2_1(c echo.Context) error {
	return c.JSON(http.StatusOK, h.buildDocument("2.1"))
}

// Version2_0 handles GET /nodeinfo/2.0. /.well-known/nodeinfo が 2.0 リンクも
// advertise するため、upstream NodeinfoServerService と同じく 2.0 も配信する
// (#1777)。schema 2.0 には software.repository が無いので省略する (upstream は
// version 2.0 で software.repository を delete する)。
func (h *Handler) Version2_0(c echo.Context) error {
	return c.JSON(http.StatusOK, h.buildDocument("2.0"))
}

// buildDocument assembles the nodeinfo document for the given schema version
// ("2.0" / "2.1"). software.repository は 2.1 のみ (schema 2.0 には無い)。
func (h *Handler) buildDocument(version string) map[string]any {
	nodeName := h.cfg.Host
	var (
		nodeDescription              string
		maintainerName               string
		maintainerEmail              string
		openRegistrations            bool
		disableRegistration          bool
		emailRequiredForSignup       bool
		enableHcaptcha               bool
		enableRecaptcha              bool
		enableMcaptcha               bool
		enableTurnstile              bool
		enableEmail                  bool
		enableServiceWorker          bool
		langs                        = []string{}
		themeColor                   = defaultThemeColor
		mergedPolicies               = role.MergeMetaPolicies(nil)
		tosURL, privacyURL, inqURL   any
		impressumURL, repoURL, fbURL any
	)
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
			openRegistrations = !m.DisableRegistration
			disableRegistration = m.DisableRegistration
			emailRequiredForSignup = m.EmailRequiredForSignup
			enableHcaptcha = m.EnableHcaptcha
			enableRecaptcha = m.EnableRecaptcha
			enableMcaptcha = m.EnableMcaptcha
			enableTurnstile = m.EnableTurnstile
			enableEmail = m.EnableEmail
			enableServiceWorker = m.EnableServiceWorker
			if ls := []string(m.Langs); ls != nil {
				langs = ls
			}
			if m.ThemeColor != nil && *m.ThemeColor != "" {
				themeColor = *m.ThemeColor
			}
			mergedPolicies = role.MergeMetaPolicies([]byte(m.Policies))
			// *string をそのまま入れると nil は JSON null、値ありはその文字列になる。
			tosURL, privacyURL, inqURL = m.TermsOfServiceURL, m.PrivacyPolicyURL, m.InquiryURL
			impressumURL, repoURL, fbURL = m.ImpressumURL, m.RepositoryURL, m.FeedbackURL
		}
	}

	var proxyAccountName any
	if h.proxyAccount != nil {
		if name, ok := h.proxyAccount(); ok {
			proxyAccountName = name
		}
	}

	maintainer := map[string]any{"name": maintainerName, "email": maintainerEmail}
	metadata := map[string]any{
		"nodeName":        nodeName,
		"nodeDescription": nodeDescription,
		"nodeAdmins":      []map[string]any{maintainer},
		// deprecated だが upstream が残しているので維持する。
		"maintainer":             maintainer,
		"langs":                  langs,
		"tosUrl":                 tosURL,
		"privacyPolicyUrl":       privacyURL,
		"inquiryUrl":             inqURL,
		"impressumUrl":           impressumURL,
		"repositoryUrl":          repoURL,
		"feedbackUrl":            fbURL,
		"disableRegistration":    disableRegistration,
		"disableLocalTimeline":   !policyBool(mergedPolicies, "ltlAvailable"),
		"disableGlobalTimeline":  !policyBool(mergedPolicies, "gtlAvailable"),
		"emailRequiredForSignup": emailRequiredForSignup,
		"enableHcaptcha":         enableHcaptcha,
		"enableRecaptcha":        enableRecaptcha,
		"enableMcaptcha":         enableMcaptcha,
		"enableTurnstile":        enableTurnstile,
		"maxNoteTextLength":      maxNoteTextLength,
		"enableEmail":            enableEmail,
		"enableServiceWorker":    enableServiceWorker,
		"proxyAccountName":       proxyAccountName,
		"themeColor":             themeColor,
		// CherryPick 本家の reversi 連合拡張と互換性を示すバージョン。
		// 相手側 (CherryPick) はこの値のメジャーバージョン一致で連合可否を
		// 判定するので、破壊的変更が無い限り 1.1.x を維持する (#417 P3)。
		// corereversi.ReversiVersion と drift しないよう定数参照する。
		"reversiVersion": corereversi.ReversiVersion,
	}

	// 統計値は repo 経由で集計。未配線なら 0 (#403)。DB error は nodeinfo を
	// 丸ごと failさせるより partial 値で返す方が federation crawler に優しい
	// ので slog.Warn でログだけ残して 0 fallback する。
	var usersTotal, usersMonth, usersHalf, localPosts, localComments int64
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

	// upstream NodeinfoServerService: software は name/version/homepage/repository
	// を持ち、2.0 は repository を delete (homepage は残す)、2.1 は homepage=repository。
	// mk-go は homepage=repository=softwareRepository で両 version に homepage を出す (#1925)。
	software := map[string]any{
		"name":     "mk-go",
		"version":  config.MkGoVersion,
		"homepage": softwareRepository,
	}
	// schema 2.0 には software.repository が無い (upstream は 2.0 で delete する)。
	if version == "2.1" {
		software["repository"] = softwareRepository
	}

	return map[string]any{
		"version":   version,
		"software":  software,
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
}

// policyBool reads a boolean policy value with a conservative false default
// (mirrors meta.PolicyBool; duplicated here to avoid importing the meta API
// package from nodeinfo)。
func policyBool(policies map[string]any, key string) bool {
	v, ok := policies[key]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}
