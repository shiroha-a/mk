// Package shapetest provides test helpers to assert that an actual API
// response matches the golden contract (the "Layer 3" runtime response check).
//
// Unlike the Layer 0 struct-reflection gate, this validates what a handler
// actually emits, so it covers ad-hoc map[string]any responses and catches
// handler populate bugs. Intended for use from handler _test.go files:
//
//	shapetest.Assert(t, "MeDetailedOnly", resp)
//
// It deliberately lives in its own package (imports testing) so handler test
// packages can share it without duplicating the validate-and-report loop.
package shapetest

import (
	"testing"

	"github.com/shiroha-a/mk/internal/entitycompat"
)

// Assert fails t with a clear message for each gated (HIGH/MED) shape drift
// between the actual response map and the named golden schema.
func Assert(t testing.TB, schemaName string, actual map[string]any) {
	t.Helper()
	for _, f := range entitycompat.ValidateResponse(schemaName, actual) {
		t.Errorf("response shape drift vs golden %s: %s [%s]: %s", schemaName, f.Field, f.Kind, f.Detail)
	}
}
