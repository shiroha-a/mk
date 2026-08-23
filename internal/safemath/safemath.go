// Package safemath provides saturating arithmetic and conversion helpers for
// values crossing fixed-width representation boundaries.
package safemath

import "math"

// Float64ToInt converts value to int by truncating toward zero. It saturates
// values outside the int range and converts NaN to zero.
func Float64ToInt(value float64) int {
	if math.IsNaN(value) {
		return 0
	}
	if value >= float64(math.MaxInt) {
		return math.MaxInt
	}
	if value <= float64(math.MinInt) {
		return math.MinInt
	}
	return int(value)
}

// Float64ToInt64 converts value to int64 by truncating toward zero. It
// saturates values outside the int64 range and converts NaN to zero.
func Float64ToInt64(value float64) int64 {
	if math.IsNaN(value) {
		return 0
	}
	if value >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	if value <= float64(math.MinInt64) {
		return math.MinInt64
	}
	return int64(value)
}

// MulFloat64 multiplies value by unit and converts the product using
// Float64ToInt64's saturation and NaN semantics.
func MulFloat64(value float64, unit int64) int64 {
	return Float64ToInt64(value * float64(unit))
}

// NegateInt64 returns the additive inverse of value. It saturates MinInt64 to
// MaxInt64 because the positive inverse is not representable.
func NegateInt64(value int64) int64 {
	if value == math.MinInt64 {
		return math.MaxInt64
	}
	return -value
}

// AddInt64 adds values in order and saturates at the first overflow to
// MinInt64 or MaxInt64.
func AddInt64(values ...int64) int64 {
	var sum int64
	for _, value := range values {
		if value > 0 && sum > math.MaxInt64-value {
			return math.MaxInt64
		}
		if value < 0 && sum < math.MinInt64-value {
			return math.MinInt64
		}
		sum += value
	}
	return sum
}

// SumExceedsInt64 reports whether the sum of values exceeds limit. It accepts
// only non-negative limits and values; it returns true for any negative input
// or when addition would exceed the limit, including through overflow.
func SumExceedsInt64(limit int64, values ...int64) bool {
	if limit < 0 {
		return true
	}
	var sum int64
	for _, value := range values {
		if value < 0 || value > limit-sum {
			return true
		}
		sum += value
	}
	return false
}
