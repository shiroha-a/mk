package plugintest_test

import (
	"context"
	"os"
	"os/exec"
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

func TestEffectivePoliciesRejectsInvalidRegistration(t *testing.T) {
	definition := plugin.Definition{
		Name: "policy", APIVersion: plugin.APIVersion,
		EffectivePolicies: func(plugin.Context, plugin.EffectivePolicyInvalidator) (plugin.EffectivePolicyRegistration, error) {
			return plugin.EffectivePolicyRegistration{Keys: []string{"canSearchNotes"}}, nil
		},
	}
	if os.Getenv("MK_PLUGINTEST_INVALID_REGISTRATION_CHILD") == "1" {
		plugintest.New(t).EffectivePolicies(definition)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestEffectivePoliciesRejectsInvalidRegistration$")
	cmd.Env = append(os.Environ(), "MK_PLUGINTEST_INVALID_REGISTRATION_CHILD=1")
	output, err := cmd.CombinedOutput()
	require.Error(t, err, "an invalid registration must fail the harness test process")
	assert.Contains(t, string(output), "EffectivePolicies の登録が不正です")
}

func TestEffectivePoliciesRejectsUnknownNativeKey(t *testing.T) {
	definition := plugin.Definition{
		Name: "policy", APIVersion: plugin.APIVersion,
		EffectivePolicies: func(plugin.Context, plugin.EffectivePolicyInvalidator) (plugin.EffectivePolicyRegistration, error) {
			return plugin.EffectivePolicyRegistration{
				Keys: []string{"noSuchPolicyKey"},
				Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
					return nil, nil
				},
			}, nil
		},
	}
	if os.Getenv("MK_PLUGINTEST_UNKNOWN_POLICY_KEY_CHILD") == "1" {
		plugintest.New(t).EffectivePolicies(definition)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestEffectivePoliciesRejectsUnknownNativeKey$")
	cmd.Env = append(os.Environ(), "MK_PLUGINTEST_UNKNOWN_POLICY_KEY_CHILD=1")
	output, err := cmd.CombinedOutput()
	require.Error(t, err, "an unknown native policy key must fail the harness test process")
	assert.Contains(t, string(output), "EffectivePolicies の登録が不正です")
	assert.NotContains(t, string(output), "noSuchPolicyKey")
}

func TestEffectivePoliciesRejectsInvalidResolverOutput(t *testing.T) {
	definition := plugin.Definition{
		Name: "policy", APIVersion: plugin.APIVersion,
		EffectivePolicies: func(plugin.Context, plugin.EffectivePolicyInvalidator) (plugin.EffectivePolicyRegistration, error) {
			return plugin.EffectivePolicyRegistration{
				Keys: []string{"canSearchNotes"},
				Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
					return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: "not-a-bool"}}, nil
				},
			}, nil
		},
	}
	if os.Getenv("MK_PLUGINTEST_INVALID_POLICY_OUTPUT_CHILD") == "1" {
		registration := plugintest.New(t).EffectivePolicies(definition)
		_, _ = registration.Resolve(context.Background(), plugin.EffectivePolicyRequest{RoleIDs: []string{}})
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestEffectivePoliciesRejectsInvalidResolverOutput$")
	cmd.Env = append(os.Environ(), "MK_PLUGINTEST_INVALID_POLICY_OUTPUT_CHILD=1")
	output, err := cmd.CombinedOutput()
	require.Error(t, err, "an invalid policy output must fail the harness test process")
	assert.Contains(t, string(output), "EffectivePolicies の出力が不正です")
}
