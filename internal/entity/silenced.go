package entity

import "sync"

// SilencedLookup resolves a user's `isSilenced` state from role policies.
// Mirrors upstream UserEntityService.pack which sets
// `isSilenced: !(await roleService.getUserPolicies(user.id)).canPublicNote`.
//
// Implemented structurally by role.Service.IsSilenced(userID) bool, so the
// router can wire it directly without an adapter (keeps layering: entity never
// imports core/role).
type SilencedLookup interface {
	IsSilenced(userID string) bool
}

// silencedLookup is process-wide so PackUserDetailed can resolve isSilenced
// without changing its signature (called from many sites). Router wires this
// once at startup via SetSilencedLookup. nil leaves isSilenced=false, which
// keeps tests that don't wire it green (matches the previous hardcoded value).
var (
	silencedLookupMu sync.RWMutex
	silencedLookup   SilencedLookup
)

// SetSilencedLookup wires the role-policy lookup used by PackUserDetailed to
// derive isSilenced. Pass nil to clear (used in tests).
func SetSilencedLookup(l SilencedLookup) {
	silencedLookupMu.Lock()
	silencedLookup = l
	silencedLookupMu.Unlock()
}

// resolveSilenced returns isSilenced for userID via the registered lookup,
// falling back to false when the lookup is unwired.
func resolveSilenced(userID string) bool {
	silencedLookupMu.RLock()
	l := silencedLookup
	silencedLookupMu.RUnlock()
	if l == nil {
		return false
	}
	return l.IsSilenced(userID)
}
