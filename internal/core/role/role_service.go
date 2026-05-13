package role

import (
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"gorm.io/gorm"
)

var (
	// ErrRoleNotFound is returned when the target role does not exist.
	ErrRoleNotFound = errors.New("role not found")
	// ErrAlreadyAssigned is returned when the user already has the role.
	ErrAlreadyAssigned = errors.New("role already assigned")
	// ErrNotAssigned is returned when the user does not have the role.
	ErrNotAssigned = errors.New("role not assigned")
)

// Policy key constants. requiredRolePolicy gate 経路 (HasRolePolicy / 各 endpoint
// での policy 文字列参照) で typo を防ぐため定数化する。新規 policy-gated
// endpoint を追加する際はここに対応 const を生やしてから call site で使う。
const (
	// PolicyCanCreateChannel gates /api/channels/create (upstream Misskey
	// #17121 / triage #1012)。
	PolicyCanCreateChannel = "canCreateChannel"

	// 以下は #1020 audit で middleware gate に昇格したもの。実 enforcement は
	// internal/server/middleware/role_policy.go の RequireRolePolicy 経由。
	// default 値はいずれも role_service.go の DefaultPolicies() を参照。
	PolicyCanSearchNotes     = "canSearchNotes"
	PolicyCanSearchUsers     = "canSearchUsers"
	PolicyCanUseTranslator   = "canUseTranslator"
	PolicyCanInvite          = "canInvite"
	PolicyCanImportFollowing = "canImportFollowing"
	PolicyCanImportBlocking  = "canImportBlocking"
	PolicyCanImportMuting    = "canImportMuting"
	PolicyCanImportUserLists = "canImportUserLists"
	PolicyCanImportAntennas  = "canImportAntennas"
)

// roleCacheTTL は GetUserRoles キャッシュの有効期限。Misskey TS 同等の
// admin 操作 (admin/roles/* 経路) は頻度が低いので、5 分に揃えて
// IsAdministrator / IsModerator / GetUserPolicies の DB 負荷を消す
// (#300 3-5)。
const roleCacheTTL = 5 * time.Minute

// roleCacheEntry は per-user の roles snapshot + 失効時刻。
type roleCacheEntry struct {
	roles     []*model.Role
	expiresAt time.Time
}

// Service manages roles and role assignments.
type Service struct {
	roleRepo       repository.RoleRepository
	assignmentRepo repository.RoleAssignmentRepository
	metaRepo       repository.MetaRepository
	idGen          id.Generator
	// userRepo は drop-in 互換 (#785) のため optional に注入される。
	// Misskey TS upstream で signup された root user は user.isRoot=true で
	// 識別され meta.rootUserId は set されない。drop-in で TS DB を引き継いだ
	// mk-go は meta.rootUserId が nil → isRootUser false → admin path で 403
	// となる regression を防ぐため、user.isRoot フラグを fallback として
	// 確認する。本番経路 (mk-go pure) では meta.rootUserId が常に set される
	// ので setter で wire しなくても挙動は変わらない (= 後方互換性は保たれる)。
	userRepo repository.UserRepository

	// userRoleCache は GetUserRoles 結果の per-user TTL キャッシュ
	// (sync.Map で hot path に lock を持ち込まない)。Assign / Unassign で
	// 当該 userID を、Delete (role 削除) で全 entry を invalidate する。
	// admin/roles/update が roleRepo を直接叩く経路は TTL でしかカバー
	// できないが、roleCacheTTL = 5 min で staleness は bounded (#300 3-5)。
	userRoleCache sync.Map // userID -> *roleCacheEntry
}

// NewService constructs a RoleService.
func NewService(
	roleRepo repository.RoleRepository,
	assignmentRepo repository.RoleAssignmentRepository,
	metaRepo repository.MetaRepository,
	idGen id.Generator,
) *Service {
	return &Service{
		roleRepo:       roleRepo,
		assignmentRepo: assignmentRepo,
		metaRepo:       metaRepo,
		idGen:          idGen,
	}
}

// SetUserRepo wires a UserRepository so isRootUser can fall back to the
// user.isRoot column for drop-in compatibility with Misskey TS DBs that
// don't populate meta.rootUserId (#785). Optional; when nil the service
// behaves as before (meta.rootUserId-only check).
//
// 想定は wire-time only (server.setupRoutes 経由)。HTTP リスナー起動後の
// 再代入は isRootUser 側の concurrent read と race するので呼ばないこと。
func (s *Service) SetUserRepo(r repository.UserRepository) {
	s.userRepo = r
}

// InvalidateUserRoleCache drops the cached role list for userID. Out-of-band
// admin paths (e.g. role mutation via repo) can call this to force a refresh.
func (s *Service) InvalidateUserRoleCache(userID string) {
	if userID == "" {
		return
	}
	s.userRoleCache.Delete(userID)
}

// InvalidateAllRoleCaches drops every cached entry. Used when a role is
// deleted (we don't know which users were assigned without an extra DB hit,
// so the simplest safe action is to flush the whole cache).
func (s *Service) InvalidateAllRoleCaches() {
	s.userRoleCache.Range(func(k, _ any) bool {
		s.userRoleCache.Delete(k)
		return true
	})
}

// GetUserRoles returns all active roles applied to the user. The returned
// slice is the union of (a) manually assigned roles that have not expired
// and (b) `target=conditional` roles whose `condFormula` evaluates to true
// for the user. Mirrors upstream Misskey TS `RoleService.getUserRoles` so
// admin-authored conditional roles (e.g. "base role for all local users")
// take effect at gate sites like HasRolePolicy (#1020).
//
// 結果は roleCacheTTL 期間 in-memory にキャッシュされる (#300 3-5)。
// Conditional 評価結果も同じ cache に乗るので、user の followers/notes
// count などが変動しても最大 5 分の反映遅延がある点に注意 (= upstream TS
// と同じ trade-off)。
func (s *Service) GetUserRoles(userID string) ([]*model.Role, error) {
	if userID == "" {
		return nil, nil
	}
	if v, ok := s.userRoleCache.Load(userID); ok {
		if entry, ok := v.(*roleCacheEntry); ok && time.Now().Before(entry.expiresAt) {
			return entry.roles, nil
		}
		s.userRoleCache.Delete(userID)
	}

	assignments, err := s.assignmentRepo.ListByUser(userID)
	if err != nil {
		return nil, err
	}
	assignedRoles := make([]*model.Role, 0, len(assignments))
	for _, a := range assignments {
		if a.Role != nil {
			assignedRoles = append(assignedRoles, a.Role)
		}
	}

	// Conditional role 評価: 全 role を fetch して target=conditional のみを
	// formula 評価で絞り込む。assigned roles と matched conditional の和集合
	// を返す。upstream TS の getUserRoles と同じ順 (assigned が先)。
	//
	// roleRepo / userRepo どちらかが未配線なら、conditional 評価は skip して
	// assigned のみを返す (= 旧挙動)。test 経路で userRepo 未注入のケースに
	// 配慮した soft-fail。
	condRoles := s.evaluateConditionalRoles(userID, assignedRoles)
	roles := append(assignedRoles, condRoles...)
	s.userRoleCache.Store(userID, &roleCacheEntry{
		roles:     roles,
		expiresAt: time.Now().Add(roleCacheTTL),
	})
	return roles, nil
}

// evaluateConditionalRoles returns the subset of `target=conditional`
// roles whose `condFormula` evaluates to true for the user. Best-effort:
// when prerequisites are missing (no userRepo wired, role list fetch
// fails, user lookup fails, formula JSON malformed) we return nil so
// callers fall back to assigned roles only. Matches upstream TS where
// evalCond swallows all errors as false.
func (s *Service) evaluateConditionalRoles(userID string, assigned []*model.Role) []*model.Role {
	if s.userRepo == nil {
		return nil
	}
	allRoles, err := s.roleRepo.List()
	if err != nil {
		return nil
	}
	// 早期 short-circuit: conditional role が 1 つも無いなら user fetch を
	// 省略する (= upstream TS と同 optimization)。これで大多数のインスタンス
	// では DB round-trip が 1 回減る。
	hasCond := false
	for _, r := range allRoles {
		if r != nil && r.Target == model.RoleTargetConditional {
			hasCond = true
			break
		}
	}
	if !hasCond {
		return nil
	}
	user, err := s.userRepo.FindByID(userID)
	if err != nil || user == nil {
		return nil
	}
	matched := make([]*model.Role, 0)
	for _, r := range allRoles {
		if r == nil || r.Target != model.RoleTargetConditional {
			continue
		}
		var formula CondFormula
		// CondFormula は datatypes.JSON。未設定だと "{}" が入っていて
		// type が空文字列のため EvalCond で false に倒れる。
		if err := json.Unmarshal(r.CondFormula, &formula); err != nil {
			continue
		}
		if EvalCond(user, assigned, formula, s.idGen) {
			matched = append(matched, r)
		}
	}
	return matched
}

// isRootUser checks if the user is the root user.
//
//  1. meta.rootUserId と一致 (mk-go native パス)
//  2. user.isRoot=true (drop-in 互換、Misskey TS DB から引き継いだ場合の
//     fallback。userRepo が SetUserRepo で wire 済みのときのみ確認、#785)
//
// userRepo 未配線でも (1) のみで動作するので既存テスト互換性は保たれる。
func (s *Service) isRootUser(userID string) bool {
	if meta, err := s.metaRepo.Fetch(); err == nil && meta.RootUserID != nil && *meta.RootUserID == userID {
		return true
	}
	if s.userRepo != nil {
		u, err := s.userRepo.FindByID(userID)
		switch {
		case err == nil:
			if u != nil && u.IsRoot {
				return true
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
			// 削除済みユーザー / 不正な userID は root ではない、ログ不要。
		default:
			// 接続不良などの transient な障害は観測したい (admin path 限定で
			// 頻度が低いのでログコストは無視できる)。
			slog.Warn("role.isRootUser: userRepo.FindByID failed",
				"userID", userID, "err", err)
		}
	}
	return false
}

// IsAdministrator checks if the user has any administrator role or is root.
func (s *Service) IsAdministrator(userID string) bool {
	if s.isRootUser(userID) {
		return true
	}
	roles, err := s.GetUserRoles(userID)
	if err != nil {
		return false
	}
	for _, r := range roles {
		if r.IsAdministrator {
			return true
		}
	}
	return false
}

// IsSilenced reports whether the user's merged role policies deny
// `canPublicNote`. Mirrors upstream Misskey where silencing is not a
// direct user flag but the outcome of a policy override on an assigned
// role. Users without any overriding role fall back to DefaultPolicies
// (canPublicNote=true) and are therefore not silenced.
func (s *Service) IsSilenced(userID string) bool {
	policies := s.GetUserPolicies(userID)
	if canPublic, ok := policies["canPublicNote"].(bool); ok {
		return !canPublic
	}
	return false
}

// IsModerator checks if the user has any moderator/admin role or is root.
func (s *Service) IsModerator(userID string) bool {
	if s.isRootUser(userID) {
		return true
	}
	roles, err := s.GetUserRoles(userID)
	if err != nil {
		return false
	}
	for _, r := range roles {
		if r.IsModerator || r.IsAdministrator {
			return true
		}
	}
	return false
}

// HasRolePolicy reports whether `userID` is allowed to perform an action gated
// by `policyKey`. upstream `ApiCallService.ts` の requiredRolePolicy check と
// 等価な判定で、以下のいずれかなら true:
//
//  1. root user (= meta.rootUserId == userID or user.isRoot)
//  2. admin role assignment あり (IsAdministrator が内部で root も含む)
//  3. merged policies[policyKey] が bool true
//
// 未知 policyKey や非 bool 値が来ると false (= deny) に倒す。policy 配線忘れ
// による over-permissive な事故を避ける fail-closed 設計。
func (s *Service) HasRolePolicy(userID, policyKey string) bool {
	if s.IsAdministrator(userID) {
		return true
	}
	policies := s.GetUserPolicies(userID)
	v, ok := policies[policyKey]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// rolePolicyOverride mirrors the on-disk shape of one entry in
// `Role.policies`:
//
//	{ "<key>": { "useDefault": bool, "priority": 0|1|2, "value": any } }
type rolePolicyOverride struct {
	UseDefault bool `json:"useDefault"`
	Priority   int  `json:"priority"`
	Value      any  `json:"value"`
}

// GetUserPolicies returns the user's effective role policies. The
// computation mirrors upstream Misskey TS `RoleService.getUserPolicies`
// so admin-authored policy overrides take effect identically (#1020):
//
//  1. Base policies = `DefaultPolicies()` merged with `meta.policies`
//     (admin が UI で設定する全 user 適用 base override)。
//  2. 各 policy key について、user の全 role (manual + conditional) から
//     その key の override を集める。entry が無い role は `useDefault=true,
//     priority=0` 扱い。
//  3. 最高 priority (2 → 1 → 0) のグループだけを aggregator で集約する:
//     - bool policies は `vs.some(v => v === true)` 相当 (OR)
//     - 数値 policies は `Math.max(...vs)` 相当
//     - `chatAvailability` は available > readonly > unavailable で優先集約
//  4. `useDefault=true` の override は base policies[name] を採用する。
//
// userID が "" の場合は base policies のみを返す (= upstream の userId==null
// と同等)。
func (s *Service) GetUserPolicies(userID string) map[string]any {
	basePolicies := DefaultPolicies()
	s.applyMetaBasePolicies(basePolicies)

	if userID == "" {
		return basePolicies
	}
	roles, err := s.GetUserRoles(userID)
	if err != nil {
		// role 取得失敗時は base policies で fallback (upstream は throw
		// するが、mk-go は fail-soft で gate を default 値に倒す)。
		return basePolicies
	}
	if len(roles) == 0 {
		return basePolicies
	}

	// 各 role の policies JSON を Unmarshal して一度だけ展開する。
	roleOverrides := make([]map[string]rolePolicyOverride, 0, len(roles))
	for _, r := range roles {
		if r == nil || len(r.Policies) == 0 {
			roleOverrides = append(roleOverrides, nil)
			continue
		}
		var m map[string]rolePolicyOverride
		if err := json.Unmarshal(r.Policies, &m); err != nil {
			roleOverrides = append(roleOverrides, nil)
			continue
		}
		roleOverrides = append(roleOverrides, m)
	}

	out := make(map[string]any, len(basePolicies))
	for key, baseVal := range basePolicies {
		out[key] = computePolicy(key, baseVal, roleOverrides)
	}
	return out
}

// applyMetaBasePolicies overlays `meta.policies` (admin UI 設定の base
// override) onto the default policies map. Best-effort: meta fetch /
// JSON unmarshal の失敗は silently skip して default のままにする
// (= upstream TS と同じ fail-soft 挙動)。
//
// JSON unmarshal は数値を float64 に倒すため、key が DefaultPolicies で int
// として宣言されている場合は明示的に int へ丸めて整合性を保つ (#1020 review:
// consumer 側の `policies[key].(int)` type assert が float64 で panic する
// regression を防ぐ)。base に無い未知 key は型情報が無いので素通し。
func (s *Service) applyMetaBasePolicies(base map[string]any) {
	if s.metaRepo == nil {
		return
	}
	meta, err := s.metaRepo.Fetch()
	if err != nil || meta == nil || len(meta.Policies) == 0 {
		return
	}
	var metaPolicies map[string]any
	if err := json.Unmarshal(meta.Policies, &metaPolicies); err != nil {
		return
	}
	for k, v := range metaPolicies {
		base[k] = coerceToBaseType(base[k], v)
	}
}

// coerceToBaseType normalises a JSON-decoded value (typically float64 for
// numbers) into the base default's Go type so downstream consumers can
// type-assert safely. Only int / int64 / float64 numeric coercions are
// handled; bool / string / slice values pass through unchanged.
func coerceToBaseType(base, override any) any {
	switch base.(type) {
	case int:
		if f, ok := override.(float64); ok {
			return int(f)
		}
		if i, ok := override.(int); ok {
			return i
		}
	case int64:
		if f, ok := override.(float64); ok {
			return int64(f)
		}
		if i, ok := override.(int); ok {
			return int64(i)
		}
	case float64:
		if i, ok := override.(int); ok {
			return float64(i)
		}
	}
	return override
}

// policyEntry pairs a role's effective value for a given policy key with
// its declared priority (0/1/2). Used internally by computePolicy.
type policyEntry struct {
	priority int
	value    any
}

// computePolicy resolves the effective value for a single policy key by
// applying upstream TS priority cascade + per-key aggregator. baseVal is
// the merged default+meta value used when a role specifies useDefault=true
// or when no role has an override.
func computePolicy(key string, baseVal any, roleOverrides []map[string]rolePolicyOverride) any {
	// 各 role について本 policy の override を組み立てる。entry 無し =
	// priority=0, useDefault=true (= base にフォールバック) として扱う。
	collected := make([]policyEntry, 0, len(roleOverrides))
	for _, m := range roleOverrides {
		if m == nil {
			collected = append(collected, policyEntry{priority: 0, value: baseVal})
			continue
		}
		p, ok := m[key]
		if !ok {
			collected = append(collected, policyEntry{priority: 0, value: baseVal})
			continue
		}
		if p.UseDefault {
			collected = append(collected, policyEntry{priority: p.Priority, value: baseVal})
		} else {
			collected = append(collected, policyEntry{priority: p.Priority, value: p.Value})
		}
	}
	// upstream: priority 2 → 1 → 0 の順で「該当 priority に少なくとも 1 件
	// あればそのグループだけ aggregate」。fallback の priority 0 は全 role
	// を対象とする (= entry が無い role の priority=0 default も含めて集約)。
	for _, prio := range []int{2, 1} {
		group := filterByPriority(collected, prio)
		if len(group) > 0 {
			return aggregatePolicyValues(key, baseVal, group)
		}
	}
	// priority=0 fallback: 全 entry を対象に集約する (= 各 role が default
	// fallback でも、複数 role の数値 max / bool OR が反映される)。
	values := make([]any, 0, len(collected))
	for _, e := range collected {
		values = append(values, e.value)
	}
	return aggregatePolicyValues(key, baseVal, values)
}

func filterByPriority(entries []policyEntry, prio int) []any {
	out := make([]any, 0, len(entries))
	for _, e := range entries {
		if e.priority == prio {
			out = append(out, e.value)
		}
	}
	return out
}

// aggregatePolicyValues applies the per-key aggregator (bool OR / numeric
// max / chatAvailability ranked) to the supplied candidate values. The
// fallback for unrecognized shapes is the baseVal so unknown policies
// behave like "useDefault for every role".
//
// Only the types actually present in `DefaultPolicies()` are wired here:
// bool, int, and string (chatAvailability)。slice (uploadableFileTypes) は
// upstream TS でも集約 semantics が定義されていない (= 通常は base / role
// 個別値のいずれかが一意に有効) ので、本実装でも base を維持する。新規 policy
// 追加で int64 / float64 / 他の type が必要になったらここに分岐を追加する。
func aggregatePolicyValues(key string, baseVal any, values []any) any {
	if len(values) == 0 {
		return baseVal
	}
	switch baseVal.(type) {
	case bool:
		for _, v := range values {
			if b, ok := v.(bool); ok && b {
				return true
			}
		}
		return false
	case int:
		return maxNumberAsInt(values, baseVal)
	case string:
		if key == "chatAvailability" {
			return aggregateChatAvailability(values)
		}
		return baseVal
	default:
		// uploadableFileTypes など slice 型は base を維持。
		return baseVal
	}
}

// maxNumberAsInt picks the maximum int across values, ignoring entries
// that fail type assertion. base is returned when no usable entry exists.
// JSON unmarshal で role policy の数値が float64 になっているケース (= role
// admin UI 由来の override) も拾えるよう、float64 を int に丸めて比較する。
func maxNumberAsInt(values []any, base any) any {
	var best int
	found := false
	for _, v := range values {
		switch x := v.(type) {
		case int:
			if !found || x > best {
				best = x
				found = true
			}
		case float64:
			xi := int(x)
			if !found || xi > best {
				best = xi
				found = true
			}
		}
	}
	if !found {
		return base
	}
	return best
}

// aggregateChatAvailability picks the most permissive value among
// {available, readonly, unavailable}, mirroring upstream TS. When the
// supplied values contain a typed entry, that one wins over fallback to
// the base default — the function always returns one of the three literal
// strings ("available" / "readonly" / "unavailable").
func aggregateChatAvailability(values []any) any {
	hasReadonly := false
	for _, v := range values {
		if s, ok := v.(string); ok {
			switch s {
			case "available":
				return "available"
			case "readonly":
				hasReadonly = true
			}
		}
	}
	if hasReadonly {
		return "readonly"
	}
	return "unavailable"
}

// Assign assigns a role to a user with an optional expiration.
func (s *Service) Assign(userID, roleID string, expiresAt *time.Time) error {
	if _, err := s.roleRepo.FindByID(roleID); err != nil {
		return ErrRoleNotFound
	}
	exists, err := s.assignmentRepo.Exists(userID, roleID)
	if err != nil {
		return err
	}
	if exists {
		return ErrAlreadyAssigned
	}
	a := &model.RoleAssignment{
		ID:        s.idGen.Generate(time.Now()),
		UserID:    userID,
		RoleID:    roleID,
		ExpiresAt: expiresAt,
	}
	if err := s.assignmentRepo.Create(a); err != nil {
		return err
	}
	s.InvalidateUserRoleCache(userID)
	return nil
}

// Unassign removes a role from a user.
func (s *Service) Unassign(userID, roleID string) error {
	exists, err := s.assignmentRepo.Exists(userID, roleID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotAssigned
	}
	if err := s.assignmentRepo.Delete(userID, roleID); err != nil {
		return err
	}
	s.InvalidateUserRoleCache(userID)
	return nil
}

// Create creates a new role.
func (s *Service) Create(name, description string, opts CreateOptions) (*model.Role, error) {
	now := time.Now()
	role := &model.Role{
		ID:              s.idGen.Generate(now),
		UpdatedAt:       now,
		LastUsedAt:      now,
		Name:            name,
		Description:     description,
		IsModerator:     opts.IsModerator,
		IsAdministrator: opts.IsAdministrator,
		IsPublic:        opts.IsPublic,
		AsBadge:         opts.AsBadge,
		IsExplorable:    opts.IsExplorable,
		Target:          model.RoleTargetManual,
		DisplayOrder:    opts.DisplayOrder,
	}
	if err := s.roleRepo.Create(role); err != nil {
		return nil, err
	}
	return role, nil
}

// CreateOptions holds optional parameters for role creation.
type CreateOptions struct {
	IsModerator     bool
	IsAdministrator bool
	IsPublic        bool
	AsBadge         bool
	IsExplorable    bool
	DisplayOrder    int
}

// Show returns a role by ID.
func (s *Service) Show(id string) (*model.Role, error) {
	r, err := s.roleRepo.FindByID(id)
	if err != nil {
		return nil, ErrRoleNotFound
	}
	return r, nil
}

// List returns all roles.
func (s *Service) List() ([]*model.Role, error) {
	return s.roleRepo.List()
}

// ListByRole returns role assignments (with User preloaded) for the given
// role, paginated by untilID/sinceID (assignment.id keyset). Misskey TS の
// admin/roles/users 互換 envelope ({id, createdAt, user}) を組み立てるため
// User だけでなく RoleAssignment 自体を返す。
func (s *Service) ListByRole(roleID, untilID, sinceID string, limit int) ([]*model.RoleAssignment, error) {
	if _, err := s.roleRepo.FindByID(roleID); err != nil {
		return nil, ErrRoleNotFound
	}
	return s.assignmentRepo.ListByRole(roleID, untilID, sinceID, limit)
}

// UpdateFields updates the named columns of an existing role and
// returns the role state after the update for audit logging. Returns
// ErrRoleNotFound if the role does not exist before the update.
//
// Misskey TS counterpart: admin/roles/update mutates the role then
// reads it back to render before/after diffs. We mirror that flow so
// the moderation log can include both snapshots.
func (s *Service) UpdateFields(id string, fields map[string]any) (*model.Role, error) {
	if _, err := s.roleRepo.FindByID(id); err != nil {
		return nil, ErrRoleNotFound
	}
	if len(fields) == 0 {
		return s.roleRepo.FindByID(id)
	}
	if err := s.roleRepo.UpdateFields(id, fields); err != nil {
		return nil, err
	}
	return s.roleRepo.FindByID(id)
}

// Delete removes a role.
func (s *Service) Delete(id string) error {
	if _, err := s.roleRepo.FindByID(id); err != nil {
		return ErrRoleNotFound
	}
	if err := s.roleRepo.Delete(id); err != nil {
		return err
	}
	// 削除した role を assigned していた user 集合は分からないので、
	// 全 cache を flush する。admin 操作で頻度が低いので O(N) flush の
	// コストは許容範囲。
	s.InvalidateAllRoleCaches()
	return nil
}

// DefaultPolicies returns the Misskey default policies.
func DefaultPolicies() map[string]any {
	return map[string]any{
		"gtlAvailable":               true,
		"ltlAvailable":               true,
		"canPublicNote":              true,
		"mentionLimit":               20,
		"canInvite":                  false,
		"inviteLimit":                0,
		"inviteLimitCycle":           10080,
		"inviteExpirationTime":       0,
		"canManageCustomEmojis":      false,
		"canManageAvatarDecorations": false,
		"canSearchNotes":             false,
		"canSearchUsers":             true,
		"canUseTranslator":           true,
		"canHideAds":                 false,
		// upstream Misskey #17121 (= 2026.5.1 fix / triage #1012): channel 作成
		// 権限の role policy。default true (= 全員許可)、admin が role 経由で
		// 個別 user を false に絞れる。`/api/channels/create` で gate される。
		PolicyCanCreateChannel:   true,
		"driveCapacityMb":        100,
		"maxFileSizeMb":          30,
		"alwaysMarkNsfw":         false,
		"canUpdateBioMedia":      true,
		"pinLimit":               5,
		"antennaLimit":           5,
		"wordMuteLimit":          200,
		"webhookLimit":           3,
		"clipLimit":              10,
		"noteEachClipsLimit":     200,
		"userListLimit":          10,
		"userEachUserListsLimit": 50,
		"rateLimitFactor":        1,
		"avatarDecorationLimit":  1,
		"canImportAntennas":      false,
		"canImportBlocking":      false,
		"canImportFollowing":     false,
		"canImportMuting":        false,
		"canImportUserLists":     false,
		"chatAvailability":       "available",
		"uploadableFileTypes":    []string{"text/*", "application/json", "image/*", "video/*", "audio/*"},
		"noteDraftLimit":         10,
		"scheduledNoteLimit":     1,
		"watermarkAvailable":     true,
	}
}
