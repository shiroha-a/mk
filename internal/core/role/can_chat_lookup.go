package role

// CanChatLookupAdapter wraps a role policy provider so that entity.PackUserLite
// can derive `canChat` from the upstream `chatAvailability === "available"`
// semantics (#988). Implements entity.CanChatLookup without creating a
// dependency from entity to core/role (= keeps the layering: api → core →
// entity, never the reverse).
type CanChatLookupAdapter struct {
	provider PolicyProvider
}

// PolicyProvider is the subset of role.Service used by the adapter. Defined
// here as an interface so tests can inject a stub without depending on the
// full Service.
type PolicyProvider interface {
	GetUserPolicies(userID string) map[string]any
}

// NewCanChatLookup returns an entity.CanChatLookup that resolves a user's
// canChat status via role policies. Use entity.SetCanChatLookup(...) to wire.
func NewCanChatLookup(provider PolicyProvider) *CanChatLookupAdapter {
	return &CanChatLookupAdapter{provider: provider}
}

// LookupCanChat reads chatAvailability from the user's merged role policies.
// Returns (true, true) when chatAvailability == "available" (= default),
// (false, true) for other values ("readonly" / "unavailable" per upstream
// taxonomy). Returns ok=false only if the policy map is missing the key
// (= legacy data), in which case PackUserLite falls back to default true.
func (a *CanChatLookupAdapter) LookupCanChat(userID string) (bool, bool) {
	policies := a.provider.GetUserPolicies(userID)
	v, ok := policies["chatAvailability"]
	if !ok {
		return false, false
	}
	s, ok := v.(string)
	if !ok {
		// マージ後 policies の chatAvailability は upstream の string enum
		// ("available" / "readonly" / "unavailable") なので、別型は壊れた
		// data。defaults へ fallback。
		return false, false
	}
	return s == "available", true
}
