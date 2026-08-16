package signupapplication

import (
	"errors"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var errBoom = errors.New("boom")

// stubRepo lets a test force each repository call to fail, so the wrappers'
// error paths are exercised without breaking the shared connection.
//
// DB() は本物を返す。transition がトランザクションを開けないと、その中の分岐に
// 到達できないため。
type stubRepo struct {
	createErr     error
	findLiveErr   error
	findLatestErr error
	findByIDErr   error
	lockErr       error
	updateErr     error
	listErr       error
	countErr      error
	expireErr     error

	live   *model.SignupApplication
	locked *model.SignupApplication
}

func (s *stubRepo) Create(*model.SignupApplication) error { return s.createErr }

func (s *stubRepo) FindByID(string) (*model.SignupApplication, error) {
	if s.findByIDErr != nil {
		return nil, s.findByIDErr
	}
	return &model.SignupApplication{ID: "x"}, nil
}

func (s *stubRepo) FindLiveByContact(string, string) (*model.SignupApplication, error) {
	if s.findLiveErr != nil {
		return nil, s.findLiveErr
	}
	if s.live != nil {
		return s.live, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (s *stubRepo) FindLatestByContact(string, string) (*model.SignupApplication, error) {
	if s.findLatestErr != nil {
		return nil, s.findLatestErr
	}
	return &model.SignupApplication{ID: "x"}, nil
}

func (s *stubRepo) FindByIDForUpdateTx(*gorm.DB, string) (*model.SignupApplication, error) {
	if s.lockErr != nil {
		return nil, s.lockErr
	}
	if s.locked != nil {
		return s.locked, nil
	}
	return &model.SignupApplication{ID: "x", Status: model.SignupApplicationPending}, nil
}

func (s *stubRepo) UpdateFieldsTx(*gorm.DB, string, map[string]any) error { return s.updateErr }

func (s *stubRepo) List(string, int, int) ([]*model.SignupApplication, error) {
	return nil, s.listErr
}

func (s *stubRepo) Count(string) (int, error) { return 0, s.countErr }

func (s *stubRepo) ExpireStale(time.Time) (int, error) { return 0, s.expireErr }

func (s *stubRepo) DB() *gorm.DB { return testDB }

func newStubService(t *testing.T, repo *stubRepo) *Service {
	t.Helper()
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	return NewService(repo, idGen)
}

func TestErrorPaths(t *testing.T) {
	t.Run("Apply propagates a lookup failure", func(t *testing.T) {
		svc := newStubService(t, &stubRepo{findLiveErr: errBoom})
		_, err := svc.Apply(testContact, "")
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("Apply wraps a create failure", func(t *testing.T) {
		svc := newStubService(t, &stubRepo{createErr: errBoom})
		_, err := svc.Apply(testContact, "")
		assert.ErrorIs(t, err, errBoom)
		assert.NotErrorIs(t, err, ErrLiveApplicationExists)
	})

	t.Run("Apply translates the unique violation", func(t *testing.T) {
		svc := newStubService(t, &stubRepo{createErr: repository.ErrSignupApplicationLiveExists})
		_, err := svc.Apply(testContact, "")
		assert.ErrorIs(t, err, ErrLiveApplicationExists)
	})

	t.Run("Latest propagates a failure", func(t *testing.T) {
		svc := newStubService(t, &stubRepo{findLatestErr: errBoom})
		_, err := svc.Latest(testContact)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("Get propagates a failure", func(t *testing.T) {
		svc := newStubService(t, &stubRepo{findByIDErr: errBoom})
		_, err := svc.Get("x")
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("List propagates a failure", func(t *testing.T) {
		svc := newStubService(t, &stubRepo{listErr: errBoom})
		_, err := svc.List(repository.SignupApplicationFilterAll, 10, 0)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("Count propagates a failure", func(t *testing.T) {
		svc := newStubService(t, &stubRepo{countErr: errBoom})
		_, err := svc.Count(repository.SignupApplicationFilterAll)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("ExpireStale propagates a failure", func(t *testing.T) {
		svc := newStubService(t, &stubRepo{expireErr: errBoom})
		_, err := svc.ExpireStale()
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("transition propagates a lock failure", func(t *testing.T) {
		svc := newStubService(t, &stubRepo{lockErr: errBoom})
		assert.ErrorIs(t, svc.Approve("x", "mod"), errBoom)
	})

	t.Run("transition propagates an update failure", func(t *testing.T) {
		svc := newStubService(t, &stubRepo{updateErr: errBoom})
		svc.SetClock(func() time.Time { return time.Now().Add(-time.Hour) })
		svc.locked(t, &model.SignupApplication{
			ID: "x", Status: model.SignupApplicationPending,
			ExpiresAt: time.Now().Add(time.Hour),
		})
		assert.ErrorIs(t, svc.Approve("x", "mod"), errBoom)
	})
}

// locked replaces the row the stub hands back under the write lock.
func (s *Service) locked(t *testing.T, app *model.SignupApplication) {
	t.Helper()
	stub, ok := s.repo.(*stubRepo)
	require.True(t, ok)
	stub.locked = app
}

// 期限切れの掃除は、ロックを取った時点で別経路が既に処理していたら何もしない。
// **ここで無条件に上書きすると、承認済みの申請を期限切れに落としうる。**
func TestExpireIfStale_SkipsWhenAlreadyHandled(t *testing.T) {
	now := time.Now().UTC()
	stale := &model.SignupApplication{
		ID: "x", Status: model.SignupApplicationPending, ExpiresAt: now.Add(-time.Hour),
	}
	// ロック後に見えるのは既に終端へ遷移した行。
	handled := &model.SignupApplication{
		ID: "x", Status: model.SignupApplicationCompleted, ExpiresAt: now.Add(-time.Hour),
	}
	repo := &stubRepo{live: stale, locked: handled, updateErr: errBoom}
	svc := newStubService(t, repo)
	svc.SetClock(func() time.Time { return now })

	got, err := svc.Current(testContact)
	// updateErr を仕込んであるので、更新に入ったらエラーが返る。返らない =
	// 何もしなかった、ということ。
	require.NoError(t, err)
	assert.Nil(t, got)
}
