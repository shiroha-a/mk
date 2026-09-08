package role

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOptOutNotificationTypesAggregatesByIntersection pins that the mk-go
// specific opt-out policy is merged the opposite way from uploadableFileTypes.
//
// **union だと複数ロールに属するほど通知が減る (#2898)。** upstream の policy は
// 緩い方に倒す (bool は OR、数値は max) ので、opt-out は全ロールが切っている型
// だけを切る。
func TestOptOutNotificationTypesAggregatesByIntersection(t *testing.T) {
	base := []string{}

	tests := []struct {
		name   string
		values []any
		want   any
	}{
		{
			name:   "single role opts out of two types",
			values: []any{[]string{"abuseReport", "note"}},
			want:   []string{"abuseReport", "note"},
		},
		{
			name: "two roles: only the shared type is cut",
			// 片方は abuseReport と note、もう片方は abuseReport だけを切る。
			// note を切っていないロールに属している以上 note は届く。
			values: []any{[]string{"abuseReport", "note"}, []string{"abuseReport"}},
			want:   []string{"abuseReport"},
		},
		{
			name:   "a role that cuts nothing keeps everything",
			values: []any{[]string{"abuseReport"}, []string{}},
			want:   []string{},
		},
		{
			name:   "disjoint roles cut nothing",
			values: []any{[]string{"abuseReport"}, []string{"note"}},
			want:   []string{},
		},
		{
			name: "invalid candidates are skipped, not treated as empty",
			// 型不一致を intersection に参加させると結果が必ず空になり、
			// 不正な値の role が 1 つあるだけで他 role の設定が消える。
			values: []any{[]string{"abuseReport"}, "not-a-slice"},
			want:   []string{"abuseReport"},
		},
		{
			name:   "all candidates invalid falls back to base",
			values: []any{"not-a-slice", 42},
			want:   base,
		},
		{
			name:   "json decoded []any is accepted",
			values: []any{[]any{"abuseReport", "note"}, []any{"abuseReport"}},
			want:   []string{"abuseReport"},
		},
		{
			name:   "whitespace-only entries are dropped before intersecting",
			values: []any{[]string{"abuseReport", "  "}, []string{"abuseReport"}},
			want:   []string{"abuseReport"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := aggregatePolicyValues(PolicyOptOutNotificationTypes, base, tt.values)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestUploadableFileTypesStillAggregatesByUnion guards that adding the
// intersection branch did not change the existing []string policy.
func TestUploadableFileTypesStillAggregatesByUnion(t *testing.T) {
	got := aggregatePolicyValues("uploadableFileTypes", []string{"image/*"},
		[]any{[]string{"image/*"}, []string{"video/*"}})
	require.Equal(t, []string{"image/*", "video/*"}, got,
		"uploadableFileTypes must stay set union")
}
