package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefinitionValidate_PolicyOnly(t *testing.T) {
	definition := Definition{
		Name: "policy-only", APIVersion: APIVersion,
		EffectivePolicies: func(Context, EffectivePolicyInvalidator) (EffectivePolicyRegistration, error) {
			return EffectivePolicyRegistration{}, nil
		},
	}
	require.NoError(t, definition.Validate())
}

func TestEffectivePolicyRegistrationValidate(t *testing.T) {
	resolver := EffectivePolicyResolver(func(context.Context, EffectivePolicyRequest) ([]EffectivePolicyContribution, error) {
		return nil, nil
	})
	tests := []struct {
		name string
		reg  EffectivePolicyRegistration
		want string
	}{
		{name: "valid", reg: EffectivePolicyRegistration{Keys: []string{"a", "b"}, Resolve: resolver}},
		{name: "nil resolver", reg: EffectivePolicyRegistration{Keys: []string{"a"}}, want: "Resolve が nil"},
		{name: "empty keys", reg: EffectivePolicyRegistration{Resolve: resolver}, want: "Keys が空"},
		{name: "empty key", reg: EffectivePolicyRegistration{Keys: []string{""}, Resolve: resolver}, want: "空のキー"},
		{name: "duplicate", reg: EffectivePolicyRegistration{Keys: []string{"a", "a"}, Resolve: resolver}, want: "重複"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.reg.Validate()
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
