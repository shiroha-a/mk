package plugintest_test

import (
	"context"
	"testing"

	"github.com/shiroha-a/mk/plugin"
	"github.com/shiroha-a/mk/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type policyInvalidatorRecorder struct{ users []string }

func (r *policyInvalidatorRecorder) InvalidateUser(_ context.Context, userID string) error {
	r.users = append(r.users, userID)
	return nil
}
func (r *policyInvalidatorRecorder) InvalidateRole(context.Context, string) error { return nil }

func TestEffectivePoliciesCapture(t *testing.T) {
	keys := []string{"canSearchNotes"}
	roles := []string{"role-a"}
	recorder := &policyInvalidatorRecorder{}
	definition := plugin.Definition{
		Name: "policy", APIVersion: plugin.APIVersion,
		EffectivePolicies: func(_ plugin.Context, invalidator plugin.EffectivePolicyInvalidator) (plugin.EffectivePolicyRegistration, error) {
			require.NoError(t, invalidator.InvalidateUser(context.Background(), "user"))
			return plugin.EffectivePolicyRegistration{
				Keys: keys,
				Resolve: func(_ context.Context, req plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
					req.RoleIDs[0] = "mutated"
					return nil, nil
				},
			}, nil
		},
	}
	registration := plugintest.New(t).WithEffectivePolicyInvalidator(recorder).EffectivePolicies(definition)
	keys[0] = "changed"
	assert.Equal(t, []string{"canSearchNotes"}, registration.Keys)
	require.NoError(t, registration.Validate())
	_, err := registration.Resolve(context.Background(), plugin.EffectivePolicyRequest{RoleIDs: roles})
	require.NoError(t, err)
	assert.Equal(t, []string{"role-a"}, roles)
	assert.Equal(t, []string{"user"}, recorder.users)
}

func TestEffectivePoliciesPreservesNilResolver(t *testing.T) {
	definition := plugin.Definition{
		Name: "policy", APIVersion: plugin.APIVersion,
		EffectivePolicies: func(plugin.Context, plugin.EffectivePolicyInvalidator) (plugin.EffectivePolicyRegistration, error) {
			return plugin.EffectivePolicyRegistration{Keys: []string{"canSearchNotes"}}, nil
		},
	}
	registration := plugintest.New(t).EffectivePolicies(definition)
	assert.Nil(t, registration.Resolve)
	require.Error(t, registration.Validate())
}
