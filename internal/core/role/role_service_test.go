package role_test

import (
	"sync"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func newTestService(t *testing.T) (*role.Service, *testutil.MockRoleRepository, *testutil.MockRoleAssignmentRepository, *testutil.MockMetaRepository) {
	t.Helper()
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := testutil.NewMockRoleAssignmentRepository(roleRepo)
	metaRepo := testutil.NewMockMetaRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	return svc, roleRepo, assignRepo, metaRepo
}

// FilterExistingRoleIDs は existing に含まれる id だけを順序保持で返し、
// 戻り値は常に非 nil (#1543)。
func TestFilterExistingRoleIDs(t *testing.T) {
	existing := map[string]bool{"r1": true, "r3": true}
	got := role.FilterExistingRoleIDs([]string{"r1", "r2", "r3"}, existing)
	assert.Equal(t, []string{"r1", "r3"}, got, "r2 (非存在) は除去")

	// 全て非存在 → 空でも非 nil ([])。
	got = role.FilterExistingRoleIDs([]string{"gone"}, existing)
	assert.NotNil(t, got)
	assert.Empty(t, got)

	// nil 入力でも非 nil の空 slice。
	got = role.FilterExistingRoleIDs(nil, existing)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

// ExistingRoleIDSet は全ロールの id 集合を返す (#1543)。
func TestExistingRoleIDSet(t *testing.T) {
	svc, roleRepo, _, _ := newTestService(t)
	roleRepo.Roles["a"] = &model.Role{ID: "a", Name: "A"}
	roleRepo.Roles["b"] = &model.Role{ID: "b", Name: "B"}

	set, err := svc.ExistingRoleIDSet()
	require.NoError(t, err)
	assert.True(t, set["a"])
	assert.True(t, set["b"])
	assert.False(t, set["ghost"])
}

func TestGetUserRoles_NoRoles(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	roles, err := svc.GetUserRoles("user1")
	require.NoError(t, err)
	assert.Empty(t, roles)
}

func TestService_IsExplorable(t *testing.T) {
	svc, roleRepo, _, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Public", IsExplorable: true}
	roleRepo.Roles["r2"] = &model.Role{ID: "r2", Name: "Hidden", IsExplorable: false}
	assert.True(t, svc.IsExplorable("r1"))
	assert.False(t, svc.IsExplorable("r2"))
	assert.False(t, svc.IsExplorable("missing"), "unknown role must be fail-closed")
}

func TestGetUserRoles_WithRoles(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	adminRole := &model.Role{ID: "r1", Name: "Admin", IsAdministrator: true}
	roleRepo.Roles["r1"] = adminRole
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}

	roles, err := svc.GetUserRoles("user1")
	require.NoError(t, err)
	assert.Len(t, roles, 1)
	assert.Equal(t, "Admin", roles[0].Name)
}

func TestGetUserRoles_ExpiredRoleExcluded(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Temp"}
	expired := time.Now().Add(-1 * time.Hour)
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1", ExpiresAt: &expired,
	}

	roles, err := svc.GetUserRoles("user1")
	require.NoError(t, err)
	assert.Empty(t, roles)
}

func TestGetUserAssigns_EmptyUserID(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	assigns, err := svc.GetUserAssigns("")
	require.NoError(t, err)
	assert.Empty(t, assigns)
}

func TestGetUserAssigns_ActiveOnly(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Active"}
	roleRepo.Roles["r2"] = &model.Role{ID: "r2", Name: "Expired"}
	future := time.Now().Add(1 * time.Hour)
	expired := time.Now().Add(-1 * time.Hour)
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1", ExpiresAt: &future,
	}
	assignRepo.Assignments["user1:r2"] = &model.RoleAssignment{
		ID: "a2", UserID: "user1", RoleID: "r2", ExpiresAt: &expired,
	}

	// upstream getUserAssigns と同じく期限切れ assignment は除外される。
	assigns, err := svc.GetUserAssigns("user1")
	require.NoError(t, err)
	require.Len(t, assigns, 1)
	assert.Equal(t, "r1", assigns[0].RoleID)
}

func TestIsAdministrator_RootUser(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	rootID := "root1"
	metaRepo.Meta = &model.Meta{ID: "x", RootUserID: &rootID}
	assert.True(t, svc.IsAdministrator("root1"))
}

// drop-in 互換 (#785): TS DB を引き継いだ場合は meta.rootUserId が nil の
// まま user.isRoot=true で root が記録されるので、SetUserRepo 経由で
// fallback できる必要がある。
func TestIsAdministrator_DropInRoot_UserIsRootFallback(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	metaRepo.Meta = &model.Meta{ID: "x"} // RootUserID は nil
	userRepo := testutil.NewMockUserRepository()
	require.NoError(t, userRepo.Create(&model.User{ID: "alice", Username: "alice", IsRoot: true}))
	svc.SetUserRepo(userRepo)

	assert.True(t, svc.IsAdministrator("alice"))
}

// user.isRoot=false のユーザーを fallback で誤って root 判定しないこと。
func TestIsAdministrator_DropIn_NonRootUser_NotPromoted(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	metaRepo.Meta = &model.Meta{ID: "x"}
	userRepo := testutil.NewMockUserRepository()
	require.NoError(t, userRepo.Create(&model.User{ID: "bob", Username: "bob", IsRoot: false}))
	svc.SetUserRepo(userRepo)

	assert.False(t, svc.IsAdministrator("bob"))
}

// SetUserRepo 未配線時は従来どおり meta.rootUserId のみで判定する
// (後方互換性 regression guard)。
func TestIsAdministrator_NoUserRepoWired_BehavesAsBefore(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	rootID := "root1"
	metaRepo.Meta = &model.Meta{ID: "x", RootUserID: &rootID}
	// SetUserRepo は呼ばない

	assert.True(t, svc.IsAdministrator("root1"))
	assert.False(t, svc.IsAdministrator("other"))
}

// userRepo.FindByID が transient error (RecordNotFound 以外) を返した場合は
// silent に false 扱い (admin 昇格に倒さない方が安全)。slog.Warn は出すが
// 現状 logger を inject しないので副作用のみ確認。
func TestIsAdministrator_UserRepoTransientError_FallsThrough(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	metaRepo.Meta = &model.Meta{ID: "x"}
	svc.SetUserRepo(&errorUserRepo{
		MockUserRepository: testutil.NewMockUserRepository(),
		err:                assert.AnError,
	})

	assert.False(t, svc.IsAdministrator("alice"))
}

func TestIsAdministrator_AdminRole(t *testing.T) {
	svc, roleRepo, assignRepo, metaRepo := newTestService(t)
	metaRepo.Meta = &model.Meta{ID: "x"}
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", IsAdministrator: true}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}
	assert.True(t, svc.IsAdministrator("user1"))
}

func TestIsAdministrator_NoRole(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	metaRepo.Meta = &model.Meta{ID: "x"}
	assert.False(t, svc.IsAdministrator("user1"))
}

func TestIsModerator_ModeratorRole(t *testing.T) {
	svc, roleRepo, assignRepo, metaRepo := newTestService(t)
	metaRepo.Meta = &model.Meta{ID: "x"}
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", IsModerator: true}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}
	assert.True(t, svc.IsModerator("user1"))
}

func TestIsModerator_AdminRoleAlsoCounts(t *testing.T) {
	svc, roleRepo, assignRepo, metaRepo := newTestService(t)
	metaRepo.Meta = &model.Meta{ID: "x"}
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", IsAdministrator: true}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}
	assert.True(t, svc.IsModerator("user1"))
}

func TestIsModerator_NoRole(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	metaRepo.Meta = &model.Meta{ID: "x"}
	assert.False(t, svc.IsModerator("user1"))
}

func TestIsAdministrator_MetaFetchError(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	// metaRepo.Meta is nil → Fetch returns error
	assert.False(t, svc.IsAdministrator("user1"))
}

func TestGetUserPolicies_Default(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	policies := svc.GetUserPolicies("user1")
	assert.Equal(t, true, policies["gtlAvailable"])
	assert.Equal(t, 100, policies["driveCapacityMb"])
}

func TestGetUserPolicies_WithRoleOverride(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	// ロールが driveCapacityMb を 500 にオーバーライド
	roleRepo.Roles["r1"] = &model.Role{
		ID:   "r1",
		Name: "Pro",
		Policies: datatypes.JSON([]byte(`{
			"driveCapacityMb": {"useDefault": false, "priority": 0, "value": 500}
		}`)),
	}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}

	policies := svc.GetUserPolicies("user1")
	// #1020 で aggregator が base 型 (int) を維持するように。旧 impl の素朴な
	// `base[key] = p.Value` は JSON unmarshal の float64 をそのまま代入していた
	// ため float64(500) を返していたが、それは consumer 側 type assertion が
	// 壊れる原因にもなっていた。新 impl は upstream TS の Math.max 同等を
	// base 型で実装する (= 数値は int / int64 / float64 各々を維持)。
	assert.Equal(t, 500, policies["driveCapacityMb"])
}

func TestGetUserPolicies_UseDefaultTrue_NotOverridden(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{
		ID:       "r1",
		Policies: datatypes.JSON([]byte(`{"driveCapacityMb": {"useDefault": true, "priority": 0, "value": 999}}`)),
	}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}

	policies := svc.GetUserPolicies("user1")
	assert.Equal(t, 100, policies["driveCapacityMb"]) // デフォルト値が維持される
}

func TestGetUserPolicies_InvalidJSON(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{
		ID:       "r1",
		Policies: datatypes.JSON([]byte(`invalid`)),
	}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}

	policies := svc.GetUserPolicies("user1")
	// 不正なJSONの場合はデフォルト値がそのまま返る
	assert.Equal(t, 100, policies["driveCapacityMb"])
}

// --- #1020: 3 種類の role (manual / conditional) + base policies (meta.policies)
// + priority aggregation を覆う追加テスト群 ---

// Conditional role が isBot=true の user に自動付与され、policy が反映される。
func TestGetUserRoles_ConditionalRole_Matched(t *testing.T) {
	svc, roleRepo, _, _ := newTestService(t)
	userRepo := testutil.NewMockUserRepository()
	require.NoError(t, userRepo.Create(&model.User{ID: "bot1", Username: "bot", IsBot: true}))
	svc.SetUserRepo(userRepo)

	// target=conditional の role で「bot 用 canSearchNotes=true」
	roleRepo.Roles["r-bot"] = &model.Role{
		ID:          "r-bot",
		Name:        "BotRole",
		Target:      model.RoleTargetConditional,
		CondFormula: datatypes.JSON([]byte(`{"type":"isBot"}`)),
		Policies:    datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":true}}`)),
	}

	roles, err := svc.GetUserRoles("bot1")
	require.NoError(t, err)
	require.Len(t, roles, 1, "bot user は conditional role でマッチして付与される")
	assert.Equal(t, "BotRole", roles[0].Name)

	// HasRolePolicy も反映される (本 PR の middleware gate と一直線)。
	assert.True(t, svc.HasRolePolicy("bot1", role.PolicyCanSearchNotes))
}

// Conditional role の formula が false に評価される user には付与されない。
func TestGetUserRoles_ConditionalRole_NotMatched(t *testing.T) {
	svc, roleRepo, _, _ := newTestService(t)
	userRepo := testutil.NewMockUserRepository()
	require.NoError(t, userRepo.Create(&model.User{ID: "u1", Username: "alice", IsBot: false}))
	svc.SetUserRepo(userRepo)

	roleRepo.Roles["r-bot"] = &model.Role{
		ID:          "r-bot",
		Target:      model.RoleTargetConditional,
		CondFormula: datatypes.JSON([]byte(`{"type":"isBot"}`)),
		Policies:    datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":true}}`)),
	}

	roles, err := svc.GetUserRoles("u1")
	require.NoError(t, err)
	assert.Empty(t, roles, "isBot=false の user は formula 不一致で 0 件")
	// canSearchNotes は default false のまま
	assert.False(t, svc.HasRolePolicy("u1", role.PolicyCanSearchNotes))
}

// userRepo 未配線 → conditional 評価 skip (= 旧挙動と同等の fail-safe)。
func TestGetUserRoles_ConditionalSkippedWhenUserRepoNotWired(t *testing.T) {
	svc, roleRepo, _, _ := newTestService(t)
	roleRepo.Roles["r-bot"] = &model.Role{
		ID:          "r-bot",
		Target:      model.RoleTargetConditional,
		CondFormula: datatypes.JSON([]byte(`{"type":"isBot"}`)),
	}
	roles, err := svc.GetUserRoles("u1")
	require.NoError(t, err)
	assert.Empty(t, roles, "userRepo 未配線では conditional 評価しない")
}

// meta.policies の base override が適用される (= 全 user に効く"ベースロール"
// 相当)。Role assignment 無しでも反映される。
func TestGetUserPolicies_MetaBasePolicyOverridesDefault(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	// meta.policies で canSearchNotes を全員 true に上書き
	metaRepo.Meta = &model.Meta{
		ID:       "x",
		Policies: datatypes.JSON([]byte(`{"canSearchNotes":true,"driveCapacityMb":200}`)),
	}
	policies := svc.GetUserPolicies("u1")
	assert.Equal(t, true, policies["canSearchNotes"], "meta.policies の base override が反映")
	// #1020 review: meta.policies 経由の数値も base の型 (int) に丸められて
	// 入る。これにより consumer 側の type assert (`policies[key].(int)`) が
	// role override / meta override どちらの経路でも安全に動く。
	assert.Equal(t, 200, policies["driveCapacityMb"])
	// 未上書きの key は default が維持される
	assert.Equal(t, 20, policies["mentionLimit"])
}

// 複数 role の bool policy は OR で集約される (canSearchNotes が 1 role でも
// true なら true)。
func TestGetUserPolicies_BoolPolicyORAcrossRoles(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1",
		Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":false}}`))}
	roleRepo.Roles["r2"] = &model.Role{ID: "r2",
		Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":true}}`))}
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}
	assignRepo.Assignments["u1:r2"] = &model.RoleAssignment{ID: "a2", UserID: "u1", RoleID: "r2"}

	policies := svc.GetUserPolicies("u1")
	assert.Equal(t, true, policies["canSearchNotes"], "r1=false でも r2=true なら OR で true")
}

// 数値 policy は max で集約される。
func TestGetUserPolicies_NumericPolicyMaxAcrossRoles(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1",
		Policies: datatypes.JSON([]byte(`{"driveCapacityMb":{"useDefault":false,"priority":0,"value":200}}`))}
	roleRepo.Roles["r2"] = &model.Role{ID: "r2",
		Policies: datatypes.JSON([]byte(`{"driveCapacityMb":{"useDefault":false,"priority":0,"value":500}}`))}
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}
	assignRepo.Assignments["u1:r2"] = &model.RoleAssignment{ID: "a2", UserID: "u1", RoleID: "r2"}

	policies := svc.GetUserPolicies("u1")
	assert.Equal(t, 500, policies["driveCapacityMb"], "r1=200, r2=500 → max=500")
}

// priority 2 は priority 1 を勝つ。priority 2 でだけ override されている
// 場合、priority 1 / 0 の値は無視される。
func TestGetUserPolicies_PriorityCascade(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r-high"] = &model.Role{ID: "r-high",
		Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":2,"value":false}}`))}
	roleRepo.Roles["r-low"] = &model.Role{ID: "r-low",
		Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":1,"value":true}}`))}
	assignRepo.Assignments["u1:r-high"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r-high"}
	assignRepo.Assignments["u1:r-low"] = &model.RoleAssignment{ID: "a2", UserID: "u1", RoleID: "r-low"}

	policies := svc.GetUserPolicies("u1")
	// priority=2 の false が勝つ (priority=1 の true は集約対象外)。
	assert.Equal(t, false, policies["canSearchNotes"])
}

// chatAvailability は available > readonly > unavailable で集約される。
func TestGetUserPolicies_ChatAvailabilityRanking(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1",
		Policies: datatypes.JSON([]byte(`{"chatAvailability":{"useDefault":false,"priority":0,"value":"readonly"}}`))}
	roleRepo.Roles["r2"] = &model.Role{ID: "r2",
		Policies: datatypes.JSON([]byte(`{"chatAvailability":{"useDefault":false,"priority":0,"value":"unavailable"}}`))}
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}
	assignRepo.Assignments["u1:r2"] = &model.RoleAssignment{ID: "a2", UserID: "u1", RoleID: "r2"}

	policies := svc.GetUserPolicies("u1")
	// readonly が available より弱いので、readonly + unavailable → readonly
	assert.Equal(t, "readonly", policies["chatAvailability"])
}

// 空 userID は base policies (default + meta.policies merge 後) のみを返す。
func TestGetUserPolicies_EmptyUserIDReturnsBaseOnly(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	metaRepo.Meta = &model.Meta{ID: "x",
		Policies: datatypes.JSON([]byte(`{"canSearchNotes":true}`))}

	policies := svc.GetUserPolicies("")
	assert.Equal(t, true, policies["canSearchNotes"], "userID=\"\" でも meta.policies は反映")
	assert.Equal(t, 20, policies["mentionLimit"], "default はそのまま")
}

// meta.policies が空 / 不正 JSON / fetch エラーのときは default のままで silently
// 続行する (fail-soft、upstream TS 互換)。
func TestGetUserPolicies_MetaPoliciesFallbacks(t *testing.T) {
	t.Run("invalid JSON falls back to default", func(t *testing.T) {
		svc, _, _, metaRepo := newTestService(t)
		metaRepo.Meta = &model.Meta{ID: "x", Policies: datatypes.JSON([]byte(`not-json`))}
		policies := svc.GetUserPolicies("")
		assert.Equal(t, false, policies["canSearchNotes"], "不正 JSON は無視されて default")
	})
	t.Run("empty policies leaves default", func(t *testing.T) {
		svc, _, _, metaRepo := newTestService(t)
		metaRepo.Meta = &model.Meta{ID: "x", Policies: datatypes.JSON([]byte(``))}
		policies := svc.GetUserPolicies("")
		assert.Equal(t, false, policies["canSearchNotes"])
	})
}

// #1034: uploadableFileTypes は set union で集約される。role の override は
// JSON unmarshal で []any 型として渡るが、aggregator がそれを取り出して
// set union 化する。
func TestGetUserPolicies_UploadableFileTypesSingleRoleOverride(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1",
		Policies: datatypes.JSON([]byte(`{"uploadableFileTypes":{"useDefault":false,"priority":0,"value":["image/jpeg"]}}`))}
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}

	policies := svc.GetUserPolicies("u1")
	got, ok := policies["uploadableFileTypes"].([]string)
	require.True(t, ok, "set union 結果は []string 型で返る")
	assert.Equal(t, []string{"image/jpeg"}, got)
}

// #1036 review: meta.policies で uploadableFileTypes を override しても、
// JSON unmarshal 由来の []any が coerceToBaseType で []string に丸まり、
// 後段の aggregator が case []string にマッチして set union 集約が動く。
// 旧 (#1034 だけだった) 実装では base 型が []any のまま残り、aggregator
// の switch が default fall through して base 維持 (= 集約 bypass) する
// regression があった。
func TestGetUserPolicies_UploadableFileTypesMetaOverrideAggregated(t *testing.T) {
	svc, roleRepo, assignRepo, metaRepo := newTestService(t)
	// meta.policies で base allowlist を ["text/*"] に絞る
	metaRepo.Meta = &model.Meta{ID: "x",
		Policies: datatypes.JSON([]byte(`{"uploadableFileTypes":["text/*"]}`))}
	// role override で image/* を追加
	roleRepo.Roles["r1"] = &model.Role{ID: "r1",
		Policies: datatypes.JSON([]byte(`{"uploadableFileTypes":{"useDefault":false,"priority":0,"value":["image/*"]}}`))}
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}

	policies := svc.GetUserPolicies("u1")
	got, ok := policies["uploadableFileTypes"].([]string)
	require.True(t, ok, "meta override 経由でも []string 型で返る")
	// 本 test の seal point は **meta override が []any のまま aggregator を
	// bypass しない** こと。挙動: base override (text/*) は set union 集約に
	// 参加せず、role override (= useDefault:false の値) のみが flatten される
	// (upstream Misskey TS の calc も同 logic)。結果は role の image/* のみ。
	assert.Equal(t, []string{"image/*"}, got)
}

// 全 role が useDefault の場合は base 値 (meta.policies 経由含む) が反映される
// regression guard。aggregator は useDefault entry の場合 base 値を candidate
// として使うので、結果は set union 後 base そのもの。本 test は base override
// が「集約に参加しない」のではなく「useDefault 経路では base が候補として
// 使われる」挙動を seal する。
func TestGetUserPolicies_UploadableFileTypesAllUseDefaultUsesBase(t *testing.T) {
	svc, roleRepo, assignRepo, metaRepo := newTestService(t)
	// meta.policies で base allowlist を ["text/*", "audio/*"] に
	metaRepo.Meta = &model.Meta{ID: "x",
		Policies: datatypes.JSON([]byte(`{"uploadableFileTypes":["text/*","audio/*"]}`))}
	// role は当該 policy で useDefault=true (= base 値を採用)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1",
		Policies: datatypes.JSON([]byte(`{"uploadableFileTypes":{"useDefault":true,"priority":0,"value":["IGNORED"]}}`))}
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}

	policies := svc.GetUserPolicies("u1")
	got, ok := policies["uploadableFileTypes"].([]string)
	require.True(t, ok)
	// base = ["text/*", "audio/*"] が candidate になり、set union 後 sort 済。
	assert.Equal(t, []string{"audio/*", "text/*"}, got)
}

// 複数 role の uploadableFileTypes が set union で merge される (= 「より
// 寛容な方向への集約」、upstream Misskey TS の calc('uploadableFileTypes',
// set union) と等価)。
func TestGetUserPolicies_UploadableFileTypesMultiRoleUnion(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1",
		Policies: datatypes.JSON([]byte(`{"uploadableFileTypes":{"useDefault":false,"priority":0,"value":["image/png","image/jpeg"]}}`))}
	roleRepo.Roles["r2"] = &model.Role{ID: "r2",
		Policies: datatypes.JSON([]byte(`{"uploadableFileTypes":{"useDefault":false,"priority":0,"value":["image/png","video/mp4"]}}`))}
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}
	assignRepo.Assignments["u1:r2"] = &model.RoleAssignment{ID: "a2", UserID: "u1", RoleID: "r2"}

	policies := svc.GetUserPolicies("u1")
	got, ok := policies["uploadableFileTypes"].([]string)
	require.True(t, ok)
	// r1 + r2 の和集合、sort 順 (image/jpeg, image/png, video/mp4)
	assert.Equal(t, []string{"image/jpeg", "image/png", "video/mp4"}, got)
}

// priority=0 fallback: 全 role が default を選んでいても、最終 aggregator は
// base を返す (regression: priority-2/1 が一件も無い時に空集合で集約しない)。
func TestGetUserPolicies_PriorityZeroFallback(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1",
		Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":true,"priority":0,"value":false}}`))}
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}

	policies := svc.GetUserPolicies("u1")
	// useDefault=true の role しか無い場合は base (= default false) のまま。
	assert.Equal(t, false, policies["canSearchNotes"])
}

// 複数 role × 全 useDefault: 全 role が当該 policy で default を選んでいる
// 場合、bool OR aggregator も base に倒れる。
// 一方で 1 つでも override true を持つ role があれば、別 role が
// useDefault=true (base=false) であっても OR で true に上がる cross-test。
func TestGetUserPolicies_MultipleRolesUseDefaultCross(t *testing.T) {
	t.Run("all use default → base のまま false", func(t *testing.T) {
		svc, roleRepo, assignRepo, _ := newTestService(t)
		roleRepo.Roles["r1"] = &model.Role{ID: "r1",
			Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":true,"priority":0,"value":true}}`))}
		roleRepo.Roles["r2"] = &model.Role{ID: "r2",
			Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":true,"priority":0,"value":true}}`))}
		assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}
		assignRepo.Assignments["u1:r2"] = &model.RoleAssignment{ID: "a2", UserID: "u1", RoleID: "r2"}

		policies := svc.GetUserPolicies("u1")
		assert.Equal(t, false, policies["canSearchNotes"], "全 role useDefault なら base false")
	})

	t.Run("1 つだけ override=true なら全体 true", func(t *testing.T) {
		svc, roleRepo, assignRepo, _ := newTestService(t)
		roleRepo.Roles["r1"] = &model.Role{ID: "r1",
			Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":true,"priority":0,"value":false}}`))}
		roleRepo.Roles["r2"] = &model.Role{ID: "r2",
			Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":true}}`))}
		assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}
		assignRepo.Assignments["u1:r2"] = &model.RoleAssignment{ID: "a2", UserID: "u1", RoleID: "r2"}

		policies := svc.GetUserPolicies("u1")
		assert.Equal(t, true, policies["canSearchNotes"], "1 role でも override true なら OR で true")
	})

	t.Run("role に当該 policy entry が無い (= 別 policy のみ override)", func(t *testing.T) {
		svc, roleRepo, assignRepo, _ := newTestService(t)
		// r1 は canInvite のみ持って canSearchNotes は entry 無し (= 暗黙 useDefault)。
		roleRepo.Roles["r1"] = &model.Role{ID: "r1",
			Policies: datatypes.JSON([]byte(`{"canInvite":{"useDefault":false,"priority":0,"value":true}}`))}
		assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}

		policies := svc.GetUserPolicies("u1")
		assert.Equal(t, false, policies["canSearchNotes"], "当該 policy entry 不在 = useDefault 扱いで base")
		assert.Equal(t, true, policies["canInvite"], "別 policy の override は影響しない")
	})
}

// meta.policies 経由の数値が base の型 (int) に丸められることを cross-check。
// role override (maxNumberAsInt 経由) と meta override (coerceToBaseType 経由)
// の両方で同じ Go 型が返ることを保証する (= consumer の type assert 安全性)。
func TestGetUserPolicies_NumericCoercion(t *testing.T) {
	t.Run("meta.policies が int に丸まる", func(t *testing.T) {
		svc, _, _, metaRepo := newTestService(t)
		metaRepo.Meta = &model.Meta{ID: "x",
			Policies: datatypes.JSON([]byte(`{"pinLimit":7}`))}

		policies := svc.GetUserPolicies("u1")
		v, ok := policies["pinLimit"].(int)
		require.True(t, ok, "meta override 経由でも int を返す")
		assert.Equal(t, 7, v)
	})
	t.Run("role override も同じ int 型", func(t *testing.T) {
		svc, roleRepo, assignRepo, _ := newTestService(t)
		roleRepo.Roles["r1"] = &model.Role{ID: "r1",
			Policies: datatypes.JSON([]byte(`{"pinLimit":{"useDefault":false,"priority":0,"value":12}}`))}
		assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}

		policies := svc.GetUserPolicies("u1")
		v, ok := policies["pinLimit"].(int)
		require.True(t, ok, "role override 経由でも int を返す")
		assert.Equal(t, 12, v)
	})
}

func TestAssign_Success(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Admin"}

	err := svc.Assign("user1", "r1", nil)
	require.NoError(t, err)
	assert.Len(t, assignRepo.Assignments, 1)
}

func TestAssign_RoleNotFound(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	err := svc.Assign("user1", "ghost", nil)
	assert.ErrorIs(t, err, role.ErrRoleNotFound)
}

func TestAssign_AlreadyAssigned(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}

	err := svc.Assign("user1", "r1", nil)
	assert.ErrorIs(t, err, role.ErrAlreadyAssigned)
}

func TestAssign_WithExpiry(t *testing.T) {
	svc, roleRepo, _, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	exp := time.Now().Add(24 * time.Hour)
	err := svc.Assign("user1", "r1", &exp)
	require.NoError(t, err)
}

// recordingRoleAssignNotifier captures OnRoleAssigned calls (#1559)。
type recordingRoleAssignNotifier struct{ calls [][2]string }

func (n *recordingRoleAssignNotifier) OnRoleAssigned(userID, roleID string) {
	n.calls = append(n.calls, [2]string{userID, roleID})
}

// #1559 [MEDIUM] public role 割当時に roleAssigned 通知が発火する。
func TestAssign_FiresRoleAssignedForPublicRole(t *testing.T) {
	svc, roleRepo, _, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "VIP", IsPublic: true}
	notifier := &recordingRoleAssignNotifier{}
	svc.SetRoleAssignNotifier(notifier)

	require.NoError(t, svc.Assign("user1", "r1", nil))
	require.Equal(t, [][2]string{{"user1", "r1"}}, notifier.calls)
}

// 非 public role の割当では通知しない (upstream の role.isPublic ガード)。
func TestAssign_NoNotificationForNonPublicRole(t *testing.T) {
	svc, roleRepo, _, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Hidden", IsPublic: false}
	notifier := &recordingRoleAssignNotifier{}
	svc.SetRoleAssignNotifier(notifier)

	require.NoError(t, svc.Assign("user1", "r1", nil))
	assert.Empty(t, notifier.calls, "non-public role は通知しない")
}

func TestUnassign_Success(t *testing.T) {
	svc, _, assignRepo, _ := newTestService(t)
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}

	err := svc.Unassign("user1", "r1")
	require.NoError(t, err)
	assert.Empty(t, assignRepo.Assignments)
}

func TestUnassign_NotAssigned(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	err := svc.Unassign("user1", "r1")
	assert.ErrorIs(t, err, role.ErrNotAssigned)
}

func TestCreate_Success(t *testing.T) {
	svc, roleRepo, _, _ := newTestService(t)
	r, err := svc.Create("Admin", "Administrator role", role.CreateOptions{
		IsAdministrator: true,
		IsPublic:        true,
	})
	require.NoError(t, err)
	assert.Equal(t, "Admin", r.Name)
	assert.True(t, r.IsAdministrator)
	assert.Len(t, roleRepo.Roles, 1)
}

func TestShow_Found(t *testing.T) {
	svc, roleRepo, _, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Test"}
	r, err := svc.Show("r1")
	require.NoError(t, err)
	assert.Equal(t, "Test", r.Name)
}

func TestShow_NotFound(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	_, err := svc.Show("ghost")
	assert.ErrorIs(t, err, role.ErrRoleNotFound)
}

func TestList(t *testing.T) {
	svc, roleRepo, _, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "A"}
	roleRepo.Roles["r2"] = &model.Role{ID: "r2", Name: "B"}
	roles, err := svc.List()
	require.NoError(t, err)
	assert.Len(t, roles, 2)
}

func TestDelete_Success(t *testing.T) {
	svc, roleRepo, _, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	err := svc.Delete("r1")
	require.NoError(t, err)
	assert.Empty(t, roleRepo.Roles)
}

func TestDelete_NotFound(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	err := svc.Delete("ghost")
	assert.ErrorIs(t, err, role.ErrRoleNotFound)
}

// --- Error path tests ---

func TestGetUserRoles_RepoError(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	// assignRepo は空なのでエラーにならないが、nil role は除外される
	roles, err := svc.GetUserRoles("user1")
	require.NoError(t, err)
	assert.Empty(t, roles)
}

func TestIsAdministrator_GetRolesError(t *testing.T) {
	// metaRepo にMeta設定してrootUser判定をスキップ → GetUserRoles を呼ぶ
	svc, _, _, metaRepo := newTestService(t)
	metaRepo.Meta = &model.Meta{ID: "x"}
	// assignRepo が空なので GetUserRoles は空を返す → false
	assert.False(t, svc.IsAdministrator("user1"))
}

func TestIsModerator_RootUser(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	rootID := "root1"
	metaRepo.Meta = &model.Meta{ID: "x", RootUserID: &rootID}
	assert.True(t, svc.IsModerator("root1"))
}

func TestIsModerator_MetaFetchError(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	// metaRepo.Meta = nil → Fetch error → isRootUser false → no roles → false
	assert.False(t, svc.IsModerator("user1"))
}

func TestAssign_ExistsError(t *testing.T) {
	// Exists がエラーを返すケースは mock では発生しにくいが、
	// role が存在して assignment が空の場合は正常に Assign できるので
	// Create のエラーパスをテスト
	svc, roleRepo, _, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	err := svc.Assign("user1", "r1", nil)
	require.NoError(t, err)
}

func TestUnassign_DeleteError(t *testing.T) {
	// Unassign で Exists true → Delete → mock では常に成功
	svc, _, assignRepo, _ := newTestService(t)
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}
	require.NoError(t, svc.Unassign("user1", "r1"))
}

func TestCreate_RepoError(t *testing.T) {
	// mock の Create は常に成功するのでエラーパスは到達しにくい
	// 正常パスの補強として CreateOptions の全フィールドを検証
	svc, _, _, _ := newTestService(t)
	r, err := svc.Create("Mod", "Moderator", role.CreateOptions{
		IsModerator:  true,
		AsBadge:      true,
		IsExplorable: true,
		DisplayOrder: 10,
	})
	require.NoError(t, err)
	assert.True(t, r.IsModerator)
	assert.Equal(t, 10, r.DisplayOrder)
}

func TestApplyRolePolicies_EmptyPolicies(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{
		ID:       "r1",
		Policies: datatypes.JSON([]byte(`{}`)),
	}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}
	policies := svc.GetUserPolicies("user1")
	assert.Equal(t, 100, policies["driveCapacityMb"]) // デフォルト維持
}

// --- Failing repository tests ---

type failingAssignRepo struct {
	*testutil.MockRoleAssignmentRepository
}

func (f *failingAssignRepo) ListByUser(_ string) ([]*model.RoleAssignment, error) {
	return nil, assert.AnError
}

func (f *failingAssignRepo) Exists(_ string, _ string) (bool, error) {
	return false, assert.AnError
}

func (f *failingAssignRepo) CountActiveByRole(_ string) (int, error) {
	return 0, assert.AnError
}

// CountAssignedUsers は active assignment 数を返す。
func TestCountAssignedUsers_Counts(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "R"}
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}
	assignRepo.Assignments["u2:r1"] = &model.RoleAssignment{ID: "a2", UserID: "u2", RoleID: "r1"}
	assert.Equal(t, 2, svc.CountAssignedUsers("r1"))
}

// repo error 時は fail-open で 0 を返す (pack 全体を失敗させない)。
func TestCountAssignedUsers_FailOpen(t *testing.T) {
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := &failingAssignRepo{testutil.NewMockRoleAssignmentRepository(roleRepo)}
	metaRepo := testutil.NewMockMetaRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	assert.Equal(t, 0, svc.CountAssignedUsers("r1"))
}

type failingRoleRepo struct {
	*testutil.MockRoleRepository
}

func (f *failingRoleRepo) Create(_ *model.Role) error { return assert.AnError }

func TestGetUserRoles_ListByUserError(t *testing.T) {
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := &failingAssignRepo{testutil.NewMockRoleAssignmentRepository(roleRepo)}
	metaRepo := testutil.NewMockMetaRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)

	_, err := svc.GetUserRoles("user1")
	assert.Error(t, err)
}

func TestIsAdministrator_GetRolesError_ReturnsFalse(t *testing.T) {
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := &failingAssignRepo{testutil.NewMockRoleAssignmentRepository(roleRepo)}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	svc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)

	assert.False(t, svc.IsAdministrator("user1"))
}

func TestIsModerator_GetRolesError_ReturnsFalse(t *testing.T) {
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := &failingAssignRepo{testutil.NewMockRoleAssignmentRepository(roleRepo)}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	svc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)

	assert.False(t, svc.IsModerator("user1"))
}

func TestAssign_ExistsRepoError(t *testing.T) {
	rr := testutil.NewMockRoleRepository()
	rr.Roles["r1"] = &model.Role{ID: "r1"}
	assignRepo := &failingAssignRepo{testutil.NewMockRoleAssignmentRepository(rr)}
	metaRepo := testutil.NewMockMetaRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := role.NewService(rr, assignRepo, metaRepo, idGen)

	err := svc.Assign("user1", "r1", nil)
	assert.Error(t, err)
}

func TestUnassign_ExistsRepoError(t *testing.T) {
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := &failingAssignRepo{testutil.NewMockRoleAssignmentRepository(roleRepo)}
	metaRepo := testutil.NewMockMetaRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)

	err := svc.Unassign("user1", "r1")
	assert.Error(t, err)
}

func TestCreate_RepoCreateError(t *testing.T) {
	rr := &failingRoleRepo{testutil.NewMockRoleRepository()}
	assignRepo := testutil.NewMockRoleAssignmentRepository(rr.MockRoleRepository)
	metaRepo := testutil.NewMockMetaRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := role.NewService(rr, assignRepo, metaRepo, idGen)

	_, err := svc.Create("Test", "desc", role.CreateOptions{})
	assert.Error(t, err)
}

func TestApplyRolePolicies_NilPolicies(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Policies: nil}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}
	policies := svc.GetUserPolicies("user1")
	assert.Equal(t, 100, policies["driveCapacityMb"])
}

func TestDefaultPolicies(t *testing.T) {
	p := role.DefaultPolicies()
	assert.Equal(t, true, p["gtlAvailable"])
	assert.Equal(t, 100, p["driveCapacityMb"])
	assert.Equal(t, 5, p["pinLimit"])
}

// #1377: DefaultPolicies は共有 cache を返すため、DefaultPoliciesClone は
// 独立した copy でなければならない。clone を mutate しても共有 default が
// 壊れないことを保証する (壊れると全リクエストの policy が汚染される)。
func TestDefaultPoliciesClone_Isolation(t *testing.T) {
	origDrive := role.DefaultPolicies()["driveCapacityMb"]

	clone := role.DefaultPoliciesClone()
	assert.Equal(t, origDrive, clone["driveCapacityMb"], "clone は同値で始まる")

	clone["driveCapacityMb"] = 999999
	clone["injected"] = true

	assert.Equal(t, origDrive, role.DefaultPolicies()["driveCapacityMb"],
		"clone の mutation が共有 default を破壊している")
	_, leaked := role.DefaultPolicies()["injected"]
	assert.False(t, leaked, "clone への key 追加が共有 default に漏れている")
}

// #1377: GetUserPolicies は applyMetaBasePolicies で base を mutate するため
// clone を使う。meta override が共有 DefaultPolicies cache を汚染しないことを
// 保証する (= 本最適化の安全性の要)。clone を使わないと本テストは fail する。
func TestGetUserPolicies_MetaOverrideDoesNotCorruptSharedDefault(t *testing.T) {
	svc, _, _, metaRepo := newTestService(t)
	metaRepo.Meta = &model.Meta{
		ID:       "x",
		Policies: datatypes.JSON([]byte(`{"driveCapacityMb": 5000}`)),
	}
	origDrive := role.DefaultPolicies()["driveCapacityMb"] // = 100

	got := svc.GetUserPolicies("") // userID="" → base + meta override のみ
	assert.Equal(t, 5000, got["driveCapacityMb"], "meta override が反映される")

	assert.Equal(t, origDrive, role.DefaultPolicies()["driveCapacityMb"],
		"GetUserPolicies の meta override が共有 default を破壊している (#1377)")
}

func TestIsSilenced_DefaultIsFalse(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	// 誰もロールを持っていない → DefaultPolicies は canPublicNote=true
	assert.False(t, svc.IsSilenced("user1"))
}

func TestIsSilenced_CanPublicNoteFalseMeansSilenced(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	// policies JSON で canPublicNote=false を上書きした role を持たせる
	roleRepo.Roles["r1"] = &model.Role{
		ID: "r1",
		Policies: datatypes.JSON([]byte(
			`{"canPublicNote":{"useDefault":false,"priority":1,"value":false}}`,
		)),
	}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}
	assert.True(t, svc.IsSilenced("user1"))
}

// --- per-user role cache (#300 3-5) -----------------------------------------

// countingAssignmentRepo wraps the mock to count ListByUser calls so we can
// verify the role cache hits.
type countingAssignmentRepo struct {
	*testutil.MockRoleAssignmentRepository
	listByUserCalls int
}

func (c *countingAssignmentRepo) ListByUser(userID string) ([]*model.RoleAssignment, error) {
	c.listByUserCalls++
	return c.MockRoleAssignmentRepository.ListByUser(userID)
}

func newServiceWithCountingAssign(t *testing.T) (*role.Service, *testutil.MockRoleRepository, *countingAssignmentRepo) {
	t.Helper()
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := &countingAssignmentRepo{
		MockRoleAssignmentRepository: testutil.NewMockRoleAssignmentRepository(roleRepo),
	}
	metaRepo := testutil.NewMockMetaRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	return svc, roleRepo, assignRepo
}

// 同一 user に対する 10 連続 GetUserRoles で assignmentRepo.ListByUser が
// 1 回しか呼ばれないことを担保 (#300 3-5)。
func TestGetUserRoles_CachedHitsRepoOnce(t *testing.T) {
	svc, roleRepo, assignRepo := newServiceWithCountingAssign(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Mod", IsModerator: true}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}

	for i := 0; i < 10; i++ {
		roles, err := svc.GetUserRoles("user1")
		require.NoError(t, err)
		require.Len(t, roles, 1)
	}
	assert.Equal(t, 1, assignRepo.listByUserCalls,
		"per-user role list should be cached; only the first call hits the repo")
}

// Assign で当該ユーザーの cache が invalidate されることを担保。
func TestAssign_InvalidatesUserRoleCache(t *testing.T) {
	svc, roleRepo, assignRepo := newServiceWithCountingAssign(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Mod"}
	roleRepo.Roles["r2"] = &model.Role{ID: "r2", Name: "Trial"}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}

	// warm cache
	_, _ = svc.GetUserRoles("user1")
	assert.Equal(t, 1, assignRepo.listByUserCalls)

	require.NoError(t, svc.Assign("user1", "r2", nil))

	// next read should miss cache and go to DB
	_, _ = svc.GetUserRoles("user1")
	assert.Equal(t, 2, assignRepo.listByUserCalls,
		"Assign must invalidate the user's role cache so next read is fresh")
}

// Unassign で当該ユーザーの cache が invalidate されることを担保。
func TestUnassign_InvalidatesUserRoleCache(t *testing.T) {
	svc, roleRepo, assignRepo := newServiceWithCountingAssign(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Mod"}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}

	_, _ = svc.GetUserRoles("user1")
	assert.Equal(t, 1, assignRepo.listByUserCalls)

	require.NoError(t, svc.Unassign("user1", "r1"))

	_, _ = svc.GetUserRoles("user1")
	assert.Equal(t, 2, assignRepo.listByUserCalls,
		"Unassign must invalidate the user's role cache")
}

// Service.Delete (role 削除) で全 user の cache が flush されること。
func TestDelete_InvalidatesAllRoleCaches(t *testing.T) {
	svc, roleRepo, assignRepo := newServiceWithCountingAssign(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Mod"}
	roleRepo.Roles["r2"] = &model.Role{ID: "r2", Name: "Trial"}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}
	assignRepo.Assignments["user2:r1"] = &model.RoleAssignment{
		ID: "a2", UserID: "user2", RoleID: "r1",
	}

	// warm cache for both users
	_, _ = svc.GetUserRoles("user1")
	_, _ = svc.GetUserRoles("user2")
	assert.Equal(t, 2, assignRepo.listByUserCalls)

	require.NoError(t, svc.Delete("r2")) // 別 role の delete でも全 flush

	_, _ = svc.GetUserRoles("user1")
	_, _ = svc.GetUserRoles("user2")
	assert.Equal(t, 4, assignRepo.listByUserCalls,
		"Delete should flush every cached entry (we don't know which users were assigned)")
}

func TestGetUserRoles_EmptyUserIDDoesNotCache(t *testing.T) {
	svc, _, assignRepo := newServiceWithCountingAssign(t)
	roles, err := svc.GetUserRoles("")
	require.NoError(t, err)
	assert.Empty(t, roles)
	assert.Equal(t, 0, assignRepo.listByUserCalls,
		"empty userID must not hit the repo at all")
}

// --- #1030: roleRepo.List() の TTL cache ---

type countingRoleRepo struct {
	*testutil.MockRoleRepository
	listCalls int
}

func (c *countingRoleRepo) List() ([]*model.Role, error) {
	c.listCalls++
	return c.MockRoleRepository.List()
}

func newServiceWithCountingRoleRepo(t *testing.T) (*role.Service, *countingRoleRepo, *testutil.MockRoleAssignmentRepository) {
	t.Helper()
	mockRoleRepo := testutil.NewMockRoleRepository()
	roleRepo := &countingRoleRepo{MockRoleRepository: mockRoleRepo}
	assignRepo := testutil.NewMockRoleAssignmentRepository(mockRoleRepo)
	metaRepo := testutil.NewMockMetaRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	return svc, roleRepo, assignRepo
}

// evaluateConditionalRoles 経由で複数 user が連続して GetUserRoles を呼んでも、
// roleRepo.List() は cache hit で 1 回しか発火しないことを保証する (#1030)。
// userRoleCache は user 単位なので別 user の cache miss でも fire するが、
// rolesListCache は global なので「1 回呼んだら 5 min 共有」になる。
func TestListRolesCached_HitsRepoOnceAcrossUsers(t *testing.T) {
	svc, roleRepo, _ := newServiceWithCountingRoleRepo(t)
	// conditional role を 1 件用意して evaluateConditionalRoles 経路を起動
	roleRepo.Roles["r-bot"] = &model.Role{
		ID:          "r-bot",
		Target:      model.RoleTargetConditional,
		CondFormula: datatypes.JSON([]byte(`{"type":"isBot"}`)),
	}
	userRepo := testutil.NewMockUserRepository()
	require.NoError(t, userRepo.Create(&model.User{ID: "u1", Username: "u1", IsBot: true}))
	require.NoError(t, userRepo.Create(&model.User{ID: "u2", Username: "u2", IsBot: false}))
	svc.SetUserRepo(userRepo)

	// 別 user の連続呼び出しで user-level cache は別 entry を持つが、roleRepo
	// 側は global TTL cache に乗るので呼び出し回数は 1 回のままになる。
	_, _ = svc.GetUserRoles("u1")
	_, _ = svc.GetUserRoles("u2")
	_, _ = svc.GetUserRoles("u1")
	assert.Equal(t, 1, roleRepo.listCalls,
		"rolesListCache は global なので user 跨ぎでも 1 回しか repo を叩かない")
}

// Create で role list cache が invalidate されることを担保。
func TestCreate_InvalidatesRoleListCache(t *testing.T) {
	svc, roleRepo, _ := newServiceWithCountingRoleRepo(t)
	roleRepo.Roles["r-bot"] = &model.Role{
		ID:          "r-bot",
		Target:      model.RoleTargetConditional,
		CondFormula: datatypes.JSON([]byte(`{"type":"isBot"}`)),
	}
	userRepo := testutil.NewMockUserRepository()
	require.NoError(t, userRepo.Create(&model.User{ID: "u1", Username: "u1", IsBot: true}))
	svc.SetUserRepo(userRepo)

	// warm cache
	_, _ = svc.GetUserRoles("u1")
	assert.Equal(t, 1, roleRepo.listCalls)

	// 新規 role を作ると role list cache が flush される
	_, err := svc.Create("NewRole", "desc", role.CreateOptions{})
	require.NoError(t, err)

	// 同 user の userRoleCache はまだ live なので GetUserRoles 自体は repo を
	// 叩かないが、cache を bust するため別 user で確認する。
	require.NoError(t, userRepo.Create(&model.User{ID: "u2", Username: "u2", IsBot: true}))
	_, _ = svc.GetUserRoles("u2")
	assert.Equal(t, 2, roleRepo.listCalls,
		"Create で role list cache が invalidate されて、次回 evaluation で repo を再 fetch")
}

// UpdateFields で role list cache が invalidate されることを担保。
func TestUpdateFields_InvalidatesRoleListCache(t *testing.T) {
	svc, roleRepo, _ := newServiceWithCountingRoleRepo(t)
	roleRepo.Roles["r-bot"] = &model.Role{
		ID:          "r-bot",
		Target:      model.RoleTargetConditional,
		CondFormula: datatypes.JSON([]byte(`{"type":"isBot"}`)),
	}
	roleRepo.Roles["r2"] = &model.Role{ID: "r2", Name: "Other"}
	userRepo := testutil.NewMockUserRepository()
	require.NoError(t, userRepo.Create(&model.User{ID: "u1", Username: "u1", IsBot: true}))
	require.NoError(t, userRepo.Create(&model.User{ID: "u2", Username: "u2", IsBot: true}))
	svc.SetUserRepo(userRepo)

	_, _ = svc.GetUserRoles("u1")
	assert.Equal(t, 1, roleRepo.listCalls)

	// 別 role の condFormula を変更しても全 role list cache は flush される
	_, err := svc.UpdateFields("r2", map[string]any{"name": "Updated"})
	require.NoError(t, err)

	_, _ = svc.GetUserRoles("u2") // 別 user で確認
	assert.Equal(t, 2, roleRepo.listCalls)
}

// PR #1102 regression guard: UpdateFields は **userRoleCache** も flush する。
// 旧版は rolesListCache だけ flush していて、既に assigned 済 user の policy
// 評価は per-user TTL (5min) の間 stale policies を返し続け、admin で policy
// を変えても効かない現象になっていた。InvalidateAllRoleCaches を呼ぶよう
// 修正したので、UpdateFields 直後に同 user の GetUserRoles を呼ぶと
// assignmentRepo.ListByUser が再 fire する (cache が live なら叩かない)。
func TestUpdateFields_InvalidatesUserRoleCache(t *testing.T) {
	svc, roleRepo, assignRepo := newServiceWithCountingAssign(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Old"}
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "u1", RoleID: "r1",
	}

	// warm cache
	_, _ = svc.GetUserRoles("u1")
	require.Equal(t, 1, assignRepo.listByUserCalls)
	// 2 度目は cache hit
	_, _ = svc.GetUserRoles("u1")
	require.Equal(t, 1, assignRepo.listByUserCalls)

	// admin が policy を update する経路を模擬
	_, err := svc.UpdateFields("r1", map[string]any{"name": "New"})
	require.NoError(t, err)

	// userRoleCache["u1"] が flush されているので、GetUserRoles は再 listing
	_, _ = svc.GetUserRoles("u1")
	assert.Equal(t, 2, assignRepo.listByUserCalls,
		"UpdateFields must flush userRoleCache so admin policy changes take effect immediately")
}

// 並行 cache miss でも roleRepo.List() は single-flight 相当で 1 回しか
// 発火しないことを担保する (#1035 review: RWMutex 化に伴う double-check
// pattern の regression guard、-race で並行性も検証する)。
func TestListRolesCached_ConcurrentMissSingleFlight(t *testing.T) {
	svc, roleRepo, _ := newServiceWithCountingRoleRepo(t)
	roleRepo.Roles["r-bot"] = &model.Role{
		ID:          "r-bot",
		Target:      model.RoleTargetConditional,
		CondFormula: datatypes.JSON([]byte(`{"type":"isBot"}`)),
	}
	userRepo := testutil.NewMockUserRepository()
	// 複数 user を作って evaluateConditionalRoles を並行に起動できるようにする
	for i := 0; i < 20; i++ {
		uid := "u" + string(rune('a'+i))
		require.NoError(t, userRepo.Create(&model.User{ID: uid, Username: uid, IsBot: true}))
	}
	svc.SetUserRepo(userRepo)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		uid := "u" + string(rune('a'+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.GetUserRoles(uid)
		}()
	}
	wg.Wait()
	// 全 20 goroutine が cache miss から開始しても、double-check pattern で
	// roleRepo.List() は 1 回しか呼ばれない (= single-flight 相当)。
	assert.Equal(t, 1, roleRepo.listCalls,
		"concurrent cache miss should de-dup to single roleRepo.List() call")
}

// Delete (= InvalidateAllRoleCaches 経由) で role list cache も flush される。
func TestDelete_InvalidatesRoleListCache(t *testing.T) {
	svc, roleRepo, _ := newServiceWithCountingRoleRepo(t)
	roleRepo.Roles["r-bot"] = &model.Role{
		ID:          "r-bot",
		Target:      model.RoleTargetConditional,
		CondFormula: datatypes.JSON([]byte(`{"type":"isBot"}`)),
	}
	roleRepo.Roles["r2"] = &model.Role{ID: "r2", Name: "Trial"}
	userRepo := testutil.NewMockUserRepository()
	require.NoError(t, userRepo.Create(&model.User{ID: "u1", Username: "u1", IsBot: true}))
	require.NoError(t, userRepo.Create(&model.User{ID: "u2", Username: "u2", IsBot: true}))
	svc.SetUserRepo(userRepo)

	_, _ = svc.GetUserRoles("u1")
	assert.Equal(t, 1, roleRepo.listCalls)

	require.NoError(t, svc.Delete("r2"))

	_, _ = svc.GetUserRoles("u2")
	assert.Equal(t, 2, roleRepo.listCalls)
}

// 失敗した Assign / Unassign は invalidate しない (DB 状態が変わらないため)。
func TestAssign_FailedDoesNotInvalidate(t *testing.T) {
	svc, roleRepo, assignRepo := newServiceWithCountingAssign(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Mod"}
	assignRepo.Assignments["user1:r1"] = &model.RoleAssignment{
		ID: "a1", UserID: "user1", RoleID: "r1",
	}

	_, _ = svc.GetUserRoles("user1") // warm

	// 同じ role を再度 Assign → ErrAlreadyAssigned で失敗
	err := svc.Assign("user1", "r1", nil)
	require.Error(t, err)

	_, _ = svc.GetUserRoles("user1")
	assert.Equal(t, 1, assignRepo.listByUserCalls,
		"failed Assign must not invalidate cache")
}

// --- ListByRole ---

func TestListByRole_RoleNotFound(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	_, err := svc.ListByRole("ghost", "", "", 10)
	assert.ErrorIs(t, err, role.ErrRoleNotFound)
}

func TestListByRole_PreloadsUser(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	// MockRoleAssignmentRepository は UserRepo がセットされていれば
	// ListByRole の戻りに User を埋める。
	userRepo := testutil.NewMockUserRepository()
	require.NoError(t, userRepo.Create(&model.User{ID: "u1", Username: "alice"}))
	assignRepo.UserRepo = userRepo
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}

	got, err := svc.ListByRole("r1", "", "", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].User)
	assert.Equal(t, "alice", got[0].User.Username)
}

func TestListByRole_RepoError(t *testing.T) {
	roleRepo := testutil.NewMockRoleRepository()
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	assignRepo := &failingListByRoleAssignRepo{testutil.NewMockRoleAssignmentRepository(roleRepo)}
	metaRepo := testutil.NewMockMetaRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)

	_, err := svc.ListByRole("r1", "", "", 10)
	assert.Error(t, err)
}

// errorUserRepo は FindByID で任意の error を返す UserRepository stub。
// ErrRecordNotFound 以外の transient な error 経路 (#785 fallback の
// silent failure 動作) を検証するために MockUserRepository を embed する
// (他のメソッドが将来呼ばれても nil deref しないよう必ず初期化済みの mock
// をセットすること)。
type errorUserRepo struct {
	*testutil.MockUserRepository
	err error
}

func (e *errorUserRepo) FindByID(_ string) (*model.User, error) {
	return nil, e.err
}

// failingListByRoleAssignRepo は ListByRole で error を返す stub。
type failingListByRoleAssignRepo struct {
	*testutil.MockRoleAssignmentRepository
}

func (f *failingListByRoleAssignRepo) ListByRole(_, _, _ string, _ int) ([]*model.RoleAssignment, error) {
	return nil, assert.AnError
}

func TestUpdateFields_Success(t *testing.T) {
	svc, roleRepo, _, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Old", Description: "x"}

	after, err := svc.UpdateFields("r1", map[string]any{"name": "New"})
	require.NoError(t, err)
	require.NotNil(t, after)
	assert.Equal(t, "New", after.Name)
	assert.Equal(t, "x", after.Description, "untouched fields stay intact")
}

func TestUpdateFields_NotFound(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	_, err := svc.UpdateFields("ghost", map[string]any{"name": "x"})
	assert.ErrorIs(t, err, role.ErrRoleNotFound)
}

func TestUpdateFields_EmptyFieldsReturnsCurrent(t *testing.T) {
	// 空 map の場合は UpdateFields を呼ばずに現在値を返す。
	// admin/roles/update が optional pointer 全部 nil で来たケース。
	svc, roleRepo, _, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Stay"}

	after, err := svc.UpdateFields("r1", map[string]any{})
	require.NoError(t, err)
	require.NotNil(t, after)
	assert.Equal(t, "Stay", after.Name)
}

// failingUpdateFieldsRoleRepo は UpdateFields で error を返す stub。
type failingUpdateFieldsRoleRepo struct {
	*testutil.MockRoleRepository
}

func (f *failingUpdateFieldsRoleRepo) UpdateFields(_ string, _ map[string]any) error {
	return assert.AnError
}

func TestUpdateFields_RepoError(t *testing.T) {
	roleRepo := testutil.NewMockRoleRepository()
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Old"}
	wrapped := &failingUpdateFieldsRoleRepo{roleRepo}
	assignRepo := testutil.NewMockRoleAssignmentRepository(roleRepo)
	metaRepo := testutil.NewMockMetaRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := role.NewService(wrapped, assignRepo, metaRepo, idGen)

	_, err := svc.UpdateFields("r1", map[string]any{"name": "New"})
	assert.Error(t, err)
}

// upstream Misskey #17121 (= 2026.5.1 fix / triage #1012): HasRolePolicy は
// channels/create 等の requiredRolePolicy gate で使う policy check。default
// policies + role override の merge を見て、root / admin / policy true の
// いずれかなら true を返す。
func TestHasRolePolicy(t *testing.T) {
	t.Run("default_canCreateChannel_true_for_plain_user", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		// 何も role 設定なし → DefaultPolicies の canCreateChannel=true がそのまま
		assert.True(t, svc.HasRolePolicy("alice", "canCreateChannel"))
	})

	t.Run("default_canHideAds_false_for_plain_user", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		// DefaultPolicies の canHideAds=false がそのまま deny される
		assert.False(t, svc.HasRolePolicy("alice", "canHideAds"))
	})

	t.Run("root_bypasses_policy_false", func(t *testing.T) {
		svc, _, _, metaRepo := newTestService(t)
		rootID := "root1"
		metaRepo.Meta = &model.Meta{ID: "x", RootUserID: &rootID}
		// canHideAds は default false だが root は無条件で true
		assert.True(t, svc.HasRolePolicy("root1", "canHideAds"))
	})

	t.Run("admin_role_bypasses_policy_false", func(t *testing.T) {
		svc, roleRepo, assignRepo, _ := newTestService(t)
		roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Admin", IsAdministrator: true}
		assignRepo.Assignments["alice:r1"] = &model.RoleAssignment{
			ID: "a1", UserID: "alice", RoleID: "r1",
		}
		// admin role assignment → policy 値に関係なく true
		assert.True(t, svc.HasRolePolicy("alice", "canHideAds"))
	})

	t.Run("role_override_can_deny_canCreateChannel", func(t *testing.T) {
		svc, roleRepo, assignRepo, _ := newTestService(t)
		// canCreateChannel=false を override する role を作成
		roleRepo.Roles["r_no_channel"] = &model.Role{
			ID:       "r_no_channel",
			Name:     "NoChannel",
			Policies: datatypes.JSON([]byte(`{"canCreateChannel": {"useDefault": false, "priority": 0, "value": false}}`)),
		}
		assignRepo.Assignments["bob:r_no_channel"] = &model.RoleAssignment{
			ID: "a2", UserID: "bob", RoleID: "r_no_channel",
		}
		assert.False(t, svc.HasRolePolicy("bob", "canCreateChannel"),
			"role override で canCreateChannel=false → deny")
	})

	t.Run("unknown_policy_key_denies", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		// 未知 key は fail-closed
		assert.False(t, svc.HasRolePolicy("alice", "thisDoesNotExist"))
	})
}

// MergeMetaPolicies overlays the instance's meta.policies JSON override onto the
// default policies (upstream { ...DEFAULT_POLICIES, ...instance.policies }) and
// coerces numeric overrides to the default's Go type.
func TestMergeMetaPolicies(t *testing.T) {
	t.Run("nil override returns defaults", func(t *testing.T) {
		merged := role.MergeMetaPolicies(nil)
		assert.Equal(t, role.DefaultPolicies()["gtlAvailable"], merged["gtlAvailable"])
		assert.Equal(t, role.DefaultPolicies()["mentionLimit"], merged["mentionLimit"])
	})

	t.Run("override replaces defaults and coerces numeric type", func(t *testing.T) {
		// JSON は数値を float64 に decode するが、default で int の key は
		// int へ coerce される必要がある (consumer の type assert 保護)。
		merged := role.MergeMetaPolicies([]byte(`{"gtlAvailable":false,"mentionLimit":99}`))
		assert.Equal(t, false, merged["gtlAvailable"])
		assert.Equal(t, 99, merged["mentionLimit"], "int policy は int で coerce される")
		// 未指定 key は default のまま。
		assert.Equal(t, true, merged["ltlAvailable"])
	})

	t.Run("invalid JSON falls back to defaults", func(t *testing.T) {
		merged := role.MergeMetaPolicies([]byte(`{not json`))
		assert.Equal(t, role.DefaultPolicies()["ltlAvailable"], merged["ltlAvailable"])
	})

	t.Run("does not mutate the shared default map", func(t *testing.T) {
		_ = role.MergeMetaPolicies([]byte(`{"gtlAvailable":false}`))
		assert.Equal(t, true, role.DefaultPolicies()["gtlAvailable"], "default cache は不変")
	})

	t.Run("slice policy override is coerced to []string", func(t *testing.T) {
		merged := role.MergeMetaPolicies([]byte(`{"uploadableFileTypes":["text/*","image/*"]}`))
		assert.Equal(t, []string{"text/*", "image/*"}, merged["uploadableFileTypes"])
	})

	t.Run("empty slice override falls back to default", func(t *testing.T) {
		// coerceToBaseType の slice 経路は normalize 後 0 件で default に巻き戻す
		// (aggregation helper 由来の意味論)。空配列にする運用は degenerate なので
		// この挙動を仕様として固定する。
		merged := role.MergeMetaPolicies([]byte(`{"uploadableFileTypes":[]}`))
		assert.Equal(t, role.DefaultPolicies()["uploadableFileTypes"], merged["uploadableFileTypes"])
	})

	t.Run("explicit JSON null override returns defaults", func(t *testing.T) {
		merged := role.MergeMetaPolicies([]byte(`null`))
		assert.Equal(t, role.DefaultPolicies()["ltlAvailable"], merged["ltlAvailable"])
	})

	t.Run("unknown key passes through", func(t *testing.T) {
		merged := role.MergeMetaPolicies([]byte(`{"someUnknownPolicy":42}`))
		assert.Equal(t, float64(42), merged["someUnknownPolicy"], "未知 key は coerce 対象外で素通し")
	})
}

func TestService_GetModerators(t *testing.T) {
	svc, roleRepo, assignRepo, metaRepo := newTestService(t)
	userRepo := testutil.NewMockUserRepository()
	svc.SetUserRepo(userRepo)

	// moderator role + 通常 role を用意。
	roleRepo.Roles["modrole"] = &model.Role{ID: "modrole", IsModerator: true}
	roleRepo.Roles["normalrole"] = &model.Role{ID: "normalrole"}
	require.NoError(t, assignRepo.Create(&model.RoleAssignment{ID: "ga1", UserID: "mod1", RoleID: "modrole"}))
	require.NoError(t, assignRepo.Create(&model.RoleAssignment{ID: "ga2", UserID: "normal1", RoleID: "normalrole"}))
	userRepo.Users["mod1"] = &model.User{ID: "mod1"}
	userRepo.Users["root1"] = &model.User{ID: "root1"}
	// root user を meta に設定 (includeRoot)。
	rootID := "root1"
	metaRepo.Meta = &model.Meta{RootUserID: &rootID}

	mods, err := svc.GetModerators()
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, u := range mods {
		ids[u.ID] = true
	}
	assert.True(t, ids["mod1"], "moderator role を持つ user が含まれる")
	assert.True(t, ids["root1"], "root user が含まれる")
	assert.False(t, ids["normal1"], "通常 user は含まれない")
}

func TestService_GetModerators_NilUserRepo(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	// userRepo 未配線 (drop-in optional) は nil を返す。
	mods, err := svc.GetModerators()
	require.NoError(t, err)
	assert.Nil(t, mods)
}
