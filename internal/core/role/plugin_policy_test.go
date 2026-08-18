package role_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/shiroha-a/mk/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// registerProvider wires a provider into svc, asserting success.
func registerProvider(t *testing.T, svc *role.Service, name string, keys []string, resolve plugin.EffectivePolicyResolver) {
	t.Helper()
	require.NoError(t, svc.RegisterEffectivePolicyProvider(name, plugin.EffectivePolicyRegistration{Keys: keys, Resolve: resolve}))
}

func assign(t *testing.T, assignRepo *testutil.MockRoleAssignmentRepository, userID, roleID string) {
	t.Helper()
	assignRepo.Assignments[userID+":"+roleID] = &model.RoleAssignment{ID: "a_" + userID + "_" + roleID, UserID: userID, RoleID: roleID}
}

type countingStringer struct{ calls *int }

func (s countingStringer) String() string {
	*s.calls++
	return "ignored"
}

// --- registration validation (load-bearing: Validate must be invoked) ---

func TestRegisterEffectivePolicyProvider_InvokesValidate(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	// Resolve nil => reg.Validate() fails => registration must be rejected.
	err := svc.RegisterEffectivePolicyProvider("bad", plugin.EffectivePolicyRegistration{Keys: []string{"canSearchNotes"}})
	require.Error(t, err, "nil Resolve must be rejected via Validate")
	// registration must NOT be stored
	p, e := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, e)
	assert.Equal(t, false, p["canSearchNotes"], "no provider applied when registration rejected (native default false)")
}

func TestRegisterEffectivePolicyProvider_RejectsEmptyName(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	err := svc.RegisterEffectivePolicyProvider("", plugin.EffectivePolicyRegistration{Keys: []string{"canSearchNotes"}, Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return nil, nil
	}})
	require.Error(t, err, "empty name must be rejected")
}

func TestRegisterEffectivePolicyProvider_RejectsUndeclaredKeyAgainstDefaults(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	reg := plugin.EffectivePolicyRegistration{Keys: []string{"noSuchPolicyKey"}, Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return nil, nil
	}}
	err := svc.RegisterEffectivePolicyProvider("p", reg)
	require.Error(t, err, "key not present in DefaultPolicies must be rejected")
}

// docs/plugins/authoring.md の公開例と同じ Definition が、Definition.Validate、
// factory、production registration、resolution の全経路を通ること。
func TestEffectivePolicy_AuthoringExamplePassesProductionValidation(t *testing.T) {
	def := plugin.Definition{
		Name:       "policy-example",
		APIVersion: plugin.APIVersion,
		EffectivePolicies: func(plugin.Context, plugin.EffectivePolicyInvalidator) (plugin.EffectivePolicyRegistration, error) {
			return plugin.EffectivePolicyRegistration{
				Keys: []string{"driveCapacityMb"},
				Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
					return []plugin.EffectivePolicyContribution{
						{Key: "driveCapacityMb", Priority: 1, Value: 1000},
					}, nil
				},
			}, nil
		},
	}

	require.NoError(t, def.Validate())
	reg, err := def.EffectivePolicies(nil, nil)
	require.NoError(t, err)
	svc, _, _, _ := newTestService(t)
	require.NoError(t, svc.RegisterEffectivePolicyProvider(def.Name, reg))
	policies, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, 1000, policies["driveCapacityMb"])
}

// --- defensive copies / immutability ---

func TestRegisterEffectivePolicyProvider_CopiesKeysSlice(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	keys := []string{"canSearchNotes"}
	reg := plugin.EffectivePolicyRegistration{Keys: keys, Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: true}}, nil
	}}
	require.NoError(t, svc.RegisterEffectivePolicyProvider("p", reg))
	keys[0] = "canInvite" // mutate the caller's slice after registration

	p, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, true, p["canSearchNotes"], "stored registration must not alias the caller's Keys slice")
	assert.Equal(t, false, p["canInvite"], "mutated key must not be honored")
}

func TestEffectivePolicy_RoleIDsSortedAndCloned(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r2"] = &model.Role{ID: "r2", Name: "B"}
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "A"}
	assign(t, assignRepo, "u1", "r2")
	assign(t, assignRepo, "u1", "r1")

	var seen []string
	mutated := false
	registerProvider(t, svc, "p", []string{"canSearchNotes"}, func(_ context.Context, req plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		seen = append([]string(nil), req.RoleIDs...)
		// mutate the slice the host passed in; must not leak anywhere
		for i := range req.RoleIDs {
			req.RoleIDs[i] = "hacked"
		}
		mutated = true
		return nil, nil
	})

	_, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.True(t, mutated, "provider must have been invoked")
	assert.Equal(t, []string{"r1", "r2"}, seen, "RoleIDs must be sorted")
}

func TestEffectivePolicy_RoleIDsIsolatedBetweenProviders(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r2"] = &model.Role{ID: "r2", Name: "B"}
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "A"}
	assign(t, assignRepo, "u1", "r2")
	assign(t, assignRepo, "u1", "r1")

	registerProvider(t, svc, "alpha", []string{"canSearchNotes"}, func(_ context.Context, req plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		for i := range req.RoleIDs {
			req.RoleIDs[i] = "mutated"
		}
		return nil, nil
	})
	var seen []string
	registerProvider(t, svc, "beta", []string{"canInvite"}, func(_ context.Context, req plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		seen = append([]string(nil), req.RoleIDs...)
		return nil, nil
	})

	_, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"r1", "r2"}, seen, "each provider must receive an isolated sorted RoleIDs slice")
}

func TestEffectivePolicy_ProviderOnlyGetsActiveRoles(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "A"}
	roleRepo.Roles["r2"] = &model.Role{ID: "r2", Name: "Expired"}
	assign(t, assignRepo, "u1", "r1")
	// expired assignment -> not active
	expired := time.Now().Add(-time.Hour)
	assignRepo.Assignments["u1:r2"] = &model.RoleAssignment{ID: "a2", UserID: "u1", RoleID: "r2", ExpiresAt: &expired}

	var seen []string
	registerProvider(t, svc, "p", []string{"canSearchNotes"}, func(_ context.Context, req plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		seen = req.RoleIDs
		return nil, nil
	})
	_, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"r1"}, seen, "only currently active role IDs are passed")
}

// --- provider contribution semantics ---

func TestEffectivePolicy_ProviderContributionHonored(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "p", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: true}}, nil
	})
	p, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, true, p["canSearchNotes"], "default false -> provider granted true")
}

func TestEffectivePolicy_UseDefaultFallsBackToNative(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "p", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, UseDefault: true, Value: true}}, nil
	})
	p, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, false, p["canSearchNotes"], "UseDefault uses native default (false), not the Value")
}

func TestEffectivePolicy_UseDefaultDoesNotInspectIgnoredValue(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	formatted := 0
	registerProvider(t, svc, "p", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{
			{Key: "canSearchNotes", Priority: 2, UseDefault: true, Value: countingStringer{calls: &formatted}, Order: 1},
			{Key: "canSearchNotes", Priority: 2, UseDefault: true, Value: countingStringer{calls: &formatted}, Order: 0},
		}, nil
	})

	var policies map[string]any
	var err error
	require.NotPanics(t, func() {
		policies, err = svc.GetUserPoliciesChecked("u1")
	})
	require.NoError(t, err)
	assert.Equal(t, false, policies["canSearchNotes"])
	assert.Zero(t, formatted, "UseDefault value must remain completely unobserved")
}

func TestEffectivePolicy_NativeProviderPriorityAggregation(t *testing.T) {
	t.Run("role priority 2 beats provider priority 1", func(t *testing.T) {
		svc, roleRepo, assignRepo, _ := newTestService(t)
		roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "A", Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":2,"value":true}}`))}
		assign(t, assignRepo, "u1", "r1")
		registerProvider(t, svc, "p", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
			return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 1, Value: false}}, nil
		})
		p, err := svc.GetUserPoliciesChecked("u1")
		require.NoError(t, err)
		assert.Equal(t, true, p["canSearchNotes"], "role priority 2 group dominates provider priority 1")
	})

	t.Run("provider priority 2 beats role priority 1", func(t *testing.T) {
		svc, roleRepo, assignRepo, _ := newTestService(t)
		roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "A", Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":1,"value":false}}`))}
		assign(t, assignRepo, "u1", "r1")
		registerProvider(t, svc, "p", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
			return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: true}}, nil
		})
		p, err := svc.GetUserPoliciesChecked("u1")
		require.NoError(t, err)
		assert.Equal(t, true, p["canSearchNotes"], "provider priority 2 group dominates role priority 1")
	})

	t.Run("same priority aggregates with role", func(t *testing.T) {
		svc, roleRepo, assignRepo, _ := newTestService(t)
		roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "A", Policies: datatypes.JSON([]byte(`{"canPublicNote":{"useDefault":false,"priority":2,"value":true}}`))}
		assign(t, assignRepo, "u1", "r1")
		registerProvider(t, svc, "p", []string{"canPublicNote"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
			return []plugin.EffectivePolicyContribution{{Key: "canPublicNote", Priority: 2, Value: false}}, nil
		})
		p, err := svc.GetUserPoliciesChecked("u1")
		require.NoError(t, err)
		assert.Equal(t, true, p["canPublicNote"], "bool OR across same-priority role+provider")
	})
}

func TestEffectivePolicy_MultipleContributionsPerProviderAndDeterminism(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "p", []string{"canSearchNotes", "canInvite"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		// returned out of Order; host must process deterministically
		return []plugin.EffectivePolicyContribution{
			{Key: "canInvite", Priority: 2, Value: true, Order: 1},
			{Key: "canSearchNotes", Priority: 2, Value: true, Order: 0},
		}, nil
	})
	first, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	second, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, true, first["canSearchNotes"])
	assert.Equal(t, true, first["canInvite"])
	assert.Equal(t, first, second, "resolution must be deterministic across calls")
}

func TestEffectivePolicy_IntegerMaxPreservesHostBoundary(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "p", []string{"driveCapacityMb"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{{Key: "driveCapacityMb", Priority: 2, Value: int(math.MaxInt)}}, nil
	})

	policies, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, int(math.MaxInt), policies["driveCapacityMb"])
}

func TestEffectivePolicy_Int64BoundaryIsHostIntSized(t *testing.T) {
	maxInt64 := int64(math.MaxInt64)
	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "p", []string{"driveCapacityMb"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{{Key: "driveCapacityMb", Priority: 2, Value: maxInt64}}, nil
	})

	policies, err := svc.GetUserPoliciesChecked("u1")
	if strconv.IntSize == 32 {
		require.ErrorIs(t, err, role.ErrEffectivePolicyProvider)
		assert.Equal(t, role.DefaultPolicies()["driveCapacityMb"], policies["driveCapacityMb"])
		return
	}
	require.NoError(t, err)
	assert.Equal(t, int(maxInt64), policies["driveCapacityMb"])
}

func TestEffectivePolicy_IntegralFloatHostIntBoundary(t *testing.T) {
	inside := float64(math.MaxInt)
	if strconv.IntSize == 64 {
		// float64(MaxInt) rounds to 2^63. Its predecessor is the largest
		// representable integral float64 that converts safely to host int.
		inside = math.Nextafter(inside, 0)
	}

	t.Run("largest safely convertible", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		registerProvider(t, svc, "p", []string{"driveCapacityMb"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
			return []plugin.EffectivePolicyContribution{{Key: "driveCapacityMb", Priority: 2, Value: inside}}, nil
		})

		policies, err := svc.GetUserPoliciesChecked("u1")
		require.NoError(t, err)
		assert.Equal(t, int(inside), policies["driveCapacityMb"])
	})

	t.Run("first integral value outside host int", func(t *testing.T) {
		outside := float64(math.MaxInt) + 1
		svc, _, _, _ := newTestService(t)
		registerProvider(t, svc, "p", []string{"driveCapacityMb"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
			return []plugin.EffectivePolicyContribution{{Key: "driveCapacityMb", Priority: 2, Value: outside}}, nil
		})

		policies, err := svc.GetUserPoliciesChecked("u1")
		require.ErrorIs(t, err, role.ErrEffectivePolicyProvider)
		assert.Equal(t, role.DefaultPolicies()["driveCapacityMb"], policies["driveCapacityMb"])
	})
}

func TestEffectivePolicy_IntegerMaxAboveFloatPrecisionIsExact(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("host int cannot represent values above float64's exact integer range")
	}
	exact := int64(1<<53 + 1)
	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "p", []string{"driveCapacityMb"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{{Key: "driveCapacityMb", Priority: 2, Value: exact}}, nil
	})

	policies, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, int(exact), policies["driveCapacityMb"])
}

func TestEffectivePolicy_MultipleIntegerContributionsUseMaxWithoutCumulativeArithmetic(t *testing.T) {
	tests := []struct {
		name  string
		other int
	}{
		{name: "inputs whose sum would overflow", other: 1},
		{name: "inputs whose product would overflow", other: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _, _ := newTestService(t)
			registerProvider(t, svc, "alpha", []string{"driveCapacityMb"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
				return []plugin.EffectivePolicyContribution{{Key: "driveCapacityMb", Priority: 2, Value: int(math.MaxInt)}}, nil
			})
			registerProvider(t, svc, "beta", []string{"driveCapacityMb"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
				return []plugin.EffectivePolicyContribution{{Key: "driveCapacityMb", Priority: 2, Value: tt.other}}, nil
			})

			policies, err := svc.GetUserPoliciesChecked("u1")
			require.NoError(t, err)
			assert.Equal(t, int(math.MaxInt), policies["driveCapacityMb"], "this contract max-aggregates and performs no cumulative arithmetic")
		})
	}
}

func TestEffectivePolicy_NegativeIntegerSentinelsPreserveNativeSemantics(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value any
	}{
		{name: "user list member limit typed int", key: "userEachUserListsLimit", value: int(-1)},
		{name: "webhook limit typed int64", key: "webhookLimit", value: int64(-1)},
		{name: "antenna limit integral float64", key: "antennaLimit", value: float64(-1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _, _ := newTestService(t)
			registerProvider(t, svc, "p", []string{tt.key}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
				return []plugin.EffectivePolicyContribution{{Key: tt.key, Priority: 2, Value: tt.value}}, nil
			})

			policies, err := svc.GetUserPoliciesChecked("u1")
			require.NoError(t, err)
			assert.Equal(t, -1, policies[tt.key])
		})
	}
}

func TestEffectivePolicy_NegativeIntegerMaxWithinSelectedPriority(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "alpha", []string{"userEachUserListsLimit"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{{Key: "userEachUserListsLimit", Priority: 2, Value: -2}}, nil
	})
	registerProvider(t, svc, "beta", []string{"userEachUserListsLimit"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{{Key: "userEachUserListsLimit", Priority: 2, Value: -1}}, nil
	})

	policies, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, -1, policies["userEachUserListsLimit"], "priority 2 excludes the positive native default, then Max selects -1")
}

func TestEffectivePolicy_PluginNameOrdering(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	var order []string
	registerProvider(t, svc, "zebra", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		order = append(order, "zebra")
		return nil, nil
	})
	registerProvider(t, svc, "alpha", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		order = append(order, "alpha")
		return nil, nil
	})
	_, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "zebra"}, order, "providers invoked in plugin-name order")
}

func TestEffectivePolicy_MalformedContributionFailsProviderClosed(t *testing.T) {
	tests := []struct {
		name      string
		keys      []string
		malformed plugin.EffectivePolicyContribution
		nativeKey string
	}{
		{name: "undeclared key", keys: []string{"canSearchNotes", "canSearchUsers"}, malformed: plugin.EffectivePolicyContribution{Key: "canInvite", Priority: 2, Value: true}, nativeKey: "canSearchUsers"},
		{name: "invalid priority", keys: []string{"canSearchNotes", "canSearchUsers"}, malformed: plugin.EffectivePolicyContribution{Key: "canSearchUsers", Priority: 3, Value: true}, nativeKey: "canSearchUsers"},
		{name: "wrong type", keys: []string{"canSearchNotes", "canSearchUsers"}, malformed: plugin.EffectivePolicyContribution{Key: "canSearchUsers", Priority: 2, Value: 1}, nativeKey: "canSearchUsers"},
		{name: "invalid availability", keys: []string{"canSearchNotes", "chatAvailability"}, malformed: plugin.EffectivePolicyContribution{Key: "chatAvailability", Priority: 2, Value: "sometimes"}, nativeKey: "chatAvailability"},
		{name: "NaN number", keys: []string{"canSearchNotes", "driveCapacityMb"}, malformed: plugin.EffectivePolicyContribution{Key: "driveCapacityMb", Priority: 2, Value: math.NaN()}, nativeKey: "driveCapacityMb"},
		{name: "positive infinity number", keys: []string{"canSearchNotes", "driveCapacityMb"}, malformed: plugin.EffectivePolicyContribution{Key: "driveCapacityMb", Priority: 2, Value: math.Inf(1)}, nativeKey: "driveCapacityMb"},
		{name: "negative infinity number", keys: []string{"canSearchNotes", "driveCapacityMb"}, malformed: plugin.EffectivePolicyContribution{Key: "driveCapacityMb", Priority: 2, Value: math.Inf(-1)}, nativeKey: "driveCapacityMb"},
		{name: "fractional integer policy", keys: []string{"canSearchNotes", "driveCapacityMb"}, malformed: plugin.EffectivePolicyContribution{Key: "driveCapacityMb", Priority: 2, Value: 1.5}, nativeKey: "driveCapacityMb"},
		{name: "malformed string array", keys: []string{"canSearchNotes", "uploadableFileTypes"}, malformed: plugin.EffectivePolicyContribution{Key: "uploadableFileTypes", Priority: 2, Value: []any{"image/png", 7}}, nativeKey: "uploadableFileTypes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _, _ := newTestService(t)
			registerProvider(t, svc, "p", tt.keys, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
				return []plugin.EffectivePolicyContribution{
					{Key: "canSearchNotes", Priority: 2, Value: true, Order: 0},
					tt.malformed,
				}, nil
			})

			p, err := svc.GetUserPoliciesChecked("u1")
			require.Equal(t, role.ErrEffectivePolicyProvider, err, "checked resolution must return only the fixed sentinel")
			assert.Equal(t, false, p["canSearchNotes"], "one malformed contribution must discard valid output and restore every declared key")
			assert.Equal(t, role.DefaultPolicies()[tt.nativeKey], p[tt.nativeKey])
		})
	}
}

func TestEffectivePolicy_DuplicateKeyOrderFailsProviderClosed(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "p", []string{"canSearchNotes", "canSearchUsers"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{
			{Key: "canSearchNotes", Priority: 2, Value: true, Order: 4},
			{Key: "canSearchNotes", Priority: 1, Value: false, Order: 4},
		}, nil
	})

	p, err := svc.GetUserPoliciesChecked("u1")
	require.Equal(t, role.ErrEffectivePolicyProvider, err)
	assert.Equal(t, false, p["canSearchNotes"])
	assert.Equal(t, role.DefaultPolicies()["canSearchUsers"], p["canSearchUsers"], "duplicate ties must restore every declared key")
}

// --- provider failure: native-only keys + checked sentinel ---

func TestEffectivePolicy_ProviderErrorCheckedRestoresDeclaredKeys(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "p", []string{"canSearchUsers", "driveCapacityMb", "chatAvailability", "uploadableFileTypes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return nil, errors.New("provider exploded")
	})
	p, err := svc.GetUserPoliciesChecked("u1")
	require.ErrorIs(t, err, role.ErrEffectivePolicyProvider)
	for _, key := range []string{"canSearchUsers", "driveCapacityMb", "chatAvailability", "uploadableFileTypes"} {
		assert.Equal(t, role.DefaultPolicies()[key], p[key])
	}
}

func TestEffectivePolicy_ProviderPanicCheckedRestoresDeclaredKeys(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "p", []string{"canSearchUsers", "driveCapacityMb"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		panic("secret provider panic")
	})

	var p map[string]any
	var err error
	require.NotPanics(t, func() {
		p, err = svc.GetUserPoliciesChecked("u1")
	})
	require.Equal(t, role.ErrEffectivePolicyProvider, err)
	assert.Equal(t, role.DefaultPolicies()["canSearchUsers"], p["canSearchUsers"])
	assert.Equal(t, role.DefaultPolicies()["driveCapacityMb"], p["driveCapacityMb"])
}

func TestEffectivePolicy_CooperativeTimeoutFallsBackToNative(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "slow", []string{"canSearchNotes"}, func(ctx context.Context, _ plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	started := time.Now()
	policies, err := svc.GetUserPoliciesChecked("")
	require.ErrorIs(t, err, role.ErrEffectivePolicyProvider)
	assert.Less(t, time.Since(started), 2*time.Second)
	assert.Equal(t, false, policies["canSearchNotes"])
}

func TestEffectivePolicy_TimeoutDisablesProviderAndBoundsHang(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	var calls atomic.Int32
	registerProvider(t, svc, "hung", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		calls.Add(1)
		<-block
		return nil, nil
	})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			policies, err := svc.GetUserPoliciesChecked("u1")
			assert.ErrorIs(t, err, role.ErrEffectivePolicyProvider)
			assert.Equal(t, false, policies["canSearchNotes"])
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("policy resolution did not time out")
	}
	assert.Equal(t, int32(1), calls.Load())

	started := time.Now()
	_, err := svc.GetUserPoliciesChecked("u2")
	assert.ErrorIs(t, err, role.ErrEffectivePolicyProvider)
	assert.Less(t, time.Since(started), 250*time.Millisecond)
	assert.Equal(t, int32(1), calls.Load())
}

func TestEffectivePolicy_ProviderErrorUncheckedRestoresOnlyDeclared(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "p", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return nil, errors.New("provider exploded")
	})
	// unchecked form returns a safe map only, failed keys native, unrelated keys untouched
	p := svc.GetUserPolicies("u1")
	assert.Equal(t, false, p["canSearchNotes"], "declared key restored to native")
	assert.Equal(t, true, p["canSearchUsers"], "undeclared key untouched (default true)")
	assert.Equal(t, 100, p["driveCapacityMb"], "undeclared numeric key untouched")
}

func TestEffectivePolicy_FailedProviderKeysUseExactNativePolicy(t *testing.T) {
	tests := []struct {
		name    string
		resolve plugin.EffectivePolicyResolver
	}{
		{
			name: "resolver error",
			resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
				return nil, errors.New("secret provider error")
			},
		},
		{
			name: "resolver panic",
			resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
				panic("secret provider panic")
			},
		},
		{
			name: "malformed output",
			resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
				return []plugin.EffectivePolicyContribution{{Key: "maxFileSizeMb", Priority: 2, Value: "not a number"}}, nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, roleRepo, assignRepo, metaRepo := newTestService(t)
			svc.SetServerMaxFileSizeMb(100)
			metaRepo.Meta = &model.Meta{
				ID:                               "x",
				ChunkedUploadMaxSessionsPerUser:  50,
				ChunkedUploadMaxPendingMbPerUser: 500,
			}
			roleRepo.Roles["restrictive"] = &model.Role{
				ID: "restrictive",
				Policies: datatypes.JSON([]byte(`{
					"uploadableFileTypes":{"useDefault":false,"priority":1,"value":["image/png"]},
					"maxFileSizeMb":{"useDefault":false,"priority":1,"value":7},
					"chunkedUploadMaxConcurrentSessions":{"useDefault":false,"priority":1,"value":2},
					"chunkedUploadMaxPendingMb":{"useDefault":false,"priority":1,"value":11}
				}`)),
			}
			assign(t, assignRepo, "u1", "restrictive")

			affected := []string{
				"uploadableFileTypes",
				"maxFileSizeMb",
				role.PolicyChunkedUploadMaxConcurrentSessions,
				role.PolicyChunkedUploadMaxPendingMb,
			}
			allSuccessfulKeys := append(append([]string(nil), affected...), "canSearchNotes")
			permissive := func(name string, calls *[]string) plugin.EffectivePolicyResolver {
				return func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
					*calls = append(*calls, name)
					return []plugin.EffectivePolicyContribution{
						{Key: "uploadableFileTypes", Priority: 2, Value: []string{"*"}},
						{Key: "maxFileSizeMb", Priority: 2, Value: 90},
						{Key: role.PolicyChunkedUploadMaxConcurrentSessions, Priority: 2, Value: 40},
						{Key: role.PolicyChunkedUploadMaxPendingMb, Priority: 2, Value: 400},
						{Key: "canSearchNotes", Priority: 2, Value: true},
					}, nil
				}
			}

			var calls []string
			registerProvider(t, svc, "zulu", allSuccessfulKeys, permissive("zulu", &calls))
			registerProvider(t, svc, "bravo", affected, func(ctx context.Context, req plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
				calls = append(calls, "bravo")
				return tt.resolve(ctx, req)
			})
			registerProvider(t, svc, "alpha", allSuccessfulKeys, permissive("alpha", &calls))

			assertSafe := func(policies map[string]any) {
				t.Helper()
				assert.Equal(t, []string{"image/png"}, policies["uploadableFileTypes"], "empty means allow-all to the upload consumer")
				assert.Equal(t, 7, policies["maxFileSizeMb"], "zero disables the upload-size gate and the final cap would grant 100")
				assert.Equal(t, 2, policies[role.PolicyChunkedUploadMaxConcurrentSessions], "zero disables the session gate and the final cap would grant 50")
				assert.Equal(t, 11, policies[role.PolicyChunkedUploadMaxPendingMb], "zero disables the pending-size gate and the final cap would grant 500")
				assert.Equal(t, true, policies["canSearchNotes"], "unaffected successful keys remain applied")
			}

			checked, err := svc.GetUserPoliciesChecked("u1")
			require.Equal(t, role.ErrEffectivePolicyProvider, err)
			assertSafe(checked)
			assert.Equal(t, []string{"alpha", "bravo", "zulu"}, calls)

			calls = nil
			assertSafe(svc.GetUserPolicies("u1"))
			assert.Equal(t, []string{"alpha", "bravo", "zulu"}, calls)
		})
	}
}

func TestEffectivePolicy_FailedProviderRestoredSliceMutationIsIsolated(t *testing.T) {
	want := []string{"text/*", "application/json", "image/*", "video/*", "audio/*"}
	shared := role.DefaultPolicies()["uploadableFileTypes"].([]string)
	original := append([]string(nil), shared...)
	defer copy(shared, original)

	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "failing", []string{"uploadableFileTypes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return nil, errors.New("secret provider error")
	})

	policies, err := svc.GetUserPoliciesChecked("u1")
	require.Equal(t, role.ErrEffectivePolicyProvider, err)
	policies["uploadableFileTypes"].([]string)[0] = "application/x-corrupt"

	assert.Equal(t, want, role.DefaultPolicies()["uploadableFileTypes"])
	next, err := svc.GetUserPoliciesChecked("u1")
	require.Equal(t, role.ErrEffectivePolicyProvider, err)
	assert.Equal(t, want, next["uploadableFileTypes"])
	assert.Equal(t, want, role.DefaultPoliciesClone()["uploadableFileTypes"])
}

func TestEffectivePolicy_ProviderSliceDoesNotAliasResolverValue(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	resolverValue := []string{"image/png"}
	registerProvider(t, svc, "provider", []string{"uploadableFileTypes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{{Key: "uploadableFileTypes", Priority: 2, Value: resolverValue}}, nil
	})

	policies, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	resolved := policies["uploadableFileTypes"].([]string)
	resolverValue[0] = "application/x-resolver-corrupt"
	assert.Equal(t, []string{"image/png"}, resolved)

	resolved[0] = "application/x-result-corrupt"
	assert.Equal(t, []string{"application/x-resolver-corrupt"}, resolverValue)
}

func TestEffectivePolicy_NoStaleSuccessReuse(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	calls := 0
	registerProvider(t, svc, "p", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		calls++
		if calls == 1 {
			return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: true}}, nil
		}
		return nil, errors.New("now failing")
	})

	first, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, true, first["canSearchNotes"], "first (successful) call is permissive")

	second, err := svc.GetUserPoliciesChecked("u1")
	require.ErrorIs(t, err, role.ErrEffectivePolicyProvider)
	assert.Equal(t, false, second["canSearchNotes"], "failure must not reuse the earlier permissive value")
}

func TestEffectivePolicy_AnonymousInvokesProvidersWithoutRepositoryLookup(t *testing.T) {
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	// repositoryをnilにして、匿名解決がrole、assignment、userを検索すればpanicさせる。
	svc := role.NewService(nil, nil, nil, idGen)

	var requests []plugin.EffectivePolicyRequest
	for _, name := range []string{"bravo", "alpha"} {
		registerProvider(t, svc, name, []string{"canSearchNotes"}, func(_ context.Context, req plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
			requests = append(requests, req)
			return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: true}}, nil
		})
	}

	policies, err := svc.GetUserPoliciesChecked("")
	require.NoError(t, err)
	assert.Equal(t, true, policies["canSearchNotes"])
	require.Len(t, requests, 2)
	for _, req := range requests {
		assert.Equal(t, "", req.UserID)
		assert.NotNil(t, req.RoleIDs)
		assert.Empty(t, req.RoleIDs)
	}
}

func TestEffectivePolicy_AnonymousUsesNativeBaselineOrderingCloningAndCaps(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	svc.SetServerMaxFileSizeMb(100)
	metaRepo.Meta = &model.Meta{
		ID:       "x",
		Policies: datatypes.JSON([]byte(`{"canSearchUsers":false}`)),
	}
	resolverValue := []string{"image/png"}
	var calls []string
	registerProvider(t, svc, "zulu", []string{"canSearchNotes", "maxFileSizeMb", "uploadableFileTypes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		calls = append(calls, "zulu")
		return []plugin.EffectivePolicyContribution{
			{Key: "canSearchNotes", Priority: 1, Value: true},
			{Key: "maxFileSizeMb", Priority: 2, Value: 500},
			{Key: "uploadableFileTypes", Priority: 2, Value: resolverValue},
		}, nil
	})
	registerProvider(t, svc, "alpha", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		calls = append(calls, "alpha")
		return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: false}}, nil
	})

	policies, err := svc.GetUserPoliciesChecked("")
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "zulu"}, calls)
	assert.Equal(t, false, policies["canSearchNotes"], "higher-priority anonymous contribution wins")
	assert.Equal(t, false, policies["canSearchUsers"], "meta-overlaid anonymous baseline is preserved")
	assert.Equal(t, 100, policies["maxFileSizeMb"], "instance caps apply after anonymous contributions")
	assert.Equal(t, []string{"image/png"}, policies["uploadableFileTypes"])

	resolverValue[0] = "application/x-resolver-corrupt"
	resolved := policies["uploadableFileTypes"].([]string)
	assert.Equal(t, []string{"image/png"}, resolved)
	resolved[0] = "application/x-result-corrupt"
	next, err := svc.GetUserPoliciesChecked("")
	require.NoError(t, err)
	assert.Equal(t, []string{"application/x-resolver-corrupt"}, next["uploadableFileTypes"])
}

func TestEffectivePolicy_AnonymousProviderFailuresUseNativeFallback(t *testing.T) {
	tests := []struct {
		name    string
		resolve plugin.EffectivePolicyResolver
	}{
		{name: "error", resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
			return nil, errors.New("secret provider error")
		}},
		{name: "panic", resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
			panic("secret provider panic")
		}},
		{name: "malformed", resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
			return []plugin.EffectivePolicyContribution{{Key: "uploadableFileTypes", Priority: 2, Value: []any{"image/png", 7}}}, nil
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _, _ := newTestService(t)
			registerProvider(t, svc, "successful", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
				return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: true}}, nil
			})
			registerProvider(t, svc, "failing", []string{"uploadableFileTypes"}, tt.resolve)

			policies, err := svc.GetUserPoliciesChecked("")
			require.Equal(t, role.ErrEffectivePolicyProvider, err)
			assert.Equal(t, role.DefaultPolicies()["uploadableFileTypes"], policies["uploadableFileTypes"])
			assert.Equal(t, true, policies["canSearchNotes"], "unaffected anonymous contributions remain")

			unchecked := svc.GetUserPolicies("")
			assert.Equal(t, role.DefaultPolicies()["uploadableFileTypes"], unchecked["uploadableFileTypes"])
			assert.Equal(t, true, unchecked["canSearchNotes"])
		})
	}
}

func TestEffectivePolicy_AnonymousDoesNotReuseStaleSuccess(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	calls := 0
	registerProvider(t, svc, "p", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		calls++
		if calls == 1 {
			return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: true}}, nil
		}
		return nil, errors.New("now failing")
	})

	first, err := svc.GetUserPoliciesChecked("")
	require.NoError(t, err)
	assert.Equal(t, true, first["canSearchNotes"])

	second, err := svc.GetUserPoliciesChecked("")
	require.Equal(t, role.ErrEffectivePolicyProvider, err)
	assert.Equal(t, false, second["canSearchNotes"])
	assert.Equal(t, 2, calls, "anonymous provider output must not be cached")
}

// --- server caps are applied last ---

func TestEffectivePolicy_ServerCapAppliedAfterProvider(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	svc.SetServerMaxFileSizeMb(100)
	registerProvider(t, svc, "p", []string{"maxFileSizeMb"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{{Key: "maxFileSizeMb", Priority: 0, Value: 500}}, nil
	})
	p, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, 100, p["maxFileSizeMb"], "server cap clamps the provider-raised value last")
}

func TestEffectivePolicy_ChunkedUploadMetaFailureLeavesNativeBaselineWithoutError(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	svc.SetServerMaxFileSizeMb(20)

	checked, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err, "meta cap failure must not leak through the checked provider error contract")
	assert.Equal(t, true, checked[role.PolicyCanUseChunkedUpload])
	assert.Equal(t, 20, checked["maxFileSizeMb"], "the independent server file-size cap still applies")
	assert.Equal(t, 4, checked[role.PolicyChunkedUploadMaxConcurrentSessions], "an unknown cap must not synthesize a numeric limit")
	assert.Equal(t, 1024, checked[role.PolicyChunkedUploadMaxPendingMb])

	unchecked := svc.GetUserPolicies("u1")
	assert.Equal(t, true, unchecked[role.PolicyCanUseChunkedUpload])
	assert.Equal(t, 20, unchecked["maxFileSizeMb"])
}

func TestEffectivePolicy_ChunkedUploadMetaFailureLeavesOrderedProviderValuesUncapped(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	keys := []string{
		role.PolicyCanUseChunkedUpload,
		role.PolicyChunkedUploadMaxConcurrentSessions,
		role.PolicyChunkedUploadMaxPendingMb,
	}
	registerProvider(t, svc, "zulu", keys, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{
			{Key: role.PolicyCanUseChunkedUpload, Priority: 2, Value: true},
			{Key: role.PolicyChunkedUploadMaxConcurrentSessions, Priority: 2, Value: math.MaxInt},
			{Key: role.PolicyChunkedUploadMaxPendingMb, Priority: 2, Value: -1},
		}, nil
	})
	registerProvider(t, svc, "alpha", keys, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{
			{Key: role.PolicyCanUseChunkedUpload, Priority: 0, Value: true},
			{Key: role.PolicyChunkedUploadMaxConcurrentSessions, Priority: 0, Value: 99},
			{Key: role.PolicyChunkedUploadMaxPendingMb, Priority: 0, Value: 99},
		}, nil
	})

	checked, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, true, checked[role.PolicyCanUseChunkedUpload])
	assert.Equal(t, math.MaxInt, checked[role.PolicyChunkedUploadMaxConcurrentSessions])
	assert.Equal(t, -1, checked[role.PolicyChunkedUploadMaxPendingMb])

	unchecked := svc.GetUserPolicies("u1")
	assert.Equal(t, true, unchecked[role.PolicyCanUseChunkedUpload])
	assert.Equal(t, math.MaxInt, unchecked[role.PolicyChunkedUploadMaxConcurrentSessions])
	assert.Equal(t, -1, unchecked[role.PolicyChunkedUploadMaxPendingMb])
}

func TestEffectivePolicy_ChunkedUploadLoadedMetaCapsProviderValues(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	metaRepo.Meta = &model.Meta{
		ID:                               "x",
		ChunkedUploadMaxSessionsPerUser:  3,
		ChunkedUploadMaxPendingMbPerUser: 96,
	}
	keys := []string{
		role.PolicyCanUseChunkedUpload,
		role.PolicyChunkedUploadMaxConcurrentSessions,
		role.PolicyChunkedUploadMaxPendingMb,
	}
	registerProvider(t, svc, "provider", keys, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{
			{Key: role.PolicyCanUseChunkedUpload, Priority: 2, Value: true},
			{Key: role.PolicyChunkedUploadMaxConcurrentSessions, Priority: 2, Value: math.MaxInt},
			{Key: role.PolicyChunkedUploadMaxPendingMb, Priority: 2, Value: -1},
		}, nil
	})

	policies, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, true, policies[role.PolicyCanUseChunkedUpload])
	assert.Equal(t, 3, policies[role.PolicyChunkedUploadMaxConcurrentSessions])
	assert.Equal(t, 96, policies[role.PolicyChunkedUploadMaxPendingMb])
}

func TestEffectivePolicy_NegativeUnlimitedCannotBypassPositiveInstanceCaps(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	svc.SetServerMaxFileSizeMb(100)
	metaRepo.Meta = &model.Meta{
		ID:                               "x",
		ChunkedUploadMaxSessionsPerUser:  2,
		ChunkedUploadMaxPendingMbPerUser: 64,
	}
	keys := []string{"maxFileSizeMb", role.PolicyChunkedUploadMaxConcurrentSessions, role.PolicyChunkedUploadMaxPendingMb}
	registerProvider(t, svc, "p", keys, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{
			{Key: "maxFileSizeMb", Priority: 2, Value: -1},
			{Key: role.PolicyChunkedUploadMaxConcurrentSessions, Priority: 2, Value: -1},
			{Key: role.PolicyChunkedUploadMaxPendingMb, Priority: 2, Value: -1},
		}, nil
	})

	policies, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, 100, policies["maxFileSizeMb"])
	assert.Equal(t, 2, policies[role.PolicyChunkedUploadMaxConcurrentSessions])
	assert.Equal(t, 64, policies[role.PolicyChunkedUploadMaxPendingMb])
}

func TestEffectivePolicy_ZeroUnlimitedCannotBypassPositiveInstanceCaps(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	svc.SetServerMaxFileSizeMb(100)
	metaRepo.Meta = &model.Meta{
		ID:                               "x",
		ChunkedUploadMaxSessionsPerUser:  2,
		ChunkedUploadMaxPendingMbPerUser: 64,
	}
	keys := []string{"maxFileSizeMb", role.PolicyChunkedUploadMaxConcurrentSessions, role.PolicyChunkedUploadMaxPendingMb}
	registerProvider(t, svc, "p", keys, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{
			{Key: "maxFileSizeMb", Priority: 2, Value: 0},
			{Key: role.PolicyChunkedUploadMaxConcurrentSessions, Priority: 2, Value: 0},
			{Key: role.PolicyChunkedUploadMaxPendingMb, Priority: 2, Value: 0},
		}, nil
	})

	policies, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, 100, policies["maxFileSizeMb"])
	assert.Equal(t, 2, policies[role.PolicyChunkedUploadMaxConcurrentSessions])
	assert.Equal(t, 64, policies[role.PolicyChunkedUploadMaxPendingMb])
}

func TestEffectivePolicy_NegativeUnlimitedSurvivesExplicitlyDisabledInstanceCaps(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	svc.SetServerMaxFileSizeMb(0)
	metaRepo.Meta = &model.Meta{ID: "x"}
	keys := []string{"maxFileSizeMb", role.PolicyChunkedUploadMaxConcurrentSessions, role.PolicyChunkedUploadMaxPendingMb}
	registerProvider(t, svc, "p", keys, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{
			{Key: "maxFileSizeMb", Priority: 2, Value: -1},
			{Key: role.PolicyChunkedUploadMaxConcurrentSessions, Priority: 2, Value: -1},
			{Key: role.PolicyChunkedUploadMaxPendingMb, Priority: 2, Value: -1},
		}, nil
	})

	policies, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, -1, policies["maxFileSizeMb"])
	assert.Equal(t, -1, policies[role.PolicyChunkedUploadMaxConcurrentSessions])
	assert.Equal(t, -1, policies[role.PolicyChunkedUploadMaxPendingMb])
}

func TestEffectivePolicy_PositiveValuesBelowInstanceCapsRemainUnchanged(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	svc.SetServerMaxFileSizeMb(100)
	metaRepo.Meta = &model.Meta{
		ID:                               "x",
		ChunkedUploadMaxSessionsPerUser:  8,
		ChunkedUploadMaxPendingMbPerUser: 512,
	}
	keys := []string{"maxFileSizeMb", role.PolicyChunkedUploadMaxConcurrentSessions, role.PolicyChunkedUploadMaxPendingMb}
	registerProvider(t, svc, "p", keys, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{
			{Key: "maxFileSizeMb", Priority: 2, Value: 50},
			{Key: role.PolicyChunkedUploadMaxConcurrentSessions, Priority: 2, Value: 4},
			{Key: role.PolicyChunkedUploadMaxPendingMb, Priority: 2, Value: 256},
		}, nil
	})

	policies, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, 50, policies["maxFileSizeMb"])
	assert.Equal(t, 4, policies[role.PolicyChunkedUploadMaxConcurrentSessions])
	assert.Equal(t, 256, policies[role.PolicyChunkedUploadMaxPendingMb])
}

// --- invalidation ---

func TestInvalidateUser_DropsUserPolicyCache(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "A", Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":true}}`))}
	assign(t, assignRepo, "u1", "r1")
	assert.Equal(t, true, svc.GetUserPolicies("u1")["canSearchNotes"])

	// out-of-band role change + explicit user invalidation
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "A", Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":false}}`))}
	require.NoError(t, svc.InvalidateUser(context.Background(), "u1"))
	assert.Equal(t, false, svc.GetUserPolicies("u1")["canSearchNotes"], "InvalidateUser must drop the cached policy input")
}

type blockingAssignmentRepo struct {
	*testutil.MockRoleAssignmentRepository

	mu         sync.Mutex
	calls      int
	firstRoles []*model.Role
	nextRoles  []*model.Role
	entered    chan struct{}
	release    chan struct{}
}

func (r *blockingAssignmentRepo) ListByUser(userID string) ([]*model.RoleAssignment, error) {
	r.mu.Lock()
	r.calls++
	first := r.calls == 1
	roles := append([]*model.Role(nil), r.nextRoles...)
	if first {
		roles = append([]*model.Role(nil), r.firstRoles...)
	}
	r.mu.Unlock()

	if first {
		close(r.entered)
		<-r.release
	}
	assignments := make([]*model.RoleAssignment, 0, len(roles))
	for i, resolvedRole := range roles {
		assignments = append(assignments, &model.RoleAssignment{
			ID:     fmt.Sprintf("a%d", i),
			UserID: userID,
			RoleID: resolvedRole.ID,
			Role:   resolvedRole,
		})
	}
	return assignments, nil
}

func TestInvalidateUser_InFlightMissCannotRepublishStaleRoles(t *testing.T) {
	roleRepo := testutil.NewMockRoleRepository()
	oldRole := &model.Role{ID: "r1", Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":true}}`))}
	freshRole := &model.Role{ID: "r1", Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":false}}`))}
	assignRepo := &blockingAssignmentRepo{
		MockRoleAssignmentRepository: testutil.NewMockRoleAssignmentRepository(roleRepo),
		firstRoles:                   []*model.Role{oldRole},
		nextRoles:                    []*model.Role{freshRole},
		entered:                      make(chan struct{}),
		release:                      make(chan struct{}),
	}
	metaRepo := testutil.NewMockMetaRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)

	firstResult := make(chan map[string]any, 1)
	go func() {
		firstResult <- svc.GetUserPolicies("u1")
	}()
	<-assignRepo.entered
	require.NoError(t, svc.InvalidateUser(context.Background(), "u1"))
	close(assignRepo.release)
	assert.Equal(t, true, (<-firstResult)["canSearchNotes"])

	assert.Equal(t, false, svc.GetUserPolicies("u1")["canSearchNotes"], "the pre-invalidation miss must not republish its stale snapshot")
}

func TestInvalidateUser_PreservesUnrelatedCachedUser(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":true}}`))}
	assign(t, assignRepo, "u2", "r1")
	assert.Equal(t, true, svc.GetUserPolicies("u2")["canSearchNotes"])

	delete(assignRepo.Assignments, "u2:r1")
	require.NoError(t, svc.InvalidateUser(context.Background(), "u1"))

	assert.Equal(t, true, svc.GetUserPolicies("u2")["canSearchNotes"], "a user invalidation must not evict unrelated completed entries")
}

func TestInvalidateRolePolicies_DropsAllHolders(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "A", Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":true}}`))}
	assign(t, assignRepo, "u1", "r1")
	assign(t, assignRepo, "u2", "r1")
	roleRepo.Roles["r2"] = &model.Role{ID: "r2", Name: "B"}
	assign(t, assignRepo, "u3", "r2")

	assert.Equal(t, true, svc.GetUserPolicies("u1")["canSearchNotes"])
	assert.Equal(t, true, svc.GetUserPolicies("u2")["canSearchNotes"])

	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "A", Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":false}}`))}
	assert.Equal(t, true, svc.GetUserPolicies("u1")["canSearchNotes"], "repository replacement must remain hidden until invalidation")
	require.NoError(t, svc.InvalidateRolePolicies(context.Background(), "r1"))
	assert.Equal(t, false, svc.GetUserPolicies("u1")["canSearchNotes"])
	assert.Equal(t, false, svc.GetUserPolicies("u2")["canSearchNotes"])
}

func TestInvalidateRolePolicies_DropsCachedConditionalHolder(t *testing.T) {
	svc, roleRepo, _, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{
		ID:          "r1",
		Target:      model.RoleTargetConditional,
		CondFormula: datatypes.JSON([]byte(`{"type":"isBot"}`)),
		Policies:    datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":true}}`)),
	}
	userRepo := testutil.NewMockUserRepository()
	require.NoError(t, userRepo.Create(&model.User{ID: "u1", Username: "bot", IsBot: true}))
	svc.SetUserRepo(userRepo)
	assert.Equal(t, true, svc.GetUserPolicies("u1")["canSearchNotes"])

	roleRepo.Roles["r1"] = &model.Role{
		ID:          "r1",
		Target:      model.RoleTargetConditional,
		CondFormula: datatypes.JSON([]byte(`{"type":"isBot"}`)),
		Policies:    datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":false}}`)),
	}
	require.NoError(t, svc.InvalidateRolePolicies(context.Background(), "r1"))

	assert.Equal(t, false, svc.GetUserPolicies("u1")["canSearchNotes"], "conditional holders are not enumerable from role assignments")
}

type blockingRoleListRepo struct {
	*testutil.MockRoleRepository

	mu      sync.Mutex
	calls   int
	current []*model.Role
	entered chan struct{}
	release chan struct{}
}

func (r *blockingRoleListRepo) List() ([]*model.Role, error) {
	r.mu.Lock()
	r.calls++
	first := r.calls == 1
	roles := append([]*model.Role(nil), r.current...)
	r.mu.Unlock()
	if first {
		close(r.entered)
		<-r.release
	}
	return roles, nil
}

func (r *blockingRoleListRepo) setRoles(roles ...*model.Role) {
	r.mu.Lock()
	r.current = append([]*model.Role(nil), roles...)
	r.mu.Unlock()
}

func TestInvalidateRolePolicies_InFlightConditionalSnapshotCannotRepublish(t *testing.T) {
	oldRole := &model.Role{
		ID:          "r1",
		Target:      model.RoleTargetConditional,
		CondFormula: datatypes.JSON([]byte(`{"type":"isBot"}`)),
		Policies:    datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":true}}`)),
	}
	freshRole := &model.Role{
		ID:          "r1",
		Target:      model.RoleTargetConditional,
		CondFormula: datatypes.JSON([]byte(`{"type":"isBot"}`)),
		Policies:    datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":false}}`)),
	}
	baseRoleRepo := testutil.NewMockRoleRepository()
	roleRepo := &blockingRoleListRepo{
		MockRoleRepository: baseRoleRepo,
		current:            []*model.Role{oldRole},
		entered:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	assignRepo := testutil.NewMockRoleAssignmentRepository(baseRoleRepo)
	metaRepo := testutil.NewMockMetaRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	userRepo := testutil.NewMockUserRepository()
	require.NoError(t, userRepo.Create(&model.User{ID: "u1", Username: "bot", IsBot: true}))
	svc.SetUserRepo(userRepo)

	firstResult := make(chan map[string]any, 1)
	go func() {
		firstResult <- svc.GetUserPolicies("u1")
	}()
	<-roleRepo.entered
	roleRepo.setRoles(freshRole)
	require.NoError(t, svc.InvalidateRolePolicies(context.Background(), "r1"))
	close(roleRepo.release)
	assert.Equal(t, true, (<-firstResult)["canSearchNotes"])

	assert.Equal(t, false, svc.GetUserPolicies("u1")["canSearchNotes"], "the in-flight conditional snapshot must not survive invalidation")
}

func TestInvalidateRolePolicies_EmptyRoleNoop(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	require.NoError(t, svc.InvalidateRolePolicies(context.Background(), ""))
}
