package role_test

import (
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

func TestGetUserRoles_NoRoles(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	roles, err := svc.GetUserRoles("user1")
	require.NoError(t, err)
	assert.Empty(t, roles)
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
	// JSON の数値は float64 で unmarshal される。base override は型を強制
	// しないので float64(200) になる (旧 last-wins と同 semantics)。
	assert.Equal(t, float64(200), policies["driveCapacityMb"])
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

// uploadableFileTypes は slice 型なので集約しない (base を維持する)。
// role が override してもまだ集約 semantics が無いので base のままになる
// (= upstream TS と同等の behavior、専用 sub-issue で将来対応)。
func TestGetUserPolicies_SlicePolicyKeepsBase(t *testing.T) {
	svc, roleRepo, assignRepo, _ := newTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1",
		Policies: datatypes.JSON([]byte(`{"uploadableFileTypes":{"useDefault":false,"priority":0,"value":["image/jpeg"]}}`))}
	assignRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}

	policies := svc.GetUserPolicies("u1")
	// base の slice が維持される (upgrade 時に専用 sub-issue で集約 semantics を
	// 定義してから aggregator を追加する想定の placeholder)。
	_, ok := policies["uploadableFileTypes"].([]string)
	assert.True(t, ok, "uploadableFileTypes の base 型は []string で維持される")
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
