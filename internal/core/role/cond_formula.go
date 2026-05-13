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
