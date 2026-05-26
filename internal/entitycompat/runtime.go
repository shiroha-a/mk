package entitycompat

import (
	"reflect"
	"strings"
)

// This file implements the "Layer 2" runtime shape check: instead of reflecting
// over struct declarations (Layer 0), it validates the *actual JSON value* a
// packer emits against the golden contract. That reaches what static analysis
// cannot — map-based packers (PackNotification returns map[string]any),
// discriminated unions, and handler populate bugs (a field declared on the
// struct but left null/absent at runtime).

// UnionSchemaNames lists the discriminated-union golden schemas the generator
// must extract per variant. These are excluded from the Layer 0 family table
// because a flat reflection diff is meaningless against a union.
func UnionSchemaNames() []string {
	return []string{"Notification"}
}

// L2FlatSchemaNames lists flat (non-union) golden schemas that are validated at
// Layer 2 against a map-based packer's runtime output. These entities are
// packed as map[string]any (not reflectable structs), so Layer 0 cannot reach
// them; the generator still extracts their flat golden schema into the snapshot
// for ValidateValue to use.
func L2FlatSchemaNames() []string {
	// L2 packer fixture / L3 handler-response 検証で実際に使う flat schema のみ
	// 列挙する (未使用 schema を snapshot に入れない)。UserList / Following /
	// EmojiDetailed は L0 family 側 (GoldenSchemaNames) で抽出済。
	return []string{
		"Announcement", "Clip", "Signin", "Page", "Antenna",
		"GalleryPost", "Hashtag", "App",
		"Flash", "InviteCode", "UserWebhook", "Channel",
	}
}

// ParseUnion parses a discriminated-union schema (variants joined by `} | {`)
// into a map keyed by each variant's `type` string literal. Variants without a
// `type` discriminator are skipped.
func ParseUnion(lines []string, schemaName string) map[string]Schema {
	open := "        " + schemaName + ": {"
	start := -1
	for i, ln := range lines {
		if ln == open {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}

	out := map[string]Schema{}
	depth := 1
	variant := Schema{}
	variantType := ""

	var curName string
	var have, optional bool
	var typ string
	var buf strings.Builder
	finalizeField := func() {
		if !have {
			return
		}
		variant[curName] = FieldShape{
			Nullable: strings.Contains(buf.String(), "| null"),
			Optional: optional,
			Type:     typ,
		}
		have = false
		buf.Reset()
	}
	finalizeVariant := func() {
		finalizeField()
		if variantType != "" {
			out[variantType] = variant
		}
		variant = Schema{}
		variantType = ""
	}

	for i := start + 1; i < len(lines); i++ {
		ln := lines[i]
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		// variant 境界: 8-space indent の `} | {` (nested の `}` は深い indent)。
		if strings.HasPrefix(ln, "        } | {") {
			finalizeVariant()
			continue
		}
		if depth == 1 {
			if m := propRe.FindStringSubmatch(ln); m != nil {
				finalizeField()
				curName = m[1]
				optional = m[2] == "?"
				typ = coarseTS(m[3])
				have = true
				if curName == "type" {
					variantType = literalOf(m[3])
				}
			}
		}
		if have {
			buf.WriteString(ln)
			buf.WriteString("\n")
		}
		depth += strings.Count(ln, "{") - strings.Count(ln, "}")
		if depth == 0 {
			finalizeVariant()
			return out
		}
	}
	finalizeVariant()
	return out
}

// literalOf extracts the single-quoted string literal from a type RHS like
// `'follow';` -> `follow`. Returns "" when the RHS is not a bare literal.
func literalOf(rhs string) string {
	rhs = strings.TrimSpace(rhs)
	if !strings.HasPrefix(rhs, "'") {
		return ""
	}
	rhs = rhs[1:]
	if i := strings.IndexByte(rhs, '\''); i >= 0 {
		return rhs[:i]
	}
	return ""
}

// ValidateValue checks a decoded JSON object (a packer's map output, or a
// struct round-tripped to a map) against a golden schema and returns findings.
// Unlike the Layer 0 reflection diff this inspects concrete runtime values, so
// it catches a required field that is present-but-null or simply not populated.
func ValidateValue(family, layer, golden string, actual map[string]any, schema Schema) []Finding {
	mk := func(field, kind string, sev Severity, detail string) Finding {
		return Finding{Family: family, Layer: layer, Golden: golden, Field: field, Kind: kind, Sev: sev, Detail: detail}
	}
	var findings []Finding
	for name, g := range schema {
		v, present := actual[name]
		if !present {
			if !g.Optional {
				findings = append(findings, mk(name, KindMissing, SevHigh,
					"required by golden but not emitted at runtime"))
			}
			continue
		}
		if v == nil {
			if !g.Nullable {
				findings = append(findings, mk(name, KindNullable, SevHigh,
					"golden non-null but runtime value is null"))
			}
			continue
		}
		if at := jsonType(v); scalarMismatch(g.Type, at) {
			findings = append(findings, mk(name, "type", SevMed,
				"golden type "+g.Type+" but runtime value is "+at))
		}
	}
	for name := range actual {
		if _, ok := schema[name]; !ok {
			findings = append(findings, mk(name, KindExtra, SevInfo,
				"emitted at runtime but not in golden"))
		}
	}
	return findings
}

// ValidateUnionValue dispatches on the value's `type` discriminator and
// validates against the matching union variant. A `type` with no matching
// variant is itself a HIGH finding: a strict client cannot dispatch it.
func ValidateUnionValue(unionName string, actual map[string]any, variants map[string]Schema) []Finding {
	t, _ := actual["type"].(string)
	schema, ok := variants[t]
	if !ok {
		return []Finding{{
			Family: unionName, Layer: unionName, Golden: unionName,
			Field: "type=" + t, Kind: KindMissing, Sev: SevHigh,
			Detail: "runtime type literal has no matching variant in the golden union",
		}}
	}
	return ValidateValue(unionName, unionName+"["+t+"]", unionName, actual, schema)
}

// jsonType returns the JSON type bucket of a Go value as it serializes.
func jsonType(v any) string {
	if v == nil {
		return "null"
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return "null"
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Map, reflect.Struct:
		return "object"
	default:
		return "other"
	}
}

// scalarMismatch reports a clear scalar type conflict. Coarse buckets
// (object/array/other/any) are skipped to avoid false positives on refs and
// packed structs.
func scalarMismatch(goldenType, actualType string) bool {
	switch goldenType {
	case "string", "boolean", "number":
		switch actualType {
		case "string", "boolean", "number":
			return goldenType != actualType
		}
	}
	return false
}
