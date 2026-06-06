package entity

import (
	"sort"
	"sync"

	"github.com/shiroha-a/mk/internal/model"
)

// UserRolesLookup resolves the roles currently assigned to a user. Mirrors
// upstream Misskey TS `RoleService.getUserRoles`:
//
//	this.roleService.getUserRoles(user.id)
//	  .then(roles => roles.filter(role => role.isPublic).sort(...).map(...))
//
// Returns nil when the lookup isn't wired (router not configured) or the
// user has no role assignments; callers then render an empty `roles` /
// `badgeRoles` array (= upstream behavior for users with no roles).
type UserRolesLookup interface {
	LookupUserRoles(userID string) []*model.Role
}

// userRolesLookup is process-wide so PackUserLite / PackUserDetailed can
// resolve roles without changing their signature. Router wires this once at
// startup via SetUserRolesLookup. nil disables enrichment, which keeps
// existing tests that don't bother wiring the lookup green (= empty roles
// / badgeRoles array).
var (
	userRolesLookupMu sync.RWMutex
	userRolesLookup   UserRolesLookup
)

// SetUserRolesLookup wires the role lookup used by PackUserLite and
// PackUserDetailed to populate the `roles` / `badgeRoles` fields. Pass nil
// to clear (used in tests).
func SetUserRolesLookup(l UserRolesLookup) {
	userRolesLookupMu.Lock()
	userRolesLookup = l
	userRolesLookupMu.Unlock()
}

// resolveUserRolesSorted returns the user's assigned roles sorted by
// displayOrder descending (= upstream sort key). Returns a fresh slice so
// callers can iterate / filter freely without mutating the role service's
// per-user cache (#761).
//
// nil is returned when the lookup isn't wired or the user has no roles.
func resolveUserRolesSorted(userID string) []*model.Role {
	userRolesLookupMu.RLock()
	l := userRolesLookup
	userRolesLookupMu.RUnlock()
	if l == nil {
		return nil
	}
	src := l.LookupUserRoles(userID)
	if len(src) == 0 {
		return nil
	}
	// 共有 cache を mutate しないよう copy してから sort する。upstream
	// は b.displayOrder - a.displayOrder (= descending) でソートしている。
	out := make([]*model.Role, len(src))
	copy(out, src)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].DisplayOrder > out[j].DisplayOrder
	})
	return out
}

// packBadgeRoles returns the user's badge roles ready for JSON serialisation
// as the `badgeRoles` field of a UserLite. Filter mirrors upstream
// `r.asBadge && r.isPublic` (the iAmModerator branch — "moderators can see
// private badges" — is not yet implemented because PackUserLite has no
// viewer context; that is a follow-up to thread viewer through the entity
// pack path).
//
// `(meta.showRoleBadgesOfRemoteUsers || user.host == null)` 条件は本実装で
// は「local user (= u.Host == nil) のみ」に限定する upstream の保守的な
// デフォルト挙動を採用する。meta フラグ参照は別 lookup pattern を要する
// ため、scope を core bug fix に絞って follow-up に回す。
//
// Always returns a non-nil slice ([]any{}) so the JSON output is `[]` not
// `null` (drop-in 互換: upstream は undefined を返すが mk-go の現実装は
// JSON `[]` 固定なので、その shape を維持する)。
func packBadgeRoles(u *model.User) []any {
	out := []any{}
	if u == nil || u.Host != nil {
		// remote user は upstream default で badge を出さない
		return out
	}
	roles := resolveUserRolesSorted(u.ID)
	for _, r := range roles {
		if r == nil || !r.AsBadge || !r.IsPublic {
			continue
		}
		out = append(out, map[string]any{
			"name": r.Name,
			// role icon は admin 設定だが remote URL を指せるため、frontend が
			// <img src> へ直接載せる前に proxy 経由へ書き換えて IP 漏洩を防ぐ
			// (#1529)。local は no-op。
			"iconUrl":      ProxyMediaURLPtr(r.IconURL),
			"displayOrder": r.DisplayOrder,
		})
	}
	return out
}

// packPublicRoles returns the user's public roles ready for JSON
// serialisation as the `roles` field of a UserDetailed. Filter mirrors
// upstream `roles.filter(role => role.isPublic)`.
//
// Always returns a non-nil slice ([]any{}) so the JSON output is `[]` not
// `null`.
func packPublicRoles(u *model.User) []any {
	out := []any{}
	if u == nil {
		return out
	}
	roles := resolveUserRolesSorted(u.ID)
	for _, r := range roles {
		if r == nil || !r.IsPublic {
			continue
		}
		out = append(out, map[string]any{
			"id":              r.ID,
			"name":            r.Name,
			"color":           r.Color,
			"iconUrl":         ProxyMediaURLPtr(r.IconURL),
			"description":     r.Description,
			"isModerator":     r.IsModerator,
			"isAdministrator": r.IsAdministrator,
			"displayOrder":    r.DisplayOrder,
		})
	}
	return out
}
