package blocking_test

import (
	"errors"
	"testing"

	"github.com/shiroha-a/mk/internal/core/blocking"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var stubError = errors.New("stub error")

func newSvc(t *testing.T) (*blocking.Service, *testutil.MockUserRepository, *testutil.MockBlockingRepository, *testutil.MockFollowingRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	blockingRepo := testutil.NewMockBlockingRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := blocking.NewService(userRepo, blockingRepo, followingRepo, idGen)
	return svc, userRepo, blockingRepo, followingRepo
}

func addUser(repo *testutil.MockUserRepository, id string) {
	repo.Users[id] = &model.User{ID: id, Username: id}
}

func TestBlock_Self(t *testing.T) {
	svc, _, _, _ := newSvc(t)
	_, err := svc.Block("a", "a")
	require.ErrorIs(t, err, blocking.ErrSelfBlock)
}

func TestBlock_NotFound(t *testing.T) {
	svc, _, _, _ := newSvc(t)
	_, err := svc.Block("a", "b")
	require.ErrorIs(t, err, blocking.ErrBlockeeNotFound)
}

func TestBlock_AlreadyBlocking(t *testing.T) {
	svc, ur, _, _ := newSvc(t)
	addUser(ur, "a")
	addUser(ur, "b")
	_, err := svc.Block("a", "b")
	require.NoError(t, err)
	_, err = svc.Block("a", "b")
	require.ErrorIs(t, err, blocking.ErrAlreadyBlocking)
}

func TestBlock_Success(t *testing.T) {
	svc, ur, _, _ := newSvc(t)
	addUser(ur, "a")
	addUser(ur, "b")
	b, err := svc.Block("a", "b")
	require.NoError(t, err)
	assert.Equal(t, "a", b.BlockerID)
}

func TestBlock_RemovesExistingFollows(t *testing.T) {
	svc, ur, _, fr := newSvc(t)
	addUser(ur, "a")
	addUser(ur, "b")
	fr.Followings["f1"] = &model.Following{ID: "f1", FollowerID: "a", FolloweeID: "b"}
	fr.Followings["f2"] = &model.Following{ID: "f2", FollowerID: "b", FolloweeID: "a"}

	_, err := svc.Block("a", "b")
	require.NoError(t, err)
	assert.Empty(t, fr.Followings)
}

func TestBlock_NoFollowingRepo(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	addUser(userRepo, "a")
	addUser(userRepo, "b")
	idGen, _ := id.NewGenerator("aidx")
	svc := blocking.NewService(userRepo, testutil.NewMockBlockingRepository(), nil, idGen)
	_, err := svc.Block("a", "b")
	require.NoError(t, err)
}

// failingBlockingRepo wraps mock to fail Exists/Create.
type failingBlockingRepo struct {
	*testutil.MockBlockingRepository
	failExists bool
	failCreate bool
	failDelete bool
}

func (f *failingBlockingRepo) Exists(blockerID, blockeeID string) (bool, error) {
	if f.failExists {
		return false, stubError
	}
	return f.MockBlockingRepository.Exists(blockerID, blockeeID)
}

func (f *failingBlockingRepo) Create(b *model.Blocking) error {
	if f.failCreate {
		return stubError
	}
	return f.MockBlockingRepository.Create(b)
}

func (f *failingBlockingRepo) Delete(b *model.Blocking) error {
	if f.failDelete {
		return stubError
	}
	return f.MockBlockingRepository.Delete(b)
}

func TestBlock_ExistsError(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	addUser(userRepo, "a")
	addUser(userRepo, "b")
	idGen, _ := id.NewGenerator("aidx")
	svc := blocking.NewService(userRepo, &failingBlockingRepo{MockBlockingRepository: testutil.NewMockBlockingRepository(), failExists: true}, nil, idGen)
	_, err := svc.Block("a", "b")
	assert.ErrorIs(t, err, stubError)
}

func TestBlock_CreateError(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	addUser(userRepo, "a")
	addUser(userRepo, "b")
	idGen, _ := id.NewGenerator("aidx")
	svc := blocking.NewService(userRepo, &failingBlockingRepo{MockBlockingRepository: testutil.NewMockBlockingRepository(), failCreate: true}, nil, idGen)
	_, err := svc.Block("a", "b")
	assert.ErrorIs(t, err, stubError)
}

func TestUnblock_Self(t *testing.T) {
	svc, _, _, _ := newSvc(t)
	err := svc.Unblock("a", "a")
	require.ErrorIs(t, err, blocking.ErrSelfBlock)
}

func TestUnblock_NotBlocking(t *testing.T) {
	svc, _, _, _ := newSvc(t)
	err := svc.Unblock("a", "b")
	require.ErrorIs(t, err, blocking.ErrNotBlocking)
}

func TestUnblock_Success(t *testing.T) {
	svc, ur, _, _ := newSvc(t)
	addUser(ur, "a")
	addUser(ur, "b")
	_, err := svc.Block("a", "b")
	require.NoError(t, err)
	require.NoError(t, svc.Unblock("a", "b"))
}

func TestIsBlockedAndList(t *testing.T) {
	svc, ur, _, _ := newSvc(t)
	addUser(ur, "a")
	addUser(ur, "b")
	_, err := svc.Block("a", "b")
	require.NoError(t, err)

	yes, err := svc.IsBlocked("a", "b")
	require.NoError(t, err)
	assert.True(t, yes)

	rows, err := svc.List("a", "", "", 0, 0)
	require.NoError(t, err)
	assert.Len(t, rows, 1)

	rows, err = svc.List("a", "", "", 5, 0)
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

// failingFollowingRepo wraps mock to make Delete fail (covers fold-in error path)
type failingFollowingRepo struct {
	*testutil.MockFollowingRepository
}

func (f *failingFollowingRepo) Delete(_ *model.Following) error {
	return stubError
}

func TestBlock_RemoveFollowingDeleteError(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	addUser(userRepo, "a")
	addUser(userRepo, "b")
	mock := testutil.NewMockFollowingRepository()
	mock.Followings["f1"] = &model.Following{ID: "f1", FollowerID: "a", FolloweeID: "b"}
	idGen, _ := id.NewGenerator("aidx")
	svc := blocking.NewService(userRepo, testutil.NewMockBlockingRepository(),
		&failingFollowingRepo{MockFollowingRepository: mock}, idGen)
	_, err := svc.Block("a", "b")
	require.NoError(t, err)
}

// Block 経由で remote follower の follow が解除されると instance counter が -1
// される (#596 — Block→自動 unfollow 経路でも incremental 維持)。
func TestBlock_DecrementsInstanceCounters(t *testing.T) {
	svc, ur, _, fr := newSvc(t)
	instanceRepo := testutil.NewMockInstanceRepository()
	host := "remote.example"
	instanceRepo.Instances[host] = &model.Instance{Host: host, FollowersCount: 5, FollowingCount: 7}
	svc.SetInstanceRepo(instanceRepo)

	addUser(ur, "alice_local")
	remote := &model.User{ID: "remote_user", Username: "remote_user", Host: &host}
	ur.Users["remote_user"] = remote

	// remote が alice を follow している状態を seed
	fr.Followings["f"] = &model.Following{
		ID:           "f",
		FollowerID:   "remote_user",
		FolloweeID:   "alice_local",
		FollowerHost: &host,
	}

	// alice が remote を block すると、remote→alice の follow も自動解除される
	_, err := svc.Block("alice_local", "remote_user")
	require.NoError(t, err)
	assert.Empty(t, fr.Followings)
	// remote 側 follower カウントが -1
	assert.Equal(t, 4, instanceRepo.Instances[host].FollowersCount)
}

// recordingFederationHook captures hook fires so we can assert that Block /
// Unblock trigger AP delivery (#1560)。
type recordingFederationHook struct {
	blocked   [][2]string
	unblocked [][2]string
}

func (h *recordingFederationHook) OnBlocked(blockerID, blockeeID string) {
	h.blocked = append(h.blocked, [2]string{blockerID, blockeeID})
}

func (h *recordingFederationHook) OnUnblocked(blockerID, blockeeID string) {
	h.unblocked = append(h.unblocked, [2]string{blockerID, blockeeID})
}

// Block / Unblock 成功時に federationHook が発火する (#1560)。
func TestBlockUnblock_FiresFederationHook(t *testing.T) {
	svc, ur, _, _ := newSvc(t)
	addUser(ur, "a")
	addUser(ur, "b")
	hook := &recordingFederationHook{}
	svc.SetFederationHook(hook)

	_, err := svc.Block("a", "b")
	require.NoError(t, err)
	require.Equal(t, [][2]string{{"a", "b"}}, hook.blocked)

	require.NoError(t, svc.Unblock("a", "b"))
	require.Equal(t, [][2]string{{"a", "b"}}, hook.unblocked)
}

// Unblock で Delete が失敗したら error を返し、hook は発火しない (#1560)。
func TestUnblock_DeleteError(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	addUser(userRepo, "a")
	addUser(userRepo, "b")
	idGen, _ := id.NewGenerator("aidx")
	repo := &failingBlockingRepo{MockBlockingRepository: testutil.NewMockBlockingRepository()}
	svc := blocking.NewService(userRepo, repo, nil, idGen)
	hook := &recordingFederationHook{}
	svc.SetFederationHook(hook)

	_, err := svc.Block("a", "b")
	require.NoError(t, err)

	repo.failDelete = true
	err = svc.Unblock("a", "b")
	require.ErrorIs(t, err, stubError)
	assert.Empty(t, hook.unblocked, "Delete 失敗時は Undo(Block) を配信しない")
}
