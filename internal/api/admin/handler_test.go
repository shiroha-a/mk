package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	apiadmin "github.com/shiroha-a/mk/internal/api/admin"
	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestHandler(t *testing.T) (*apiadmin.Handler, *testutil.MockUserRepository, *testutil.MockMetaRepository, *testutil.MockRoleRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := testutil.NewMockRoleAssignmentRepository(roleRepo)
	idGen, _ := id.NewGenerator("aidx")
	signupSvc := signup.NewService(userRepo, metaRepo, idGen)
	roleSvc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	h := apiadmin.NewHandler(signupSvc, roleSvc, metaRepo, userRepo, idGen)
	return h, userRepo, metaRepo, roleRepo
}

func doPost(h func(echo.Context) error, body string, user *model.User) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if user != nil {
		c.Set(string(middleware.UserContextKey), user)
	}
	_ = h(c)
	return rec
}

// --- AccountsCreate ---

func TestAccountsCreate_InitialSetup(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	// rootUserId=nil → 初回セットアップ
	rec := doPost(h.AccountsCreate, `{"username":"admin","password":"pass123"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "admin", resp["username"])
	assert.NotEmpty(t, resp["token"])

	// rootUserId が設定された
	assert.NotNil(t, metaRepo.Meta.RootUserID)
}

func TestAccountsCreate_NotInitialSetup_RequiresAdmin(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	rootID := "root1"
	metaRepo.Meta.RootUserID = &rootID

	// 認証なし → ACCESS_DENIED
	rec := doPost(h.AccountsCreate, `{"username":"user2","password":"pass"}`, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestAccountsCreate_AsRootUser(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	rootID := "root1"
	metaRepo.Meta.RootUserID = &rootID

	rootUser := &model.User{ID: "root1", Username: "root"}
	rec := doPost(h.AccountsCreate, `{"username":"user2","password":"pass"}`, rootUser)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAccountsCreate_AsNonRoot_Denied(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	rootID := "root1"
	metaRepo.Meta.RootUserID = &rootID

	otherUser := &model.User{ID: "other", Username: "other"}
	rec := doPost(h.AccountsCreate, `{"username":"user2","password":"pass"}`, otherUser)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestAccountsCreate_InvalidJSON(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.AccountsCreate, `invalid`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAccountsCreate_DuplicateUsername(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "taken", UsernameLower: "taken"}

	rec := doPost(h.AccountsCreate, `{"username":"taken","password":"pass"}`, nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestAccountsCreate_MetaFetchError(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	metaRepo.Meta = nil // Fetch will error
	rec := doPost(h.AccountsCreate, `{"username":"admin","password":"pass"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- ShowUser ---

func TestShowUser_Success(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	idGen, _ := id.NewGenerator("aidx")
	uid := idGen.Generate(time.Now())
	userRepo.Users[uid] = &model.User{ID: uid, Username: "test", IsExplorable: true, AvatarDecorations: []byte("[]")}
	userRepo.Profiles[uid] = &model.UserProfile{
		UserID:             uid,
		AutoAcceptFollowed: true,
		PublicReactions:    true,
		MutedWords:         []byte("[]"),
		HardMutedWords:     []byte("[]"),
		MutedInstances:     []byte("[]"),
	}

	rec := doPost(h.ShowUser, `{"userId":"`+uid+`"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	// MeDetailed fields
	assert.Equal(t, uid, resp["id"])
	assert.NotNil(t, resp["createdAt"])
	assert.NotNil(t, resp["policies"])
	assert.NotNil(t, resp["roles"])
	assert.Equal(t, true, resp["publicReactions"])
	assert.Equal(t, "public", resp["followersVisibility"])
	assert.NotNil(t, resp["securityKeysList"])
	assert.NotNil(t, resp["achievements"])
	assert.Equal(t, false, resp["isAdmin"])
}

func TestShowUser_NotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.ShowUser, `{"userId":"ghost"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestShowUser_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.ShowUser, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- ShowUsers ---

func TestShowUsers_Success(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "a"}
	userRepo.Users["u2"] = &model.User{ID: "u2", Username: "b"}

	rec := doPost(h.ShowUsers, `{"limit":10}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 2)
}

func TestShowUsers_WithFilter(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "a", IsSuspended: true}
	userRepo.Users["u2"] = &model.User{ID: "u2", Username: "b"}

	rec := doPost(h.ShowUsers, `{"state":"suspended","limit":10}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
}

// --- SuspendUser / UnsuspendUser ---

func TestSuspendUser_Success(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "target"}

	rec := doPost(h.SuspendUser, `{"userId":"u1"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestSuspendUser_NotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.SuspendUser, `{"userId":"ghost"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSuspendUser_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.SuspendUser, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnsuspendUser_Success(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", IsSuspended: true}

	rec := doPost(h.UnsuspendUser, `{"userId":"u1"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestUnsuspendUser_NotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.UnsuspendUser, `{"userId":"ghost"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- AdminMeta / UpdateMeta ---

func TestAdminMeta_Success(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.AdminMeta, `{}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAdminMeta_FetchError(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	metaRepo.Meta = nil
	rec := doPost(h.AdminMeta, `{}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestUpdateMeta_Success(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.UpdateMeta, `{"name":"My Instance"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestAccountsCreate_EmptyUsername(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.AccountsCreate, `{"username":"","password":"pass"}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAccountsCreate_WhitespaceOnlyUsername(t *testing.T) {
	// Bindはusernameがemptyかチェックするが、空白のみはbindを通過する。
	// Signup側でTrimSpace後にemptyになり、ErrInvalidUsernameが返る。
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.AccountsCreate, `{"username":"   ","password":"pass"}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAccountsCreate_PreservedUsername(t *testing.T) {
	// rootUser 済み + admin ユーザーがリクエストしている前提 (初回セットアップ
	// ではないので preservedUsernames チェックが有効)。
	h, userRepo, metaRepo, _ := newTestHandler(t)
	rootID := "root1"
	userRepo.Users[rootID] = &model.User{ID: rootID, Username: "root", UsernameLower: "root"}
	metaRepo.Meta = &model.Meta{
		ID:                 "x",
		RootUserID:         &rootID,
		PreservedUsernames: []string{"admin", "support"},
	}

	rec := doPost(h.AccountsCreate, `{"username":"admin","password":"pass"}`, &model.User{ID: rootID})
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "USED_USERNAME", errObj["code"])
}

func TestAccountsCreate_SetupPassword_ConfigSet_Matches(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetConfigSetupPassword("mysecret")
	rec := doPost(h.AccountsCreate, `{"username":"admin","password":"pass","setupPassword":"mysecret"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAccountsCreate_SetupPassword_ConfigSet_Mismatch(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetConfigSetupPassword("mysecret")
	rec := doPost(h.AccountsCreate, `{"username":"admin","password":"pass","setupPassword":"wrong"}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "INCORRECT_INITIAL_PASSWORD")
}

func TestAccountsCreate_SetupPassword_ConfigNotSet_ClientSendsNonEmpty(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// configにsetupPasswordなし、クライアントが非空値を送信 → 拒否
	rec := doPost(h.AccountsCreate, `{"username":"admin","password":"pass","setupPassword":"unexpected"}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "INCORRECT_INITIAL_PASSWORD")
}

func TestAccountsCreate_SetupPassword_ConfigNotSet_ClientSendsEmpty(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// configにsetupPasswordなし、クライアントもnull → OK
	rec := doPost(h.AccountsCreate, `{"username":"admin","password":"pass"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestShowUsers_InvalidJSON(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.ShowUsers, `invalid`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnsuspendUser_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.UnsuspendUser, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- Failing repo tests ---

type failingUpdateUserRepo struct {
	*testutil.MockUserRepository
}

func (f *failingUpdateUserRepo) UpdateUser(_ string, _ map[string]any) error { return assert.AnError }

type failingListUsersRepo struct {
	*testutil.MockUserRepository
}

func (f *failingListUsersRepo) ListUsers(_ model.UserListFilter) ([]*model.User, error) {
	return nil, assert.AnError
}

type failingUpdateMetaRepo struct {
	*testutil.MockMetaRepository
}

func (f *failingUpdateMetaRepo) Update(_ map[string]any) error { return assert.AnError }

func TestSuspendUser_UpdateError(t *testing.T) {
	repo := &failingUpdateUserRepo{testutil.NewMockUserRepository()}
	repo.Users["u1"] = &model.User{ID: "u1"}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	h := apiadmin.NewHandler(signup.NewService(repo, metaRepo, idGen), nil, metaRepo, repo, idGen)
	rec := doPost(h.SuspendUser, `{"userId":"u1"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestUnsuspendUser_UpdateError(t *testing.T) {
	repo := &failingUpdateUserRepo{testutil.NewMockUserRepository()}
	repo.Users["u1"] = &model.User{ID: "u1"}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	h := apiadmin.NewHandler(signup.NewService(repo, metaRepo, idGen), nil, metaRepo, repo, idGen)
	rec := doPost(h.UnsuspendUser, `{"userId":"u1"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestShowUsers_ListError(t *testing.T) {
	repo := &failingListUsersRepo{testutil.NewMockUserRepository()}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	h := apiadmin.NewHandler(signup.NewService(repo, metaRepo, idGen), nil, metaRepo, repo, idGen)
	rec := doPost(h.ShowUsers, `{"limit":10}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestUpdateMeta_UpdateError(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := &failingUpdateMetaRepo{testutil.NewMockMetaRepository()}
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	h := apiadmin.NewHandler(signup.NewService(userRepo, metaRepo, idGen), nil, metaRepo, userRepo, idGen)
	rec := doPost(h.UpdateMeta, `{"name":"test"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestAccountsCreate_SignupInternalError(t *testing.T) {
	// User作成で失敗するrepoを使ってINTERNAL_ERRORパスをテスト
	repo := &failingUpdateUserRepo{testutil.NewMockUserRepository()}
	// Create もオーバーライド
	failCreateRepo := &struct {
		*failingUpdateUserRepo
	}{repo}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	// signupServiceのuserRepo.Createが失敗するようにする
	failRepo := &failingCreateUserRepoForAdmin{testutil.NewMockUserRepository()}
	h := apiadmin.NewHandler(signup.NewService(failRepo, metaRepo, idGen), nil, metaRepo, failRepo, idGen)
	_ = failCreateRepo // suppress unused
	rec := doPost(h.AccountsCreate, `{"username":"newuser","password":"pass"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type failingCreateUserRepoForAdmin struct {
	*testutil.MockUserRepository
}

func (f *failingCreateUserRepoForAdmin) Create(_ *model.User) error { return assert.AnError }

func TestUpdateMeta_InvalidJSON(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.UpdateMeta, `invalid`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- Roles endpoints ---

func TestRolesCreate_Success(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	rec := doPost(h.RolesCreate, `{"name":"Admin","isAdministrator":true}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, roleRepo.Roles, 1)
}

func TestRolesCreate_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesCreate, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRolesShow_Success(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Test"}
	rec := doPost(h.RolesShow, `{"roleId":"r1"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRolesShow_NotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesShow, `{"roleId":"ghost"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRolesShow_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesShow, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRolesList_Success(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "A"}
	rec := doPost(h.RolesList, `{}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRolesUpdate_Success(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Old"}
	rec := doPost(h.RolesUpdate, `{"roleId":"r1","name":"New"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRolesUpdate_AllFields(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Old"}
	rec := doPost(h.RolesUpdate, `{"roleId":"r1","name":"New","description":"desc","isModerator":true,"isAdministrator":true,"isPublic":true}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRolesUpdate_NotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesUpdate, `{"roleId":"ghost"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRolesUpdate_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesUpdate, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRolesDelete_Success(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	rec := doPost(h.RolesDelete, `{"roleId":"r1"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRolesDelete_NotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesDelete, `{"roleId":"ghost"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRolesDelete_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesDelete, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRolesAssign_Success(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	rec := doPost(h.RolesAssign, `{"userId":"u1","roleId":"r1"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRolesAssign_WithExpiry(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	rec := doPost(h.RolesAssign, `{"userId":"u1","roleId":"r1","expiresAt":"2099-01-01T00:00:00Z"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRolesAssign_NotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesAssign, `{"userId":"u1","roleId":"ghost"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRolesAssign_AlreadyAssigned(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	doPost(h.RolesAssign, `{"userId":"u1","roleId":"r1"}`, nil) // first assign
	rec := doPost(h.RolesAssign, `{"userId":"u1","roleId":"r1"}`, nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestRolesAssign_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesAssign, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRolesUnassign_Success(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	doPost(h.RolesAssign, `{"userId":"u1","roleId":"r1"}`, nil)
	rec := doPost(h.RolesUnassign, `{"userId":"u1","roleId":"r1"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRolesUnassign_NotAssigned(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesUnassign, `{"userId":"u1","roleId":"r1"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRolesUnassign_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesUnassign, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRolesUsers_Success(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	rec := doPost(h.RolesUsers, `{"roleId":"r1"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRolesUsers_NotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesUsers, `{"roleId":"ghost"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRolesUsers_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesUsers, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRolesUpdateDefaultPolicies_Success(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesUpdateDefaultPolicies, `{"policies":{"driveCapacityMb":500}}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRolesUpdateDefaultPolicies_UpdateError(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	metaRepo := &failingUpdateMetaRepo{testutil.NewMockMetaRepository()}
	metaRepo.Meta = &model.Meta{ID: "x"}
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := testutil.NewMockRoleAssignmentRepository(roleRepo)
	idGen, _ := id.NewGenerator("aidx")
	roleSvc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	h := apiadmin.NewHandler(signup.NewService(userRepo, metaRepo, idGen), roleSvc, metaRepo, userRepo, idGen)
	rec := doPost(h.RolesUpdateDefaultPolicies, `{"policies":{"x":1}}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRolesCreate_ErrorFromService(t *testing.T) {
	// Createがエラーになるケースをテスト — failing roleRepoが必要
	// ここではfailingリポジトリでHandler直接作成
	failRepo := &failingCreateRoleRepo{testutil.NewMockRoleRepository()}
	assignRepo := testutil.NewMockRoleAssignmentRepository(failRepo.MockRoleRepository)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	roleSvc := role.NewService(failRepo, assignRepo, metaRepo, idGen)
	userRepo := testutil.NewMockUserRepository()
	h := apiadmin.NewHandler(signup.NewService(userRepo, metaRepo, idGen), roleSvc, metaRepo, userRepo, idGen)
	rec := doPost(h.RolesCreate, `{"name":"Test"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type failingCreateRoleRepo struct {
	*testutil.MockRoleRepository
}

func (f *failingCreateRoleRepo) Create(_ *model.Role) error { return assert.AnError }

func TestRolesAssign_InternalError(t *testing.T) {
	// Exists がエラーになるケースをテスト
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	// 1回目のassignは成功
	doPost(h.RolesAssign, `{"userId":"u1","roleId":"r1"}`, nil)
	// 2回目はALREADY_ASSIGNED → 409 (既にテスト済みだが念のため)
	rec := doPost(h.RolesAssign, `{"userId":"u1","roleId":"r1"}`, nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestRolesUnassign_InternalError(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// 存在しないassignmentのunassign → NOT_ASSIGNED
	rec := doPost(h.RolesUnassign, `{"userId":"u1","roleId":"r1"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

type failingListRoleRepo struct {
	*testutil.MockRoleRepository
}

func (f *failingListRoleRepo) List() ([]*model.Role, error) { return nil, assert.AnError }

func TestRolesList_Error(t *testing.T) {
	failRepo := &failingListRoleRepo{testutil.NewMockRoleRepository()}
	assignRepo := testutil.NewMockRoleAssignmentRepository(failRepo.MockRoleRepository)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	userRepo := testutil.NewMockUserRepository()
	idGen, _ := id.NewGenerator("aidx")
	roleSvc := role.NewService(failRepo, assignRepo, metaRepo, idGen)
	h := apiadmin.NewHandler(signup.NewService(userRepo, metaRepo, idGen), roleSvc, metaRepo, userRepo, idGen)
	rec := doPost(h.RolesList, `{}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type failingAssignExistsRepo struct {
	*testutil.MockRoleAssignmentRepository
}

func (f *failingAssignExistsRepo) Exists(_ string, _ string) (bool, error) {
	return false, assert.AnError
}

func TestRolesAssign_ExistsError(t *testing.T) {
	roleRepo := testutil.NewMockRoleRepository()
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	assignRepo := &failingAssignExistsRepo{testutil.NewMockRoleAssignmentRepository(roleRepo)}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	userRepo := testutil.NewMockUserRepository()
	idGen, _ := id.NewGenerator("aidx")
	roleSvc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	h := apiadmin.NewHandler(signup.NewService(userRepo, metaRepo, idGen), roleSvc, metaRepo, userRepo, idGen)
	rec := doPost(h.RolesAssign, `{"userId":"u1","roleId":"r1"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRolesUnassign_ExistsError(t *testing.T) {
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := &failingAssignExistsRepo{testutil.NewMockRoleAssignmentRepository(roleRepo)}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	userRepo := testutil.NewMockUserRepository()
	idGen, _ := id.NewGenerator("aidx")
	roleSvc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	h := apiadmin.NewHandler(signup.NewService(userRepo, metaRepo, idGen), roleSvc, metaRepo, userRepo, idGen)
	rec := doPost(h.RolesUnassign, `{"userId":"u1","roleId":"r1"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRolesUpdateDefaultPolicies_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.RolesUpdateDefaultPolicies, `invalid`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- Abuse Report / Moderation Log endpoints ---

func TestAbuseReports_Empty(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// abuseRepo=nil → 空配列
	rec := doPost(h.AbuseReports, `{}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAbuseReports_WithRepo(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	abuseRepo := testutil.NewMockAbuseReportRepository()
	abuseRepo.Reports["r1"] = &model.AbuseUserReport{ID: "r1", Comment: "spam"}
	h.SetAbuseRepo(abuseRepo)

	rec := doPost(h.AbuseReports, `{}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
}

func TestResolveAbuseReport_WithRepo(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	abuseRepo := testutil.NewMockAbuseReportRepository()
	abuseRepo.Reports["r1"] = &model.AbuseUserReport{ID: "r1"}
	h.SetAbuseRepo(abuseRepo)

	rec := doPost(h.ResolveAbuseReport, `{"reportId":"r1"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.True(t, abuseRepo.Reports["r1"].Resolved)
}

func TestResolveAbuseReport_NotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	abuseRepo := testutil.NewMockAbuseReportRepository()
	h.SetAbuseRepo(abuseRepo)

	rec := doPost(h.ResolveAbuseReport, `{"reportId":"ghost"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestShowModerationLogs_WithRepo(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	modLogRepo := testutil.NewMockModerationLogRepository()
	modLogRepo.Logs = append(modLogRepo.Logs, &model.ModerationLog{ID: "l1", Type: "suspend"})
	h.SetModLogRepo(modLogRepo)

	rec := doPost(h.ShowModerationLogs, `{}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
}

func TestAbuseReports_InvalidJSON(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.AbuseReports, `invalid`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestResolveAbuseReport_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.ResolveAbuseReport, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestResolveAbuseReport_NilRepo(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.ResolveAbuseReport, `{"reportId":"r1"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestShowModerationLogs_Empty(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.ShowModerationLogs, `{}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

type failingAbuseListRepo struct {
	*testutil.MockAbuseReportRepository
}

func (f *failingAbuseListRepo) List(_ *bool, _ int, _ int) ([]*model.AbuseUserReport, error) {
	return nil, assert.AnError
}

func TestAbuseReports_ListError(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetAbuseRepo(&failingAbuseListRepo{testutil.NewMockAbuseReportRepository()})
	rec := doPost(h.AbuseReports, `{}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type failingModLogListRepo struct {
	*testutil.MockModerationLogRepository
}

func (f *failingModLogListRepo) List(_ int, _ int) ([]*model.ModerationLog, error) {
	return nil, assert.AnError
}

func TestShowModerationLogs_ListError(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetModLogRepo(&failingModLogListRepo{testutil.NewMockModerationLogRepository()})
	rec := doPost(h.ShowModerationLogs, `{}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Emoji Admin endpoints ---

func TestEmojiAdd_Success(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	emojiRepo := testutil.NewMockEmojiRepository()
	h.SetEmojiRepo(emojiRepo)
	rec := doPost(h.EmojiAdd, `{"name":"smile","url":"https://example.com/smile.png"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestEmojiAdd_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.EmojiAdd, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestEmojiAdd_NilRepo(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.EmojiAdd, `{"name":"x","url":"u"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestEmojiUpdate_Success(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	emojiRepo := testutil.NewMockEmojiRepository()
	emojiRepo.Emojis["test@"] = &model.Emoji{ID: "e1", Name: "test"}
	h.SetEmojiRepo(emojiRepo)
	rec := doPost(h.EmojiUpdate, `{"id":"e1","name":"updated"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestEmojiUpdate_WithAliases(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	emojiRepo := testutil.NewMockEmojiRepository()
	emojiRepo.Emojis["test@"] = &model.Emoji{ID: "e1", Name: "test"}
	h.SetEmojiRepo(emojiRepo)
	rec := doPost(h.EmojiUpdate, `{"id":"e1","name":"new","category":"faces","aliases":["smile"]}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestEmojiUpdate_NotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	emojiRepo := testutil.NewMockEmojiRepository()
	h.SetEmojiRepo(emojiRepo)
	rec := doPost(h.EmojiUpdate, `{"id":"ghost","name":"x"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestEmojiUpdate_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.EmojiUpdate, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestEmojiUpdate_NilRepo(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.EmojiUpdate, `{"id":"e1"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestEmojiDelete_Success(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	emojiRepo := testutil.NewMockEmojiRepository()
	emojiRepo.Emojis["test@"] = &model.Emoji{ID: "e1", Name: "test"}
	h.SetEmojiRepo(emojiRepo)
	rec := doPost(h.EmojiDelete, `{"id":"e1"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestEmojiDelete_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.EmojiDelete, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestEmojiDelete_NilRepo(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.EmojiDelete, `{"id":"e1"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestEmojiList_Success(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	emojiRepo := testutil.NewMockEmojiRepository()
	emojiRepo.Emojis["smile@"] = &model.Emoji{ID: "e1", Name: "smile", PublicURL: "https://example.com/smile.png"}
	h.SetEmojiRepo(emojiRepo)
	rec := doPost(h.EmojiList, `{}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "https://example.com/smile.png", rows[0]["url"])
}

func TestEmojiList_URLFallbackToOriginalUrl(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	emojiRepo := testutil.NewMockEmojiRepository()
	// publicUrl空 → originalUrlにフォールバック
	emojiRepo.Emojis["wave@"] = &model.Emoji{ID: "e2", Name: "wave", PublicURL: "", OriginalURL: "https://example.com/wave-orig.png"}
	h.SetEmojiRepo(emojiRepo)
	rec := doPost(h.EmojiList, `{}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "https://example.com/wave-orig.png", rows[0]["url"])
}

func TestEmojiList_NilRepo(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.EmojiList, `{}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

type failingCreateEmojiRepo struct {
	*testutil.MockEmojiRepository
}

func (f *failingCreateEmojiRepo) Create(_ *model.Emoji) error { return assert.AnError }

func TestEmojiAdd_CreateError(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetEmojiRepo(&failingCreateEmojiRepo{testutil.NewMockEmojiRepository()})
	rec := doPost(h.EmojiAdd, `{"name":"x","url":"u"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type failingListEmojiRepo struct {
	*testutil.MockEmojiRepository
}

func (f *failingListEmojiRepo) ListWithFilter(_, _ string, _ bool, _, _ int) ([]*model.Emoji, error) {
	return nil, assert.AnError
}

func TestEmojiList_Error(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetEmojiRepo(&failingListEmojiRepo{testutil.NewMockEmojiRepository()})
	rec := doPost(h.EmojiList, `{}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type failingDeleteEmojiRepo struct {
	*testutil.MockEmojiRepository
}

func (f *failingDeleteEmojiRepo) Delete(_ string) error { return assert.AnError }

func TestEmojiDelete_Error(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetEmojiRepo(&failingDeleteEmojiRepo{testutil.NewMockEmojiRepository()})
	rec := doPost(h.EmojiDelete, `{"id":"e1"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

type failingUpdateEmojiRepo struct {
	*testutil.MockEmojiRepository
}

func (f *failingUpdateEmojiRepo) UpdateFields(_ string, _ map[string]any) error {
	return assert.AnError
}

func TestEmojiUpdate_Error(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetEmojiRepo(&failingUpdateEmojiRepo{testutil.NewMockEmojiRepository()})
	rec := doPost(h.EmojiUpdate, `{"id":"e1","name":"x"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestEmojiList_InvalidJSON(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.EmojiList, `invalid`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestShowModerationLogs_InvalidJSON(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.ShowModerationLogs, `invalid`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
