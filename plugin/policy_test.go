package plugin

import (
	"context"
	"math"
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

func TestEffectivePolicyDefaultsIsolation(t *testing.T) {
	first := EffectivePolicyDefaults()
	first["canSearchNotes"] = true
	first["uploadableFileTypes"].([]string)[0] = "changed"

	second := EffectivePolicyDefaults()
	assert.Equal(t, false, second["canSearchNotes"])
	assert.Equal(t, "text/*", second["uploadableFileTypes"].([]string)[0])
}

func TestValidateEffectivePolicyRegistrationRejectsUnknownNativeKey(t *testing.T) {
	reg := EffectivePolicyRegistration{
		Keys: []string{"noSuchPolicyKey"},
		Resolve: func(context.Context, EffectivePolicyRequest) ([]EffectivePolicyContribution, error) {
			return nil, nil
		},
	}
	require.Error(t, ValidateEffectivePolicyRegistration(reg))
}

func TestValidateEffectivePolicyContributions(t *testing.T) {
	tests := []struct {
		name          string
		keys          []string
		contributions []EffectivePolicyContribution
		valid         bool
	}{
		{name: "valid bool", keys: []string{"canSearchNotes"}, contributions: []EffectivePolicyContribution{{Key: "canSearchNotes", Value: true}}, valid: true},
		{name: "wrong type", keys: []string{"canSearchNotes"}, contributions: []EffectivePolicyContribution{{Key: "canSearchNotes", Value: "true"}}},
		{name: "undeclared key", keys: []string{"canSearchNotes"}, contributions: []EffectivePolicyContribution{{Key: "canSearchUsers", Value: true}}},
		{name: "duplicate order", keys: []string{"canSearchNotes"}, contributions: []EffectivePolicyContribution{{Key: "canSearchNotes", Value: true}, {Key: "canSearchNotes", Value: false}}},
		{name: "invalid priority", keys: []string{"canSearchNotes"}, contributions: []EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 3, Value: true}}},
		{name: "invalid enum", keys: []string{"chatAvailability"}, contributions: []EffectivePolicyContribution{{Key: "chatAvailability", Value: "hidden"}}},
		{name: "invalid string list", keys: []string{"uploadableFileTypes"}, contributions: []EffectivePolicyContribution{{Key: "uploadableFileTypes", Value: []any{"image/*", 1}}}},
		{name: "invalid number", keys: []string{"mentionLimit"}, contributions: []EffectivePolicyContribution{{Key: "mentionLimit", Value: math.NaN()}}},
		{name: "use default", keys: []string{"canSearchNotes"}, contributions: []EffectivePolicyContribution{{Key: "canSearchNotes", UseDefault: true, Value: "ignored"}}, valid: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.valid, ValidateEffectivePolicyContributions(tc.keys, tc.contributions))
		})
	}
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
