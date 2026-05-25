// Package entitycompat provides a static "Layer 0" API shape drift detector.
//
// It reflects over mk-go's entity DTO structs (internal/entity) and diffs
// their JSON shape against the Misskey API contract (the misskey-js autogen
// `types.ts`, which mirrors the OpenAPI `components.schemas`). The class of
// mismatch it surfaces — fields the contract declares that mk-go omits, and
// nullability/optionality divergences — is exactly what makes Misskey-compatible
// 3rd-party clients crash on a non-null cast.
//
// The detector needs no running server, no browser, and no Docker, so it runs
// as a fast deterministic `go test` gate. The golden contract is consumed from
// a committed snapshot (testdata/golden_schemas.json) regenerated from the
// pinned submodule on upstream catch-up, keeping the gate hermetic.
package entitycompat

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// FieldShape is the normalized shape of a single JSON field: whether it may
// serialize to null, whether it may be absent, and a coarse type bucket.
type FieldShape struct {
	Nullable bool   `json:"nullable"`
	Optional bool   `json:"optional"`
	Type     string `json:"type"`
}

// Schema maps a JSON field name to its shape.
type Schema map[string]FieldShape

// GoldenSet is the committed contract snapshot: golden schema name -> Schema.
type GoldenSet map[string]Schema

// Severity ranks a finding for gating. HIGH and MED are gated by the drift
// test; LOW and INFO are reported but non-blocking.
type Severity string

const (
	SevHigh Severity = "HIGH"
	SevMed  Severity = "MED"
	SevLow  Severity = "LOW"
	SevInfo Severity = "INFO"
)

// Kind classifies what sort of drift a finding represents.
const (
	KindMissing  = "missing"  // golden has a field mk-go does not emit
	KindNullable = "nullable" // mk-go may emit null where golden is non-null
	KindOmit     = "omit"     // mk-go may omit a field golden requires
	KindExtra    = "extra"    // mk-go emits a field absent from golden
	KindLayer    = "layer"    // present in a sibling layer of the same family
)

// Finding is a single detected (or informational) shape difference.
type Finding struct {
	Family string
	Layer  string
	Golden string
	Field  string
	Kind   string
	Sev    Severity
	Detail string
}

// ---- mk-go side: reflection ------------------------------------------------

// reflectOwnFields returns the shape of the non-embedded fields declared
// directly on t. Embedded layers map to a sibling golden schema, so they are
// excluded here and handled by their own Layer entry.
func reflectOwnFields(t reflect.Type) Schema {
	out := Schema{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			continue
		}
		name, opt, ok := jsonName(f)
		if !ok {
			continue
		}
		out[name] = FieldShape{
			// omitempty 付きポインタは nil で「省略」= optional であり null は
			// 出さない。omitempty 無しのポインタだけが nil 時に `null` を出す。
			Nullable: f.Type.Kind() == reflect.Ptr && !opt,
			Optional: opt,
			Type:     coarseGo(f.Type),
		}
	}
	return out
}

// reflectAllFields flattens every json field reachable from t, recursing into
// embedded structs. Used to build the per-family "present somewhere" set.
func reflectAllFields(t reflect.Type) Schema {
	out := Schema{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Anonymous {
				walk(f.Type)
				continue
			}
			if name, opt, ok := jsonName(f); ok {
				out[name] = FieldShape{
					Nullable: f.Type.Kind() == reflect.Ptr && !opt,
					Optional: opt,
					Type:     coarseGo(f.Type),
				}
			}
		}
	}
	walk(t)
	return out
}

func jsonName(f reflect.StructField) (name string, omitempty, ok bool) {
	tag := f.Tag.Get("json")
	if tag == "" || tag == "-" {
		return "", false, false
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		return "", false, false
	}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty, true
}

func coarseGo(t reflect.Type) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
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
		return "any"
	}
}

// ---- golden side: types.ts parser -----------------------------------------

var propRe = regexp.MustCompile(`^            (\w+)(\??):\s*(.*)$`)

// ParseTypesFile parses the openapi-typescript schema blocks named in `names`
// out of the given types.ts content (split into lines). Missing schemas are
// silently skipped; callers can compare the returned key set against `names`.
func ParseTypesFile(lines []string, names []string) GoldenSet {
	out := GoldenSet{}
	for _, n := range names {
		if s, ok := parseSchema(lines, n); ok {
			out[n] = s
		}
	}
	return out
}

// parseSchema extracts the top-level properties of one openapi-typescript
// schema block. Brace depth is tracked so fields of nested inline object types
// are not mistaken for top-level properties.
func parseSchema(lines []string, schemaName string) (Schema, bool) {
	open := "        " + schemaName + ": {"
	start := -1
	for i, ln := range lines {
		if ln == open {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, false
	}

	out := Schema{}
	depth := 1

	var curName string
	var have bool
	var optional bool
	var typ string
	var buf strings.Builder
	finalize := func() {
		if !have {
			return
		}
		out[curName] = FieldShape{
			Nullable: strings.Contains(buf.String(), "| null"),
			Optional: optional,
			Type:     typ,
		}
		have = false
		buf.Reset()
	}

	for i := start + 1; i < len(lines); i++ {
		ln := lines[i]
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if depth == 1 {
			if m := propRe.FindStringSubmatch(ln); m != nil {
				finalize()
				curName = m[1]
				optional = m[2] == "?"
				typ = coarseTS(m[3])
				have = true
			}
		}
		if have {
			buf.WriteString(ln)
			buf.WriteString("\n")
		}
		depth += strings.Count(ln, "{") - strings.Count(ln, "}")
		if depth == 0 {
			finalize()
			return out, true
		}
	}
	finalize()
	return out, true
}

func coarseTS(rhs string) string {
	rhs = strings.TrimSuffix(strings.TrimSpace(rhs), ";")
	rhs = strings.TrimSpace(strings.TrimSuffix(rhs, "| null"))
	rhs = strings.TrimSpace(rhs)
	switch {
	case rhs == "":
		return "object"
	case strings.HasSuffix(rhs, "[]"):
		return "array"
	case strings.HasPrefix(rhs, "{"):
		return "object"
	case strings.HasPrefix(rhs, "'") || strings.Contains(rhs, "' | '"):
		return "string"
	case rhs == "string":
		return "string"
	case rhs == "boolean":
		return "boolean"
	case rhs == "number":
		return "number"
	case strings.Contains(rhs, "components['schemas']"):
		return "object"
	default:
		return "other"
	}
}

// ---- diff -------------------------------------------------------------------

// DiffFamily compares every layer of fam against the golden snapshot and
// returns findings sorted by severity. famPresent is the union of all fields
// emitted anywhere in the family, used to downgrade a layer-local "missing" to
// an informational layer-placement note when the field exists elsewhere.
func DiffFamily(fam Family, golden GoldenSet) []Finding {
	famPresent := map[string]bool{}
	for _, l := range fam.Layers {
		for name := range reflectAllFields(l.Type) {
			famPresent[name] = true
		}
	}

	var findings []Finding
	for _, l := range fam.Layers {
		gold, ok := golden[l.Golden]
		if !ok {
			continue
		}
		mk := reflectOwnFields(l.Type)

		for name, g := range gold {
			if _, ok := mk[name]; ok {
				continue
			}
			if famPresent[name] {
				findings = append(findings, Finding{fam.Name, l.Label, l.Golden, name, KindLayer, SevInfo,
					"present in another layer of the same family"})
				continue
			}
			sev := SevHigh
			detail := "declared in golden, absent in all mk-go structs"
			if g.Optional {
				sev = SevLow
				detail = "declared in golden (optional), absent in mk-go"
			}
			findings = append(findings, Finding{fam.Name, l.Label, l.Golden, name, KindMissing, sev, detail})
		}

		for name, g := range gold {
			m, ok := mk[name]
			if !ok {
				continue
			}
			if !g.Nullable && m.Nullable {
				findings = append(findings, Finding{fam.Name, l.Label, l.Golden, name, KindNullable, SevHigh,
					"golden=non-null, mk-go=nullable: may emit null where client casts non-null"})
			}
			if !g.Optional && m.Optional {
				findings = append(findings, Finding{fam.Name, l.Label, l.Golden, name, KindOmit, SevMed,
					"golden=required, mk-go=omitempty: may omit a field the client requires"})
			}
		}

		for name := range mk {
			if _, ok := gold[name]; !ok {
				findings = append(findings, Finding{fam.Name, l.Label, l.Golden, name, KindExtra, SevInfo,
					"in mk-go but not in golden (extension/alias?)"})
			}
		}
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Layer != findings[j].Layer {
			return findings[i].Layer < findings[j].Layer
		}
		if sevRank(findings[i].Sev) != sevRank(findings[j].Sev) {
			return sevRank(findings[i].Sev) < sevRank(findings[j].Sev)
		}
		return findings[i].Field < findings[j].Field
	})
	return findings
}

func sevRank(s Severity) int {
	switch s {
	case SevHigh:
		return 0
	case SevMed:
		return 1
	case SevLow:
		return 2
	default:
		return 3
	}
}

// GoldenSchemaNames returns the set of golden schema names referenced by the
// family table, so the generator knows which schemas to extract.
func GoldenSchemaNames() []string {
	seen := map[string]bool{}
	var names []string
	for _, fam := range Families() {
		for _, l := range fam.Layers {
			if !seen[l.Golden] {
				seen[l.Golden] = true
				names = append(names, l.Golden)
			}
		}
	}
	sort.Strings(names)
	return names
}
