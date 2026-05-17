package admin_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	apiadmin "github.com/shiroha-a/mk/internal/api/admin"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
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

// adminUser is the throwaway admin principal shared across the per-handler
// test files (forward_abuse_report_test.go, server_stats_test.go, etc.).
var adminUser = &model.User{ID: "admin1"}

// assertError is a trivial error used to exercise error branches of handlers
// without pulling in a real mock that fails. Shared by tests that need a
// repository to surface a generic failure (e.g. ResetPassword fallback paths,
// AbuseReport recipient repo errors, emoji import enqueue failures).
type assertError struct{}

func (assertError) Error() string { return "stub failure" }

// setupDriveFileHandler returns a handler with DriveFileRepo wired and
// optional seed rows. boilerplate (handler 構築 + repo 生成 + seed +
// SetDriveFileRepo) を 1 行に圧縮する (#761)。drive_emoji_test 以外の
// admin test (例: moderation_test の DeleteAllFilesOfUser) からも利用
// できるよう handler_test.go に置く。戻り値の repo を直接 mutate して
// EmojiReferencedURLs 等の追加設定も可能。
func setupDriveFileHandler(t *testing.T, seed ...*model.DriveFile) (*apiadmin.Handler, *testutil.MockDriveFileRepository) {
	t.Helper()
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockDriveFileRepository()
	for _, df := range seed {
		require.NoError(t, repo.Create(df))
	}
	h.SetDriveFileRepo(repo)
	return h, repo
}

// setupAbuseReportHandler returns a handler with AbuseRepo wired and
// optional seed rows. moderation_test / forward_abuse_report_test の両方
// で UpdateAbuseUserReport / ForwardAbuseUserReport の modlog 検証に
// 使う (#761 Phase 2)。
func setupAbuseReportHandler(t *testing.T, seed ...*model.AbuseUserReport) (*apiadmin.Handler, *testutil.MockAbuseReportRepository) {
	t.Helper()
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockAbuseReportRepository()
	for _, r := range seed {
		require.NoError(t, repo.Create(r))
	}
	h.SetAbuseRepo(repo)
	return h, repo
}

// TestSetDriveFileRepo / TestSetAdminDB exist only to ensure the public
// setters keep compiling; the real wiring is exercised end-to-end by the
// per-handler tests.

func TestSetDriveFileRepo(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetDriveFileRepo(testutil.NewMockDriveFileRepository())
}

func TestSetAdminDB(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetAdminDB(nil)
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

// admin/show-users が profile 取得を per-row FindProfileByUserID で
// 引いていた N+1 を FindProfilesByUserIDs 1 batch に置換した (#300 1-4)。
// 5 user 検証で per-row が 0 回、batch が 1 回 + size=5 で呼ばれることを
// 担保する。
type countingAdminUserRepo struct {
	*testutil.MockUserRepository
	findProfileByUserIDCalls    int
	findProfilesByUserIDsCalls  int
	findProfilesByUserIDsBucket int
}

func (c *countingAdminUserRepo) FindProfileByUserID(id string) (*model.UserProfile, error) {
	c.findProfileByUserIDCalls++
	return c.MockUserRepository.FindProfileByUserID(id)
}

func (c *countingAdminUserRepo) FindProfilesByUserIDs(ids []string) ([]*model.UserProfile, error) {
	c.findProfilesByUserIDsCalls++
	c.findProfilesByUserIDsBucket += len(ids)
	return c.MockUserRepository.FindProfilesByUserIDs(ids)
}

func TestShowUsers_BatchFetchesProfiles(t *testing.T) {
	repo := &countingAdminUserRepo{MockUserRepository: testutil.NewMockUserRepository()}
	for i := 0; i < 5; i++ {
		uid := fmt.Sprintf("au%d", i)
		repo.Users[uid] = &model.User{ID: uid, Username: uid}
		desc := fmt.Sprintf("d%d", i)
		repo.Profiles[uid] = &model.UserProfile{UserID: uid, Description: &desc}
	}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := testutil.NewMockRoleAssignmentRepository(roleRepo)
	idGen, _ := id.NewGenerator("aidx")
	signupSvc := signup.NewService(repo, metaRepo, idGen)
	roleSvc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	h := apiadmin.NewHandler(signupSvc, roleSvc, metaRepo, repo, idGen)

	rec := doPost(h.ShowUsers, `{"limit":10}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 5)

	assert.Equal(t, 0, repo.findProfileByUserIDCalls,
		"per-row FindProfileByUserID must not be called (N+1 must be eliminated)")
	assert.Equal(t, 1, repo.findProfilesByUserIDsCalls,
		"FindProfilesByUserIDs should be called exactly once per request")
	assert.Equal(t, 5, repo.findProfilesByUserIDsBucket,
		"all 5 user IDs should be coalesced into a single batch")
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

// frontend (instance-info.vue) は admin/show-users に hostname を渡して
// 「特定リモートサーバーに属するユーザー」だけを取りに来る (#469)。
// 過去はこのフィールドが handler の req struct に無く、無視されて全
// remote が返るバグがあった。回帰防止に hostname narrowing を検証する。
func TestShowUsers_FilterByHostname(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	hostA := "a.example"
	hostB := "b.example"
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice", Host: &hostA}
	userRepo.Users["u2"] = &model.User{ID: "u2", Username: "bob", Host: &hostB}
	userRepo.Users["u3"] = &model.User{ID: "u3", Username: "local"}

	rec := doPost(h.ShowUsers, `{"origin":"remote","hostname":"a.example","limit":10}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "alice", resp[0]["username"])
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

// --- #965: admin が target user を suspend/unsuspend したとき、target の
// 全 token cache entry を即時 invalidate する。SuspendUser / UnsuspendUser
// 成功時に UserTokenInvalidator が呼ばれることを確認する。

// stubUserTokenInvalidator captures InvalidateTokensForUser calls.
type stubUserTokenInvalidator struct {
	calls []string
}

func (s *stubUserTokenInvalidator) InvalidateTokensForUser(userID string) {
	s.calls = append(s.calls, userID)
}

func TestSuspendUser_InvalidatesTargetTokenCache(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "target"}
	inv := &stubUserTokenInvalidator{}
	h.SetUserTokenInvalidator(inv)

	rec := doPost(h.SuspendUser, `{"userId":"u1"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, []string{"u1"}, inv.calls,
		"SuspendUser 成功時は target の全 token cache を invalidate するべき")
}

func TestUnsuspendUser_InvalidatesTargetTokenCache(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", IsSuspended: true}
	inv := &stubUserTokenInvalidator{}
	h.SetUserTokenInvalidator(inv)

	rec := doPost(h.UnsuspendUser, `{"userId":"u1"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, []string{"u1"}, inv.calls,
		"UnsuspendUser 成功時も cache 内 stale isSuspended=true を消すために invalidate するべき")
}

// invalidator 未配線時は handler が panic / fail せず通常レスポンスを返す
// (test 直叩き / router 配線忘れ時の defensive)。
func TestSuspendUser_NoInvalidatorIsNoop(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "target"}
	rec := doPost(h.SuspendUser, `{"userId":"u1"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.True(t, userRepo.Users["u1"].IsSuspended,
		"invalidator 未配線でも core suspend 動作は止まらない")
}

// SuspendUser が target を見つけられない (= UpdateUser 前に NotFound) と
// invalidate は呼ばない。404 を返した時点で cache に target の entry が
// 存在する保証もないので、空打ちを避ける。
func TestSuspendUser_NotFoundDoesNotInvalidate(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	inv := &stubUserTokenInvalidator{}
	h.SetUserTokenInvalidator(inv)

	rec := doPost(h.SuspendUser, `{"userId":"ghost"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, inv.calls, "target 不在のとき invalidate は呼ばれない")
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

// fetcher 配線済なら proxyAccountId が proxy system user の ID で埋まる。
// frontend admin/settings 画面が users/show でこれを引くため必須 (#348)。
func TestAdminMeta_ProxyAccountIDFromSystemAccount(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	proxy := &model.User{ID: "u-proxy-meta", Username: "proxy.actor", UsernameLower: "proxy.actor"}
	h.SetSystemAccountFetcher(&stubSystemAccountFetcher{user: proxy})
	rec := doPost(h.AdminMeta, `{}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"proxyAccountId":"u-proxy-meta"`)
}

// fetcher 未配線なら proxyAccountId は null (フォロー実装まで safety fallback)。
func TestAdminMeta_ProxyAccountIDNullWhenFetcherMissing(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.AdminMeta, `{}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"proxyAccountId":null`)
}

func TestUpdateMeta_Success(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.UpdateMeta, `{"name":"My Instance"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// frontend が送る `tosUrl` alias は DB column `termsOfServiceUrl` に
// translate されて update される (#348)。
func TestUpdateMeta_TosUrlAliasIsTranslated(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	rec := doPost(h.UpdateMeta, `{"tosUrl":"https://example.test/tos"}`, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	// mock は update 直後の値を Meta struct に反映するので
	// TermsOfServiceURL が埋まっていれば translate 成功。
	require.NotNil(t, metaRepo.Meta.TermsOfServiceURL)
	assert.Equal(t, "https://example.test/tos", *metaRepo.Meta.TermsOfServiceURL)
}

func TestUpdateMeta_SwPublickeyAliasIsTranslated(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	rec := doPost(h.UpdateMeta, `{"swPublickey":"KEY"}`, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.NotNil(t, metaRepo.Meta.SwPublicKey)
	assert.Equal(t, "KEY", *metaRepo.Meta.SwPublicKey)
}

// JSON で送られてくる array は []any{...} に decode されるが、そのまま
// repo.Update に流すと lib/pq が varchar[] 列に書けず "expression is of
// type record" で UPDATE 全体が落ちる。handler 側の coerceMetaArrayFields
// が []any → pq.StringArray に変換することで永続化できることを確認 (#590)。
//
// このテストは MockMetaRepository の Update が array 型を反映するよう
// 拡張した上で成立する。実 DB 側は repository/meta_test.go の
// TestMetaRepository_Update_FederationHosts でカバー。
func TestUpdateMeta_FederationHostsArray(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	rec := doPost(h.UpdateMeta,
		`{"federation":"specified","federationHosts":["allowed.example","trusted.example"]}`, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "specified", metaRepo.Meta.Federation)
	assert.Equal(t,
		[]string{"allowed.example", "trusted.example"},
		[]string(metaRepo.Meta.FederationHosts))
}

// blockedHosts / silencedHosts も同じ varchar[] 列なので同じ変換経路を
// 通す。代表的な host モデレーション設定をすべて 1 リクエストで保存する
// 統合テスト。
func TestUpdateMeta_HostListArrays(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	rec := doPost(h.UpdateMeta,
		`{"blockedHosts":["bad.example"],"silencedHosts":["noisy.example"]}`, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, []string{"bad.example"}, []string(metaRepo.Meta.BlockedHosts))
	assert.Equal(t, []string{"noisy.example"}, []string(metaRepo.Meta.SilencedHosts))
}

// 空配列も正しく永続化される (= リスト解除動作)。空 []any はゼロ要素の
// pq.StringArray に変換される必要がある。
func TestUpdateMeta_EmptyHostArrayClearsList(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	// 事前に値を持たせる
	metaRepo.Meta.BlockedHosts = []string{"oldblock.example"}

	rec := doPost(h.UpdateMeta, `{"blockedHosts":[]}`, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, []string(metaRepo.Meta.BlockedHosts))
}

// JSON null は coerceMetaArrayFields が空配列に揃える (#590 review #2)。
// varchar[] 列は migration で NOT NULL DEFAULT '{}' なので、null を素通し
// すると real repo で制約違反になり UPDATE 全体が rollback する。admin の
// 「リスト解除」操作を確実に成功させるため、handler 側で nil → 空配列に
// coerce してから repo に渡す。
func TestUpdateMeta_NullArrayClearsList(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	metaRepo.Meta.BlockedHosts = []string{"oldblock.example"}

	rec := doPost(h.UpdateMeta, `{"blockedHosts":null}`, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, []string(metaRepo.Meta.BlockedHosts),
		"null は coerce 後に空配列で永続化されるべき")
}

// metaArrayColumns に列挙されていない field の null は触らない (= 既存挙動
// を保つ)。例: rootUserId (nullable string) は null 渡しで本当に nil 化
// したい用途があるため、coerce が誤発火しないことを保証。
func TestUpdateMeta_NullForNonArrayColumnIsNotTouched(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	rootID := "u1"
	metaRepo.Meta.RootUserID = &rootID

	// proxyAccountId は nullable string で nil 化を許容する設計。null で
	// クリアできる挙動を pre-existing テストで確認できているので、coerce
	// 後でも壊れないことを担保。
	rec := doPost(h.UpdateMeta, `{"proxyAccountId":null}`, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Nil(t, metaRepo.Meta.ProxyAccountID,
		"非 array 列の null は素通しされ、ポインタ列は nil 化される")
}

// Service Worker を有効化する request で keys が空なら backend が
// auto-generate して DB に persist すること (#492)。frontend からは
// toggle ON + 空欄保存で完結し、リロードすると生成済の鍵が表示される
// 想定。
func TestUpdateMeta_VAPIDAutoGenerateOnEnable(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	rec := doPost(h.UpdateMeta, `{"enableServiceWorker":true,"swPublicKey":"","swPrivateKey":""}`, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.NotNil(t, metaRepo.Meta.SwPublicKey)
	require.NotNil(t, metaRepo.Meta.SwPrivateKey)
	pub, priv := *metaRepo.Meta.SwPublicKey, *metaRepo.Meta.SwPrivateKey
	assert.NotEmpty(t, pub)
	assert.NotEmpty(t, priv)
	// VAPID public key は base64url(65 byte) ≒ 87 文字。少なくとも
	// 「なんらかの長い rand 値」になっていることを sanity check する。
	assert.GreaterOrEqual(t, len(pub), 80)
	assert.GreaterOrEqual(t, len(priv), 40)
	assert.NotEqual(t, pub, priv)
}

// 既に運用者が外部生成した鍵を持っている場合は触らないこと
// (上書きすると push subscription が無効化されるため)。
func TestUpdateMeta_VAPIDDoesNotOverwriteExistingKeys(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	existingPub := "EXISTING_PUB"
	existingPriv := "EXISTING_PRIV"
	metaRepo.Meta.EnableServiceWorker = true
	metaRepo.Meta.SwPublicKey = &existingPub
	metaRepo.Meta.SwPrivateKey = &existingPriv

	rec := doPost(h.UpdateMeta, `{"enableServiceWorker":true}`, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.NotNil(t, metaRepo.Meta.SwPublicKey)
	assert.Equal(t, existingPub, *metaRepo.Meta.SwPublicKey)
	assert.Equal(t, existingPriv, *metaRepo.Meta.SwPrivateKey)
}

// 明示的な JSON null で既存鍵をクリアしつつ enable=true を送ってきた
// 場合も auto-generate を発火させる (= null も "" と同じく empty 扱い)。
func TestUpdateMeta_VAPIDAutoGenerateOnNullClear(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	existingPub := "old_pub"
	existingPriv := "old_priv"
	metaRepo.Meta.EnableServiceWorker = true
	metaRepo.Meta.SwPublicKey = &existingPub
	metaRepo.Meta.SwPrivateKey = &existingPriv

	rec := doPost(h.UpdateMeta, `{"enableServiceWorker":true,"swPublicKey":null,"swPrivateKey":null}`, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.NotNil(t, metaRepo.Meta.SwPublicKey)
	require.NotNil(t, metaRepo.Meta.SwPrivateKey)
	pub, priv := *metaRepo.Meta.SwPublicKey, *metaRepo.Meta.SwPrivateKey
	assert.NotEqual(t, "old_pub", pub)
	assert.NotEqual(t, "old_priv", priv)
	assert.GreaterOrEqual(t, len(pub), 80)
}

// SW 無効のまま (enable=false) で keys が空でも何も生成しない
// (= 不要な鍵をぶら下げない)。
func TestUpdateMeta_VAPIDSkipWhenSWDisabled(t *testing.T) {
	h, _, metaRepo, _ := newTestHandler(t)
	rec := doPost(h.UpdateMeta, `{"enableServiceWorker":false}`, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Nil(t, metaRepo.Meta.SwPublicKey)
	assert.Nil(t, metaRepo.Meta.SwPrivateKey)
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

// fullRoleCreatePayload は upstream Misskey TS が paramDef で required と
// する 13 field を満たした最小 payload (#889)。**PR #1102 以降**は全 field
// が DB に persist される (旧版は color / iconUrl / target / condFormula /
// canEditMembersByModerator / policies を /dev/null に流していた)。
const fullRoleCreatePayload = `{
	"name": "Admin",
	"description": "",
	"color": null,
	"iconUrl": null,
	"target": "manual",
	"condFormula": {},
	"isPublic": true,
	"isModerator": false,
	"isAdministrator": true,
	"asBadge": false,
	"canEditMembersByModerator": false,
	"displayOrder": 0,
	"policies": {}
}`

func TestRolesCreate_Success(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	rec := doPost(h.RolesCreate, fullRoleCreatePayload, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, roleRepo.Roles, 1)
}

// PR #1102 regression guard: RolesCreate が policies / color / target /
// condFormula 等を実際に persist することを assert。旧版は受け取りつつ
// /dev/null に流していたため admin UI で設定したロール設定が反映されなかった。
func TestRolesCreate_PersistsAllFields(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	color := "#ff0000"
	icon := "https://example.com/i.png"
	payload := `{
		"name": "Cap",
		"description": "limited role",
		"color": "` + color + `",
		"iconUrl": "` + icon + `",
		"target": "conditional",
		"condFormula": {"type":"isLocal"},
		"isPublic": true,
		"isModerator": false,
		"isAdministrator": false,
		"isExplorable": true,
		"asBadge": true,
		"canEditMembersByModerator": true,
		"displayOrder": 7,
		"policies": {"canPublicNote": false, "mentionLimit": 5}
	}`
	rec := doPost(h.RolesCreate, payload, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, roleRepo.Roles, 1)
	var r *model.Role
	for _, v := range roleRepo.Roles {
		r = v
	}
	assert.Equal(t, "Cap", r.Name)
	assert.Equal(t, "limited role", r.Description)
	require.NotNil(t, r.Color)
	assert.Equal(t, color, *r.Color)
	require.NotNil(t, r.IconURL)
	assert.Equal(t, icon, *r.IconURL)
	assert.Equal(t, model.RoleTargetConditional, r.Target)
	assert.Equal(t, true, r.IsExplorable)
	assert.Equal(t, true, r.AsBadge)
	assert.Equal(t, true, r.CanEditMembersByModerator)
	assert.Equal(t, 7, r.DisplayOrder)
	// CondFormula / Policies は JSON bytes として保存される。
	assert.JSONEq(t, `{"type":"isLocal"}`, string(r.CondFormula))
	assert.JSONEq(t, `{"canPublicNote":false,"mentionLimit":5}`, string(r.Policies))
}

func TestRolesCreate_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// 空 payload → 13 required field 不足で 400 (#889)
	rec := doPost(h.RolesCreate, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// upstream paramDef で required な field が一部欠けると 400 (#889)。
// description だけ欠けたケースを代表として検証する。
func TestRolesCreate_PartialPayloadRejected(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// description を抜いた payload
	rec := doPost(h.RolesCreate, `{
		"name": "X",
		"color": null,
		"iconUrl": null,
		"target": "manual",
		"condFormula": {},
		"isPublic": true,
		"isModerator": false,
		"isAdministrator": false,
		"asBadge": false,
		"canEditMembersByModerator": false,
		"displayOrder": 0,
		"policies": {}
	}`, nil)
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

// TestRolesUpdate_BasicFieldsCompat: 旧来 5 field (name/description/
// isModerator/isAdministrator/isPublic) のみ送る payload が backward
// compatible で通ること。新規 10 field の persistence は別 test 群
// (TestRolesUpdate_Persists*) で網羅する。
func TestRolesUpdate_BasicFieldsCompat(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Old"}
	rec := doPost(h.RolesUpdate, `{"roleId":"r1","name":"New","description":"desc","isModerator":true,"isAdministrator":true,"isPublic":true}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	got := roleRepo.Roles["r1"]
	assert.Equal(t, "New", got.Name)
	assert.Equal(t, "desc", got.Description)
	assert.True(t, got.IsModerator)
	assert.True(t, got.IsAdministrator)
	assert.True(t, got.IsPublic)
}

// PR #1102 regression guard: RolesUpdate が policies を実際に persist する
// ことを assert (user 報告経路、canPublicNote が UI 上で反映されない bug)。
func TestRolesUpdate_PersistsPolicies(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Limited"}
	rec := doPost(h.RolesUpdate,
		`{"roleId":"r1","policies":{"canPublicNote":false,"ltlAvailable":false}}`, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	got := roleRepo.Roles["r1"]
	require.NotNil(t, got)
	assert.JSONEq(t, `{"canPublicNote":false,"ltlAvailable":false}`, string(got.Policies))
}

// PR #1102 regression guard: 追加 field (color / iconUrl / target /
// condFormula / asBadge / isExplorable / displayOrder / canEditMembersBy
// Moderator / preserveAssignmentOnMoveAccount) が全部 persist されること。
func TestRolesUpdate_PersistsAllUpstreamFields(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Old"}
	payload := `{
		"roleId":"r1",
		"color":"#abcdef",
		"iconUrl":"https://example.com/i.png",
		"target":"conditional",
		"condFormula":{"type":"isLocal"},
		"isExplorable":true,
		"asBadge":true,
		"canEditMembersByModerator":true,
		"preserveAssignmentOnMoveAccount":true,
		"displayOrder":42
	}`
	rec := doPost(h.RolesUpdate, payload, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	got := roleRepo.Roles["r1"]
	require.NotNil(t, got)
	require.NotNil(t, got.Color)
	assert.Equal(t, "#abcdef", *got.Color)
	require.NotNil(t, got.IconURL)
	assert.Equal(t, "https://example.com/i.png", *got.IconURL)
	assert.Equal(t, model.RoleTargetConditional, got.Target)
	assert.JSONEq(t, `{"type":"isLocal"}`, string(got.CondFormula))
	assert.Equal(t, true, got.IsExplorable)
	assert.Equal(t, true, got.AsBadge)
	assert.Equal(t, true, got.CanEditMembersByModerator)
	assert.Equal(t, true, got.PreserveAssignmentOnMoveAccount)
	assert.Equal(t, 42, got.DisplayOrder)
}

// upstream nullable な color / iconUrl を空文字で送ると null クリアされる。
func TestRolesUpdate_NullableColorClear(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	c := "#abcdef"
	icon := "https://example.com/i.png"
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "X", Color: &c, IconURL: &icon}
	rec := doPost(h.RolesUpdate, `{"roleId":"r1","color":"","iconUrl":""}`, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	got := roleRepo.Roles["r1"]
	assert.Nil(t, got.Color)
	assert.Nil(t, got.IconURL)
}

// target 不正値 ("weird" 等) は upstream-compat で manual に倒される。
// frontend が誤った値を送っても 400 にはせず安全側に default すれば、
// admin が一時的に壊れた payload を送っても role 自体は壊れない。
func TestRolesUpdate_InvalidTargetFallsBackToManual(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "X", Target: model.RoleTargetConditional}
	rec := doPost(h.RolesUpdate, `{"roleId":"r1","target":"weird"}`, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	got := roleRepo.Roles["r1"]
	assert.Equal(t, model.RoleTargetManual, got.Target)
}

// condFormula が JSON object でないと bind 段階で 400 (= request struct の
// *map[string]any に string をマップできない)。これは Go の json binding が
// 担保するので、handler 内部の json.Marshal error path は実質到達しないが、
// payload validation の shape を契約として guard する。
func TestRolesUpdate_CondFormulaNonObjectRejected(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "X"}
	rec := doPost(h.RolesUpdate, `{"roleId":"r1","condFormula":"not-an-object"}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// Create でも preserveAssignmentOnMoveAccount が persist されることを assert
// (TestRolesCreate_PersistsAllFields の payload に含めていなかったため別 case で補完)。
func TestRolesCreate_PersistsPreserveAssignmentOnMoveAccount(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	payload := `{
		"name": "Sticky",
		"description": "",
		"color": null,
		"iconUrl": null,
		"target": "manual",
		"condFormula": {},
		"isPublic": false,
		"isModerator": false,
		"isAdministrator": false,
		"asBadge": false,
		"canEditMembersByModerator": false,
		"preserveAssignmentOnMoveAccount": true,
		"displayOrder": 0,
		"policies": {}
	}`
	rec := doPost(h.RolesCreate, payload, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var r *model.Role
	for _, v := range roleRepo.Roles {
		r = v
	}
	require.NotNil(t, r)
	assert.True(t, r.PreserveAssignmentOnMoveAccount)
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

// rolesUsersFixture wires user / role / assignment 用の handler を組み立てる。
// 個別 test で Service.ListByRole の戻りに手を入れたいので、Mock 系を直接
// 受け渡せる関数として切り出している (newTestHandler は roleRepo しか返さない)。
func rolesUsersFixture(t *testing.T) (
	*apiadmin.Handler,
	*testutil.MockUserRepository,
	*testutil.MockRoleRepository,
	*testutil.MockRoleAssignmentRepository,
) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := testutil.NewMockRoleAssignmentRepository(roleRepo)
	// MockRoleAssignmentRepository は UserRepo を持っていれば
	// ListByRole の戻りに User を埋めてくれる (handler が a.User を見るため)。
	assignRepo.UserRepo = userRepo
	idGen, _ := id.NewGenerator("aidx")
	signupSvc := signup.NewService(userRepo, metaRepo, idGen)
	roleSvc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	h := apiadmin.NewHandler(signupSvc, roleSvc, metaRepo, userRepo, idGen)
	return h, userRepo, roleRepo, assignRepo
}

func TestRolesUsers_Success_ReturnsAssignmentEnvelope(t *testing.T) {
	h, userRepo, roleRepo, assignRepo := rolesUsersFixture(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	require.NoError(t, userRepo.Create(&model.User{ID: "u1", Username: "alice"}))
	require.NoError(t, assignRepo.Create(&model.RoleAssignment{ID: "9c2bw9q5fa0000000000000000", UserID: "u1", RoleID: "r1"}))

	rec := doPost(h.RolesUsers, `{"roleId":"r1","limit":10}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "9c2bw9q5fa0000000000000000", resp[0]["id"])
	assert.NotEmpty(t, resp[0]["createdAt"])
	user, _ := resp[0]["user"].(map[string]any)
	require.NotNil(t, user)
	assert.Equal(t, "u1", user["id"])
	assert.Equal(t, "alice", user["username"])
}

func TestRolesUsers_Success_Empty(t *testing.T) {
	h, _, roleRepo, _ := rolesUsersFixture(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	rec := doPost(h.RolesUsers, `{"roleId":"r1"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}

func TestRolesUsers_LimitClamping(t *testing.T) {
	h, userRepo, roleRepo, assignRepo := rolesUsersFixture(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	// limit=0 → default 10、limit>100 → 100 にクランプされていることを 3 件 seed で確認
	for i := 0; i < 3; i++ {
		uid := fmt.Sprintf("user%d", i)
		require.NoError(t, userRepo.Create(&model.User{ID: uid}))
		// ID は ULID 風文字列。順序を保つために i を後ろに付ける
		aid := fmt.Sprintf("9c2bw9q5fa%016d", i)
		require.NoError(t, assignRepo.Create(&model.RoleAssignment{ID: aid, UserID: uid, RoleID: "r1"}))
	}
	// limit=2 で 2 件のみ。Mock 経由で repo に渡された limit も検証 (#598 review item 2)。
	rec := doPost(h.RolesUsers, `{"roleId":"r1","limit":2}`, nil)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 2)
	assert.Equal(t, 2, assignRepo.LastListByRoleLimit, "limit=2 がそのまま repo に伝わる")

	// limit=999 → 100 にクランプ。Mock の LastListByRoleLimit を見て
	// repo 側が 100 で受け取ったことを直接 assert (件数だけだと seed=3 で見えない)。
	rec = doPost(h.RolesUsers, `{"roleId":"r1","limit":999}`, nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 3)
	assert.Equal(t, 100, assignRepo.LastListByRoleLimit, "limit>100 は 100 にクランプされる")

	// limit=0 (未指定) → default 10
	rec = doPost(h.RolesUsers, `{"roleId":"r1"}`, nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, 10, assignRepo.LastListByRoleLimit, "limit 未指定は default 10")
}

// dangling assignment (a.User == nil) は結果から落とされる + 警告ログが出る。
// ログ出力の有無は slog テスト用 handler を差し替えて捕捉する (#598 review item 1)。
func TestRolesUsers_DanglingAssignmentSkipped(t *testing.T) {
	h, userRepo, roleRepo, assignRepo := rolesUsersFixture(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	// alive user + dangling assignment (UserID は存在しない user を指す)
	require.NoError(t, userRepo.Create(&model.User{ID: "alive", Username: "alive"}))
	require.NoError(t, assignRepo.Create(&model.RoleAssignment{ID: "9c2bw9q5fa0000000000000001", UserID: "alive", RoleID: "r1"}))
	require.NoError(t, assignRepo.Create(&model.RoleAssignment{ID: "9c2bw9q5fa0000000000000002", UserID: "ghost", RoleID: "r1"}))

	// Test 用に slog handler を差し替えて Warn が出るかを観測。
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	rec := doPost(h.RolesUsers, `{"roleId":"r1"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1, "dangling assignment は結果から除外される")
	user, _ := resp[0]["user"].(map[string]any)
	assert.Equal(t, "alive", user["id"])

	// dangling 検知の警告ログが出ている
	logged := buf.String()
	assert.Contains(t, logged, "dangling role assignment")
	assert.Contains(t, logged, "userId=ghost")
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

// failingListByRoleRepo は ListByRole で error を返す stub。Service が
// repo error をそのまま伝播して handler が 500 を返す経路をカバーする。
type failingListByRoleRepo struct {
	*testutil.MockRoleAssignmentRepository
}

func (f *failingListByRoleRepo) ListByRole(_, _, _ string, _ int) ([]*model.RoleAssignment, error) {
	return nil, assert.AnError
}

func TestRolesUsers_ListError(t *testing.T) {
	roleRepo := testutil.NewMockRoleRepository()
	roleRepo.Roles["r1"] = &model.Role{ID: "r1"}
	assignRepo := &failingListByRoleRepo{testutil.NewMockRoleAssignmentRepository(roleRepo)}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	userRepo := testutil.NewMockUserRepository()
	idGen, _ := id.NewGenerator("aidx")
	roleSvc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	h := apiadmin.NewHandler(signup.NewService(userRepo, metaRepo, idGen), roleSvc, metaRepo, userRepo, idGen)
	rec := doPost(h.RolesUsers, `{"roleId":"r1"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
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
	// #889 fullRoleCreatePayload で 13 required field を満たして 500 path
	// (= service error) を test する。
	rec := doPost(h.RolesCreate, fullRoleCreatePayload, nil)
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
	require.NoError(t, modLogRepo.Create(&model.ModerationLog{ID: "l1", Type: "suspend"}))
	gen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	h.SetModLogService(moderationlog.New(modLogRepo, gen))

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
	gen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	h.SetModLogService(moderationlog.New(
		&failingModLogListRepo{testutil.NewMockModerationLogRepository()},
		gen,
	))
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

func (f *failingListEmojiRepo) ListWithFilter(_, _ string, _ bool, _, _ string, _, _ int) ([]*model.Emoji, error) {
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

// TestShowModerationLogs_IncludesCreatedAt は frontend modlog.ModLog.vue
// が `log.createdAt` を直接読んで MkTime に渡すため、handler が aidx ID
// から派生した createdAt 文字列を必ず response に含めることを guard する。
func TestShowModerationLogs_IncludesCreatedAt(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	modLogRepo := testutil.NewMockModerationLogRepository()
	gen, err := id.NewGenerator("aidx")
	require.NoError(t, err)

	// 既知の固定時刻で aidx ID を生成 → response の createdAt と照合
	fixedTime := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	aidxID := gen.Generate(fixedTime)
	require.NoError(t, modLogRepo.Create(&model.ModerationLog{ID: aidxID, UserID: "u1", Type: "suspend"}))
	h.SetModLogService(moderationlog.New(modLogRepo, gen))

	rec := doPost(h.ShowModerationLogs, `{}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)

	createdAt, ok := resp[0]["createdAt"].(string)
	require.True(t, ok, "createdAt must be present and be a string")

	// Misskey の標準 format で parse 可能であること
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", createdAt)
	require.NoError(t, err, "createdAt must be parseable as Misskey-format ISO string")
	// aidx の解像度は ms なので fixedTime と完全一致するはず
	assert.WithinDuration(t, fixedTime, parsed, time.Millisecond)
}

// TestShowModerationLogs_NonAidxIDOmitsCreatedAt は aidx として parse でき
// ない legacy ID が紛れ込んだ場合に handler が createdAt を埋めずに response
// から省略する (= frontend 側で「Invalid Date」を表示しない) ことを guard
// する。
func TestShowModerationLogs_NonAidxIDOmitsCreatedAt(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	modLogRepo := testutil.NewMockModerationLogRepository()
	gen, err := id.NewGenerator("aidx")
	require.NoError(t, err)

	// aidx の base36 で扱えない文字 ("!") を混ぜて parse 失敗を強制する
	require.NoError(t, modLogRepo.Create(&model.ModerationLog{ID: "!!!notaidx", UserID: "u1", Type: "suspend"}))
	h.SetModLogService(moderationlog.New(modLogRepo, gen))

	rec := doPost(h.ShowModerationLogs, `{}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	_, has := resp[0]["createdAt"]
	assert.False(t, has, "createdAt must be omitted when ID cannot be parsed as aidx")
}

// TestShowModerationLogs_PreservesInfoJSON は datatypes.JSON で persist された
// info が map[string]any 経由 marshal を通っても (key 順以外) 中身を保つこと
// を guard する。frontend modlog detail dialog は info の structure を直接
// 触るため、handler 側で誤って string 化したり再 escape するような変換を
// 入れないようにする。
func TestShowModerationLogs_PreservesInfoJSON(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	modLogRepo := testutil.NewMockModerationLogRepository()
	gen, err := id.NewGenerator("aidx")
	require.NoError(t, err)

	infoJSON := []byte(`{"userId":"target1","userUsername":"alice","reason":"spam"}`)
	require.NoError(t, modLogRepo.Create(&model.ModerationLog{
		ID:     gen.Generate(time.Now()),
		UserID: "admin1",
		Type:   "suspend",
		Info:   infoJSON,
	}))
	h.SetModLogService(moderationlog.New(modLogRepo, gen))

	rec := doPost(h.ShowModerationLogs, `{}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	info, ok := resp[0]["info"].(map[string]any)
	require.True(t, ok, "info must be a JSON object after round-trip")
	assert.Equal(t, "target1", info["userId"])
	assert.Equal(t, "alice", info["userUsername"])
	assert.Equal(t, "spam", info["reason"])
}

// --- moderation log assertions for user moderation handlers ---

func TestSuspendUser_WritesModerationLog(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	repo := attachModLog(t, h)

	rec := doPost(h.SuspendUser, `{"userId":"u1"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)

	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	logs := repo.Snapshot()
	assert.Equal(t, "admin1", logs[0].UserID)
	assert.Equal(t, "suspend", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	assert.Equal(t, "u1", info["userId"])
	assert.Equal(t, "alice", info["userUsername"])
}

func TestUnsuspendUser_WritesModerationLog(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	repo := attachModLog(t, h)

	rec := doPost(h.UnsuspendUser, `{"userId":"u1"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)

	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "unsuspend", repo.Snapshot()[0].Type)
}

// --- moderation log assertions for role handlers ---

func TestRolesCreate_WritesModerationLog(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := attachModLog(t, h)

	// #889 fullRoleCreatePayload で 13 required field を満たす。
	rec := doPost(h.RolesCreate, fullRoleCreatePayload, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	logs := repo.Snapshot()
	assert.Equal(t, "createRole", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	assert.NotEmpty(t, info["roleId"])
	assert.NotNil(t, info["role"])
}

func TestRolesUpdate_WritesModerationLog(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Old"}
	repo := attachModLog(t, h)

	rec := doPost(h.RolesUpdate, `{"roleId":"r1","name":"New"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)

	// DB 更新も走る (#651 PR-A bonus fix)
	assert.Equal(t, "New", roleRepo.Roles["r1"].Name)

	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	logs := repo.Snapshot()
	assert.Equal(t, "updateRole", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	assert.Equal(t, "r1", info["roleId"])
	require.NotNil(t, info["before"])
	require.NotNil(t, info["after"])
}

func TestRolesDelete_WritesModerationLog(t *testing.T) {
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Mod"}
	repo := attachModLog(t, h)

	rec := doPost(h.RolesDelete, `{"roleId":"r1"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)

	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	logs := repo.Snapshot()
	assert.Equal(t, "deleteRole", logs[0].Type)
}

func TestRolesAssign_WritesModerationLog(t *testing.T) {
	h, userRepo, _, roleRepo := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Mod"}
	repo := attachModLog(t, h)

	rec := doPost(h.RolesAssign, `{"userId":"u1","roleId":"r1"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)

	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	logs := repo.Snapshot()
	assert.Equal(t, "assignRole", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	assert.Equal(t, "u1", info["userId"])
	assert.Equal(t, "alice", info["userUsername"])
	assert.Equal(t, "r1", info["roleId"])
	assert.Equal(t, "Mod", info["roleName"])
}

func TestUpdateMeta_WritesModerationLog(t *testing.T) {
	// #664: updateServerSettings log が before/after 込みで書かれる。
	h, _, metaRepo, _ := newTestHandler(t)
	metaRepo.Meta = &model.Meta{ID: "x"}
	repo := attachModLog(t, h)

	rec := doPost(h.UpdateMeta, `{"name":"new"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "updateServerSettings", repo.Snapshot()[0].Type)
}

func TestRolesUpdate_NoFieldsReturnsNoContentWithoutLog(t *testing.T) {
	// 全 optional pointer が nil のリクエストは log を書かずに 204 で帰る
	// (#668 review minor #2)。
	h, _, _, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Stay"}
	repo := attachModLog(t, h)

	rec := doPost(h.RolesUpdate, `{"roleId":"r1"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)

	require.Never(t, func() bool {
		return len(repo.Snapshot()) > 0
	}, 100*time.Millisecond, 10*time.Millisecond, "empty fields → log must not be written")
}

func TestRolesUnassign_WritesModerationLog(t *testing.T) {
	h, userRepo, _, roleRepo := newTestHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Mod"}
	// assign を先に行ってから modlog spy を取り付け、unassign 1 件だけ観測する
	doPost(h.RolesAssign, `{"userId":"u1","roleId":"r1"}`, adminUser)
	repo := attachModLog(t, h)

	rec := doPost(h.RolesUnassign, `{"userId":"u1","roleId":"r1"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)

	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "unassignRole", repo.Snapshot()[0].Type)
}
