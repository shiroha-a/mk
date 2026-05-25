package entitycompat

import (
	_ "embed"
	"encoding/json"
)

// embeddedGoldenJSON is the committed golden flat-schema snapshot, embedded at
// build time so callers in other packages (handler response-shape tests) can
// validate without a filesystem path (their CWD differs from this package).
//
//go:embed testdata/golden_schemas.json
var embeddedGoldenJSON []byte

// EmbeddedGolden returns the embedded golden flat-schema snapshot.
func EmbeddedGolden() GoldenSet {
	var g GoldenSet
	_ = json.Unmarshal(embeddedGoldenJSON, &g)
	return g
}

// ValidateResponse is the "Layer 3" runtime response check: it validates an
// actual API response map against the named golden schema and returns the
// gated (HIGH/MED) findings. Unlike Layer 0 (struct reflection) it inspects
// what a handler actually emits, so it catches handler populate bugs and
// ad-hoc map[string]any responses that no reflectable struct backs.
//
// A non-existent schema name yields a single HIGH finding so misuse (typo /
// not-extracted schema) is visible rather than silently passing.
func ValidateResponse(schemaName string, actual map[string]any) []Finding {
	schema, ok := EmbeddedGolden()[schemaName]
	if !ok {
		return []Finding{{
			Family: schemaName, Layer: schemaName, Golden: schemaName,
			Field: schemaName, Kind: KindMissing, Sev: SevHigh,
			Detail: "golden schema not found in snapshot (typo, or add to GoldenSchemaNames/L2FlatSchemaNames)",
		}}
	}
	var gated []Finding
	for _, f := range ValidateValue(schemaName, schemaName, schemaName, actual, schema) {
		if f.Sev == SevHigh || f.Sev == SevMed {
			gated = append(gated, f)
		}
	}
	return gated
}
