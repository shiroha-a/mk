package move_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/move"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeResolver returns preset results and records which URIs were asked.
type fakeResolver struct {
	byURI map[string]*model.User
	err   error
	calls []string
}

func (f *fakeResolver) ResolveActor(uri string) (*model.User, error) {
	f.calls = append(f.calls, uri)
	if f.err != nil {
		return nil, f.err
	}
	return f.byURI[uri], nil
}

// fakeDeliverer captures the activity body for assertions.
type fakeDeliverer struct {
	called    int
	signer    string
	body      []byte
	returnErr error
}

func (f *fakeDeliverer) DeliverToFollowers(signerUserID string, body []byte) error {
	f.called++
	f.signer = signerUserID
	f.body = body
	return f.returnErr
}

func strPtr(s string) *string { return &s }

// newService is a helper that wires all the mocks together. baseURL is fixed
// so tests can predict generated srcURI (urls.UserURI).
func newService(resolver move.Resolver, deliverer move.Deliverer) (*move.Service, *testutil.MockUserRepository, *testutil.MockFollowingRepository) {
	userRepo := testutil.NewMockUserRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	urls := activitypub.NewURLBuilder("https://local.example")
	renderer := activitypub.NewRenderer(urls)
	svc := move.NewService(userRepo, followingRepo, urls, renderer, resolver, deliverer)
	return svc, userRepo, followingRepo
}

func TestMove_Success(t *testing.T) {
	srcURI := "https://local.example/users/me"
	dstURI := "https://other.example/users/new"

	dst := &model.User{
		ID:          "dst",
		URI:         strPtr(dstURI),
		AlsoKnownAs: strPtr(srcURI),
	}
	resolver := &fakeResolver{byURI: map[string]*model.User{dstURI: dst}}
	deliverer := &fakeDeliverer{}

	svc, userRepo, _ := newService(resolver, deliverer)
	me := &model.User{ID: "me", URI: strPtr(srcURI)}
	userRepo.Users[me.ID] = me

	require.NoError(t, svc.Move(me, dstURI))

	// In-place struct was updated so the caller sees the new state.
	assert.NotNil(t, me.MovedToURI)
	assert.Equal(t, dstURI, *me.MovedToURI)
	assert.NotNil(t, me.MovedAt)
	require.NotNil(t, me.AlsoKnownAs)
	assert.Equal(t, dstURI, *me.AlsoKnownAs)

	// UpdateUser was persisted to the repo row (not just the in-place struct).
	// これを検証しないと UpdateUser 呼び出しが消えたリグレッションを検出できない。
	persisted := userRepo.Users["me"]
	require.NotNil(t, persisted.MovedToURI)
	assert.Equal(t, dstURI, *persisted.MovedToURI)
	assert.NotNil(t, persisted.MovedAt)
	require.NotNil(t, persisted.AlsoKnownAs)
	assert.Equal(t, dstURI, *persisted.AlsoKnownAs)

	// Delivery was enqueued with a Move-shaped body.
	require.Equal(t, 1, deliverer.called)
	assert.Equal(t, "me", deliverer.signer)
	var body map[string]any
	require.NoError(t, json.Unmarshal(deliverer.body, &body))
	assert.Equal(t, "Move", body["type"])
	assert.Equal(t, srcURI, body["actor"])
	assert.Equal(t, srcURI, body["object"])
	assert.Equal(t, dstURI, body["target"])
	// To に follower collection URI が入る (一部 AP 実装が visibility 判定で参照)
	if to, ok := body["to"].([]any); ok {
		require.Len(t, to, 1)
		assert.Equal(t, srcURI+"/followers", to[0])
	} else {
		t.Fatalf("Move activity has no to addressing: %v", body)
	}
}

func TestMove_AlreadyMoved(t *testing.T) {
	svc, _, _ := newService(&fakeResolver{}, &fakeDeliverer{})
	existing := "https://other.example/users/x"
	me := &model.User{ID: "me", URI: strPtr("https://local.example/users/me"), MovedToURI: &existing}
	assert.ErrorIs(t, svc.Move(me, "https://other.example/users/new"), move.ErrAlreadyMoved)
}

func TestMove_RemoteSourceForbidden(t *testing.T) {
	svc, _, _ := newService(&fakeResolver{}, &fakeDeliverer{})
	host := "remote.example"
	me := &model.User{ID: "me", Host: &host, URI: strPtr("https://remote.example/users/me")}
	assert.ErrorIs(t, svc.Move(me, "https://other.example/users/new"), move.ErrRemoteSourceForbidden)
}

func TestMove_EmptyDestinationURI(t *testing.T) {
	svc, _, _ := newService(&fakeResolver{}, &fakeDeliverer{})
	me := &model.User{ID: "me"}
	assert.ErrorIs(t, svc.Move(me, ""), move.ErrNoSuchUser)
	assert.ErrorIs(t, svc.Move(me, "   "), move.ErrNoSuchUser)
}

func TestMove_ResolverError(t *testing.T) {
	resolver := &fakeResolver{err: errors.New("boom")}
	svc, _, _ := newService(resolver, &fakeDeliverer{})
	me := &model.User{ID: "me", URI: strPtr("https://local.example/users/me")}
	assert.ErrorIs(t, svc.Move(me, "https://other.example/users/new"), move.ErrNoSuchUser)
}

func TestMove_ResolverReturnsNil(t *testing.T) {
	resolver := &fakeResolver{byURI: map[string]*model.User{}}
	svc, _, _ := newService(resolver, &fakeDeliverer{})
	me := &model.User{ID: "me", URI: strPtr("https://local.example/users/me")}
	assert.ErrorIs(t, svc.Move(me, "https://other.example/users/new"), move.ErrNoSuchUser)
}

func TestMove_ResolverMissing(t *testing.T) {
	svc, _, _ := newService(nil, nil)
	me := &model.User{ID: "me", URI: strPtr("https://local.example/users/me")}
	assert.ErrorIs(t, svc.Move(me, "https://other.example/users/new"), move.ErrNoSuchUser)
}

func TestMove_AlsoKnownAsMissing(t *testing.T) {
	dstURI := "https://other.example/users/new"
	dst := &model.User{ID: "dst", URI: strPtr(dstURI), AlsoKnownAs: nil}
	resolver := &fakeResolver{byURI: map[string]*model.User{dstURI: dst}}
	svc, _, _ := newService(resolver, &fakeDeliverer{})
	me := &model.User{ID: "me", URI: strPtr("https://local.example/users/me")}
	assert.ErrorIs(t, svc.Move(me, dstURI), move.ErrDestinationForbids)
}

func TestMove_DestinationAlreadyMoved(t *testing.T) {
	srcURI := "https://local.example/users/me"
	dstURI := "https://other.example/users/new"
	other := "https://yet.another/users/z"
	dst := &model.User{
		ID:          "dst",
		URI:         strPtr(dstURI),
		AlsoKnownAs: strPtr(srcURI),
		MovedToURI:  &other,
	}
	resolver := &fakeResolver{byURI: map[string]*model.User{dstURI: dst}}
	svc, _, _ := newService(resolver, &fakeDeliverer{})
	me := &model.User{ID: "me", URI: strPtr(srcURI)}
	assert.ErrorIs(t, svc.Move(me, dstURI), move.ErrDestinationForbids)
}

func TestMove_AlsoKnownAsIncludesMultipleValues(t *testing.T) {
	// 自分の URI が csv の途中にあっても検出されることを確認する。
	srcURI := "https://local.example/users/me"
	dstURI := "https://other.example/users/new"
	csv := "https://foo/users/a, " + srcURI + " , https://bar/users/b"
	dst := &model.User{ID: "dst", URI: strPtr(dstURI), AlsoKnownAs: &csv}
	resolver := &fakeResolver{byURI: map[string]*model.User{dstURI: dst}}
	deliverer := &fakeDeliverer{}
	svc, userRepo, _ := newService(resolver, deliverer)
	me := &model.User{ID: "me", URI: strPtr(srcURI)}
	userRepo.Users[me.ID] = me

	require.NoError(t, svc.Move(me, dstURI))
	assert.Equal(t, 1, deliverer.called)
}

func TestMove_DelivererMissing_StillUpdatesDB(t *testing.T) {
	srcURI := "https://local.example/users/me"
	dstURI := "https://other.example/users/new"
	dst := &model.User{ID: "dst", URI: strPtr(dstURI), AlsoKnownAs: strPtr(srcURI)}
	resolver := &fakeResolver{byURI: map[string]*model.User{dstURI: dst}}
	svc, userRepo, _ := newService(resolver, nil)
	me := &model.User{ID: "me", URI: strPtr(srcURI)}
	userRepo.Users[me.ID] = me

	require.NoError(t, svc.Move(me, dstURI))
	require.NotNil(t, me.MovedToURI)
	assert.Equal(t, dstURI, *me.MovedToURI)
}

func TestMove_AppendAlsoKnownAsDedup(t *testing.T) {
	srcURI := "https://local.example/users/me"
	dstURI := "https://other.example/users/new"
	dst := &model.User{ID: "dst", URI: strPtr(dstURI), AlsoKnownAs: strPtr(srcURI)}
	resolver := &fakeResolver{byURI: map[string]*model.User{dstURI: dst}}
	svc, userRepo, _ := newService(resolver, &fakeDeliverer{})
	// me は既に dst を alsoKnownAs に入れている (2 回 Move を叩いたケース相当)
	existing := dstURI
	me := &model.User{ID: "me", URI: strPtr(srcURI), AlsoKnownAs: &existing}
	userRepo.Users[me.ID] = me

	require.NoError(t, svc.Move(me, dstURI))
	require.NotNil(t, me.AlsoKnownAs)
	// 重複は追加されない。
	assert.Equal(t, dstURI, *me.AlsoKnownAs)
}

// アカウント移行は不可逆なので、DB コミット後の配送失敗は握り潰して nil を
// 返す (そうしないとクライアントが再試行した際に ErrAlreadyMoved で詰む)。
func TestMove_DelivererErrorIsSwallowedAfterDBCommit(t *testing.T) {
	srcURI := "https://local.example/users/me"
	dstURI := "https://other.example/users/new"
	dst := &model.User{ID: "dst", URI: strPtr(dstURI), AlsoKnownAs: strPtr(srcURI)}
	resolver := &fakeResolver{byURI: map[string]*model.User{dstURI: dst}}
	deliverer := &fakeDeliverer{returnErr: errors.New("enqueue boom")}
	svc, userRepo, _ := newService(resolver, deliverer)
	me := &model.User{ID: "me", URI: strPtr(srcURI)}
	userRepo.Users[me.ID] = me

	require.NoError(t, svc.Move(me, dstURI))
	assert.Equal(t, 1, deliverer.called, "配送は実行されているべき")
	// DB は確実に更新されている。
	persisted := userRepo.Users["me"]
	require.NotNil(t, persisted.MovedToURI)
	assert.Equal(t, dstURI, *persisted.MovedToURI)
}

func TestMove_NilSource(t *testing.T) {
	svc, _, _ := newService(&fakeResolver{}, &fakeDeliverer{})
	assert.ErrorIs(t, svc.Move(nil, "https://other.example/users/new"), move.ErrNoSuchUser)
}

// UpdateUser がエラーを返した場合、エラーが伝搬して delivery は発生しない。
func TestMove_UserRepoUpdateError(t *testing.T) {
	srcURI := "https://local.example/users/me"
	dstURI := "https://other.example/users/new"
	dst := &model.User{ID: "dst", URI: strPtr(dstURI), AlsoKnownAs: strPtr(srcURI)}
	resolver := &fakeResolver{byURI: map[string]*model.User{dstURI: dst}}
	deliverer := &fakeDeliverer{}

	userRepo := testutil.NewMockUserRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	urls := activitypub.NewURLBuilder("https://local.example")
	renderer := activitypub.NewRenderer(urls)
	// UpdateUser がエラーを返すラッパー。
	failRepo := &failingUserRepo{MockUserRepository: userRepo}
	svc := move.NewService(failRepo, followingRepo, urls, renderer, resolver, deliverer)
	me := &model.User{ID: "me", URI: strPtr(srcURI)}

	err := svc.Move(me, dstURI)
	require.Error(t, err)
	assert.Equal(t, 0, deliverer.called, "update 失敗時は deliver されない")
}

type failingUserRepo struct {
	*testutil.MockUserRepository
}

func (f *failingUserRepo) UpdateUser(string, map[string]any) error {
	return errors.New("update boom")
}

// 解決後の dst.URI が空なら dstURI 引数をそのまま canonical として使うこと。
func TestMove_FallbackToInputURIWhenResolverURIEmpty(t *testing.T) {
	srcURI := "https://local.example/users/me"
	dstURI := "https://other.example/users/new"
	dst := &model.User{ID: "dst", URI: nil, AlsoKnownAs: strPtr(srcURI)}
	resolver := &fakeResolver{byURI: map[string]*model.User{dstURI: dst}}
	svc, userRepo, _ := newService(resolver, &fakeDeliverer{})
	me := &model.User{ID: "me", URI: strPtr(srcURI)}
	userRepo.Users[me.ID] = me

	require.NoError(t, svc.Move(me, dstURI))
	require.NotNil(t, me.MovedToURI)
	assert.Equal(t, dstURI, *me.MovedToURI)
}

// fakeFollowQueue captures the follow payloads scheduled for follower migration.
type fakeFollowQueue struct {
	calls     int
	payloads  []queue.FollowPayload
	returnErr error
}

func (f *fakeFollowQueue) EnqueueFollowBulk(payloads []queue.FollowPayload) error {
	f.calls++
	f.payloads = append(f.payloads, payloads...)
	return f.returnErr
}

// putLocalFollowing registers a Following row whose follower is local
// (followerHost nil)。リモートフォロワーは移行対象外なので host 有りと区別する。
// MockFollowingRepository.Followings は id をキーにした map なので、衝突しない
// 合成 id を振る。
func putLocalFollowing(repo *testutil.MockFollowingRepository, followerID, followeeID string) {
	id := followerID + "->" + followeeID
	repo.Followings[id] = &model.Following{ID: id, FollowerID: followerID, FolloweeID: followeeID}
}

func putRemoteFollowing(repo *testutil.MockFollowingRepository, followerID, followeeID, host string) {
	id := followerID + "->" + followeeID
	repo.Followings[id] = &model.Following{
		ID: id, FollowerID: followerID, FolloweeID: followeeID, FollowerHost: &host,
	}
}

// 移行するとローカルフォロワーが移行先を follow し直す (#2418)。
// upstream postMoveProcess の後半にあたる。
func TestMove_MigratesLocalFollowers(t *testing.T) {
	resolver := &fakeResolver{}
	svc, userRepo, followingRepo := newService(resolver, &fakeDeliverer{})
	fq := &fakeFollowQueue{}
	svc.SetFollowQueue(fq)

	src := &model.User{ID: "src", Username: "src", URI: strPtr("https://local.example/users/src")}
	userRepo.Users["src"] = src
	dstURI := "https://remote.example/users/dst"
	resolver.byURI = map[string]*model.User{dstURI: {
		ID:          "dst",
		URI:         strPtr(dstURI),
		AlsoKnownAs: strPtr("https://local.example/users/src"),
	}}
	putLocalFollowing(followingRepo, "localA", "src")
	putLocalFollowing(followingRepo, "localB", "src")
	putRemoteFollowing(followingRepo, "remoteC", "src", "remote.example")
	putLocalFollowing(followingRepo, "localD", "someone-else") // 別ユーザーのフォロワーは無関係

	require.NoError(t, svc.Move(src, dstURI))

	require.Equal(t, 1, fq.calls, "follow jobs must be scheduled in one bulk call")
	got := make([]string, 0, len(fq.payloads))
	for _, p := range fq.payloads {
		assert.Equal(t, "dst", p.FolloweeID, "followers must be migrated onto the destination")
		got = append(got, p.FollowerID)
	}
	assert.ElementsMatch(t, []string{"localA", "localB"}, got,
		"only local followers of the source are migrated")
}

// proxy account はリスト購読のため機械的に follow しているだけなので、移行先へ
// follow させない (upstream の followerId: Not(proxy.id))。
func TestMove_ExcludesProxyAccountFromMigration(t *testing.T) {
	resolver := &fakeResolver{}
	svc, userRepo, followingRepo := newService(resolver, &fakeDeliverer{})
	fq := &fakeFollowQueue{}
	svc.SetFollowQueue(fq)
	svc.SetProxyAccountResolver(func() (string, bool) { return "proxy", true })

	src := &model.User{ID: "src", Username: "src", URI: strPtr("https://local.example/users/src")}
	userRepo.Users["src"] = src
	dstURI := "https://remote.example/users/dst"
	resolver.byURI = map[string]*model.User{dstURI: {
		ID:          "dst",
		URI:         strPtr(dstURI),
		AlsoKnownAs: strPtr("https://local.example/users/src"),
	}}
	putLocalFollowing(followingRepo, "proxy", "src")
	putLocalFollowing(followingRepo, "localA", "src")

	require.NoError(t, svc.Move(src, dstURI))

	require.Len(t, fq.payloads, 1)
	assert.Equal(t, "localA", fq.payloads[0].FollowerID, "proxy account must be excluded")
}

// upstream は「unfollow せず following count だけ落とす」。旧アカウントがまだ
// 機能しうるので関係を切らない、という意図的な設計 (#2418)。
func TestMove_AdjustsCountsWithoutUnfollowing(t *testing.T) {
	resolver := &fakeResolver{}
	svc, userRepo, followingRepo := newService(resolver, &fakeDeliverer{})
	svc.SetFollowQueue(&fakeFollowQueue{})

	src := &model.User{
		ID: "src", Username: "src", URI: strPtr("https://local.example/users/src"),
		FollowersCount: 2, FollowingCount: 1,
	}
	userRepo.Users["src"] = src
	userRepo.Users["localA"] = &model.User{ID: "localA", FollowingCount: 5}
	userRepo.Users["followee1"] = &model.User{ID: "followee1", FollowersCount: 9}
	dstURI := "https://remote.example/users/dst"
	resolver.byURI = map[string]*model.User{dstURI: {
		ID:          "dst",
		URI:         strPtr(dstURI),
		AlsoKnownAs: strPtr("https://local.example/users/src"),
	}}
	putLocalFollowing(followingRepo, "localA", "src")
	putLocalFollowing(followingRepo, "src", "followee1") // src 自身のフォロー

	require.NoError(t, svc.Move(src, dstURI))

	assert.Equal(t, 0, userRepo.Users["src"].FollowersCount, "old account counters are zeroed")
	assert.Equal(t, 0, userRepo.Users["src"].FollowingCount)
	assert.Equal(t, 4, userRepo.Users["localA"].FollowingCount,
		"local follower's followingCount is decremented by 1")
	assert.Equal(t, 8, userRepo.Users["followee1"].FollowersCount,
		"followee of the old account loses one follower count")

	// フォロー行そのものは残る。ここが upstream の要点で、素直に unfollow へ
	// 置き換えると挙動が変わる。
	assert.Len(t, followingRepo.Followings, 2, "following rows must NOT be removed")
}

// フォロワーが 1 人も居ない移行ではカウント調整も enqueue も行わない。
func TestMove_NoFollowersSkipsMigration(t *testing.T) {
	resolver := &fakeResolver{}
	svc, userRepo, _ := newService(resolver, &fakeDeliverer{})
	fq := &fakeFollowQueue{}
	svc.SetFollowQueue(fq)

	src := &model.User{
		ID: "src", Username: "src", URI: strPtr("https://local.example/users/src"),
		FollowersCount: 3,
	}
	userRepo.Users["src"] = src
	dstURI := "https://remote.example/users/dst"
	resolver.byURI = map[string]*model.User{dstURI: {
		ID:          "dst",
		URI:         strPtr(dstURI),
		AlsoKnownAs: strPtr("https://local.example/users/src"),
	}}

	require.NoError(t, svc.Move(src, dstURI))

	assert.Zero(t, fq.calls, "no follow jobs when there are no local followers")
	assert.Equal(t, 3, userRepo.Users["src"].FollowersCount,
		"counters are left alone when there is nothing to migrate")
}

// queue の enqueue が失敗しても Move 自体は成功させる。DB は既にコミット済みで、
// エラーを返して再試行されると次回 ErrAlreadyMoved で詰むため。
func TestMove_FollowQueueFailureDoesNotFailMove(t *testing.T) {
	resolver := &fakeResolver{}
	svc, userRepo, followingRepo := newService(resolver, &fakeDeliverer{})
	svc.SetFollowQueue(&fakeFollowQueue{returnErr: errors.New("boom")})

	src := &model.User{ID: "src", Username: "src", URI: strPtr("https://local.example/users/src")}
	userRepo.Users["src"] = src
	dstURI := "https://remote.example/users/dst"
	resolver.byURI = map[string]*model.User{dstURI: {
		ID:          "dst",
		URI:         strPtr(dstURI),
		AlsoKnownAs: strPtr("https://local.example/users/src"),
	}}
	putLocalFollowing(followingRepo, "localA", "src")

	assert.NoError(t, svc.Move(src, dstURI))
	require.NotNil(t, src.MovedToURI, "the move itself still succeeded")
}

// countFailUserRepo はカウント更新だけ失敗する。PostMoveProcess は
// best-effort なので、これらが失敗しても移行は完了しフォロワー移行の
// enqueue まで進む。
type countFailUserRepo struct {
	*testutil.MockUserRepository
	failUpdate bool
}

func (f *countFailUserRepo) UpdateUser(userID string, fields map[string]any) error {
	if f.failUpdate {
		if _, ok := fields["followersCount"]; ok {
			return errors.New("counter reset boom")
		}
	}
	return f.MockUserRepository.UpdateUser(userID, fields)
}

func (f *countFailUserRepo) IncrementFollowingCount(string, int) error {
	return errors.New("decrement following boom")
}

func (f *countFailUserRepo) IncrementFollowersCount(string, int) error {
	return errors.New("decrement followers boom")
}

// listFailFollowingRepo は ListFolloweeIDs だけ失敗する。
type listFailFollowingRepo struct {
	*testutil.MockFollowingRepository
	failLocalFollowers bool
}

func (f *listFailFollowingRepo) ListLocalFollowerIDs(followeeID string) ([]string, error) {
	if f.failLocalFollowers {
		return nil, errors.New("list local followers boom")
	}
	return f.MockFollowingRepository.ListLocalFollowerIDs(followeeID)
}

func (f *listFailFollowingRepo) ListFolloweeIDs(string) ([]string, error) {
	return nil, errors.New("list followees boom")
}

// moveWithRepos wires a Service with the supplied repos and runs a successful move.
func moveWithRepos(t *testing.T, userRepo repository.UserRepository, followingRepo repository.FollowingRepository, fq move.FollowEnqueuer) error {
	t.Helper()
	dstURI := "https://remote.example/users/dst"
	resolver := &fakeResolver{byURI: map[string]*model.User{dstURI: {
		ID:          "dst",
		URI:         strPtr(dstURI),
		AlsoKnownAs: strPtr("https://local.example/users/src"),
	}}}
	urls := activitypub.NewURLBuilder("https://local.example")
	svc := move.NewService(userRepo, followingRepo, urls, activitypub.NewRenderer(urls), resolver, &fakeDeliverer{})
	if fq != nil {
		svc.SetFollowQueue(fq)
	}
	src := &model.User{ID: "src", Username: "src", URI: strPtr("https://local.example/users/src")}
	return svc.Move(src, dstURI)
}

// フォロワー一覧の取得に失敗しても移行そのものは成功させる (best-effort)。
// DB は既にコミット済みで、再試行させると ErrAlreadyMoved で詰むため。
func TestMove_ListFollowersFailureDoesNotFailMove(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	userRepo.Users["src"] = &model.User{ID: "src"}
	followingRepo := &listFailFollowingRepo{
		MockFollowingRepository: testutil.NewMockFollowingRepository(),
		failLocalFollowers:      true,
	}
	fq := &fakeFollowQueue{}

	require.NoError(t, moveWithRepos(t, userRepo, followingRepo, fq))
	assert.Zero(t, fq.calls, "no follow jobs when the follower list could not be read")
}

// カウント更新が軒並み失敗しても移行は完了し、フォロワー移行の enqueue まで進む。
func TestMove_CountAdjustmentFailuresAreBestEffort(t *testing.T) {
	mock := testutil.NewMockUserRepository()
	mock.Users["src"] = &model.User{ID: "src"}
	userRepo := &countFailUserRepo{MockUserRepository: mock, failUpdate: true}
	inner := testutil.NewMockFollowingRepository()
	putLocalFollowing(inner, "localA", "src")
	followingRepo := &listFailFollowingRepo{MockFollowingRepository: inner}
	fq := &fakeFollowQueue{}

	require.NoError(t, moveWithRepos(t, userRepo, followingRepo, fq))
	require.Len(t, fq.payloads, 1, "follower migration proceeds despite counter failures")
	assert.Equal(t, "localA", fq.payloads[0].FollowerID)
}

// queue 未配線でも移行は成立させる。フォロワー引き継ぎだけが行われない。
func TestMove_WithoutFollowQueueStillMoves(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	userRepo.Users["src"] = &model.User{ID: "src", FollowersCount: 1}
	followingRepo := testutil.NewMockFollowingRepository()
	putLocalFollowing(followingRepo, "localA", "src")

	require.NoError(t, moveWithRepos(t, userRepo, followingRepo, nil))
	// queue が無くてもカウント調整は済ませる (upstream の順序と同じ)。
	assert.Equal(t, 0, userRepo.Users["src"].FollowersCount)
}

// PostMoveProcess を直接呼んだときの nil ガード。
func TestPostMoveProcess_NilArgs(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	urls := activitypub.NewURLBuilder("https://local.example")
	svc := move.NewService(userRepo, followingRepo, urls, activitypub.NewRenderer(urls), &fakeResolver{}, &fakeDeliverer{})

	assert.NotPanics(t, func() { svc.PostMoveProcess(nil, &model.User{ID: "dst"}) })
	assert.NotPanics(t, func() { svc.PostMoveProcess(&model.User{ID: "src"}, nil) })
}
