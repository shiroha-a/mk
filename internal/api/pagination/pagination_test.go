package pagination

import "testing"

func TestClampLimit(t *testing.T) {
	tests := []struct {
		name           string
		limit, def, mx int
		want           int
	}{
		{"omitted (zero) uses default", 0, 10, 100, 10},
		{"negative uses default", -5, 10, 100, 10},
		{"in range is kept", 42, 10, 100, 42},
		{"at max is kept", 100, 10, 100, 100},
		{"over max is clamped", 200, 10, 100, 100},
		{"non-standard default", 0, 30, 100, 30},
		{"non-standard max clamp", 999, 30, 500, 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClampLimit(tt.limit, tt.def, tt.mx); got != tt.want {
				t.Errorf("ClampLimit(%d, %d, %d) = %d, want %d", tt.limit, tt.def, tt.mx, got, tt.want)
			}
		})
	}
}
