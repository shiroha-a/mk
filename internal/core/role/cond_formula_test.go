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

// coerceToBaseType / maxNumber / aggregateChatAvailability は internal
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

	// base が bool / string は素通し。
	assert.Equal(t, true, coerceToBaseType(false, true))
	assert.Equal(t, "value", coerceToBaseType("base", "value"))
}

// #1036 review: base が []string (uploadableFileTypes 等) のときの coerce。
// JSON unmarshal は []any で来るので []string に丸める。これで後段の
// aggregatePolicyValues の case []string 分岐が meta.policies override 経由
// でも fire するようになり、set union 集約が一貫して動く。
func TestCoerceToBaseType_StringSlice(t *testing.T) {
	base := []string{"image/*"}

	// []any (JSON unmarshal 由来) → []string に正規化
	got := coerceToBaseType(base, []any{"image/*", " video/* "})
	assert.Equal(t, []string{"image/*", "video/*"}, got)

	// []string はそのまま返す
	assert.Equal(t, []string{"a"}, coerceToBaseType(base, []string{"a"}))

	// 空文字 entry は normalize で skip される
	got = coerceToBaseType(base, []any{"image/*", "", "  "})
	assert.Equal(t, []string{"image/*"}, got)

	// 完全に空 (normalize 後 0 件) は base にフォールバック
	got = coerceToBaseType(base, []any{"", "  "})
	assert.Equal(t, base, got)

	// 型不一致 (string / object 等) は base にフォールバック
	assert.Equal(t, base, coerceToBaseType(base, "not a slice"))
	assert.Equal(t, base, coerceToBaseType(base, 42))
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
// / maxNumber 経由 test だけでなく helper 単体としての挙動も保証する。
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
	got := maxNumber([]any{"not a number", true, nil}, 99)
	assert.Equal(t, 99, got)
}

func TestMaxNumberAsInt_SkipsInvalidFloat(t *testing.T) {
	// NaN / Inf は silently skip。usable entry が無ければ base に倒す。
	got := maxNumber([]any{math.NaN(), math.Inf(1), math.Inf(-1)}, 99)
	assert.Equal(t, 99, got)
	// NaN/Inf 混在でも valid な float entry があれば、それを int に丸めて返す。
	got = maxNumber([]any{math.NaN(), float64(42), math.Inf(1)}, 99)
	assert.Equal(t, 42, got)
}

func TestMaxNumber_ExactMixedNumericOrdering(t *testing.T) {
	assert.Equal(t, 3, maxNumber([]any{int64(3), 2.5}, 0))
	assert.Equal(t, 1.5, maxNumber([]any{1, 1.5}, 0))
	assert.Equal(t, 1.5, maxNumber([]any{1.5, 1}, 0))
	assert.Equal(t, -1, maxNumber([]any{-1.5, -1}, 0))
	assert.Equal(t, -1, maxNumber([]any{-1, -1.5}, 0))
	assert.Equal(t, 2.5, maxNumber([]any{1.5, 2.5}, 0))
}

func TestPolicyUnlimitedOrAboveCap(t *testing.T) {
	assert.True(t, policyUnlimitedOrAboveCap(-1, 10))
	assert.True(t, policyUnlimitedOrAboveCap(11, 10))
	assert.False(t, policyUnlimitedOrAboveCap(5, 10))
	assert.True(t, policyUnlimitedOrAboveCap(int64(-1), 10))
	assert.True(t, policyUnlimitedOrAboveCap(int64(11), 10))
	assert.False(t, policyUnlimitedOrAboveCap(int64(5), 10))
	assert.True(t, policyUnlimitedOrAboveCap(-0.5, 10))
	assert.True(t, policyUnlimitedOrAboveCap(10.5, 10))
	assert.False(t, policyUnlimitedOrAboveCap(0.5, 10))
	assert.False(t, policyUnlimitedOrAboveCap("invalid", 10))
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

// **本人が満たせる条件で管理者を配れないこと (#3037)。**
//
// 管理画面は条件を並べるだけなので、`isCat` にチェックを入れた管理者ロールを
// 作るのは操作としては簡単だが、そのロールは**「猫と名乗る」だけで誰でも
// 取れる**。作った側は「条件を満たす人に配る」つもりで、「誰でも自分で
// 満たせる条件」だとは気付きにくい。
func TestCondDependsOnUserControlledValue(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want bool
	}{
		// 本人が切り替えられる / 積み上げられる。
		{"isCat", `{"type":"isCat"}`, true},
		{"isBot", `{"type":"isBot"}`, true},
		{"isLocked", `{"type":"isLocked"}`, true},
		{"isExplorable", `{"type":"isExplorable"}`, true},
		{"notesMoreThanOrEq", `{"type":"notesMoreThanOrEq","value":10}`, true},
		{"followersMoreThanOrEq", `{"type":"followersMoreThanOrEq","value":10}`, true},
		{"followingLessThanOrEq", `{"type":"followingLessThanOrEq","value":10}`, true},
		// **登録するだけで満たせる (#3037 レビュー 2 周目)。**
		// 「本人が値を変えられるか」で考えると取りこぼしていた。
		{"isLocal", `{"type":"isLocal"}`, true},
		{"createdLessThan", `{"type":"createdLessThan","sec":86400}`, true},
		// 攻撃者がアカウントを用意できない。
		{"isRemote", `{"type":"isRemote"}`, false},
		{"isSuspended", `{"type":"isSuspended"}`, false},
		// **`createdMoreThan` は閾値で変わる (#3037 レビュー 3 周目)。**
		// `evalCondAt` は `t.Before(now - sec)` なので、短い `sec` は
		// 実質全アカウントに一致する。管理画面の既定値は 86400 (1 日)。
		{"createdMoreThan 1時間", `{"type":"createdMoreThan","sec":3600}`, true},
		{"createdMoreThan 1日 (管理画面の既定)", `{"type":"createdMoreThan","sec":86400}`, true},
		{"createdMoreThan 0秒", `{"type":"createdMoreThan","sec":0}`, true},
		// 30 日以上は運営者の明示的な判断として通す。
		{"createdMoreThan 1年", `{"type":"createdMoreThan","sec":31536000}`, false},
		{"createdLessThan 0秒", `{"type":"createdLessThan","sec":0}`, false},
		{"roleAssignedTo", `{"type":"roleAssignedTo","roleId":"r1"}`, false},
		// **否定は向きが入れ替わる。** 新規アカウントは「1 年以上前に
		// 作られて**いない**」「凍結されて**いない**」「そのロールを持って
		// **いない**」をどれもそのまま満たす。
		{"not createdMoreThan", `{"type":"not","value":{"type":"createdMoreThan","sec":31536000}}`, true},
		{"not isSuspended", `{"type":"not","value":{"type":"isSuspended"}}`, true},
		{"not roleAssignedTo", `{"type":"not","value":{"type":"roleAssignedTo","roleId":"r1"}}`, true},
		{"not isLocal", `{"type":"not","value":{"type":"isLocal"}}`, false},
		// **恒真式。** 条件を書いたつもりで全員に一致する。
		{"空の and", `{"type":"and","values":[]}`, true},
		{"空の or を否定", `{"type":"not","value":{"type":"or","values":[]}}`, true},
		{"未知の type を否定", `{"type":"not","value":{"type":"somethingNew"}}`, true},
		// **中身の無い `not` は恒偽 (レビュー 3 周目で訂正)。**
		// `evalCondAt` は nil の operand に false を返すので `not(nil)` は
		// 定数 false = 誰にも一致しない。2 周目は恒真だと決め打って極性を
		// 逆に持っており、**二重否定で符号が戻って `not(not())` が
		// 素通り**していた。
		{"中身の無い not", `{"type":"not"}`, false},
		{"中身の無い not の否定 (恒真)", `{"type":"not","value":{"type":"not"}}`, true},
		{"and(not) の否定 (恒真)", `{"type":"not","value":{"type":"and","values":[{"type":"not"}]}}`, true},
		{"or(not) の否定 (恒真)", `{"type":"not","value":{"type":"or","values":[{"type":"not"}]}}`, true},
		// 空の `or` そのものは誰にも一致しない。
		{"空の or", `{"type":"or","values":[]}`, false},
		// **入れ子も見る。** and / or / not のどこかにあれば同じこと。
		{"and の中", `{"type":"and","values":[{"type":"isLocal"},{"type":"isCat"}]}`, true},
		{"or の中", `{"type":"or","values":[{"type":"isCat"}]}`, true},
		{"not の中", `{"type":"not","value":{"type":"isCat"}}`, true},
		{"深い入れ子", `{"type":"and","values":[{"type":"or","values":[{"type":"not","value":{"type":"isBot"}}]}]}`, true},
		// **`and` は全部を満たす必要がある。** `isLocal` は登録すれば満たせるが
		// 「1 年以上前に作られた」は用意できないので、この組み合わせは安全。
		// 「どこかに危ない葉があるか」で見るとこの正当な設定を弾く。
		{"入れ子だが全部安全", `{"type":"and","values":[{"type":"isLocal"},{"type":"createdMoreThan","sec":31536000}]}`, false},
		// **短い `sec` と組んでも安全にはならない。** 「登録して 1 秒経った
		// ローカル利用者は全員」= 空の `and` と同じ恒真式。
		{"and だが sec が短い", `{"type":"and","values":[{"type":"isLocal"},{"type":"createdMoreThan","sec":1}]}`, true},
		// **`or` はどれか 1 つで足りる。**
		{"or に危ない枝が 1 つ", `{"type":"or","values":[{"type":"createdMoreThan","sec":31536000},{"type":"isCat"}]}`, true},
		{"or が全部安全", `{"type":"or","values":[{"type":"createdMoreThan","sec":31536000},{"type":"isRemote"}]}`, false},
		// **知らない型は判定できないので拒否側に倒す。** 評価は false に
		// 倒れるが、`not` で包まれると恒真式になる。
		{"未知の type", `{"type":"somethingNew"}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var f CondFormula
			require.NoError(t, json.Unmarshal([]byte(tt.raw), &f))
			assert.Equal(t, tt.want, CondDependsOnUserControlledValue(f))
		})
	}
}

// **参照の収集は `not` と入れ子の中まで見る (#3037 レビュー 3 周目)。**
//
// `RoleGrantsPrivilegeIndirectly` がこの結果で「そのロールを配ると特権が付くか」
// を判定する。`not(roleAssignedTo staff)` を持つ特権ロールでは、`staff` を
// **外した**ときにメンバーが特権を得るので、unassign 側の保護にこの枝が要る。
// 2 周目はこの再帰を書いたのにテストを置いておらず、**丸ごと消しても
// `internal/core/role` と `internal/api/admin` が緑のまま**だった。
func TestCondReferencedRoleIDs(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want []string
	}{
		{"単体", `{"type":"roleAssignedTo","roleId":"r1"}`, []string{"r1"}},
		{"not の中", `{"type":"not","value":{"type":"roleAssignedTo","roleId":"r1"}}`, []string{"r1"}},
		{"and の中", `{"type":"and","values":[{"type":"isCat"},{"type":"roleAssignedTo","roleId":"r1"}]}`, []string{"r1"}},
		{
			"深い入れ子で複数",
			`{"type":"or","values":[{"type":"not","value":{"type":"roleAssignedTo","roleId":"a"}},{"type":"and","values":[{"type":"roleAssignedTo","roleId":"b"}]}]}`,
			[]string{"a", "b"},
		},
		{"roleId が空なら拾わない", `{"type":"roleAssignedTo"}`, nil},
		{"参照が無い", `{"type":"isCat"}`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var f CondFormula
			require.NoError(t, json.Unmarshal([]byte(tt.raw), &f))
			assert.Equal(t, tt.want, CondReferencedRoleIDs(f))
		})
	}
}
