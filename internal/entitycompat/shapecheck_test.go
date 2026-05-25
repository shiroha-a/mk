package entitycompat

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// --- the actual drift gate --------------------------------------------------

// TestEntityShapeDrift is the CI gate. It loads the committed golden contract
// snapshot, diffs it against the live entity structs, and fails on any HIGH/MED
// drift not recorded in the baseline allowlist. New drift in a PR breaks here;
// fixing a baselined drift turns its allowlist entry stale and also breaks here
// (prompting allowlist cleanup).
func TestEntityShapeDrift(t *testing.T) {
	golden, err := LoadGoldenSnapshot(filepath.Join("testdata", "golden_schemas.json"))
	if err != nil {
		t.Fatalf("load golden: %v", err)
	}
	allow, err := LoadAllowlist(filepath.Join("testdata", "allowlist.json"))
	if err != nil {
		t.Fatalf("load allowlist: %v", err)
	}

	// すべての referenced golden schema が snapshot に存在し、かつ非空か確認する。
	// 0 フィールドは types.ts 書式変更でパーサが空振りした兆候で、放置すると
	// ゲートが vacuously pass してしまう (silent failure)。
	for _, name := range GoldenSchemaNames() {
		s, ok := golden[name]
		if !ok {
			t.Errorf("golden snapshot missing schema %q; run `go run ./tools/shapediff` to regenerate", name)
			continue
		}
		if len(s) == 0 {
			t.Errorf("golden schema %q has 0 fields; snapshot likely broken (types.ts format change)", name)
		}
	}

	violations, stale := RunGate(GatedFindings(golden), allow)
	for _, v := range violations {
		t.Errorf("NEW shape drift: %s.%s [%s] (golden %s): %s\n  -> fix the struct, or add to testdata/allowlist.json with a reason",
			v.Layer, v.Field, v.Kind, v.Golden, v.Detail)
	}
	for _, s := range stale {
		t.Errorf("stale allowlist entry: %s.%s [%s] no longer drifts; remove it from testdata/allowlist.json", s.Layer, s.Field, s.Kind)
	}
}

// --- unit tests: golden parser ----------------------------------------------

func TestParseSchema(t *testing.T) {
	lines := []string{
		"        Sample: {",
		"            /** doc comment */",
		"            id: string;",
		"            name: string | null;",
		"            count?: number;",
		"            tags: string[];",
		"            nested: {",
		"                inner: string;", // must NOT be captured as top-level
		"            };",
		"            kind: 'a' | 'b';",
		"        };",
		"        Other: {",
		"            x: number;",
		"        };",
	}
	got, ok := parseSchema(lines, "Sample")
	if !ok {
		t.Fatal("Sample not found")
	}
	want := Schema{
		"id":     {Nullable: false, Optional: false, Type: "string"},
		"name":   {Nullable: true, Optional: false, Type: "string"},
		"count":  {Nullable: false, Optional: true, Type: "number"},
		"tags":   {Nullable: false, Optional: false, Type: "array"},
		"nested": {Nullable: false, Optional: false, Type: "object"},
		"kind":   {Nullable: false, Optional: false, Type: "string"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseSchema mismatch:\n got=%+v\nwant=%+v", got, want)
	}
	if _, ok := got["inner"]; ok {
		t.Error("nested field leaked into top-level properties")
	}

	if _, ok := parseSchema(lines, "Missing"); ok {
		t.Error("expected Missing schema to be absent")
	}
}

func TestParseSchemaUnion(t *testing.T) {
	// discriminated union: `} | {` keeps brace depth at 1, so all variant
	// fields are flattened into one schema.
	lines := []string{
		"        U: {",
		"            type: 'a';",
		"            onlyA: string;",
		"        } | {",
		"            type: 'b';",
		"            onlyB: number;",
		"        };",
	}
	got, ok := parseSchema(lines, "U")
	if !ok {
		t.Fatal("U not found")
	}
	for _, f := range []string{"type", "onlyA", "onlyB"} {
		if _, ok := got[f]; !ok {
			t.Errorf("union field %q not captured", f)
		}
	}
}

func TestCoarseTS(t *testing.T) {
	cases := map[string]string{
		"string":                          "string",
		"string | null":                   "string",
		"boolean":                         "boolean",
		"number":                          "number",
		"string[]":                        "array",
		"'x' | 'y'":                       "string",
		"'solo'":                          "string",
		"{":                               "object",
		"components['schemas']['Note']":   "object",
		"components['schemas']['Note'][]": "array",
		"":                                "object",
		"SomethingElse":                   "other",
	}
	for in, want := range cases {
		if got := coarseTS(in); got != want {
			t.Errorf("coarseTS(%q)=%q want %q", in, got, want)
		}
	}
}

// --- unit tests: reflection -------------------------------------------------

type embedded struct {
	Base string `json:"base"`
}

type reflectFixture struct {
	embedded
	Keep   string  `json:"keep"`
	Null   *string `json:"nul"`
	Opt    *bool   `json:"opt,omitempty"`
	Num    int     `json:"num"`
	Arr    []int   `json:"arr"`
	M      map[string]int
	Skip   string `json:"-"`
	NoTag  string
	Anon   any             `json:"anon"`
	Nested struct{ X int } `json:"nested"`
}

func TestReflectOwnFields(t *testing.T) {
	got := reflectOwnFields(reflect.TypeOf(reflectFixture{}))
	if _, ok := got["base"]; ok {
		t.Error("embedded field should be excluded from own fields")
	}
	if got["keep"].Nullable || got["keep"].Optional {
		t.Error("keep should be non-null, required")
	}
	if !got["nul"].Nullable || got["nul"].Optional {
		t.Error("nul: pointer w/o omitempty should be nullable, not optional")
	}
	if got["opt"].Nullable || !got["opt"].Optional {
		t.Error("opt: pointer w/ omitempty should be optional, not nullable")
	}
	if got["num"].Type != "number" || got["arr"].Type != "array" {
		t.Errorf("type buckets wrong: num=%s arr=%s", got["num"].Type, got["arr"].Type)
	}
	if _, ok := got["-"]; ok {
		t.Error("json:\"-\" field must be skipped")
	}
	if _, ok := got["NoTag"]; ok {
		t.Error("untagged field must be skipped")
	}
	// map with no tag is skipped (no json name); anon any -> "any"; nested struct -> object
	if got["anon"].Type != "any" {
		t.Errorf("anon type=%s want any", got["anon"].Type)
	}
	if got["nested"].Type != "object" {
		t.Errorf("nested type=%s want object", got["nested"].Type)
	}
}

func TestReflectAllFields(t *testing.T) {
	got := reflectAllFields(reflect.TypeOf(reflectFixture{}))
	if _, ok := got["base"]; !ok {
		t.Error("reflectAllFields must include embedded fields")
	}
	if _, ok := got["keep"]; !ok {
		t.Error("reflectAllFields must include own fields")
	}
}

func TestJSONName(t *testing.T) {
	typ := reflect.TypeOf(struct {
		A string `json:"a"`
		B string `json:"b,omitempty"`
		C string `json:"-"`
		D string `json:""`
		E string
	}{})
	mustField := func(n string) reflect.StructField {
		f, ok := typ.FieldByName(n)
		if !ok {
			t.Fatalf("field %s not found", n)
		}
		return f
	}
	if n, o, ok := jsonName(mustField("A")); !ok || n != "a" || o {
		t.Errorf("A: got %q %v %v", n, o, ok)
	}
	if n, o, ok := jsonName(mustField("B")); !ok || n != "b" || !o {
		t.Errorf("B: got %q %v %v", n, o, ok)
	}
	if _, _, ok := jsonName(mustField("C")); ok {
		t.Error("C: json:\"-\" should be skipped")
	}
	if _, _, ok := jsonName(mustField("D")); ok {
		t.Error("D: empty name should be skipped")
	}
	if _, _, ok := jsonName(mustField("E")); ok {
		t.Error("E: no tag should be skipped")
	}
}

func TestCoarseGo(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{"", "string"},
		{true, "boolean"},
		{int(0), "number"},
		{uint(0), "number"},
		{float64(0), "number"},
		{[]int{}, "array"},
		{map[string]int{}, "object"},
		{struct{}{}, "object"},
	}
	for _, c := range cases {
		if got := coarseGo(reflect.TypeOf(c.v)); got != c.want {
			t.Errorf("coarseGo(%T)=%q want %q", c.v, got, c.want)
		}
	}
	// pointer is unwrapped
	s := ""
	if got := coarseGo(reflect.TypeOf(&s)); got != "string" {
		t.Errorf("coarseGo(*string)=%q want string", got)
	}
	// channel falls through to "any"
	if got := coarseGo(reflect.TypeOf(make(chan int))); got != "any" {
		t.Errorf("coarseGo(chan)=%q want any", got)
	}
}

// --- unit tests: diff -------------------------------------------------------

type diffLayerA struct {
	Keep      string  `json:"keep"`
	NullField *string `json:"nullableField"`       // nullable (no omitempty)
	OmitField *bool   `json:"omitField,omitempty"` // optional
	ExtraF    string  `json:"extraField"`
}

type diffLayerB struct {
	Shared string `json:"sharedField"`
}

func TestDiffFamily(t *testing.T) {
	golden := GoldenSet{
		"GA": Schema{
			"keep":          {},
			"nullableField": {Nullable: false},                  // mk nullable -> HIGH nullable
			"omitField":     {Optional: false},                  // mk optional -> MED omit
			"missReq":       {Nullable: false, Optional: false}, // missing, not in family -> HIGH
			"missOpt":       {Optional: true},                   // missing, optional -> LOW
			"sharedField":   {},                                 // missing from A own, present in B -> INFO layer
		},
		"GB": Schema{"sharedField": {}},
	}
	fam := Family{
		Name: "T",
		Layers: []Layer{
			{"A", "GA", reflect.TypeOf(diffLayerA{})},
			{"B", "GB", reflect.TypeOf(diffLayerB{})},
			{"Z", "ZZ-not-in-golden", reflect.TypeOf(diffLayerB{})}, // golden missing -> skipped
		},
	}

	got := map[string]Finding{}
	for _, f := range DiffFamily(fam, golden) {
		got[f.Field+"/"+f.Kind] = f
	}

	check := func(key string, sev Severity, kind string) {
		f, ok := got[key]
		if !ok {
			t.Errorf("expected finding %s", key)
			return
		}
		if f.Sev != sev || f.Kind != kind {
			t.Errorf("%s: sev=%s kind=%s want %s/%s", key, f.Sev, f.Kind, sev, kind)
		}
	}
	check("nullableField/nullable", SevHigh, KindNullable)
	check("omitField/omit", SevMed, KindOmit)
	check("missReq/missing", SevHigh, KindMissing)
	check("missOpt/missing", SevLow, KindMissing)
	check("sharedField/layer", SevInfo, KindLayer)
	check("extraField/extra", SevInfo, KindExtra)
	if _, ok := got["keep/missing"]; ok {
		t.Error("keep should match, not be reported")
	}
}

// --- unit tests: gate + loaders ---------------------------------------------

func TestRunGate(t *testing.T) {
	findings := []Finding{
		{Layer: "L", Field: "a", Kind: KindMissing, Sev: SevHigh},
		{Layer: "L", Field: "b", Kind: KindOmit, Sev: SevMed},
	}
	allow := []AllowEntry{
		{Layer: "L", Field: "a", Kind: KindMissing, Reason: "ok"},
		{Layer: "L", Field: "gone", Kind: KindMissing, Reason: "stale"},
	}
	violations, stale := RunGate(findings, allow)
	if len(violations) != 1 || violations[0].Field != "b" {
		t.Errorf("violations=%+v want only b", violations)
	}
	if len(stale) != 1 || stale[0].Field != "gone" {
		t.Errorf("stale=%+v want only gone", stale)
	}
}

func TestGatedFindingsReal(t *testing.T) {
	golden, err := LoadGoldenSnapshot(filepath.Join("testdata", "golden_schemas.json"))
	if err != nil {
		t.Fatalf("load golden: %v", err)
	}
	f := GatedFindings(golden)
	if len(f) == 0 {
		t.Error("expected baseline gated findings from real golden snapshot")
	}
	for _, x := range f {
		if x.Sev != SevHigh && x.Sev != SevMed {
			t.Errorf("GatedFindings returned non-gated severity %s", x.Sev)
		}
	}
}

func TestLoaders(t *testing.T) {
	if _, err := LoadGoldenSnapshot("testdata/does-not-exist.json"); err == nil {
		t.Error("expected error for missing golden file")
	}
	if _, err := LoadAllowlist("testdata/does-not-exist.json"); err == nil {
		t.Error("expected error for missing allowlist file")
	}
	// malformed JSON exercises the decode error branch.
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGoldenSnapshot(bad); err == nil {
		t.Error("expected decode error for malformed golden JSON")
	}
	if _, err := LoadAllowlist(bad); err == nil {
		t.Error("expected decode error for malformed allowlist JSON")
	}
}

func TestParseTypesFile(t *testing.T) {
	lines := []string{
		"        Foo: {",
		"            a: string;",
		"        };",
		"        Bar: {",
		"            b: number;",
		"        };",
	}
	got := ParseTypesFile(lines, []string{"Foo", "Bar", "Absent"})
	if len(got) != 2 {
		t.Fatalf("want 2 schemas, got %d (%v)", len(got), got)
	}
	if got["Foo"]["a"].Type != "string" || got["Bar"]["b"].Type != "number" {
		t.Errorf("unexpected parse: %+v", got)
	}
	if _, ok := got["Absent"]; ok {
		t.Error("Absent schema should be skipped, not present")
	}
}

func TestMappingSanity(t *testing.T) {
	names := GoldenSchemaNames()
	if len(names) == 0 {
		t.Fatal("no golden schema names")
	}
	// names must be unique
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("duplicate golden schema name %q", n)
		}
		seen[n] = true
	}
	if len(Families()) == 0 {
		t.Fatal("no families defined")
	}
}
