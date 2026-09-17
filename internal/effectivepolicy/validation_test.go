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

// **管理者が入れる policy の値の型検査 (#3037)。**
//
// consumer は `if limit, ok := role.PolicyNumber(v); ok { ...gate... }` の形で
// 読むので、数値の policy に文字列が入ると**上限違反で弾かれるのではなく上限
// そのものが消える**。
func TestValidatePolicyValue(t *testing.T) {
	for _, tt := range []struct {
		name  string
		key   string
		value any
		want  bool
	}{
		{"数値に数値", "maxFileSizeMb", 30, true},
		{"数値に float", "maxFileSizeMb", 1.5, true},
		{"数値に文字列", "maxFileSizeMb", "30", false},
		{"数値に配列", "driveCapacityMb", []any{1, 2}, false},
		{"bool に bool", "canInvite", true, true},
		{"bool に文字列", "canInvite", "true", false},
		{"列挙に正しい値", "chatAvailability", "readonly", true},
		{"列挙に知らない値", "chatAvailability", "whatever", false},
		// **未知のキーは通す。** upstream は object lookup なので誰も読まない。
		// 弾くと、upstream が新しい policy を足したときに mk-go だけが拒否する。
		{"未知のキー", "somethingNew", "anything", true},
		// **空文字を含む文字列配列は通す (#3037 レビュー)。** 管理画面の
		// `MkTextarea` は `split('\n')` をそのまま送るので、末尾で Enter を
		// 押す / 欄を空にするだけで `[""]` が飛ぶ。受け側の
		// `aggregateStringSetUnion` は元から trim して空を読み飛ばす。
		{"文字列配列に空要素", "uploadableFileTypes", []any{"image/*", ""}, true},
		{"文字列配列が空だけ", "uploadableFileTypes", []any{""}, true},
		{"文字列配列 ([]string)", "uploadableFileTypes", []string{"image/*"}, true},
		{"文字列配列に数値", "uploadableFileTypes", []any{"image/*", 1}, false},
		{"文字列配列に文字列", "uploadableFileTypes", "image/*", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ValidatePolicyValue(tt.key, tt.value))
		})
	}
}
