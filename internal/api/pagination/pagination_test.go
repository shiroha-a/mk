package pagination

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func ptr(v int) *int { return &v }

// upstream の paramDef は limit を `minimum: 1 / maximum: <max>` で検証し、
// 範囲外は ajv が 400 にする。省略時だけ default が入る。
func TestResolveLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit *int
		want  int
		ok    bool
	}{
		{"省略は default", nil, 10, true},
		{"範囲内はそのまま", ptr(50), 50, true},
		{"下限ちょうど", ptr(1), 1, true},
		{"上限ちょうど", ptr(100), 100, true},
		{"0 は範囲外", ptr(0), 0, false},
		{"負値は範囲外", ptr(-1), 0, false},
		{"上限超過は範囲外", ptr(101), 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResolveLimit(tt.limit, 10, 100)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// def / max は endpoint ごとに違う。
func TestResolveLimit_PerEndpointBounds(t *testing.T) {
	got, ok := ResolveLimit(nil, 30, 100)
	assert.True(t, ok)
	assert.Equal(t, 30, got)

	_, ok = ResolveLimit(ptr(11), 5, 10)
	assert.False(t, ok, "endpoint の maximum を超えたら拒否する")
}
