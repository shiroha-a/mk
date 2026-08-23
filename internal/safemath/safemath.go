package safemath

import "math"

func MulInt(value int, unit int64) int64 {
	return MulInt64(int64(value), unit)
}

func MulInt64(value, unit int64) int64 {
	if value == 0 || unit == 0 {
		return 0
	}
	if value > 0 {
		if unit > 0 && value > math.MaxInt64/unit {
			return math.MaxInt64
		}
		if unit < 0 && unit < math.MinInt64/value {
			return math.MinInt64
		}
	} else {
		if unit > 0 && value < math.MinInt64/unit {
			return math.MinInt64
		}
		if unit < 0 && value < math.MaxInt64/unit {
			return math.MaxInt64
		}
	}
	return value * unit
}

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

func MulFloat64(value float64, unit int64) int64 {
	return Float64ToInt64(value * float64(unit))
}

func NegateInt64(value int64) int64 {
	if value == math.MinInt64 {
		return math.MaxInt64
	}
	return -value
}

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
