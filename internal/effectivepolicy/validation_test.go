package effectivepolicy

import (
	"context"
	"math"
	"testing"

	"github.com/shiroha-a/mk/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultsIsolation(t *testing.T) {
	first := Defaults()
	first["canSearchNotes"] = true
	first["uploadableFileTypes"].([]string)[0] = "changed"

	second := Defaults()
	assert.Equal(t, false, second["canSearchNotes"])
	assert.Equal(t, "text/*", second["uploadableFileTypes"].([]string)[0])
}

func TestValidateRegistration(t *testing.T) {
	resolver := func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return nil, nil
	}
	require.NoError(t, ValidateRegistration(plugin.EffectivePolicyRegistration{Keys: []string{"canSearchNotes"}, Resolve: resolver}))
	require.Error(t, ValidateRegistration(plugin.EffectivePolicyRegistration{Keys: []string{"canSearchNotes"}}))
	require.Error(t, ValidateRegistration(plugin.EffectivePolicyRegistration{Keys: []string{"noSuchPolicyKey"}, Resolve: resolver}))
}

func TestValidateContributions(t *testing.T) {
	maxIntFloat := -float64(math.MinInt)
	tests := []struct {
		name          string
		keys          []string
		contributions []plugin.EffectivePolicyContribution
		valid         bool
	}{
		{name: "empty", valid: true},
		{name: "valid bool", keys: []string{"canSearchNotes"}, contributions: []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: true}}, valid: true},
		{name: "wrong bool type", keys: []string{"canSearchNotes"}, contributions: []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: "true"}}},
		{name: "undeclared key", keys: []string{"canSearchNotes"}, contributions: []plugin.EffectivePolicyContribution{{Key: "canSearchUsers", Value: true}}},
		{name: "unknown native key", keys: []string{"unknown"}, contributions: []plugin.EffectivePolicyContribution{{Key: "unknown", Value: true}}},
		{name: "duplicate order", keys: []string{"canSearchNotes"}, contributions: []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: true}, {Key: "canSearchNotes", Value: false}}},
		{name: "low priority", keys: []string{"canSearchNotes"}, contributions: []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: -1, Value: true}}},
		{name: "high priority", keys: []string{"canSearchNotes"}, contributions: []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 3, Value: true}}},
		{name: "valid string", keys: []string{"chatAvailability"}, contributions: []plugin.EffectivePolicyContribution{{Key: "chatAvailability", Value: "readonly"}}, valid: true},
		{name: "wrong string type", keys: []string{"chatAvailability"}, contributions: []plugin.EffectivePolicyContribution{{Key: "chatAvailability", Value: true}}},
		{name: "invalid enum", keys: []string{"chatAvailability"}, contributions: []plugin.EffectivePolicyContribution{{Key: "chatAvailability", Value: "hidden"}}},
		{name: "valid string slice", keys: []string{"uploadableFileTypes"}, contributions: []plugin.EffectivePolicyContribution{{Key: "uploadableFileTypes", Value: []string{"image/*"}}}, valid: true},
		{name: "blank string slice", keys: []string{"uploadableFileTypes"}, contributions: []plugin.EffectivePolicyContribution{{Key: "uploadableFileTypes", Value: []string{" "}}}},
		{name: "valid any slice", keys: []string{"uploadableFileTypes"}, contributions: []plugin.EffectivePolicyContribution{{Key: "uploadableFileTypes", Value: []any{"image/*"}}}, valid: true},
		{name: "non-string any slice", keys: []string{"uploadableFileTypes"}, contributions: []plugin.EffectivePolicyContribution{{Key: "uploadableFileTypes", Value: []any{1}}}},
		{name: "blank any slice", keys: []string{"uploadableFileTypes"}, contributions: []plugin.EffectivePolicyContribution{{Key: "uploadableFileTypes", Value: []any{" "}}}},
		{name: "wrong slice type", keys: []string{"uploadableFileTypes"}, contributions: []plugin.EffectivePolicyContribution{{Key: "uploadableFileTypes", Value: true}}},
		{name: "valid int", keys: []string{"mentionLimit"}, contributions: []plugin.EffectivePolicyContribution{{Key: "mentionLimit", Value: 1}}, valid: true},
		{name: "valid int64", keys: []string{"mentionLimit"}, contributions: []plugin.EffectivePolicyContribution{{Key: "mentionLimit", Value: int64(1)}}, valid: true},
		{name: "valid float", keys: []string{"mentionLimit"}, contributions: []plugin.EffectivePolicyContribution{{Key: "mentionLimit", Value: 1.5}}, valid: true},
		{name: "nan", keys: []string{"mentionLimit"}, contributions: []plugin.EffectivePolicyContribution{{Key: "mentionLimit", Value: math.NaN()}}},
		{name: "infinity", keys: []string{"mentionLimit"}, contributions: []plugin.EffectivePolicyContribution{{Key: "mentionLimit", Value: math.Inf(1)}}},
		{name: "float overflow", keys: []string{"mentionLimit"}, contributions: []plugin.EffectivePolicyContribution{{Key: "mentionLimit", Value: maxIntFloat}}},
		{name: "wrong number type", keys: []string{"mentionLimit"}, contributions: []plugin.EffectivePolicyContribution{{Key: "mentionLimit", Value: "1"}}},
		{name: "use default", keys: []string{"canSearchNotes"}, contributions: []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", UseDefault: true, Value: "ignored"}}, valid: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.valid, ValidateContributions(tc.keys, tc.contributions))
		})
	}
}

func TestValueValidRejectsUnknownNativeType(t *testing.T) {
	assert.False(t, valueValid("unknown", struct{}{}, true))
}
