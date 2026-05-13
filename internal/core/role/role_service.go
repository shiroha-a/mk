package role

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"sort"
	"strings"
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

	// 以下は #1024 でフィールド単位 gate に使う policy key。endpoint 全体の
	// access 拒否ではなく、特定 field (avatarId / bannerId / note watermark
	// 等) の更新を拒否する経路で使う。Misskey TS は restrictedByRole error
	// で reject、mk-go も apierr.RestrictedByRole で同 shape を返す。
	PolicyCanPublicNote     = "canPublicNote"
	PolicyCanUpdateBioMedia = "canUpdateBioMedia"
	// PolicyAlwaysMarkNsfw — true なら user の drive upload を強制 NSFW 化
	// し、isSensitive=false への変更も拒否する。#1028 で drive service /
	// i/update gate と共に enforcement 追加。
	PolicyAlwaysMarkNsfw = "alwaysMarkNsfw"
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

	// rolesListCache は roleRepo.List() 結果の TTL キャッシュ (#1030)。
	// evaluateConditionalRoles から呼ばれて全 role を fetch する経路で、cache
	// miss 時に毎リクエスト全 role を DB から取ってしまう問題への対策。
	// 上流 Misskey TS の rolesCache (= RoleService の global) と等価。
	// userRoleCache (per-user) は同じ TTL 内で先に hit するので、本 cache が
	// fire するのは cache miss / TTL 失効 / conditional role 評価が要る user
	// に限られる。Create / UpdateFields / Delete で flush する。
	//
	// sync.RWMutex で hot path (cache hit) を RLock に倒し、cache miss /
	// invalidate の writer のみ Lock を取る。複数 goroutine が同時に cache
	// hit する場合のロック競合を排除する (#1035 review)。
	rolesListMu        sync.RWMutex
	rolesListCache     []*model.Role
	rolesListExpiresAt time.Time
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
// so the simplest safe action is to flush the whole cache). 全 user role
// cache に加え roleRepo.List() cache (#1030) も flush する。
func (s *Service) InvalidateAllRoleCaches() {
	s.userRoleCache.Range(func(k, _ any) bool {
		s.userRoleCache.Delete(k)
		return true
	})
	s.invalidateRolesListCache()
}

// listRolesCached returns the role list backed by a single-slot TTL cache
// (#1030). roleRepo.List() 直接 access の代替で、evaluateConditionalRoles の
// hot path で全 role fetch を amortize する。TTL 失効 / cache miss 時のみ
// roleRepo.List() を叩く。
//
// Create / UpdateFields / Delete で cache invalidate する設計だが、外部経路
// (admin/roles の直接 repo 更新等) は TTL でしか cover できない。
// roleCacheTTL = 5 min で staleness は bounded (= userRoleCache と同 trade-off)。
//
// 返却される slice は **shared snapshot** で、caller は read-only に扱う
// (mutate 不可、append / sort 等で書き換えない)。複数 goroutine が同 cache
// snapshot を同時に iterate するので、要素 (*model.Role) の field mutate
// もしない。mutation が要るなら slices.Clone(...) で copy を取ってから操作する。
func (s *Service) listRolesCached() ([]*model.Role, error) {
	// Fast path: RLock で cache hit を確認、cache hit なら lock を即座に
	// 解放して return。複数 goroutine の hot path 並列実行を許す。
	s.rolesListMu.RLock()
	if s.rolesListCache != nil && time.Now().Before(s.rolesListExpiresAt) {
		roles := s.rolesListCache
		s.rolesListMu.RUnlock()
		return roles, nil
	}
	s.rolesListMu.RUnlock()

	// Slow path: write lock で cache を更新。再 check で double-check pattern
	// (= 他 goroutine が RUnlock 〜 Lock 間に cache を埋めた場合、重複 fetch
	// を避ける)。これにより同時 cache miss する N goroutine でも roleRepo.List()
	// は通常 1 回しか発火しない (single-flight 相当)。
	s.rolesListMu.Lock()
	defer s.rolesListMu.Unlock()
	if s.rolesListCache != nil && time.Now().Before(s.rolesListExpiresAt) {
		return s.rolesListCache, nil
	}
	roles, err := s.roleRepo.List()
	if err != nil {
		return nil, err
	}
	s.rolesListCache = roles
	s.rolesListExpiresAt = time.Now().Add(roleCacheTTL)
	return roles, nil
}

// invalidateRolesListCache clears the rolesListCache so the next call hits
// roleRepo.List() again. Called from Create / UpdateFields / Delete and
// from InvalidateAllRoleCaches.
func (s *Service) invalidateRolesListCache() {
	s.rolesListMu.Lock()
	defer s.rolesListMu.Unlock()
	s.rolesListCache = nil
	s.rolesListExpiresAt = time.Time{}
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
	allRoles, err := s.listRolesCached()
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
//
// NaN / +-Inf / overflow を伴う float64 は silently truncate せず base に
// 倒す (= 不正値 fail-soft)。Admin UI 経由で異常値が入っても overflow した
// 整数を consumer に渡さない安全策。
func coerceToBaseType(base, override any) any {
	switch base.(type) {
	case int:
		if i, ok := override.(int); ok {
			return i
		}
		if f, ok := override.(float64); ok {
			if !isFiniteAndInRange(f, math.MinInt, math.MaxInt) {
				return base
			}
			return int(f)
		}
	case int64:
		if i, ok := override.(int64); ok {
			return i
		}
		if i, ok := override.(int); ok {
			return int64(i)
		}
		if f, ok := override.(float64); ok {
			if !isFiniteAndInRange(f, math.MinInt64, math.MaxInt64) {
				return base
			}
			return int64(f)
		}
	case float64:
		if f, ok := override.(float64); ok {
			// float64 base はそのまま受ける (DefaultPolicies に該当する base 型
			// は現状存在しないが、将来 float64 policy が追加されたとき NaN/Inf
			// が consumer に漏れないよう guard する)。
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return base
			}
			return f
		}
		if i, ok := override.(int); ok {
			return float64(i)
		}
	case []string:
		// uploadableFileTypes 等の slice 型 policy。JSON unmarshal は
		// `[]any` で返すので `[]string` に coerce する (#1034 follow-up:
		// aggregator の `case []string` 経路を bypass しないために必要)。
		// trim + 空文字 skip は normalizeStringSlice に委譲して role 内で
		// 一貫させる。
		if s, ok := override.([]string); ok {
			return s
		}
		// normalizeStringSlice は 0 件のときも non-nil な空 slice を返すので、
		// len で判定する (= 「override は slice だが全 entry が空文字 / 型
		// 不一致で 0 件残らなかった」case は base にフォールバック)。
		if normalized := normalizeStringSlice(override); len(normalized) > 0 {
			return normalized
		}
		// 型不一致 (object / bool 等) や normalize 後 0 件は base に倒す。
		return base
	}
	return override
}

// isFiniteAndInRange returns true when v is a finite float64 that can be
// truncated to an int / int64 without overflow. lo / hi are taken as float64
// so callers can use untyped consts like `math.MinInt` / `math.MaxInt`
// (their underlying type is `int`, which is platform-dependent — taking
// float64 here keeps the helper portable on 32-bit builds).
//
// 上限は `<` で厳密比較する。`float64(math.MaxInt64)` は IEEE 754 で表現
// できず `2^63` に丸まるため、`v == 2^63` で受け入れると `int64(v)` の
// 結果が implementation-defined になる。`v < hi` でその境界を弾く。
// 下限の `>= lo` は `math.MinInt*` (= -2^N) が float64 で exactly 表現
// できるので等号を許容して OK。
func isFiniteAndInRange(v, lo, hi float64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return false
	}
	return v >= lo && v < hi
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
// max / chatAvailability ranked / []string set union) to the supplied
// candidate values. The fallback for unrecognized shapes is the baseVal
// so unknown policies behave like "useDefault for every role".
//
// Only the types actually present in `DefaultPolicies()` are wired here:
// bool, int, string (chatAvailability), []string (uploadableFileTypes)。
// 新規 policy 追加で int64 / float64 / 他の type が必要になったら分岐を追加。
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
	case []string:
		// upstream RoleService.calc('uploadableFileTypes', set union) と等価。
		// 全 role の値を flatten + trim + 空文字 skip した set union を deterministic
		// に sort して返す。各 entry は []string (DefaultPolicies) または []any
		// (JSON unmarshal 経由) のどちらでも accept する (#1034)。
		return aggregateStringSetUnion(values, baseVal)
	default:
		return baseVal
	}
}

// aggregateStringSetUnion merges the supplied candidate values into a
// deterministic []string by set union. Each candidate may be []string
// (DefaultPolicies / Go literal) or []any (JSON unmarshal 由来)。trim +
// 空文字 skip で upstream Misskey TS `RoleService.calc` の per-entry trim と
// 一致させる。
//
// 全候補から取り出せた string が 1 個も無いときは baseVal をそのまま返す
// (= "全 role が空 list / 不正 値の場合 base default を維持" の fail-soft)。
func aggregateStringSetUnion(values []any, baseVal any) any {
	set := make(map[string]struct{})
	for _, v := range values {
		for _, s := range normalizeStringSlice(v) {
			set[s] = struct{}{}
		}
	}
	if len(set) == 0 {
		return baseVal
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// normalizeStringSlice coerces a JSON-decoded or Go literal slice into a
// []string, trimming whitespace and skipping empty entries. Accepts
// []string / []any / nil。型不一致は nil で返す (= caller 側で fail-soft)。
//
// 戻り値の semantics:
//   - 型不一致 (string / int / map など slice 以外) → nil
//   - slice 型で 0 件入力 / 全 entry が空文字 / 型不一致 → 非 nil の空 slice
//     ([]string{})
//
// caller は「使える entry の有無」を見るときは `len(...) > 0` で判定する
// (`!= nil` だと 0 件の空 slice も「あり」扱いになる)。
//
// drive package の normalizeMimePatterns と同等の logic だが、role 側で
// 集約に使うため独立 helper を持つ (パッケージ境界を越えるより少量の
// duplicate を許容)。
func normalizeStringSlice(raw any) []string {
	switch v := raw.(type) {
	case nil:
		return nil
	case []string:
		out := make([]string, 0, len(v))
		for _, s := range v {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}

// maxNumberAsInt picks the maximum int across values, ignoring entries
// that fail type assertion. base is returned when no usable entry exists.
// JSON unmarshal で role policy の数値が float64 になっているケース (= role
// admin UI 由来の override) も拾えるよう、float64 を int に丸めて比較する。
// NaN / +-Inf / overflow した float64 entry は coerceToBaseType と同じく
// silently skip する (= 不正値が aggregator を汚染しない fail-soft)。
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
			if !isFiniteAndInRange(x, math.MinInt, math.MaxInt) {
				continue
			}
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
	// 新規 role を追加したので role list cache を invalidate (#1030)。
	// conditional role が増える可能性があり、stale cache だと evaluation で
	// 見逃すため。
	s.invalidateRolesListCache()
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
	// CondFormula / Target / Policies が変わると evaluateConditionalRoles の
	// 結果が変わるので role list cache を invalidate (#1030)。具体的な field
	// を区別せず一律 flush するのは admin 経路で頻度が低いから (= 5min stale
	// な userRoleCache と同 trade-off)。
	s.invalidateRolesListCache()
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
