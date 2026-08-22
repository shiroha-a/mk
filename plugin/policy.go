package plugin

import (
	"context"
	"fmt"
)

// EffectivePolicyRequest is the input to an effective policy resolver.
type EffectivePolicyRequest struct {
	// UserID is empty when the user is anonymous.
	UserID string
	// RoleIDs are active role IDs, sorted without duplicates. Anonymous
	// requests receive a non-nil empty slice.
	RoleIDs []string
}

// EffectivePolicyContribution is one policy key's contribution.
type EffectivePolicyContribution struct {
	Key        string
	Priority   int
	UseDefault bool
	Value      any
	// Order distinguishes multiple contributions for the same key and controls
	// their deterministic provider-local order. Each (Key, Order) pair must be
	// unique within one resolver result.
	Order int
}

// EffectivePolicyResolver computes effective policy contributions for a user.
type EffectivePolicyResolver func(context.Context, EffectivePolicyRequest) ([]EffectivePolicyContribution, error)

// EffectivePolicyRegistration declares an effective-policy provider.
type EffectivePolicyRegistration struct {
	Keys    []string
	Resolve EffectivePolicyResolver
}

// Validate reports whether the registration is usable.
func (r EffectivePolicyRegistration) Validate() error {
	if r.Resolve == nil {
		return fmt.Errorf("plugin: EffectivePolicies の Resolve が nil です")
	}
	if len(r.Keys) == 0 {
		return fmt.Errorf("plugin: EffectivePolicies の Keys が空です")
	}
	seen := make(map[string]bool, len(r.Keys))
	for _, key := range r.Keys {
		if key == "" {
			return fmt.Errorf("plugin: EffectivePolicies の Keys に空のキーが含まれています")
		}
		if seen[key] {
			return fmt.Errorf("plugin: EffectivePolicies の Keys に重複があります (%q)", key)
		}
		seen[key] = true
	}
	return nil
}

// EffectivePolicyInvalidator drops cached policy inputs and successful
// provider output after committed state changes.
type EffectivePolicyInvalidator interface {
	InvalidateUser(context.Context, string) error
	// InvalidateRole is intentionally broader than one role because conditional
	// role holders cannot be enumerated from assignments.
	InvalidateRole(context.Context, string) error
}
