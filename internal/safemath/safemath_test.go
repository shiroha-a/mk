package safemath

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMulInt64(t *testing.T) {
	tests := []struct {
		name        string
		value, unit int64
		want        int64
	}{
		{"zero", 0, -3, 0},
		{"positive exact", math.MaxInt64 / 2, 2, math.MaxInt64 - 1},
		{"positive overflow", math.MaxInt64/2 + 1, 2, math.MaxInt64},
		{"negative overflow", math.MinInt64/2 - 1, 2, math.MinInt64},
		{"positive negative overflow", math.MaxInt64, -2, math.MinInt64},
		{"negative positive overflow", math.MinInt64, 2, math.MinInt64},
		{"negative negative overflow", math.MinInt64, -1, math.MaxInt64},
		{"negative negative exact", -7, -6, 42},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MulInt64(tt.value, tt.unit))
		})
	}
}

func TestMulInt(t *testing.T) {
	assert.Equal(t, int64(42), MulInt(7, 6))
	assert.Equal(t, int64(-42), MulInt(7, -6))
	assert.Equal(t, MulInt64(int64(math.MaxInt), 2), MulInt(math.MaxInt, 2))
}

func TestFloat64ToInt64(t *testing.T) {
	assert.Equal(t, int64(42), Float64ToInt64(42.9))
	assert.Equal(t, int64(-42), Float64ToInt64(-42.9))
	assert.Equal(t, int64(0), Float64ToInt64(math.NaN()))
	assert.Equal(t, int64(math.MaxInt64), Float64ToInt64(math.Inf(1)))
	assert.Equal(t, int64(math.MinInt64), Float64ToInt64(math.Inf(-1)))
	assert.Equal(t, int64(math.MaxInt64), Float64ToInt64(float64(math.MaxInt64)))
	assert.Equal(t, int64(math.MinInt64), Float64ToInt64(float64(math.MinInt64)))
}

func TestMulFloat64(t *testing.T) {
	assert.Equal(t, int64(90_000), MulFloat64(1.5, 60_000))
	assert.Equal(t, int64(-90_000), MulFloat64(1.5, -60_000))
	assert.Equal(t, int64(math.MaxInt64), MulFloat64(math.MaxFloat64, 2))
	assert.Equal(t, int64(math.MinInt64), MulFloat64(-math.MaxFloat64, 2))
	assert.Equal(t, int64(0), MulFloat64(math.NaN(), 1))
}

func TestFloat64ToInt(t *testing.T) {
	assert.Equal(t, 42, Float64ToInt(42.9))
	assert.Equal(t, -42, Float64ToInt(-42.9))
	assert.Equal(t, 0, Float64ToInt(math.NaN()))
	assert.Equal(t, math.MaxInt, Float64ToInt(math.Inf(1)))
	assert.Equal(t, math.MinInt, Float64ToInt(math.Inf(-1)))
	assert.Equal(t, math.MaxInt, Float64ToInt(float64(math.MaxInt)))
	assert.Equal(t, math.MinInt, Float64ToInt(float64(math.MinInt)))
}

func TestNegateInt64(t *testing.T) {
	assert.Equal(t, int64(-42), NegateInt64(42))
	assert.Equal(t, int64(42), NegateInt64(-42))
	assert.Equal(t, int64(math.MaxInt64), NegateInt64(math.MinInt64))
}

func TestAddInt64(t *testing.T) {
	assert.Equal(t, int64(6), AddInt64(1, 2, 3))
	assert.Equal(t, int64(math.MaxInt64), AddInt64(math.MaxInt64, 1))
	assert.Equal(t, int64(math.MinInt64), AddInt64(math.MinInt64, -1))
	assert.Equal(t, int64(math.MaxInt64), AddInt64(math.MaxInt64-1, 1, 1))
}

func TestSumExceedsInt64(t *testing.T) {
	assert.False(t, SumExceedsInt64(10, 4, 6))
	assert.True(t, SumExceedsInt64(10, 4, 7))
	assert.True(t, SumExceedsInt64(math.MaxInt64, math.MaxInt64, 1))
	assert.True(t, SumExceedsInt64(10, 1, -1))
	assert.True(t, SumExceedsInt64(-1))
}
