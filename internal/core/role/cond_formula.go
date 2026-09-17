package role

import (
	"encoding/json"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
)

// CondFormulaType enumerates the supported `Role.condFormula.type` values
// (upstream Misskey TS RoleService.evalCond, 17 variants). Documented as
// strings to match the on-disk JSON shape exactly.
type CondFormulaType string

const (
	CondTypeAnd                   CondFormulaType = "and"
	CondTypeOr                    CondFormulaType = "or"
	CondTypeNot                   CondFormulaType = "not"
	CondTypeRoleAssignedTo        CondFormulaType = "roleAssignedTo"
	CondTypeIsLocal               CondFormulaType = "isLocal"
	CondTypeIsRemote              CondFormulaType = "isRemote"
	CondTypeIsSuspended           CondFormulaType = "isSuspended"
	CondTypeIsLocked              CondFormulaType = "isLocked"
	CondTypeIsBot                 CondFormulaType = "isBot"
	CondTypeIsCat                 CondFormulaType = "isCat"
	CondTypeIsExplorable          CondFormulaType = "isExplorable"
	CondTypeCreatedLessThan       CondFormulaType = "createdLessThan"
	CondTypeCreatedMoreThan       CondFormulaType = "createdMoreThan"
	CondTypeFollowersLessThanOrEq CondFormulaType = "followersLessThanOrEq"
	CondTypeFollowersMoreThanOrEq CondFormulaType = "followersMoreThanOrEq"
	CondTypeFollowingLessThanOrEq CondFormulaType = "followingLessThanOrEq"
	CondTypeFollowingMoreThanOrEq CondFormulaType = "followingMoreThanOrEq"
	CondTypeNotesLessThanOrEq     CondFormulaType = "notesLessThanOrEq"
	CondTypeNotesMoreThanOrEq     CondFormulaType = "notesMoreThanOrEq"
)

// CondFormula mirrors `RoleCondFormulaValue` in upstream Misskey TS. The
// shape is intentionally a union: only the fields relevant to `Type` are
// populated, the rest stay zero. JSON unmarshal preserves this exactly,
// so callers should branch on Type before reading other fields.
type CondFormula struct {
	Type CondFormulaType `json:"type"`

	// and / or: nested operand list.
	Values []CondFormula `json:"values,omitempty"`
	// not: single nested operand. Stored as a pointer so we can detect
	// "operand omitted" vs. "zero CondFormula" cases.
	Value *CondFormula `json:"value,omitempty"`

	// roleAssignedTo: target role id.
	RoleID string `json:"roleId,omitempty"`

	// createdLessThan / createdMoreThan: duration in seconds (json field
	// `sec`).
	Sec int64 `json:"sec,omitempty"`

	// followers/following/notes Less/MoreThanOrEq: numeric threshold.
	// json field `value` collides with the `not` operand, so we use a
	// separate intermediate representation via UnmarshalJSON below.
	NumValue int64 `json:"-"`
}

// rawCondFormula is the on-disk shape: both `value` (object) and a numeric
// `value` need to be accepted on the same field per upstream JSON schema.
// Encode/decode through a custom UnmarshalJSON so numeric thresholds are
// captured into NumValue while structural operands go to Value.
type rawCondFormula struct {
	Type   CondFormulaType `json:"type"`
	Values []CondFormula   `json:"values,omitempty"`
	Value  json.RawMessage `json:"value,omitempty"`
	RoleID string          `json:"roleId,omitempty"`
	Sec    int64           `json:"sec,omitempty"`
}

// UnmarshalJSON decodes a CondFormula handling the polymorphic `value`
// field (either a nested formula object for "not" or a numeric threshold
// for follower/note count comparators).
func (c *CondFormula) UnmarshalJSON(data []byte) error {
	var raw rawCondFormula
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Type = raw.Type
	c.Values = raw.Values
	c.RoleID = raw.RoleID
	c.Sec = raw.Sec
	if len(raw.Value) == 0 {
		return nil
	}
	// "not" の operand は CondFormula、それ以外 (numeric comparator) は int。
	switch c.Type {
	case CondTypeNot:
		nested := new(CondFormula)
		if err := json.Unmarshal(raw.Value, nested); err != nil {
			return err
		}
		c.Value = nested
	default:
		// numeric comparator
		if err := json.Unmarshal(raw.Value, &c.NumValue); err != nil {
			return err
		}
	}
	return nil
}

// EvalCond evaluates the conditional role formula against the given user
// and the set of roles already manually assigned to that user. Mirrors
// upstream Misskey TS RoleService.evalCond exactly so cond formulas
// authored in the TS admin UI continue to evaluate identically on mk-go.
//
// idGen is used only by the time-based comparators (createdLessThan /
// createdMoreThan). Pass nil to disable those branches (they return false).
//
// Errors during sub-evaluation (e.g. malformed nested formula) are
// swallowed and treated as false, matching upstream's `catch { return
// false }` behavior. This is a deliberate fail-closed posture for
// untrusted JSON in the DB.
func EvalCond(user *model.User, assignedRoles []*model.Role, formula CondFormula, idGen id.Generator) bool {
	return evalCondAt(user, assignedRoles, formula, idGen, time.Now())
}

// evalCondAt is the testable form of EvalCond. The `now` argument lets
// time-based comparators be exercised deterministically. The exported
// EvalCond uses time.Now() so callers never need to deal with the seam.
func evalCondAt(user *model.User, assignedRoles []*model.Role, formula CondFormula, idGen id.Generator, now time.Time) bool {
	if user == nil {
		return false
	}
	switch formula.Type {
	case CondTypeAnd:
		for _, v := range formula.Values {
			if !evalCondAt(user, assignedRoles, v, idGen, now) {
				return false
			}
		}
		return true
	case CondTypeOr:
		for _, v := range formula.Values {
			if evalCondAt(user, assignedRoles, v, idGen, now) {
				return true
			}
		}
		return false
	case CondTypeNot:
		if formula.Value == nil {
			return false
		}
		return !evalCondAt(user, assignedRoles, *formula.Value, idGen, now)
	case CondTypeRoleAssignedTo:
		for _, r := range assignedRoles {
			if r != nil && r.ID == formula.RoleID {
				return true
			}
		}
		return false
	case CondTypeIsLocal:
		return user.Host == nil
	case CondTypeIsRemote:
		return user.Host != nil
	case CondTypeIsSuspended:
		return user.IsSuspended
	case CondTypeIsLocked:
		return user.IsLocked
	case CondTypeIsBot:
		return user.IsBot
	case CondTypeIsCat:
		return user.IsCat
	case CondTypeIsExplorable:
		return user.IsExplorable
	case CondTypeCreatedLessThan:
		// upstream は「user の id 由来の作成時刻 > now - sec」を「最近作られた」
		// として true。idGen 未配線時は false に倒す (formula 解釈不能扱い)。
		if idGen == nil {
			return false
		}
		t, err := idGen.ParseTime(user.ID)
		if err != nil {
			return false
		}
		return t.After(now.Add(-time.Duration(formula.Sec) * time.Second))
	case CondTypeCreatedMoreThan:
		if idGen == nil {
			return false
		}
		t, err := idGen.ParseTime(user.ID)
		if err != nil {
			return false
		}
		return t.Before(now.Add(-time.Duration(formula.Sec) * time.Second))
	case CondTypeFollowersLessThanOrEq:
		return int64(user.FollowersCount) <= formula.NumValue
	case CondTypeFollowersMoreThanOrEq:
		return int64(user.FollowersCount) >= formula.NumValue
	case CondTypeFollowingLessThanOrEq:
		return int64(user.FollowingCount) <= formula.NumValue
	case CondTypeFollowingMoreThanOrEq:
		return int64(user.FollowingCount) >= formula.NumValue
	case CondTypeNotesLessThanOrEq:
		return int64(user.NotesCount) <= formula.NumValue
	case CondTypeNotesMoreThanOrEq:
		return int64(user.NotesCount) >= formula.NumValue
	default:
		// 未知 type は false に倒す。新 type が upstream で追加されたとき
		// silently true にして over-permissive にしない fail-closed 設計。
		return false
	}
}

// condSatisfiability says whether a new account can satisfy a leaf condition,
// and whether it can satisfy its negation.
//
// **述語は「本人がその値を変えられるか」ではなく「条件を満たすアカウントを
// 自分で用意できるか」(#3037 レビュー 2 周目)。** 前者で考えると `isLocal` と
// `createdLessThan` を取りこぼす — どちらも**登録するだけ**で満たせるので、
// `isLocal` + `isAdministrator` は「このインスタンスの全ローカル利用者が
// 管理者」、`createdLessThan` は「今から登録した人が管理者」になる。
// `isCat` と同じ危険度なのに、1 周目の集合はどちらも通していた。
//
// **否定側を別に持つのが要点。** `not(createdMoreThan 1年)` は「作られて
// 1 年未満」= 登録するだけで満たせるが、`createdMoreThan` 自体は満たせない。
// 1 つの集合で「危ないかどうか」を決めると、この非対称を表せない。
type condSatisfiability struct {
	positive bool // その条件そのものを満たせるか
	negative bool // その条件の否定を満たせるか
}

// condLeafSatisfiability is the table for every non-composite type.
var condLeafSatisfiability = map[CondFormulaType]condSatisfiability{
	// 登録するだけで満たせる。古いアカウントは用意できない。
	CondTypeIsLocal:         {positive: true, negative: false},
	CondTypeCreatedLessThan: {positive: true, negative: false},
	CondTypeCreatedMoreThan: {positive: false, negative: true},
	// リモート利用者はこのインスタンスにサインインできないので、ロールの
	// 権限を API で使えない。逆に「リモートでない」は登録すれば満たせる。
	CondTypeIsRemote: {positive: false, negative: true},
	// 凍結はモデレーターが決めるうえ、凍結中はサインインできない。
	// 「凍結されていない」は新規アカウントがそのまま満たす。
	CondTypeIsSuspended: {positive: false, negative: true},
	// プロフィール設定のトグル。どちらの向きにもできる。
	CondTypeIsLocked:     {positive: true, negative: true},
	CondTypeIsBot:        {positive: true, negative: true},
	CondTypeIsCat:        {positive: true, negative: true},
	CondTypeIsExplorable: {positive: true, negative: true},
	// 新規アカウントは 0 なので下限側はそのまま満たし、上限側は捨て
	// アカウントを並べれば積み上げられる。
	CondTypeFollowersLessThanOrEq: {positive: true, negative: true},
	CondTypeFollowersMoreThanOrEq: {positive: true, negative: true},
	CondTypeFollowingLessThanOrEq: {positive: true, negative: true},
	CondTypeFollowingMoreThanOrEq: {positive: true, negative: true},
	CondTypeNotesLessThanOrEq:     {positive: true, negative: true},
	CondTypeNotesMoreThanOrEq:     {positive: true, negative: true},
	// **誰かが配る必要がある。** ただし配れるのがモデレーターなら迂回に
	// なるので、そちらは `RoleGrantsPrivilegeIndirectly` が別に見る。
	// 「そのロールを持っていない」は新規アカウントがそのまま満たす。
	CondTypeRoleAssignedTo: {positive: false, negative: true},
}

// CondDependsOnUserControlledValue reports whether an account the attacker can
// create would satisfy the formula.
//
// **条件つきロールで管理者 / モデレーター / 特権 policy を配れるかの判定に
// 使う。** 管理画面は条件を並べるだけなので、`isCat` にチェックを入れた
// 管理者ロールを作るのは操作としてはごく簡単だが、**そのロールは「猫と名乗る」
// だけで誰でも取れる**。作った側は「条件を満たす人に配る」つもりで、「誰でも
// 自分で満たせる条件」だとは気付きにくい。
func CondDependsOnUserControlledValue(f CondFormula) bool {
	return condSatisfiable(f, false)
}

// condSatisfiable folds the formula, carrying whether we are under a negation.
//
// **and / or は畳み方が違う。** `and` は全部を満たす必要があるので
// `and(isLocal, createdMoreThan 1年)` は満たせない (登録しただけの
// アカウントは 1 年前に作られていない)。素朴に「どこかに危ない葉があるか」で
// 見ると、この正当な設定を弾いてしまう。否定の下では De Morgan で入れ替わる。
func condSatisfiable(f CondFormula, negated bool) bool {
	switch f.Type {
	case CondTypeNot:
		// **中身が無い `not` は `not(false)` = 恒真。**
		if f.Value == nil {
			return !negated
		}
		return condSatisfiable(*f.Value, !negated)
	case CondTypeAnd:
		// 空の `and` は恒真 (否定すると恒偽)。
		if len(f.Values) == 0 {
			return !negated
		}
		// 肯定の `and` は全部を満たす必要がある。否定の下では
		// De Morgan で `or(not ...)` になるのでどれか 1 つでよい。
		return foldOperands(f.Values, negated, negated)
	case CondTypeOr:
		// 空の `or` は恒偽 (否定すると恒真)。
		if len(f.Values) == 0 {
			return negated
		}
		// 肯定の `or` はどれか 1 つ。否定の下では `and(not ...)` になる。
		return foldOperands(f.Values, negated, !negated)
	}
	sat, known := condLeafSatisfiability[f.Type]
	if !known {
		// **知らない型は判定できないので拒否側に倒す。**
		// `evaluateCondFormula` は未知 type を false にするので、`not` で
		// 包むと**恒真式**になる (`not(bogus)` は全員に一致)。
		return true
	}
	if negated {
		return sat.negative
	}
	return sat.positive
}

// foldOperands combines operands with "any" when anyWins, else with "all".
func foldOperands(values []CondFormula, negated, anyWins bool) bool {
	if anyWins {
		for _, v := range values {
			if condSatisfiable(v, negated) {
				return true
			}
		}
		return false
	}
	for _, v := range values {
		if !condSatisfiable(v, negated) {
			return false
		}
	}
	return true
}

// CondReferencedRoleIDs collects every roleId the formula keys off.
//
// **`roleAssignedTo` は「誰かが配る必要がある」ので単体では自己付与できない
// が、配れるのがモデレーターなら迂回になる (#3037 レビュー 2 周目)。**
// 呼び出し側 (`RoleGrantsPrivilegeIndirectly`) がその判定に使う。
func CondReferencedRoleIDs(f CondFormula) []string {
	var ids []string
	if f.Type == CondTypeRoleAssignedTo && f.RoleID != "" {
		ids = append(ids, f.RoleID)
	}
	if f.Value != nil {
		ids = append(ids, CondReferencedRoleIDs(*f.Value)...)
	}
	for _, v := range f.Values {
		ids = append(ids, CondReferencedRoleIDs(v)...)
	}
	return ids
}
