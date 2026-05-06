package signup_test

import (
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService(t *testing.T) (*signup.Service, *testutil.MockUserRepository, *testutil.MockMetaRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	svc := signup.NewService(userRepo, metaRepo, idGen)
	return svc, userRepo, metaRepo
}

func TestSignup_Success(t *testing.T) {
	svc, userRepo, _ := newTestService(t)
	result, err := svc.Signup("testuser", "password123", false)
	require.NoError(t, err)
	assert.Equal(t, "testuser", result.User.Username)
	assert.NotEmpty(t, result.Token)
	assert.Len(t, userRepo.Users, 1)
	assert.Len(t, userRepo.Profiles, 1)

	// パスワードがハッシュ化されている
	profile := userRepo.Profiles[result.User.ID]
	assert.NotNil(t, profile.Password)
	assert.NotEqual(t, "password123", *profile.Password)
}

func TestSignup_InitialSetup_SetsRootUser(t *testing.T) {
	svc, _, metaRepo := newTestService(t)
	result, err := svc.Signup("admin", "pass", true)
	require.NoError(t, err)

	// rootUserId が設定される
	assert.NotNil(t, metaRepo.Meta.RootUserID)
	assert.Equal(t, result.User.ID, *metaRepo.Meta.RootUserID)
}

func TestSignup_NotInitialSetup_DoesNotSetRootUser(t *testing.T) {
	svc, _, metaRepo := newTestService(t)
	_, err := svc.Signup("user1", "pass", false)
	require.NoError(t, err)
	assert.Nil(t, metaRepo.Meta.RootUserID)
}

func TestSignup_DuplicateUsername(t *testing.T) {
	svc, userRepo, _ := newTestService(t)
	userRepo.Users["existing"] = &model.User{
		ID:            "existing",
		Username:      "taken",
		UsernameLower: "taken",
	}

	_, err := svc.Signup("taken", "pass", false)
	assert.ErrorIs(t, err, signup.ErrUsernameAlreadyExists)
}

func TestSignup_EmptyUsername(t *testing.T) {
	svc, _, _ := newTestService(t)
	_, err := svc.Signup("", "pass", false)
	assert.ErrorIs(t, err, signup.ErrInvalidUsername)
}

func TestSignup_TooLongUsername(t *testing.T) {
	svc, _, _ := newTestService(t)
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'a'
	}
	_, err := svc.Signup(string(long), "pass", false)
	assert.ErrorIs(t, err, signup.ErrInvalidUsername)
}

// upstream Misskey TS の `localUsernameSchema` は `^\w{1,20}$` (#800)。
// 境界 (= 20 char OK / 21 char NG) と character set (= alphanumeric +
// underscore のみ) を明確に担保する。
func TestSignup_UsernameBoundary20Chars(t *testing.T) {
	svc, _, _ := newTestService(t)
	// 20 char ちょうど (= 上限) は通る
	r, err := svc.Signup("a234567890123456789a", "pass", false)
	require.NoError(t, err)
	assert.Equal(t, "a234567890123456789a", r.User.Username)
}

func TestSignup_Username21CharsRejected(t *testing.T) {
	svc, _, _ := newTestService(t)
	// 21 char (= 上限超過) は ErrInvalidUsername
	_, err := svc.Signup("a234567890123456789ab", "pass", false)
	assert.ErrorIs(t, err, signup.ErrInvalidUsername)
}

func TestSignup_UsernameIllegalCharsRejected(t *testing.T) {
	svc, _, _ := newTestService(t)
	for _, name := range []string{
		"alice-bob", // hyphen 不可
		"alice.bob", // dot 不可
		"alice@bob", // @ 不可
		"alice bob", // space 不可 (TrimSpace で trim されない middle space)
		"アリス",       // 非 ASCII 不可
	} {
		_, err := svc.Signup(name, "pass", false)
		assert.ErrorIs(t, err, signup.ErrInvalidUsername, "name=%q", name)
	}
}

func TestSignup_PreservedUsernameRejected(t *testing.T) {
	svc, _, metaRepo := newTestService(t)
	// meta.preservedUsernames に "admin" が入っている想定。
	metaRepo.Meta.PreservedUsernames = []string{"admin", "root", "System"}

	_, err := svc.Signup("admin", "pass", false)
	assert.ErrorIs(t, err, signup.ErrUsernameReserved)

	// case-insensitive (slot "System" を大文字で登録、小文字で試行)
	_, err = svc.Signup("SYSTEM", "pass", false)
	assert.ErrorIs(t, err, signup.ErrUsernameReserved)
}

func TestSignup_PreservedUsernameBypassedOnInitialSetup(t *testing.T) {
	// 初回セットアップ時は root / admin が予約ワードでも作成可能にする。
	// そうでないと本家デフォルトで admin / root が永久に作成できなくなる。
	svc, _, metaRepo := newTestService(t)
	metaRepo.Meta.PreservedUsernames = []string{"admin"}

	result, err := svc.Signup("admin", "pass", true)
	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestSignup_PreservedUsernameAllowsOthers(t *testing.T) {
	svc, _, metaRepo := newTestService(t)
	metaRepo.Meta.PreservedUsernames = []string{"admin"}

	result, err := svc.Signup("alice", "pass", false)
	require.NoError(t, err)
	assert.Equal(t, "alice", result.User.Username)
}

// --- Failing repo tests ---

type failingCreateUserRepo struct {
	*testutil.MockUserRepository
}

func (f *failingCreateUserRepo) Create(_ *model.User) error { return assert.AnError }

type failingCreateProfileRepo struct {
	*testutil.MockUserRepository
}

func (f *failingCreateProfileRepo) CreateProfile(_ *model.UserProfile) error { return assert.AnError }

func TestSignup_UserCreateError(t *testing.T) {
	repo := &failingCreateUserRepo{testutil.NewMockUserRepository()}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	svc := signup.NewService(repo, metaRepo, idGen)
	_, err := svc.Signup("user1", "pass", false)
	assert.Error(t, err)
}

func TestSignup_ProfileCreateError(t *testing.T) {
	repo := &failingCreateProfileRepo{testutil.NewMockUserRepository()}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	svc := signup.NewService(repo, metaRepo, idGen)
	_, err := svc.Signup("user1", "pass", false)
	assert.Error(t, err)
}

func TestSignup_TokenIs16Chars(t *testing.T) {
	svc, _, _ := newTestService(t)
	result, err := svc.Signup("user1", "pass", false)
	require.NoError(t, err)
	assert.Len(t, result.Token, 16) // 8 bytes hex = 16 chars
}

func TestSignup_WithKeypairRepo(t *testing.T) {
	svc, _, _ := newTestService(t)
	keypairRepo := testutil.NewMockUserKeypairRepository()
	svc.SetKeypairRepo(keypairRepo)

	result, err := svc.Signup("alice", "pass", false)
	require.NoError(t, err)

	// Keypair created for the new user.
	k, ok := keypairRepo.Keypairs[result.User.ID]
	require.True(t, ok)
	assert.NotEmpty(t, k.PublicKey)
	assert.NotEmpty(t, k.PrivateKey)
}

type failingCreateKeypairRepo struct{}

func (f *failingCreateKeypairRepo) Create(_ *model.UserKeypair) error { return assert.AnError }
func (f *failingCreateKeypairRepo) FindByUserID(_ string) (*model.UserKeypair, error) {
	return nil, assert.AnError
}

func TestSignup_KeypairCreateError(t *testing.T) {
	svc, _, _ := newTestService(t)
	svc.SetKeypairRepo(&failingCreateKeypairRepo{})

	_, err := svc.Signup("alice", "pass", false)
	assert.Error(t, err)
}

// --- Pending signup (#595) ---

func TestCreatePending_Success(t *testing.T) {
	svc, _, _ := newTestService(t)
	pendingRepo := testutil.NewMockUserPendingRepository()
	svc.SetUserPendingRepo(pendingRepo)

	row, err := svc.CreatePending("alice", "alice@example.com", "secret123", nil)
	require.NoError(t, err)
	assert.Equal(t, "alice", row.Username)
	assert.Equal(t, "alice@example.com", row.Email)
	assert.NotEmpty(t, row.Code)
	// Password は bcrypt hash 済 (再 hash 防止)。
	assert.NotEqual(t, "secret123", row.Password)
	assert.Len(t, pendingRepo.Rows, 1)
}

func TestCreatePending_DuplicateUsername(t *testing.T) {
	svc, userRepo, _ := newTestService(t)
	pendingRepo := testutil.NewMockUserPendingRepository()
	svc.SetUserPendingRepo(pendingRepo)
	require.NoError(t, userRepo.Create(&model.User{ID: "u1", Username: "alice", UsernameLower: "alice"}))

	_, err := svc.CreatePending("Alice", "a@example.com", "pw", nil)
	assert.ErrorIs(t, err, signup.ErrUsernameAlreadyExists)
}

func TestCreatePending_ReservedUsername(t *testing.T) {
	svc, _, metaRepo := newTestService(t)
	metaRepo.Meta.PreservedUsernames = []string{"admin"}
	pendingRepo := testutil.NewMockUserPendingRepository()
	svc.SetUserPendingRepo(pendingRepo)

	_, err := svc.CreatePending("admin", "a@example.com", "pw", nil)
	assert.ErrorIs(t, err, signup.ErrUsernameReserved)
}

func TestCreatePending_InvalidUsername(t *testing.T) {
	svc, _, _ := newTestService(t)
	pendingRepo := testutil.NewMockUserPendingRepository()
	svc.SetUserPendingRepo(pendingRepo)

	_, err := svc.CreatePending("", "a@example.com", "pw", nil)
	assert.ErrorIs(t, err, signup.ErrInvalidUsername)
}

func TestPromotePending_Success(t *testing.T) {
	svc, userRepo, _ := newTestService(t)
	pendingRepo := testutil.NewMockUserPendingRepository()
	svc.SetUserPendingRepo(pendingRepo)

	row, err := svc.CreatePending("bob", "bob@example.com", "pw123", nil)
	require.NoError(t, err)

	result, err := svc.PromotePending(row.Code)
	require.NoError(t, err)
	assert.Equal(t, "bob", result.User.Username)
	assert.NotEmpty(t, result.Token)
	// pending row が消費されている
	assert.Empty(t, pendingRepo.Rows)
	// profile に email + emailVerified=true がセット
	prof := userRepo.Profiles[result.User.ID]
	require.NotNil(t, prof)
	require.NotNil(t, prof.Email)
	assert.Equal(t, "bob@example.com", *prof.Email)
	assert.True(t, prof.EmailVerified)
	// Password ハッシュは Pending と完全一致 (再 hash されていない)
	require.NotNil(t, prof.Password)
	assert.Equal(t, row.Password, *prof.Password)
}

func TestPromotePending_NotFound(t *testing.T) {
	svc, _, _ := newTestService(t)
	pendingRepo := testutil.NewMockUserPendingRepository()
	svc.SetUserPendingRepo(pendingRepo)

	_, err := svc.PromotePending("ghost")
	assert.ErrorIs(t, err, signup.ErrPendingNotFound)
}

func TestPromotePending_UsernameClash(t *testing.T) {
	svc, userRepo, _ := newTestService(t)
	pendingRepo := testutil.NewMockUserPendingRepository()
	svc.SetUserPendingRepo(pendingRepo)

	row, err := svc.CreatePending("clash", "c@example.com", "pw", nil)
	require.NoError(t, err)

	// pending 確定前に同名 user が登録されたケースを再現
	require.NoError(t, userRepo.Create(&model.User{ID: "u_clash", Username: "Clash", UsernameLower: "clash"}))

	_, err = svc.PromotePending(row.Code)
	assert.ErrorIs(t, err, signup.ErrUsernameAlreadyExists)
}

func TestPromotePending_Expired(t *testing.T) {
	// PendingSignupTTL = 24h と十分長く、ParseTime も成功するため通常 path で
	// expired を再現するのは難しい。Create 後に row.ID を 24h+ 前の ULID に
	// 書き換えて expired path を踏ませる。
	svc, _, _ := newTestService(t)
	pendingRepo := testutil.NewMockUserPendingRepository()
	svc.SetUserPendingRepo(pendingRepo)

	row, err := svc.CreatePending("expuser", "exp@example.com", "pw", nil)
	require.NoError(t, err)

	// ULID を 25h 前の時刻で再生成 (idGen は newTestService で aidx 固定)。
	idGen, _ := id.NewGenerator("aidx")
	old := idGen.Generate(time.Now().Add(-25 * time.Hour))
	delete(pendingRepo.Rows, row.ID)
	row.ID = old
	pendingRepo.Rows[old] = row

	_, err = svc.PromotePending(row.Code)
	assert.ErrorIs(t, err, signup.ErrPendingExpired)
}

func TestPromotePending_WithKeypair(t *testing.T) {
	svc, _, _ := newTestService(t)
	pendingRepo := testutil.NewMockUserPendingRepository()
	svc.SetUserPendingRepo(pendingRepo)
	keypairRepo := testutil.NewMockUserKeypairRepository()
	svc.SetKeypairRepo(keypairRepo)

	row, err := svc.CreatePending("kpuser", "kp@example.com", "pw", nil)
	require.NoError(t, err)

	result, err := svc.PromotePending(row.Code)
	require.NoError(t, err)
	_, ok := keypairRepo.Keypairs[result.User.ID]
	assert.True(t, ok, "keypair must be created during PromotePending")
}

func TestPromotePending_WebhookHookFires(t *testing.T) {
	svc, _, _ := newTestService(t)
	pendingRepo := testutil.NewMockUserPendingRepository()
	svc.SetUserPendingRepo(pendingRepo)
	hook := &recordingHook{}
	svc.SetWebhookHook(hook)

	row, err := svc.CreatePending("hookuser", "hook@example.com", "pw", nil)
	require.NoError(t, err)

	_, err = svc.PromotePending(row.Code)
	require.NoError(t, err)
	assert.Equal(t, 1, hook.calls, "OnUserCreated must fire after promotion")
}

// recordingHook は WebhookHook の呼び出し回数を数える。
type recordingHook struct{ calls int }

func (r *recordingHook) OnUserCreated(_ *model.User) { r.calls++ }

// invitationTicketID を渡すと pending row に保存され、PromotePending
// 完了時に SignupResult.InvitationTicketID として伝搬される (#600 item 5)。
func TestCreatePending_StoresInvitationTicketID(t *testing.T) {
	svc, _, _ := newTestService(t)
	pendingRepo := testutil.NewMockUserPendingRepository()
	svc.SetUserPendingRepo(pendingRepo)

	tid := "ticket_abc"
	row, err := svc.CreatePending("inv", "inv@example.com", "pw", &tid)
	require.NoError(t, err)
	require.NotNil(t, row.InvitationTicketID)
	assert.Equal(t, "ticket_abc", *row.InvitationTicketID)
}

func TestPromotePending_PropagatesInvitationTicketID(t *testing.T) {
	svc, _, _ := newTestService(t)
	pendingRepo := testutil.NewMockUserPendingRepository()
	svc.SetUserPendingRepo(pendingRepo)

	tid := "ticket_xyz"
	row, err := svc.CreatePending("invuser", "invu@example.com", "pw", &tid)
	require.NoError(t, err)

	result, err := svc.PromotePending(row.Code)
	require.NoError(t, err)
	require.NotNil(t, result.InvitationTicketID)
	assert.Equal(t, "ticket_xyz", *result.InvitationTicketID)
}

func TestPromotePending_NoTicketIDReturnsNil(t *testing.T) {
	svc, _, _ := newTestService(t)
	pendingRepo := testutil.NewMockUserPendingRepository()
	svc.SetUserPendingRepo(pendingRepo)

	row, err := svc.CreatePending("noinv", "noinv@example.com", "pw", nil)
	require.NoError(t, err)

	result, err := svc.PromotePending(row.Code)
	require.NoError(t, err)
	assert.Nil(t, result.InvitationTicketID, "non-invitation pending では nil で伝搬する")
}
