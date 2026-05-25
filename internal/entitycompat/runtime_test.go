package entitycompat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
)

// --- unit tests: union parser + runtime validator ---------------------------

func TestParseUnion(t *testing.T) {
	lines := []string{
		"        N: {",
		"            id: string;",
		"            type: 'a';",
		"            onlyA: string;",
		"        } | {",
		"            id: string;",
		"            type: 'b';",
		"            onlyB: number | null;",
		"        };",
	}
	got := ParseUnion(lines, "N")
	if len(got) != 2 {
		t.Fatalf("want 2 variants, got %d: %v", len(got), got)
	}
	if _, ok := got["a"]["onlyA"]; !ok {
		t.Error("variant a missing onlyA")
	}
	if f, ok := got["b"]["onlyB"]; !ok || !f.Nullable {
		t.Errorf("variant b onlyB: %+v ok=%v (want nullable)", f, ok)
	}
	if ParseUnion(lines, "Absent") != nil {
		t.Error("absent union should return nil")
	}
}

func TestLiteralOf(t *testing.T) {
	cases := map[string]string{"'follow';": "follow", "'a' | 'b'": "a", "string;": "", "": ""}
	for in, want := range cases {
		if got := literalOf(in); got != want {
			t.Errorf("literalOf(%q)=%q want %q", in, got, want)
		}
	}
}

func TestValidateValue(t *testing.T) {
	schema := Schema{
		"req":      {Type: "string"},
		"nullable": {Type: "string", Nullable: true},
		"nonnull":  {Type: "string"},
		"opt":      {Type: "string", Optional: true},
		"num":      {Type: "number"},
	}
	actual := map[string]any{
		"req":      "ok",
		"nullable": nil,   // allowed (nullable)
		"nonnull":  nil,   // VIOLATION: null on non-null
		"num":      "str", // VIOLATION: type mismatch
		"extra":    1,     // INFO
		// "opt" absent -> allowed
	}
	got := map[string]Severity{}
	for _, f := range ValidateValue("L", "G", actual, schema) {
		got[f.Field+"/"+f.Kind] = f.Sev
	}
	if got["nonnull/nullable"] != SevHigh {
		t.Error("expected nonnull null violation HIGH")
	}
	if got["num/type"] != SevMed {
		t.Error("expected num type mismatch MED")
	}
	if got["extra/extra"] != SevInfo {
		t.Error("expected extra INFO")
	}
	if _, ok := got["nullable/nullable"]; ok {
		t.Error("nullable=null must not be flagged")
	}
	if _, ok := got["opt/missing"]; ok {
		t.Error("absent optional must not be flagged")
	}

	// missing required
	got2 := map[string]Severity{}
	for _, f := range ValidateValue("L", "G", map[string]any{"nullable": "x", "nonnull": "x", "num": 1}, schema) {
		got2[f.Field+"/"+f.Kind] = f.Sev
	}
	if got2["req/missing"] != SevHigh {
		t.Error("expected req missing HIGH")
	}
}

func TestValidateUnionValue(t *testing.T) {
	variants := map[string]Schema{
		"a": {"id": {Type: "string"}, "type": {Type: "string"}},
	}
	// matching variant, clean
	if f := ValidateUnionValue("N", map[string]any{"type": "a", "id": "x"}, variants); len(f) != 0 {
		t.Errorf("clean variant should have no findings, got %v", f)
	}
	// unknown type literal
	f := ValidateUnionValue("N", map[string]any{"type": "zzz"}, variants)
	if len(f) != 1 || f[0].Sev != SevHigh {
		t.Errorf("unknown type should be 1 HIGH finding, got %v", f)
	}
}

func TestJSONTypeAndScalar(t *testing.T) {
	s := "x"
	cases := []struct {
		v    any
		want string
	}{
		{"x", "string"}, {true, "boolean"}, {1, "number"}, {1.5, "number"},
		{[]int{1}, "array"}, {map[string]int{}, "object"}, {struct{}{}, "object"},
		{nil, "null"}, {&s, "string"}, {(*string)(nil), "null"},
	}
	for _, c := range cases {
		if got := jsonType(c.v); got != c.want {
			t.Errorf("jsonType(%#v)=%q want %q", c.v, got, c.want)
		}
	}
	if !scalarMismatch("string", "number") {
		t.Error("string vs number should mismatch")
	}
	if scalarMismatch("object", "array") {
		t.Error("object/array buckets must be skipped")
	}
	if scalarMismatch("string", "string") {
		t.Error("same type must not mismatch")
	}
}

// --- L2 integration: validate real PackNotification output ------------------

func loadUnions(t *testing.T) map[string]map[string]Schema {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "golden_unions.json"))
	if err != nil {
		t.Fatalf("read golden_unions: %v", err)
	}
	var u map[string]map[string]Schema
	if err := json.Unmarshal(b, &u); err != nil {
		t.Fatalf("decode golden_unions: %v", err)
	}
	return u
}

// notifFixture builds a packed notification map for a given type using the
// same construction pattern as the entity package's own packer tests.
func notifFixture(typ notification.Type, withUser, withNote bool, extra map[string]any) map[string]any {
	idGen, _ := id.NewGenerator("aidx")
	n := &notification.Notification{
		ID:         "n1",
		CreatedAt:  time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		Type:       typ,
		NotifierID: "u_actor",
		Extra:      extra,
	}
	var user *model.User
	var note *model.Note
	if withUser {
		user = &model.User{ID: "u_actor", Username: "actor", UsernameLower: "actor"}
	}
	if withNote {
		s := "hi"
		n.NoteID = "note1"
		note = &model.Note{ID: "note1", UserID: "u_me", Text: &s}
	}
	if typ == notification.TypeReaction {
		n.Reaction = ":smile:"
	}
	return entity.PackNotification(n, user, note, idGen, nil, nil)
}

// TestNotificationShapeL2 validates actual PackNotification map output against
// the golden Notification union — the map-based, discriminated-union surface
// that the Layer 0 reflection gate cannot reach.
func TestNotificationShapeL2(t *testing.T) {
	unions := loadUnions(t)
	notif := unions["Notification"]
	if len(notif) == 0 {
		t.Fatal("no Notification union variants in snapshot")
	}

	cases := []struct {
		typ      notification.Type
		withUser bool
		withNote bool
		extra    map[string]any
	}{
		{notification.TypeFollow, true, false, nil},
		{notification.TypeReaction, true, true, nil},
		{notification.TypeMention, true, true, nil},
		{notification.TypeReply, true, true, nil},
		{notification.TypeRenote, true, true, nil},
		{notification.TypeQuote, true, true, nil},
		{notification.TypeReceiveFollowReq, true, false, nil},
		{notification.TypePollVote, true, true, nil},
		{notification.TypeImportCompleted, false, false, map[string]any{"fileId": "df_1"}},
	}

	allow, err := LoadAllowlist(filepath.Join("testdata", "allowlist_l2.json"))
	if err != nil {
		t.Fatalf("load L2 allowlist: %v", err)
	}

	var gated []Finding
	for _, c := range cases {
		out := notifFixture(c.typ, c.withUser, c.withNote, c.extra)
		for _, f := range ValidateUnionValue("Notification", out, notif) {
			if f.Sev == SevHigh || f.Sev == SevMed {
				gated = append(gated, f)
			}
		}
	}

	violations, stale := RunGate(gated, allow)
	for _, v := range violations {
		t.Errorf("NEW notification shape drift: %s [%s]: %s\n  -> fix the packer, or add to testdata/allowlist_l2.json with a reason",
			v.Field, v.Kind, v.Detail)
	}
	for _, s := range stale {
		t.Errorf("stale L2 allowlist entry: %s [%s] no longer drifts; remove it from testdata/allowlist_l2.json", s.Field, s.Kind)
	}
}
