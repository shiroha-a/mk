package role

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// strptr is a small helper to build *string fields inline.
func strptr(s string) *string { return &s }

func TestCondFormula_UnmarshalNumValue(t *testing.T) {
	// followersMoreThanOrEq: `value` フィールドは整数で来る。Custom unmarshal
	// が NumValue に詰めることを確認する (= 旧来の generic Value any 設計と
	// 比べて型安全になっていることの regression guard)。
	raw := `{"type":"followersMoreThanOrEq","value":42}`
	var f CondFormula
	require.NoError(t, json.Unmarshal([]byte(raw), &f))
	assert.Equal(t, CondTypeFollowersMoreThanOrEq, f.Type)
	assert.Equal(t, int64(42), f.NumValue)
	assert.Nil(t, f.Value)
}

func TestCondFormula_UnmarshalNotNested(t *testing.T) {
	// not は value にネストされた CondFormula を持つ。Custom unmarshal が
	// Value (pointer) に nested を詰めることを確認。
	raw := `{"type":"not","value":{"type":"isBot"}}`
	var f CondFormula
	require.NoError(t, json.Unmarshal([]byte(raw), &f))
	assert.Equal(t, CondTypeNot, f.Type)
	require.NotNil(t, f.Value)
	assert.Equal(t, CondTypeIsBot, f.Value.Type)
}

func TestCondFormula_UnmarshalAndOr(t *testing.T) {
	raw := `{"type":"and","values":[{"type":"isBot"},{"type":"isCat"}]}`
	var f CondFormula
	require.NoError(t, json.Unmarshal([]byte(raw), &f))
	assert.Equal(t, CondTypeAnd, f.Type)
	assert.Len(t, f.Values, 2)
	assert.Equal(t, CondTypeIsBot, f.Values[0].Type)
	assert.Equal(t, CondTypeIsCat, f.Values[1].Type)
}

func TestEvalCond_NilUser(t *testing.T) {
	// 防御的: nil user は全 type で false。upstream は user! で non-null
	// 仮定だが、Go では呼び出し元のミスで nil が漏れるので安全側に倒す。
	assert.False(t, EvalCond(nil, nil, CondFormula{Type: CondTypeIsBot}, nil))
}

func TestEvalCond_BooleanUserFlags(t *testing.T) {
	tests := []struct {
		name   string
		user   *model.User
		ftype  CondFormulaType
		expect bool
	}{
		{"isBot=true", &model.User{IsBot: true}, CondTypeIsBot, true},
		{"isBot=false", &model.User{IsBot: false}, CondTypeIsBot, false},
		{"isCat=true", &model.User{IsCat: true}, CondTypeIsCat, true},
		{"isLocked=true", &model.User{IsLocked: true}, CondTypeIsLocked, true},
		{"isSuspended=true", &model.User{IsSuspended: true}, CondTypeIsSuspended, true},
		{"isExplorable=true", &model.User{IsExplorable: true}, CondTypeIsExplorable, true},
		{"isLocal (host nil)", &model.User{}, CondTypeIsLocal, true},
		{"isLocal (host set) false", &model.User{Host: strptr("remote.example")}, CondTypeIsLocal, false},
		{"isRemote (host set)", &model.User{Host: strptr("remote.example")}, CondTypeIsRemote, true},
		{"isRemote (host nil) false", &model.User{}, CondTypeIsRemote, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expect, EvalCond(tc.user, nil, CondFormula{Type: tc.ftype}, nil))
		})
	}
}

func TestEvalCond_NumericComparators(t *testing.T) {
	user := &model.User{FollowersCount: 10, FollowingCount: 5, NotesCount: 100}

	tests := []struct {
		name   string
		ftype  CondFormulaType
		num    int64
		expect bool
	}{
		{"followersLessThanOrEq 10==10", CondTypeFollowersLessThanOrEq, 10, true},
		{"followersLessThanOrEq 10<5", CondTypeFollowersLessThanOrEq, 5, false},
		{"followersMoreThanOrEq 10>=10", CondTypeFollowersMoreThanOrEq, 10, true},
		{"followersMoreThanOrEq 10>=20", CondTypeFollowersMoreThanOrEq, 20, false},
		{"followingLessThanOrEq 5<=10", CondTypeFollowingLessThanOrEq, 10, true},
		{"followingMoreThanOrEq 5>=1", CondTypeFollowingMoreThanOrEq, 1, true},
		{"notesLessThanOrEq 100<=100", CondTypeNotesLessThanOrEq, 100, true},
		{"notesMoreThanOrEq 100>=200", CondTypeNotesMoreThanOrEq, 200, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := CondFormula{Type: tc.ftype, NumValue: tc.num}
			assert.Equal(t, tc.expect, EvalCond(user, nil, f, nil))
		})
	}
}

func TestEvalCond_RoleAssignedTo(t *testing.T) {
	assigned := []*model.Role{
		{ID: "role-a"},
		{ID: "role-b"},
	}
	user := &model.User{ID: "u1"}

	assert.True(t, EvalCond(user, assigned, CondFormula{Type: CondTypeRoleAssignedTo, RoleID: "role-a"}, nil))
	assert.False(t, EvalCond(user, assigned, CondFormula{Type: CondTypeRoleAssignedTo, RoleID: "role-c"}, nil))
	// nil entry は skip して継続する (= panic しない regression guard)。
	assert.True(t, EvalCond(user, []*model.Role{nil, {ID: "role-x"}},
		CondFormula{Type: CondTypeRoleAssignedTo, RoleID: "role-x"}, nil))
}

func TestEvalCond_AndOrNot(t *testing.T) {
	user := &model.User{IsBot: true, IsCat: false}

	// and: isBot && isCat → false
	andF := CondFormula{Type: CondTypeAnd, Values: []CondFormula{
		{Type: CondTypeIsBot}, {Type: CondTypeIsCat},
	}}
	assert.False(t, EvalCond(user, nil, andF, nil))

	// or: isBot || isCat → true
	orF := CondFormula{Type: CondTypeOr, Values: []CondFormula{
		{Type: CondTypeIsBot}, {Type: CondTypeIsCat},
	}}
	assert.True(t, EvalCond(user, nil, orF, nil))

	// not: !isCat → true
	notF := CondFormula{Type: CondTypeNot, Value: &CondFormula{Type: CondTypeIsCat}}
	assert.True(t, EvalCond(user, nil, notF, nil))

	// not with nil operand → false (defensive)
	assert.False(t, EvalCond(user, nil, CondFormula{Type: CondTypeNot}, nil))

	// empty and (=true), empty or (=false): JS / TS 同等
	assert.True(t, EvalCond(user, nil, CondFormula{Type: CondTypeAnd}, nil))
	assert.False(t, EvalCond(user, nil, CondFormula{Type: CondTypeOr}, nil))
}

func TestEvalCond_CreatedComparators(t *testing.T) {
	g, err := id.NewGenerator("aidx")
	require.NoError(t, err)

	// "now" を固定して、その 1 時間前 / 1 時間後の id を生成して評価する。
	now := time.Now()
	pastID := g.Generate(now.Add(-1 * time.Hour))
	user := &model.User{ID: pastID}

	// createdLessThan sec=7200 (= 2h) → user は 1h 前なので「2h 以内」=true
	assert.True(t, evalCondAt(user, nil,
		CondFormula{Type: CondTypeCreatedLessThan, Sec: 7200}, g, now))
	// createdLessThan sec=600 (= 10min) → user は 1h 前なので「10min 以内」=false
	assert.False(t, evalCondAt(user, nil,
		CondFormula{Type: CondTypeCreatedLessThan, Sec: 600}, g, now))
	// createdMoreThan sec=600 → 「10min より前」=true
	assert.True(t, evalCondAt(user, nil,
		CondFormula{Type: CondTypeCreatedMoreThan, Sec: 600}, g, now))
	// createdMoreThan sec=7200 → 「2h より前」=false
	assert.False(t, evalCondAt(user, nil,
		CondFormula{Type: CondTypeCreatedMoreThan, Sec: 7200}, g, now))
}

func TestEvalCond_CreatedComparators_NilGenOrBadID(t *testing.T) {
	user := &model.User{ID: "not-a-valid-id"}
	// idGen 未配線 → false (fail-closed)
	assert.False(t, EvalCond(user, nil, CondFormula{Type: CondTypeCreatedLessThan, Sec: 60}, nil))
	assert.False(t, EvalCond(user, nil, CondFormula{Type: CondTypeCreatedMoreThan, Sec: 60}, nil))

	// idGen 配線済でも parse error → false
	g, _ := id.NewGenerator("aidx")
	assert.False(t, EvalCond(user, nil, CondFormula{Type: CondTypeCreatedLessThan, Sec: 60}, g))
	assert.False(t, EvalCond(user, nil, CondFormula{Type: CondTypeCreatedMoreThan, Sec: 60}, g))
}

func TestEvalCond_UnknownType(t *testing.T) {
	// 未知 type は false に倒す (fail-closed)。upstream で新 type が追加されたとき
	// silently true にして over-permissive にしないための regression guard。
	assert.False(t, EvalCond(&model.User{}, nil, CondFormula{Type: "definitelyNotAType"}, nil))
}

// coerceToBaseType / maxNumberAsInt / aggregateChatAvailability は internal
// helper だが、JSON 値 (float64) → base 型 (int / int64 / float64) の
// 変換ロジックが consumer 側 type assert の安全性を担保するので、direct
// unit test で 100% に近い coverage を狙う。
func TestCoerceToBaseType(t *testing.T) {
	// base が int の場合: int / float64 / 型不一致 (string) は素通し。
	assert.Equal(t, 42, coerceToBaseType(0, 42))
	assert.Equal(t, 42, coerceToBaseType(0, float64(42)))
	assert.Equal(t, "x", coerceToBaseType(0, "x"))

	// base が int64 の場合: int64 / int / float64 / それ以外は素通し。
	assert.Equal(t, int64(7), coerceToBaseType(int64(0), int64(7)))
	assert.Equal(t, int64(7), coerceToBaseType(int64(0), 7))
	assert.Equal(t, int64(7), coerceToBaseType(int64(0), float64(7)))
	assert.Equal(t, true, coerceToBaseType(int64(0), true))

	// base が float64 の場合: float64 / int / それ以外は素通し。
	assert.Equal(t, float64(3.5), coerceToBaseType(float64(0), float64(3.5)))
	assert.Equal(t, float64(3), coerceToBaseType(float64(0), 3))
	assert.Equal(t, "x", coerceToBaseType(float64(0), "x"))

	// base が bool / string / slice 等は素通し。
	assert.Equal(t, true, coerceToBaseType(false, true))
	assert.Equal(t, "value", coerceToBaseType("base", "value"))
}

// coerce は NaN / +-Inf / int レンジ超過の float64 を silently truncate せず
// base に倒す (admin UI で異常値が入ってきた場合の fail-soft)。
func TestCoerceToBaseType_RejectsInvalidFloat(t *testing.T) {
	// base int の場合
	assert.Equal(t, 99, coerceToBaseType(99, math.NaN()))
	assert.Equal(t, 99, coerceToBaseType(99, math.Inf(1)))
	assert.Equal(t, 99, coerceToBaseType(99, math.Inf(-1)))
	// base int64 の場合
	assert.Equal(t, int64(99), coerceToBaseType(int64(99), math.NaN()))
	assert.Equal(t, int64(99), coerceToBaseType(int64(99), math.Inf(1)))
	// base float64 の場合: NaN / Inf も base に倒す (DefaultPolicies に
	// float64 policy は現状無いが将来追加された時に consumer に漏らさない)。
	assert.Equal(t, float64(0.5), coerceToBaseType(float64(0.5), math.NaN()))
	assert.Equal(t, float64(0.5), coerceToBaseType(float64(0.5), math.Inf(1)))
	assert.Equal(t, float64(0.5), coerceToBaseType(float64(0.5), math.Inf(-1)))
}

// `float64(math.MaxInt64)` は IEEE 754 で表現できず `2^63` に丸まるため、
// strict less-than guard でこの境界値も reject する (= int64(2^63) が
// implementation-defined になる落とし穴を踏まない)。
func TestCoerceToBaseType_Rejects2Pow63Boundary(t *testing.T) {
	overflow := float64(math.MaxInt64) // 実体は 2^63、math.MaxInt64+1 相当
	assert.Equal(t, int64(99), coerceToBaseType(int64(99), overflow))
	// 同じ値は int base でも (64-bit platform 想定で) reject される。
	assert.Equal(t, 99, coerceToBaseType(99, overflow))
}

// isFiniteAndInRange の境界条件を直接 cover する。caller の coerceToBaseType
// / maxNumberAsInt 経由 test だけでなく helper 単体としての挙動も保証する。
func TestIsFiniteAndInRange(t *testing.T) {
	// finite + 範囲内 → true (下限は等号許容、上限は厳密 less-than)。
	assert.True(t, isFiniteAndInRange(0, -100, 100))
	assert.True(t, isFiniteAndInRange(-100, -100, 100), "lo 境界は inclusive")
	assert.False(t, isFiniteAndInRange(100, -100, 100), "hi 境界は exclusive")
	assert.False(t, isFiniteAndInRange(101, -100, 100))
	assert.False(t, isFiniteAndInRange(-101, -100, 100))
	// NaN / +-Inf は常に false。
	assert.False(t, isFiniteAndInRange(math.NaN(), -100, 100))
	assert.False(t, isFiniteAndInRange(math.Inf(1), -100, 100))
	assert.False(t, isFiniteAndInRange(math.Inf(-1), -100, 100))
}

func TestMaxNumberAsInt_NoUsableValues(t *testing.T) {
	// 全 entry が int / float64 以外 → base を返す (found=false path)。
	got := maxNumberAsInt([]any{"not a number", true, nil}, 99)
	assert.Equal(t, 99, got)
}

func TestMaxNumberAsInt_SkipsInvalidFloat(t *testing.T) {
	// NaN / Inf は silently skip。usable entry が無ければ base に倒す。
	got := maxNumberAsInt([]any{math.NaN(), math.Inf(1), math.Inf(-1)}, 99)
	assert.Equal(t, 99, got)
	// NaN/Inf 混在でも valid な float entry があれば、それを int に丸めて返す。
	got = maxNumberAsInt([]any{math.NaN(), float64(42), math.Inf(1)}, 99)
	assert.Equal(t, 42, got)
}

func TestAggregateChatAvailability_AvailableShortCircuit(t *testing.T) {
	// "available" を含む場合は readonly / unavailable をスキップして即返す。
	got := aggregateChatAvailability([]any{"readonly", "unavailable", "available"})
	assert.Equal(t, "available", got)
}

// #1034: slice 型 base (uploadableFileTypes 等) は set union で集約される。
// 旧 KeepsBase 挙動からの差分: 単一 override は set union で抽出され、
// override に書かれた pattern が結果に現れる。
func TestAggregatePolicyValues_SliceBaseSetUnion(t *testing.T) {
	base := []string{"image/*"}
	got := aggregatePolicyValues("uploadableFileTypes", base, []any{[]string{"video/*"}})
	// upstream RoleService.calc(...set union) と等価。base は集約対象外で
	// override 値のみから set を作るので base "image/*" は結果に含まれない。
	assert.Equal(t, []string{"video/*"}, got)
}

// values が空なら型に依らず base を返す (= 旧来挙動の維持)。
func TestAggregatePolicyValues_EmptyValuesReturnsBase(t *testing.T) {
	assert.Equal(t, 42, aggregatePolicyValues("anything", 42, nil))
}

// set union の deterministic 出力 + entry trim + 空文字 skip を直接 cover。
func TestAggregatePolicyValues_SliceSetUnionDeterministic(t *testing.T) {
	base := []string{"image/*"}
	// 複数 role 由来の override を flatten + dedup + trim + sort
	got := aggregatePolicyValues("uploadableFileTypes", base, []any{
		[]string{"image/*", "  text/* "}, // trim される
		[]string{"video/*", "image/*"},   // dedup される
		[]string{"audio/*", ""},          // 空文字は skip
	})
	// sort 後の deterministic 順
	assert.Equal(t, []string{"audio/*", "image/*", "text/*", "video/*"}, got)
}

// JSON unmarshal 由来の []any も []string と同じく accept する。
func TestAggregatePolicyValues_SliceSetUnionFromJSONAny(t *testing.T) {
	base := []string{"image/*"}
	got := aggregatePolicyValues("uploadableFileTypes", base, []any{
		[]any{"image/*", "video/*"},
	})
	assert.Equal(t, []string{"image/*", "video/*"}, got)
}

// 全候補から取り出せた entry が 0 個なら base を返す (= fail-soft)。
func TestAggregatePolicyValues_SliceSetUnionEmptyFallsBackToBase(t *testing.T) {
	base := []string{"image/*"}
	// values は空 slice の override / 空文字のみの override / 型不一致の混在。
	got := aggregatePolicyValues("uploadableFileTypes", base, []any{
		[]string{},   // 空 slice
		[]string{""}, // 空文字のみ
		42,           // 型不一致
	})
	// 結果 set が空なので base が返る。
	assert.Equal(t, base, got)
}

// normalizeStringSlice の direct unit test (型 coercion 全 path)。
func TestNormalizeStringSlice(t *testing.T) {
	assert.Nil(t, normalizeStringSlice(nil))
	assert.Equal(t, []string{"a", "b"}, normalizeStringSlice([]string{"a", " b "}))
	assert.Equal(t, []string{}, normalizeStringSlice([]string{"", "  "}))
	assert.Equal(t, []string{"x"}, normalizeStringSlice([]any{42, "x", true, "  "}))
	// 型不一致 (string / slice ではない) は nil
	assert.Nil(t, normalizeStringSlice("not a slice"))
	assert.Nil(t, normalizeStringSlice(42))
}

func TestCondFormula_UnmarshalErrors(t *testing.T) {
	// invalid outer JSON は error を伝搬する。
	var f CondFormula
	require.Error(t, json.Unmarshal([]byte(`not json`), &f))

	// not の operand に malformed JSON が来た場合も error を伝搬する。
	// raw.Value = []byte("not json") は json.RawMessage の constraint は
	// pass するが、nested の json.Unmarshal が失敗する。
	require.Error(t, json.Unmarshal([]byte(`{"type":"not","value":"not-an-object"}`), &f))

	// numeric comparator に文字列が来た場合も error。
	require.Error(t, json.Unmarshal([]byte(`{"type":"followersMoreThanOrEq","value":"abc"}`), &f))
}
