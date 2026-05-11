package entity

import "sync"

// CanChatLookup resolves a user's effective canChat status by consulting
// role policies. Mirrors upstream Misskey TS `UserEntityService.pack`:
//
//	canChat: this.roleService.getUserPolicies(user.id)
//	  .then(r => r.chatAvailability === 'available')
//
// Returns ok=false when the lookup isn't wired (router not configured), or
// when policy data isn't available for this user; the caller then falls
// back to the default true (matches DefaultPolicies' "available").
type CanChatLookup interface {
	LookupCanChat(userID string) (canChat bool, ok bool)
}

// canChatLookup is process-wide so PackUserLite can resolve canChat without
// changing its signature (called from 100+ sites). Router wires this once
// at startup via SetCanChatLookup. nil disables enrichment, which keeps
// existing tests that don't bother wiring the lookup green (the result is
// the upstream default `chatAvailability = "available"` → canChat=true).
var (
	canChatLookupMu sync.RWMutex
	canChatLookup   CanChatLookup
)

// SetCanChatLookup wires the role-policy lookup used by PackUserLite to
// derive canChat. Pass nil to clear (used in tests).
func SetCanChatLookup(l CanChatLookup) {
	canChatLookupMu.Lock()
	canChatLookup = l
	canChatLookupMu.Unlock()
}

// resolveCanChat returns the canChat value for userID via the registered
// lookup, falling back to true (= upstream default `chatAvailability ===
// "available"`) when lookup is unwired or returns ok=false.
//
// upstream の lookup は async だが mk-go の role.Service は in-memory
// cache (#761) を持つ pure sync function なので、entity 層から直接
// 呼んで OK (DB 負荷は加わらない)。
func resolveCanChat(userID string) bool {
	canChatLookupMu.RLock()
	l := canChatLookup
	canChatLookupMu.RUnlock()
	if l == nil {
		return true
	}
	val, ok := l.LookupCanChat(userID)
	if !ok {
		return true
	}
	return val
}
