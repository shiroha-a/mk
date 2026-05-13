package role

import (
	"encoding/json"
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
