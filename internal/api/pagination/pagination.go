// Package pagination provides shared helpers for Misskey-compatible list
// pagination params (limit / sinceId / untilId).
package pagination

// ClampLimit normalizes a requested page-size limit to an endpoint's bounds.
//
// def is the value applied when the caller omits limit (limit <= 0); max is the
// upper bound. Both must match the upstream Misskey paramDef for the endpoint
// (`default` / `maximum`), which the limit-spec drift gate
// (entitycompat.TestLimitSpecDrift) verifies by reading the literals at each
// ClampLimit call site. Unlike Misskey — which rejects out-of-range limits via
// ajv — mk-go clamps, which is strictly more lenient; only the default/max
// VALUES are contractually gated.
func ClampLimit(limit, def, max int) int {
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}
