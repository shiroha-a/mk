package registrationbot

import (
	"errors"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubUsedUsernames records reservations.
type stubUsedUsernames struct {
	names     map[string]bool
	createErr error
}

func newStubUsedUsernames() *stubUsedUsernames {
	return &stubUsedUsernames{names: map[string]bool{}}
}

func (s *stubUsedUsernames) Create(username string) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.names[username] = true
	return nil
}

func (s *stubUsedUsernames) Exists(username string) (bool, error) {
	return s.names[username], nil
}

func newService(t *testing.T) (*Service, *testutil.MockUserRepository, *stubUsedUsernames) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	keypairRepo := testutil.NewMockUserKeypairRepository()
	used := newStubUsedUsernames()
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	return NewService(userRepo, keypairRepo, used, idGen), userRepo, used
}

func TestEnsure_CreatesTheBot(t *testing.T) {
	svc, userRepo, used := newService(t)

	bot, err := svc.Ensure()
	require.NoError(t, err)

	assert.Equal(t, Username, bot.Username)
	assert.Nil(t, bot.Host)
	assert.True(t, bot.IsBot, "Service として配信されるための条件")
	// **isLocked は false。** true だとフォローが承認待ちのまま滞留し、承認する
	// 手段が無いので永久に成立しない = 通知フィルタを通せない。
	assert.False(t, bot.IsLocked)
	assert.False(t, bot.IsExplorable)

	// **ドットを含めないこと。** 含めるとシステムアカウント判定に当たり、
	// AP 上 Application として配信されてしまう。
	assert.NotContains(t, bot.Username, ".")

	// 通常の認証経路を作らないこと。
	assert.Nil(t, bot.Token)
	profile, err := userRepo.FindProfileByUserID(bot.ID)
	require.NoError(t, err)
	assert.Empty(t, profile.Password)

	assert.True(t, used.names[Username], "ユーザー名を予約すること")
}

func TestEnsure_IsIdempotent(t *testing.T) {
	svc, userRepo, _ := newService(t)

	first, err := svc.Ensure()
	require.NoError(t, err)
	second, err := svc.Ensure()
	require.NoError(t, err)

	// **作り直さないこと。** actor URI が変わると、それまでのフォロワーが失われて
	// 通知が相手の受信設定を通らなくなる。
	assert.Equal(t, first.ID, second.ID)
	assert.Len(t, userRepo.Users, 1)
}

// 人間のアカウントが同じ名前を持っていたら、黙って別名にせず明確に失敗すること。
func TestEnsure_RejectsWhenTakenByHuman(t *testing.T) {
	svc, userRepo, _ := newService(t)
	require.NoError(t, userRepo.Create(&model.User{
		ID: "human-1", Username: Username, UsernameLower: Username, IsBot: false,
	}))

	_, err := svc.Ensure()
	assert.ErrorIs(t, err, ErrUsernameTaken)
}

// 途中で失敗した残骸があっても、次の呼び出しで揃うこと。
func TestEnsure_CompletesPartialState(t *testing.T) {
	svc, userRepo, _ := newService(t)
	require.NoError(t, userRepo.Create(&model.User{
		ID: "bot-1", Username: Username, UsernameLower: Username, IsBot: true,
	}))

	bot, err := svc.Ensure()
	require.NoError(t, err)
	assert.Equal(t, "bot-1", bot.ID)

	_, err = userRepo.FindProfileByUserID(bot.ID)
	assert.NoError(t, err, "profile が補われること")
}

// 予約が既にあるなら、二重登録のエラーは成功として扱う (冪等)。
func TestEnsure_ReservationAlreadyPresent(t *testing.T) {
	svc, _, used := newService(t)
	used.names[Username] = true
	used.createErr = errors.New("duplicate key")

	_, err := svc.Ensure()
	assert.NoError(t, err)
}

func TestEnsure_ReservationFailure(t *testing.T) {
	svc, _, used := newService(t)
	used.createErr = errors.New("boom")

	_, err := svc.Ensure()
	assert.Error(t, err)
}

func TestEnsure_WithoutUsedUsernameRepo(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	keypairRepo := testutil.NewMockUserKeypairRepository()
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	svc := NewService(userRepo, keypairRepo, nil, idGen)

	bot, err := svc.Ensure()
	require.NoError(t, err)
	assert.Equal(t, Username, bot.Username)
}

// reset-password / アカウント削除のガードが使う判定。**通常のシステムアカウントの
// ガードはユーザー名のドットで判定するので、この bot は対象外になる。**
func TestIsBotAccount(t *testing.T) {
	host := "remote.example"
	tests := []struct {
		name string
		user *model.User
		want bool
	}{
		{name: "the bot", user: &model.User{UsernameLower: Username}, want: true},
		{name: "nil", user: nil, want: false},
		{name: "other local user", user: &model.User{UsernameLower: "alice"}, want: false},
		{
			name: "remote user with the same name",
			user: &model.User{UsernameLower: Username, Host: &host},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsBotAccount(tt.user))
		})
	}
}

func TestSetClock_IgnoresNil(t *testing.T) {
	svc, _, _ := newService(t)
	svc.SetClock(nil)
	_, err := svc.Ensure()
	assert.NoError(t, err)
}

// failingUserRepo forces one of the writes to fail.
type failingUserRepo struct {
	*testutil.MockUserRepository
	createErr        error
	createProfileErr error
}

func (f *failingUserRepo) Create(u *model.User) error {
	if f.createErr != nil {
		return f.createErr
	}
	return f.MockUserRepository.Create(u)
}

func (f *failingUserRepo) CreateProfile(p *model.UserProfile) error {
	if f.createProfileErr != nil {
		return f.createProfileErr
	}
	return f.MockUserRepository.CreateProfile(p)
}

type failingKeypairRepo struct {
	repository.UserKeypairRepository
	createErr error
}

func (f *failingKeypairRepo) Create(k *model.UserKeypair) error {
	if f.createErr != nil {
		return f.createErr
	}
	return f.UserKeypairRepository.Create(k)
}

func TestEnsure_WriteFailures(t *testing.T) {
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)

	t.Run("user create fails", func(t *testing.T) {
		repo := &failingUserRepo{
			MockUserRepository: testutil.NewMockUserRepository(),
			createErr:          errors.New("boom"),
		}
		svc := NewService(repo, testutil.NewMockUserKeypairRepository(), newStubUsedUsernames(), idGen)
		_, err := svc.Ensure()
		assert.Error(t, err)
	})

	t.Run("profile create fails", func(t *testing.T) {
		repo := &failingUserRepo{
			MockUserRepository: testutil.NewMockUserRepository(),
			createProfileErr:   errors.New("boom"),
		}
		svc := NewService(repo, testutil.NewMockUserKeypairRepository(), newStubUsedUsernames(), idGen)
		_, err := svc.Ensure()
		assert.Error(t, err)
	})

	t.Run("keypair create fails", func(t *testing.T) {
		kp := &failingKeypairRepo{
			UserKeypairRepository: testutil.NewMockUserKeypairRepository(),
			createErr:             errors.New("boom"),
		}
		svc := NewService(testutil.NewMockUserRepository(), kp, newStubUsedUsernames(), idGen)
		_, err := svc.Ensure()
		assert.Error(t, err)
	})
}

func TestSetClock(t *testing.T) {
	svc, _, _ := newService(t)
	called := false
	svc.SetClock(func() time.Time {
		called = true
		return time.Now()
	})
	_, err := svc.Ensure()
	require.NoError(t, err)
	assert.True(t, called)
}
