package role

import (
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"gorm.io/datatypes"
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

	// 以下は分割アップロード (#2313) の gate。mk-go 独自 policy で upstream
	// Misskey には対応するものが無い。enforcement は core/drive の chunked
	// upload service 側。セッションは「開いたまま放置」で S3 の未完了
	// マルチパートアップロードを溜められるので、可否だけでなく同時数と
	// 未完了バイト数にも上限を置く。
	PolicyCanUseChunkedUpload                = "canUseChunkedUpload"
	PolicyChunkedUploadMaxConcurrentSessions = "chunkedUploadMaxConcurrentSessions"
	PolicyChunkedUploadMaxPendingMb          = "chunkedUploadMaxPendingMb"

	// 以下は #1025 で admin 系 endpoint の gate に使う policy key。upstream
	// Misskey TS は ApiCallService.ts で requiredRolePolicy のみで gate し
	// (requireModerator/Admin flag は admin/emoji や avatar-decorations に
	// 付いていない)、admin role 持ちのみ自動 bypass する semantics。mk-go も
	// middleware.RequireRolePolicy 経由で同 挙動に揃える。
	PolicyCanManageCustomEmojis      = "canManageCustomEmojis"
	PolicyCanManageAvatarDecorations = "canManageAvatarDecorations"

	// 以下は #1026 で timeline endpoint の gate に使う policy key。匿名アクセス
	// 可な経路 (local-timeline / global-timeline) と認証必須経路 (hybrid-
	// timeline) の両方で使う。upstream Misskey TS は handler 内で
	// `getUserPolicies(me ? me.id : null)` を呼んで匿名なら base policies の
	// 値を見る pattern (空文字 userID で同等)。
	PolicyLtlAvailable = "ltlAvailable"
	PolicyGtlAvailable = "gtlAvailable"
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

type roleListFlight struct {
	epoch uint64
	done  chan struct{}
}

func cloneRole(role *model.Role) *model.Role {
	if role == nil {
		return nil
	}
	cloned := *role
	if role.Color != nil {
		color := *role.Color
		cloned.Color = &color
	}
	if role.IconURL != nil {
		iconURL := *role.IconURL
		cloned.IconURL = &iconURL
	}
	if role.CondFormula != nil {
		cloned.CondFormula = make(datatypes.JSON, len(role.CondFormula))
		copy(cloned.CondFormula, role.CondFormula)
	}
	if role.Policies != nil {
		cloned.Policies = make(datatypes.JSON, len(role.Policies))
		copy(cloned.Policies, role.Policies)
	}
	return &cloned
}

func cloneRoles(roles []*model.Role) []*model.Role {
	if roles == nil {
		return nil
	}
	cloned := make([]*model.Role, len(roles))
	for i, role := range roles {
		cloned[i] = cloneRole(role)
	}
	return cloned
}

// Service manages roles and role assignments.
type Service struct {
	roleRepo       repository.RoleRepository
	assignmentRepo repository.RoleAssignmentRepository
	metaRepo       repository.MetaRepository
	idGen          id.Generator
	// serverMaxFileSizeMb は config.maxFileSize を MB 換算した server 全体の
	// upload 上限。GetUserPolicies の maxFileSizeMb を upstream #17389 同様に
	// この値で cap する (0 なら cap しない、router で配線)。
	serverMaxFileSizeMb int
	// userRepo は drop-in 互換 (#785) のため optional に注入される。
	// Misskey TS upstream で signup された root user は user.isRoot=true で
	// 識別され meta.rootUserId は set されない。drop-in で TS DB を引き継いだ
	// mk-go は meta.rootUserId が nil → isRootUser false → admin path で 403
	// となる regression を防ぐため、user.isRoot フラグを fallback として
	// 確認する。本番経路 (mk-go pure) では meta.rootUserId が常に set される
	// ので setter で wire しなくても挙動は変わらない (= 後方互換性は保たれる)。
	userRepo repository.UserRepository

	// userRoleCache は GetUserRoles 結果の per-user TTL キャッシュ。
	// Assign / Unassign で
	// 当該 userID を、Delete (role 削除) で全 entry を invalidate する。
	// admin/roles/update が roleRepo を直接叩く経路は TTL でしかカバー
	// できないが、roleCacheTTL = 5 min で staleness は bounded (#300 3-5)。
	userRoleCache sync.Map // userID -> *roleCacheEntry
	// roleCacheMuはcacheのload/store/deleteとepochを同期する。repository呼び出し中は
	// 保持せず、invalidation後に古いmiss結果がpublishされることだけを防ぐ。
	roleCacheMu    sync.Mutex
	roleCacheEpoch uint64

	// rolesListCache は roleRepo.List() 結果の TTL キャッシュ (#1030)。
	// evaluateConditionalRoles から呼ばれて全 role を fetch する経路で、cache
	// miss 時に毎リクエスト全 role を DB から取ってしまう問題への対策。
	// 上流 Misskey TS の rolesCache (= RoleService の global) と等価。
	// userRoleCache (per-user) は同じ TTL 内で先に hit するので、本 cache が
	// fire するのは cache miss / TTL 失効 / conditional role 評価が要る user
	// に限られる。Create / UpdateFields / Delete で flush する。
	//
	rolesListCache     []*model.Role
	rolesListExpiresAt time.Time
	rolesListFlight    *roleListFlight

	// roleAssignNotifier は local public role 割当時に被割当ユーザーへ
	// 'roleAssigned' 通知を送る (#1559)。循環依存を避けるため interface で
	// 受け取る (実装は core/notification.Hook)。nil なら通知しない。
	roleAssignNotifier RoleAssignNotifier

	// policyProviderMu は policyProviders を guard する。登録は wire-time の
	// 書き込みのみで、解決の hot path は RLock で snapshot を取る。
	policyProviderMu sync.RWMutex
	// policyProviders は plugin name でソートされた不変の登録一覧。
	// snapshotPolicyProviders が防御的コピーを返す (caller は書き換え不可)。
	policyProviders []policyProvider
}

// RoleAssignNotifier records a 'roleAssigned' notification on role assignment
// (#1559)。実装は core/notification。
type RoleAssignNotifier interface {
	OnRoleAssigned(userID, roleID string)
}

// SetRoleAssignNotifier wires the notifier used by Assign to send a
// 'roleAssigned' notification for public roles (upstream RoleService.assign)。
func (s *Service) SetRoleAssignNotifier(n RoleAssignNotifier) {
	s.roleAssignNotifier = n
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
	s.roleCacheMu.Lock()
	defer s.roleCacheMu.Unlock()
	s.roleCacheEpoch++
	s.userRoleCache.Delete(userID)
}

// InvalidateAllRoleCaches drops every cached entry. Used when a role is
// deleted (we don't know which users were assigned without an extra DB hit,
// so the simplest safe action is to flush the whole cache). 全 user role
// cache に加え roleRepo.List() cache (#1030) も flush する。
func (s *Service) InvalidateAllRoleCaches() {
	s.roleCacheMu.Lock()
	defer s.roleCacheMu.Unlock()
	s.roleCacheEpoch++
	s.userRoleCache.Range(func(k, _ any) bool {
		s.userRoleCache.Delete(k)
		return true
	})
	s.rolesListCache = nil
	s.rolesListExpiresAt = time.Time{}
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
// Cache entries are immutable snapshots. Repository results are cloned before
// publication and every return receives a separate deep clone.
func (s *Service) listRolesCached() ([]*model.Role, error) {
	for {
		s.roleCacheMu.Lock()
		if s.rolesListCache != nil && time.Now().Before(s.rolesListExpiresAt) {
			roles := s.rolesListCache
			s.roleCacheMu.Unlock()
			return cloneRoles(roles), nil
		}
		epoch := s.roleCacheEpoch
		if flight := s.rolesListFlight; flight != nil && flight.epoch == epoch {
			done := flight.done
			s.roleCacheMu.Unlock()
			<-done
			continue
		}
		flight := &roleListFlight{epoch: epoch, done: make(chan struct{})}
		s.rolesListFlight = flight
		s.roleCacheMu.Unlock()

		roles, err := s.roleRepo.List()
		snapshot := cloneRoles(roles)

		s.roleCacheMu.Lock()
		if err == nil && s.roleCacheEpoch == epoch {
			s.rolesListCache = snapshot
			s.rolesListExpiresAt = time.Now().Add(roleCacheTTL)
		}
		if s.rolesListFlight == flight {
			s.rolesListFlight = nil
		}
		close(flight.done)
		s.roleCacheMu.Unlock()
		return cloneRoles(snapshot), err
	}
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
	s.roleCacheMu.Lock()
	if v, ok := s.userRoleCache.Load(userID); ok {
		if entry, ok := v.(*roleCacheEntry); ok && time.Now().Before(entry.expiresAt) {
			roles := entry.roles
			s.roleCacheMu.Unlock()
			return cloneRoles(roles), nil
		}
		s.userRoleCache.Delete(userID)
	}
	epoch := s.roleCacheEpoch
	s.roleCacheMu.Unlock()

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
	// #2106 S5: cache 失効を「TTL」と「最も早い assignment expiresAt」の min にする。
	// ListByUser は fetch 時点で有効な assignment しか返さないが、TTL 中に期限切れに
	// なる time-limited assignment はそのまま cache に残り、最大 roleCacheTTL の間
	// role/policy として効き続けてしまう (upstream は getUserAssigns で read 毎に
	// expiresAt を再 filter)。entry を soonest expiry で drop すれば、次 read が
	// 再 fetch して DB filter が期限切れ assignment を除外する。
	cacheExpiry := time.Now().Add(roleCacheTTL)
	for _, a := range assignments {
		if a.ExpiresAt != nil && a.ExpiresAt.Before(cacheExpiry) {
			cacheExpiry = *a.ExpiresAt
		}
	}
	snapshot := cloneRoles(roles)
	s.roleCacheMu.Lock()
	if s.roleCacheEpoch == epoch {
		s.userRoleCache.Store(userID, &roleCacheEntry{
			roles:     snapshot,
			expiresAt: cacheExpiry,
		})
	}
	s.roleCacheMu.Unlock()
	return cloneRoles(snapshot), nil
}

// GetUserAssigns returns the user's currently active role assignments.
// Mirrors upstream RoleService.getUserAssigns: expired assignments are
// filtered out (assignmentRepo.ListByUser already excludes rows whose
// expiresAt has passed). Used by admin/show-user to expose assignment
// metadata (createdAt / expiresAt / roleId) separately from the resolved
// role list returned by GetUserRoles.
func (s *Service) GetUserAssigns(userID string) ([]*model.RoleAssignment, error) {
	if userID == "" {
		return nil, nil
	}
	return s.assignmentRepo.ListByUser(userID)
}

// GetUserAssign returns the user's active assignment for one exact role.
func (s *Service) GetUserAssign(userID, roleID string, at time.Time) (*model.RoleAssignment, error) {
	if userID == "" || roleID == "" {
		return nil, nil
	}
	return s.assignmentRepo.FindActive(userID, roleID, at)
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

// IsExplorable reports whether the role's timeline is publicly streamable
// (isExplorable). Used by the roleTimeline WS channel gate (#1549). Missing role
// or lookup error returns false (fail-closed). Backed by the cached role list,
// so no per-event DB query.
func (s *Service) IsExplorable(roleID string) bool {
	roles, err := s.listRolesCached()
	if err != nil {
		return false
	}
	for _, r := range roles {
		if r.ID == roleID {
			return r.IsExplorable
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

// GetModerators returns the users that are moderators or administrators
// (including the root user) with non-expired role assignments. Mirrors
// upstream RoleService.getModerators(includeAdmins, includeRoot,
// excludeExpire). Used by the checkModeratorsActivity cron (#1563). Returns
// nil when userRepo is not wired (drop-in optional, #785).
func (s *Service) GetModerators() ([]*model.User, error) {
	if s.userRepo == nil {
		return nil, nil
	}
	roles, err := s.listRolesCached()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	idSet := make(map[string]struct{})
	for _, r := range roles {
		if !(r.IsModerator || r.IsAdministrator) {
			continue
		}
		// moderator/admin role は通常少数なので大きい limit で 1 query 全件取得。
		assigns, err := s.assignmentRepo.ListByRole(r.ID, "", "", 100000)
		if err != nil {
			return nil, err
		}
		for _, a := range assigns {
			if a.ExpiresAt == nil || a.ExpiresAt.After(now) {
				idSet[a.UserID] = struct{}{}
			}
		}
	}
	// includeRoot=true: meta.rootUserId を含める。
	if meta, err := s.metaRepo.Fetch(); err == nil && meta.RootUserID != nil && *meta.RootUserID != "" {
		idSet[*meta.RootUserID] = struct{}{}
	}
	if len(idSet) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	return s.userRepo.FindManyByIDs(ids)
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

// parseRolePolicies decodes a role's `policies` jsonb column, skipping entries
// that do not have the expected `{useDefault, priority, value}` shape.
//
// key ごとに decode するのが要点。map 全体を 1 回で Unmarshal すると、
// 想定外の値が 1 つ混ざっただけでその role の override が丸ごと落ちる
// (「サイレンス用ロールを割り当てたのに効かない」等の silent failure)。
// upstream は JS の object lookup なので既知 policy 名だけを見ており、
// 余計なキーは何の影響も与えない。
func parseRolePolicies(raw []byte) map[string]rolePolicyOverride {
	if len(raw) == 0 {
		return nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	out := make(map[string]rolePolicyOverride, len(entries))
	for key, val := range entries {
		var o rolePolicyOverride
		if err := json.Unmarshal(val, &o); err != nil {
			continue
		}
		out[key] = o
	}
	return out
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
// SetServerMaxFileSizeMb wires the server-wide upload size limit (MB) used to
// cap the aggregated maxFileSizeMb policy (upstream #17389)。0 で cap 無効。
func (s *Service) SetServerMaxFileSizeMb(mb int) { s.serverMaxFileSizeMb = mb }

// capServerMaxFileSize caps the maxFileSizeMb policy at serverMaxFileSizeMb,
// mirroring upstream `Math.min(serverMaxFileSizeMb, aggregated)` (#17389)。
// base-only / role 集約どちらの経路でも適用するため、全 return 直前で呼ぶ。
func (s *Service) capServerMaxFileSize(policies map[string]any) map[string]any {
	if s.serverMaxFileSizeMb <= 0 || policies == nil {
		return policies
	}
	if v, ok := policies["maxFileSizeMb"]; ok {
		if policyUnlimitedOrAboveCap(v, s.serverMaxFileSizeMb) {
			policies["maxFileSizeMb"] = s.serverMaxFileSizeMb
		}
	}
	return policies
}

// capChunkedUpload caps the chunked upload policies (#2313) at the instance
// settings held in `meta`, in the same shape as capServerMaxFileSize. Without
// it an admin could grant a role enough concurrent sessions / pending bytes to
// fill object storage with incomplete multipart uploads.
//
// meta 由来なので startup 時の field ではなく都度 Fetch する (metaRepo は
// cached 実装が配線される)。Fetch 失敗時は cap せず素通しにする — ここで
// fail-closed に倒すと transient DB error で全 user の分割アップロードが
// 止まるが、cap 漏れの影響は「admin が設定した role 値がそのまま効く」に
// とどまるため。
func (s *Service) capChunkedUpload(policies map[string]any) map[string]any {
	if policies == nil || s.metaRepo == nil {
		return policies
	}
	meta, err := s.metaRepo.Fetch()
	if err != nil || meta == nil {
		return policies
	}
	capIntPolicy(policies, PolicyChunkedUploadMaxConcurrentSessions, meta.ChunkedUploadMaxSessionsPerUser)
	capIntPolicy(policies, PolicyChunkedUploadMaxPendingMb, meta.ChunkedUploadMaxPendingMbPerUser)
	return policies
}

// capIntPolicy clamps policies[key] to limit. limit <= 0 disables the cap,
// matching capServerMaxFileSize's "0 で cap 無効" convention.
func capIntPolicy(policies map[string]any, key string, limit int) {
	if limit <= 0 {
		return
	}
	v, ok := policies[key]
	if !ok {
		return
	}
	if policyUnlimitedOrAboveCap(v, limit) {
		policies[key] = limit
	}
}

// applyServerCaps applies every instance-level cap to the aggregated policies.
// GetUserPolicies の全 return 直前で呼ぶ。
func (s *Service) applyServerCaps(policies map[string]any) map[string]any {
	return s.capChunkedUpload(s.capServerMaxFileSize(policies))
}

// policyUnlimitedOrAboveCap reports whether a numeric policy would disable a
// consumer's gate (<= 0) or exceed an authoritative positive instance cap.
// float64 is compared directly so a valid fractional maxFileSizeMb is not
// truncated to zero and accidentally replaced by a much larger cap.
func policyUnlimitedOrAboveCap(v any, limit int) bool {
	switch x := v.(type) {
	case int:
		return x <= 0 || x > limit
	case int64:
		return x <= 0 || x > int64(limit)
	case float64:
		return x <= 0 || x > float64(limit)
	}
	return false
}

func (s *Service) GetUserPolicies(userID string) map[string]any {
	// 解決は resolvePolicies に委譲する (provider 失敗時は errorless で
	// native-onlyの安全なmapを返す)。checked版はGetUserPoliciesCheckedがerrorを返す。
	out, _ := s.resolvePolicies(userID)
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
// or when no role has an override. extra carries effective-policy provider
// contributions for the key (already validated & type-checked by the host);
// it is merged into the same priority cascade as the role overrides.
func computePolicy(key string, baseVal any, roleOverrides []map[string]rolePolicyOverride, extra []policyEntry) any {
	// 各 role について本 policy の override を組み立てる。entry 無し =
	// priority=0, useDefault=true (= base にフォールバック) として扱う。
	collected := make([]policyEntry, 0, len(roleOverrides)+len(extra))
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
	// provider contribution は同じ priority cascade に参加させる
	// (= native と provider は同一 priority グループ内で aggregate される)。
	collected = append(collected, extra...)
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
		return maxNumber(values, baseVal)
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

// maxNumber picks the maximum host-int-compatible number without converting
// typed integers through float64. Fractional float64 values remain supported
// for native role policies such as sub-megabyte maxFileSizeMb values.
func maxNumber(values []any, base any) any {
	var best intBaseNumeric
	found := false
	for _, v := range values {
		candidate, ok := intBaseNumber(v)
		if !ok {
			continue
		}
		if !found || candidate.greaterThan(best) {
			best = candidate
			found = true
		}
	}
	if !found {
		return base
	}
	return best.normalized
}

type intBaseNumeric struct {
	integer    bool
	intValue   int64
	floatValue float64
	normalized any
}

func intBaseNumber(value any) (intBaseNumeric, bool) {
	switch v := value.(type) {
	case int:
		return intBaseNumeric{integer: true, intValue: int64(v), normalized: v}, true
	case int64:
		converted := int(v)
		if int64(converted) != v {
			return intBaseNumeric{}, false
		}
		return intBaseNumeric{integer: true, intValue: v, normalized: converted}, true
	case float64:
		converted, ok := safelyConvertFloatToHostInt(v)
		if !ok {
			return intBaseNumeric{}, false
		}
		if v == math.Trunc(v) {
			return intBaseNumeric{integer: true, intValue: int64(converted), normalized: converted}, true
		}
		return intBaseNumeric{floatValue: v, normalized: v}, true
	default:
		return intBaseNumeric{}, false
	}
}

func (n intBaseNumeric) greaterThan(other intBaseNumeric) bool {
	if n.integer && other.integer {
		return n.intValue > other.intValue
	}
	if !n.integer && !other.integer {
		return n.floatValue > other.floatValue
	}
	if n.integer {
		return compareIntToFraction(n.intValue, other.floatValue) > 0
	}
	return compareIntToFraction(other.intValue, n.floatValue) < 0
}

// compareIntToFraction compares exact i with finite, in-range, non-integral f.
// Converting f's truncated integer part is safe and avoids converting i to a
// potentially lossy float64.
func compareIntToFraction(i int64, f float64) int {
	truncated := int64(f)
	if f > 0 {
		if i <= truncated {
			return -1
		}
		return 1
	}
	if i < truncated {
		return -1
	}
	return 1
}

// safelyConvertFloatToHostInt checks the host-int half-open range before the
// conversion. The positive bound is exclusive because float64(MaxInt64)
// rounds to 2^63, while the exact MaxInt32 remains safely below 2^31.
func safelyConvertFloatToHostInt(v float64) (int, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	min := float64(math.MinInt)
	maxExclusive := -min
	if v < min || v >= maxExclusive {
		return 0, false
	}
	return int(v), true
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
	role, err := s.roleRepo.FindByID(roleID)
	if err != nil {
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
	// upstream RoleService.assign は role.lastUsedAt を更新する (#2061)。eval は
	// lastUsedAt を使わないので rolesListCache の invalidate は不要 (admin/roles/list は
	// ListByLastUsed で fresh に引く)。失敗しても割当自体は成功扱いで続行する。
	if err := s.roleRepo.UpdateFields(roleID, map[string]any{"lastUsedAt": time.Now()}); err != nil {
		slog.Warn("role assign: lastUsedAt update failed", "role", roleID, "err", err)
	}
	s.InvalidateUserRoleCache(userID)
	// public role の割当のみ通知する (upstream RoleService.assign の
	// `if (role.isPublic && user.host === null)`)。local 判定は notifier 側の
	// notifyLocalUser が host==nil で担保するので、ここでは isPublic だけ見る。
	if role != nil && role.IsPublic && s.roleAssignNotifier != nil {
		s.roleAssignNotifier.OnRoleAssigned(userID, roleID)
	}
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
	// upstream RoleService.unassign も role.lastUsedAt を更新する (#2061)。
	if err := s.roleRepo.UpdateFields(roleID, map[string]any{"lastUsedAt": time.Now()}); err != nil {
		slog.Warn("role unassign: lastUsedAt update failed", "role", roleID, "err", err)
	}
	s.InvalidateUserRoleCache(userID)
	return nil
}

// Create creates a new role.
func (s *Service) Create(name, description string, opts CreateOptions) (*model.Role, error) {
	now := time.Now()
	target := model.RoleTargetManual
	if opts.Target == model.RoleTargetConditional {
		target = model.RoleTargetConditional
	}
	role := &model.Role{
		ID:                              s.idGen.Generate(now),
		UpdatedAt:                       now,
		LastUsedAt:                      now,
		Name:                            name,
		Description:                     description,
		Color:                           opts.Color,
		IconURL:                         opts.IconURL,
		IsModerator:                     opts.IsModerator,
		IsAdministrator:                 opts.IsAdministrator,
		IsPublic:                        opts.IsPublic,
		AsBadge:                         opts.AsBadge,
		IsExplorable:                    opts.IsExplorable,
		Target:                          target,
		DisplayOrder:                    opts.DisplayOrder,
		CanEditMembersByModerator:       opts.CanEditMembersByModerator,
		PreserveAssignmentOnMoveAccount: opts.PreserveAssignmentOnMoveAccount,
	}
	if len(opts.CondFormula) > 0 {
		role.CondFormula = opts.CondFormula
	}
	if len(opts.Policies) > 0 {
		role.Policies = opts.Policies
	}
	if err := s.roleRepo.Create(role); err != nil {
		return nil, err
	}
	// 新規 role 追加時は条件分岐せず常に全 cache を flush する。理由:
	//  (a) conditional role の場合は condFormula 評価で全 user の userRole
	//      Cache に影響が出る (= 既存 user が新規 role に hit するかも)。
	//  (b) opts から「conditional or not」を判別して flush 範囲を絞ることも
	//      可能だが、admin role 作成は超低頻度 (~週 1) なので最適化価値が低い。
	s.InvalidateAllRoleCaches()
	return role, nil
}

// CreateOptions holds optional parameters for role creation.
//
// Color / IconURL は upstream paramDef で nullable; nil ポインタはそのまま
// 「設定なし」を意味する。CondFormula / Policies は upstream JSON object で、
// nil / 空なら model のデフォルト ("{}") が使われる。
type CreateOptions struct {
	Color                           *string
	IconURL                         *string
	Target                          model.RoleTarget
	CondFormula                     datatypes.JSON
	IsModerator                     bool
	IsAdministrator                 bool
	IsPublic                        bool
	AsBadge                         bool
	IsExplorable                    bool
	DisplayOrder                    int
	Policies                        datatypes.JSON
	CanEditMembersByModerator       bool
	PreserveAssignmentOnMoveAccount bool
}

// Show returns a role by ID.
func (s *Service) Show(id string) (*model.Role, error) {
	r, err := s.roleRepo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRoleNotFound
		}
		return nil, err
	}
	return r, nil
}

// List returns all roles.
func (s *Service) List() ([]*model.Role, error) {
	return s.roleRepo.List()
}

// ListByLastUsed returns roles ordered by lastUsedAt DESC for admin/roles/list
// (upstream `order: { lastUsedAt: 'DESC' }`、#2061)。eval 用の cache を経由せず
// 毎回 fresh で引くので、Assign/Unassign の lastUsedAt 更新が即反映される。
func (s *Service) ListByLastUsed() ([]*model.Role, error) {
	return s.roleRepo.ListByLastUsed()
}

// ExistingRoleIDSet returns the set of all current role ids. get-avatar-
// decorations / get-emoji 系が削除済ロール ID を除去するために使う (#1543)。
func (s *Service) ExistingRoleIDSet() (map[string]bool, error) {
	roles, err := s.roleRepo.List()
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(roles))
	for _, r := range roles {
		set[r.ID] = true
	}
	return set, nil
}

// FilterExistingRoleIDs returns the subset of ids that exist in `existing`,
// preserving input order. 戻り値は常に非 nil (空でも []) なので JSON では null
// ではなく [] になる。upstream get-avatar-decorations.ts は roleIds を現存ロール
// で filter して削除済ロール ID を除去する (#1543)。
func FilterExistingRoleIDs(ids []string, existing map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if existing[id] {
			out = append(out, id)
		}
	}
	return out
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

// CountAssignedUsers returns the number of users currently assigned to a role
// (non-expired assignments). Used by the Role packer's usersCount field. On
// repository error it returns 0 so packing never fails the whole response.
func (s *Service) CountAssignedUsers(roleID string) int {
	n, err := s.assignmentRepo.CountActiveByRole(roleID)
	if err != nil {
		// fail-soft: pack 全体を失敗させない (upstream は 500 だが mk-go は
		// best-effort で 0 を返す)。ただし silent に 0 を返すと corrupt/transient
		// DB error が usersCount:0 として隠れるため warn で観測可能にする。
		slog.Warn("role: CountActiveByRole failed; usersCount falls back to 0", "roleId", roleID, "err", err)
		return 0
	}
	return n
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
	// role の field 変更は (a) conditional role の評価結果に直結
	// (target / condFormula) または (b) 既に role assigned の user の policy
	// 結果に直結 (policies / isModerator / isAdministrator) する。前者は
	// rolesListCache のみで十分だが後者は per-user userRoleCache も flush
	// しないと最大 5min stale な policy が返り続け、admin の意図と乖離して
	// 「policy 反映されない」となる (PR #1102 で塞ぐ user 報告経路)。
	// admin 経由 update は頻度が低いので、field 差分判定せず常に全 cache を
	// flush する保守的選択を取る。
	s.InvalidateAllRoleCaches()
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

// defaultPoliciesCache holds the immutable default policy map, built once at
// package init. DefaultPolicies hands this shared instance to read-only
// callers so high-traffic endpoints (meta calls it 3x/req, users/show, i)
// avoid a per-request 39-entry map allocation — previously the single largest
// allocation source (#1377, ~19% of all allocs in the HTTP bench), driving GC
// pressure / tail latency.
var defaultPoliciesCache = buildDefaultPolicies()

var mutableDefaultPolicyKeys = func() []string {
	keys := make([]string, 0, 1)
	for key, value := range defaultPoliciesCache {
		if _, mutable := cloneMutablePolicyValue(value); mutable {
			keys = append(keys, key)
		}
	}
	return keys
}()

// DefaultPolicies returns the shared, immutable Misskey default policies map.
//
// The returned map is shared across all callers and MUST NOT be mutated. Use
// DefaultPoliciesClone when you need to overlay meta / role overrides.
func DefaultPolicies() map[string]any {
	return defaultPoliciesCache
}

// DefaultPoliciesClone returns a fresh mutable copy of the default policies for
// callers that overlay or return values (e.g. GetUserPolicies merging meta /
// role overrides). Mutable policy values are cloned as well so callers cannot
// mutate the shared defaults through a returned slice.
func DefaultPoliciesClone() map[string]any {
	clone := maps.Clone(defaultPoliciesCache)
	for _, key := range mutableDefaultPolicyKeys {
		clone[key] = clonePolicyValue(defaultPoliciesCache[key])
	}
	return clone
}

func clonePolicyValue(value any) any {
	clone, _ := cloneMutablePolicyValue(value)
	return clone
}

func cloneMutablePolicyValue(value any) (any, bool) {
	switch value := value.(type) {
	case []string:
		return append([]string(nil), value...), true
	case []any:
		return append([]any(nil), value...), true
	default:
		return value, false
	}
}

// MergeMetaPolicies returns the default policies overlaid with the instance's
// `meta.policies` JSON override — the upstream `{ ...DEFAULT_POLICIES,
// ...instance.policies }` shape used by both MetaEntityService.pack and
// admin/meta. Numeric overrides are coerced to the default's Go type so
// downstream consumers can type-assert safely (JSON decodes numbers as
// float64). Empty / invalid JSON yields the defaults unchanged (fail-soft).
func MergeMetaPolicies(rawPolicies []byte) map[string]any {
	base := DefaultPoliciesClone()
	if len(rawPolicies) == 0 {
		return base
	}
	var override map[string]any
	if err := json.Unmarshal(rawPolicies, &override); err != nil {
		return base
	}
	for k, v := range override {
		base[k] = coerceToBaseType(base[k], v)
	}
	return base
}

// buildDefaultPolicies constructs the default policy map. Called once to seed
// defaultPoliciesCache; do not call per-request (use DefaultPolicies /
// DefaultPoliciesClone).
func buildDefaultPolicies() map[string]any {
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
		// 分割アップロード (#2313)。mk-go 独自。既定で許可するが、インスタンス
		// 側の `meta.chunkedUploadEnabled` が false なら機能ごと無効なので、
		// この既定 true だけでは何も有効にならない。
		PolicyCanUseChunkedUpload:                true,
		PolicyChunkedUploadMaxConcurrentSessions: 4,
		PolicyChunkedUploadMaxPendingMb:          1024,
	}
}

// FindRole returns the role by id. アカウント移行のロール引き継ぎ (#2419) が
// `preserveAssignmentOnMoveAccount` を見るために使う。upstream は getRoles() で
// 全件取ってから find するが、移行 1 回あたりの assignment 数は少ないので
// id 引きで足りる。
func (s *Service) FindRole(roleID string) (*model.Role, error) {
	if roleID == "" {
		return nil, ErrRoleNotFound
	}
	return s.roleRepo.FindByID(roleID)
}

// IsAlreadyAssigned reports whether err means the user already has the role.
// 呼び出し側 (core/move) が role パッケージの sentinel error に直接依存せずに
// 済むよう、判定をここで公開する。
func (s *Service) IsAlreadyAssigned(err error) bool {
	return errors.Is(err, ErrAlreadyAssigned)
}
