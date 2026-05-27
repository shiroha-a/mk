package entitycompat

import (
	_ "embed"
	"encoding/json"
	"sync"
)

// embeddedGoldenJSON is the committed golden flat-schema snapshot, embedded at
// build time so callers in other packages (handler response-shape tests) can
// validate without a filesystem path (their CWD differs from this package).
//
//go:embed testdata/golden_schemas.json
var embeddedGoldenJSON []byte

// embeddedUnionsJSON is the committed golden discriminated-union snapshot
// (union name -> type literal -> variant schema), embedded so L3 response
// checks can validate union-shaped responses (e.g. Notification) without a
// filesystem path (#1326)。
//
//go:embed testdata/golden_unions.json
var embeddedUnionsJSON []byte

var (
	embeddedGoldenOnce sync.Once
	embeddedGolden     GoldenSet
	embeddedUnionsOnce sync.Once
	embeddedUnions     map[string]map[string]Schema
)

// EmbeddedGolden returns the embedded golden flat-schema snapshot. The parse is
// memoized: L3 response checks across many handler tests call this repeatedly,
// so the JSON is unmarshaled once. Callers must treat the result as read-only.
func EmbeddedGolden() GoldenSet {
	embeddedGoldenOnce.Do(func() {
		_ = json.Unmarshal(embeddedGoldenJSON, &embeddedGolden)
	})
	return embeddedGolden
}

// EmbeddedUnions returns the embedded golden discriminated-union snapshot
// (union name -> type literal -> variant schema), memoized like EmbeddedGolden.
func EmbeddedUnions() map[string]map[string]Schema {
	embeddedUnionsOnce.Do(func() {
		_ = json.Unmarshal(embeddedUnionsJSON, &embeddedUnions)
	})
	return embeddedUnions
}

// ValidateResponse is the "Layer 3" runtime response check: it validates an
// actual API response map against the named golden schema and returns the
// gated (HIGH/MED) findings. Unlike Layer 0 (struct reflection) it inspects
// what a handler actually emits, so it catches handler populate bugs and
// ad-hoc map[string]any responses that no reflectable struct backs.
//
// A non-existent schema name yields a single HIGH finding so misuse (typo /
// not-extracted schema) is visible rather than silently passing.
//
// schemaName may name either a flat golden schema or a discriminated union
// (e.g. Notification); the union variants are dispatched on the value's `type`
// discriminator (#1326)。
func ValidateResponse(schemaName string, actual map[string]any) []Finding {
	if schema, ok := EmbeddedGolden()[schemaName]; ok {
		return gateHighMed(ValidateValue(schemaName, schemaName, schemaName, actual, schema))
	}
	if variants, ok := EmbeddedUnions()[schemaName]; ok {
		return gateHighMed(ValidateUnionValue(schemaName, actual, variants))
	}
	return []Finding{{
		Family: schemaName, Layer: schemaName, Golden: schemaName,
		Field: schemaName, Kind: KindMissing, Sev: SevHigh,
		Detail: "golden schema not found in snapshot (typo, or add to GoldenSchemaNames/L2FlatSchemaNames/UnionSchemaNames)",
	}}
}

// gateHighMed keeps only the gating (HIGH/MED) findings; LOW/INFO are reported
// elsewhere but never fail an L3 assertion.
func gateHighMed(findings []Finding) []Finding {
	var gated []Finding
	for _, f := range findings {
		if f.Sev == SevHigh || f.Sev == SevMed {
			gated = append(gated, f)
		}
	}
	return gated
}
