package federation_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/activitypub"
	coreblocking "github.com/shiroha-a/mk/internal/core/blocking"
	"github.com/shiroha-a/mk/internal/core/federation"
	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const aliceActor = `{
	"@context": "https://www.w3.org/ns/activitystreams",
	"id": "https://remote.example/users/alice",
	"type": "Person",
	"preferredUsername": "alice",
	"name": "Alice",
	"inbox": "https://remote.example/users/alice/inbox",
	"publicKey": {"publicKeyPem": "PEM"}
}`

func newProcessor(t *testing.T, fetcherBody string) (*federation.Processor, *testutil.MockUserRepository, *testutil.MockFollowingRepository, *testutil.MockNoteRepository) {
	t.Helper()
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	resolver := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(fetcherBody)}, idGen)
	followingSvc := corefollowing.NewService(repo, followingRepo, testutil.NewMockFollowRequestRepository(), idGen)
	processor := federation.NewProcessor(resolver, followingSvc, nil, nil, repo, noteRepo)
	return processor, repo, followingRepo, noteRepo
}

func TestProcess_FollowHappyPath(t *testing.T) {
	p, repo, followingRepo, _ := newProcessor(t, aliceActor)
	// 受信側の自分 (local) を登録
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{
		"type": "Follow",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	require.NoError(t, p.Process(body))
	assert.Len(t, followingRepo.Followings, 1)
}

// stubInboundFollowAcceptor records SendAcceptForInboundFollow /
// SendRejectForInboundFollow calls for assertion.
type stubInboundFollowAcceptor struct {
	calls []struct {
		followerID, followeeID string
		followRaw              string
	}
	rejects []struct {
		followerID, followeeID string
		followRaw              string
	}
}

func (s *stubInboundFollowAcceptor) SendAcceptForInboundFollow(follower, followee *model.User, raw json.RawMessage) error {
	s.calls = append(s.calls, struct {
		followerID, followeeID string
		followRaw              string
	}{follower.ID, followee.ID, string(raw)})
	return nil
}

func (s *stubInboundFollowAcceptor) SendRejectForInboundFollow(follower, followee *model.User, raw json.RawMessage) error {
	s.rejects = append(s.rejects, struct {
		followerID, followeeID string
		followRaw              string
	}{follower.ID, followee.ID, string(raw)})
	return nil
}

func TestProcess_FollowInvokesAcceptorWithOriginalRaw(t *testing.T) {
	// inbound Follow が成立 (Following 作成) したら、original Follow の raw を
	// そのまま InboundFollowAcceptor に渡す。相手は自分の送った Follow.id と
	// 照合できるようになる。
	p, repo, _, _ := newProcessor(t, aliceActor)
	p.SetLocalBaseURL("https://example.com")
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	acceptor := &stubInboundFollowAcceptor{}
	p.SetInboundFollowAcceptor(acceptor)

	body := []byte(`{
		"type": "Follow",
		"id": "https://remote.example/follows/abc123",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	require.NoError(t, p.Process(body))
	require.Len(t, acceptor.calls, 1)
	assert.Equal(t, "bob", acceptor.calls[0].followeeID)
	// original の id を含んだ raw JSON が渡っていること。
	assert.Contains(t, acceptor.calls[0].followRaw, "https://remote.example/follows/abc123")
}

func TestProcess_FollowAlreadyFollowingStillSendsAccept(t *testing.T) {
	// 既に Following が成立している状態で再度 Follow が来た場合 (相手が
	// Accept を見逃したなど) も Accept を再送する。remote 側の pending
	// 状態を idempotent に解消するため。
	p, repo, followingRepo, _ := newProcessor(t, aliceActor)
	p.SetLocalBaseURL("https://example.com")
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	// 事前に Following を作っておく (remote alice → local bob)
	// alice の ID は aliceActor の ResolveActor 結果に依存するので一旦
	// Process を走らせて row を作らせる。
	body := []byte(`{
		"type": "Follow",
		"id": "https://remote.example/follows/first",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	require.NoError(t, p.Process(body))
	require.Len(t, followingRepo.Followings, 1)

	// 同じ Follow を再送 → ErrAlreadyFollowing で service.Follow は弾くが
	// acceptor は呼ばれるべき。
	acceptor := &stubInboundFollowAcceptor{}
	p.SetInboundFollowAcceptor(acceptor)
	retryBody := []byte(`{
		"type": "Follow",
		"id": "https://remote.example/follows/retry",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	require.NoError(t, p.Process(retryBody))
	require.Len(t, acceptor.calls, 1)
	assert.Contains(t, acceptor.calls[0].followRaw, "https://remote.example/follows/retry")
}

func TestProcess_FollowAlreadyRequestedStillSucceeds(t *testing.T) {
	// locked local user に対する再送 Follow。1回目で FollowRequest が作られ、
	// 2回目は ErrAlreadyRequested で弾かれるが handleFollow 側では吸収して
	// エラーを返さない (相手が retry を続けるのを防ぐ)。Accept は送らない
	// (まだ承認されていないので)。
	p, repo, _, _ := newProcessor(t, aliceActor)
	p.SetLocalBaseURL("https://example.com")
	// locked local user
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", IsLocked: true}
	acceptor := &stubInboundFollowAcceptor{}
	p.SetInboundFollowAcceptor(acceptor)

	body := []byte(`{
		"type": "Follow",
		"id": "https://remote.example/follows/first",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	require.NoError(t, p.Process(body))
	// 2回目もエラーを返さない
	require.NoError(t, p.Process(body))
	// locked 相手の Follow は Accept を送らない (承認時に送る)
	assert.Empty(t, acceptor.calls)
}

func TestProcess_FollowLocalUserByIDResolution(t *testing.T) {
	// 実本番のローカルユーザー row は user.uri が NULL のまま保存されるため、
	// FindByURI では解決できない。localBaseURL が設定されていれば
	// "{baseURL}/users/{id}" 形式の URI から ID を抜き出して FindByID で
	// lookup する経路が通るはず。
	p, repo, followingRepo, _ := newProcessor(t, aliceActor)
	p.SetLocalBaseURL("https://example.com")
	// URI を持たないローカルユーザー
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}

	body := []byte(`{
		"type": "Follow",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	require.NoError(t, p.Process(body))
	require.Len(t, followingRepo.Followings, 1)
	for _, f := range followingRepo.Followings {
		assert.Equal(t, "bob", f.FolloweeID)
	}
}

func TestProcess_FollowAlreadyFollowing(t *testing.T) {
	p, repo, followingRepo, _ := newProcessor(t, aliceActor)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}
	// alice → bob を resolver で予め解決させる
	body := []byte(`{
		"type": "Follow",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	require.NoError(t, p.Process(body))

	// 2回目: ErrAlreadyFollowing は飲み込まれる
	require.NoError(t, p.Process(body))
	assert.Len(t, followingRepo.Followings, 1)
}

func TestProcess_FollowUnknownFollowee(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	body := []byte(`{
		"type": "Follow",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/ghost"
	}`)
	err := p.Process(body)
	assert.Error(t, err)
}

func TestProcess_FollowResolveError(t *testing.T) {
	p, _, _, _ := newProcessor(t, "{not json")
	body := []byte(`{
		"type": "Follow",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	err := p.Process(body)
	assert.Error(t, err)
}

func TestProcess_FollowMissingObject(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice"}`)
	err := p.Process(body)
	assert.Error(t, err)
}

func TestProcess_UndoFollow(t *testing.T) {
	p, repo, followingRepo, _ := newProcessor(t, aliceActor)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	// First Follow
	follow := []byte(`{
		"type": "Follow",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	require.NoError(t, p.Process(follow))
	require.Len(t, followingRepo.Followings, 1)

	// Then Undo
	undo := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Follow",
			"actor": "https://remote.example/users/alice",
			"object": "https://example.com/users/bob"
		}
	}`)
	require.NoError(t, p.Process(undo))
	assert.Empty(t, followingRepo.Followings)
}

// #1948-21: locked local user 宛の Undo(Follow) は pending FollowRequest を取り消す
// (upstream undoFollow は cancelFollowRequest を呼ぶ。これが無いと phantom request が残る)。
func TestProcess_UndoFollowCancelsPendingRequest(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	followReqRepo := testutil.NewMockFollowRequestRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	resolver := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(aliceActor)}, idGen)
	followingSvc := corefollowing.NewService(repo, followingRepo, followReqRepo, idGen)
	p := federation.NewProcessor(resolver, followingSvc, nil, nil, repo, noteRepo)
	p.SetLocalBaseURL("https://example.com")
	p.SetInboundFollowAcceptor(&stubInboundFollowAcceptor{})
	// locked local user → Follow は FollowRequest を作る。
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", IsLocked: true}

	follow := []byte(`{
		"type": "Follow",
		"id": "https://remote.example/follows/1",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	require.NoError(t, p.Process(follow))
	received, _ := followReqRepo.CountReceived("bob")
	require.Equal(t, int64(1), received, "locked user への Follow で FollowRequest が作られる")

	undo := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Follow",
			"actor": "https://remote.example/users/alice",
			"object": "https://example.com/users/bob"
		}
	}`)
	require.NoError(t, p.Process(undo))
	received, _ = followReqRepo.CountReceived("bob")
	assert.Equal(t, int64(0), received, "Undo(Follow) で pending FollowRequest が削除される (#1948-21)")
}

// #1948-21: target が無い Move は actor を force-resolve しない (upstream は
// 'skip: invalid activity target')。
func TestProcess_MoveWithoutTargetSkips(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	body := []byte(`{"type":"Move","actor":"https://remote.example/users/alice"}`)
	require.NoError(t, p.Process(body))
	_, err := repo.FindByURI("https://remote.example/users/alice")
	assert.Error(t, err, "target が無い Move は actor を force-resolve しない (#1948-21)")
}

// #1948-21: target がある Move は actor を force-resolve する (upstream updatePerson)。
func TestProcess_MoveWithTargetResolvesActor(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	body := []byte(`{"type":"Move","actor":"https://remote.example/users/alice","target":"https://remote.example/users/alice2"}`)
	require.NoError(t, p.Process(body))
	u, err := repo.FindByURI("https://remote.example/users/alice")
	require.NoError(t, err, "target がある Move は actor を force-resolve する (#1948-21)")
	assert.NotNil(t, u)
}

func TestProcess_UndoUnknownType(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	undo := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {"type":"Like"}
	}`)
	err := p.Process(undo)
	assert.ErrorIs(t, err, federation.ErrUnsupportedActivity)
}

func TestProcess_UndoBadInner(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	undo := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": "not-a-json-object"
	}`)
	err := p.Process(undo)
	assert.Error(t, err)
}

func TestProcess_UndoNotFollowing(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	undo := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Follow",
			"actor": "https://remote.example/users/alice",
			"object": "https://example.com/users/bob"
		}
	}`)
	// 既にフォロー解除済みでもエラーにならない
	require.NoError(t, p.Process(undo))
}

func TestProcess_UndoResolveError(t *testing.T) {
	p, _, _, _ := newProcessor(t, "{not json")
	undo := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {"type":"Follow","actor":"x","object":"https://example.com/users/bob"}
	}`)
	err := p.Process(undo)
	assert.Error(t, err)
}

func TestProcess_UndoUnknownFollowee(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	undo := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {"type":"Follow","actor":"x","object":"https://example.com/users/ghost"}
	}`)
	err := p.Process(undo)
	assert.Error(t, err)
}

func TestProcess_UndoMissingObject(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	undo := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {"type":"Follow","actor":"x"}
	}`)
	err := p.Process(undo)
	assert.Error(t, err)
}

func TestProcess_Accept(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	body := []byte(`{"type":"Accept","actor":"https://remote.example/users/alice","object":{}}`)
	require.NoError(t, p.Process(body))
}

func TestProcess_UnsupportedType(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	body := []byte(`{"type":"Like","actor":"https://remote.example/users/alice"}`)
	assert.ErrorIs(t, p.Process(body), federation.ErrUnsupportedActivity)
}

func TestProcess_UnknownType(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	body := []byte(`{"type":"TentativeReject","actor":"https://remote.example/users/alice"}`)
	assert.ErrorIs(t, p.Process(body), federation.ErrUnsupportedActivity)
}

func TestProcess_BadJSON(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	err := p.Process([]byte(`{not json`))
	assert.Error(t, err)
}

func TestProcess_MissingActor(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	err := p.Process([]byte(`{"type":"Follow"}`))
	assert.Error(t, err)
}

// failingFollowingService isn't easy to construct since it's a concrete type;
// we exercise the non-AlreadyFollowing follow error path by using a follower
// who doesn't exist yet (resolver create works but follow service errors out
// because follower==followee). Use SelfFollow.
func TestProcess_UndoFollowWithNestedObject(t *testing.T) {
	p, repo, followingRepo, _ := newProcessor(t, aliceActor)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	// First follow
	follow := []byte(`{
		"type": "Follow",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	require.NoError(t, p.Process(follow))
	require.Len(t, followingRepo.Followings, 1)

	// Undo with nested object form (object: {id: ...})
	undo := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Follow",
			"object": {"id": "https://example.com/users/bob"}
		}
	}`)
	require.NoError(t, p.Process(undo))
	assert.Empty(t, followingRepo.Followings)
}

func TestProcess_UndoNestedObjectInvalid(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	// nested object missing id field
	undo := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Follow",
			"object": {"foo": "bar"}
		}
	}`)
	err := p.Process(undo)
	assert.Error(t, err)
}

func TestProcess_UndoNestedObjectBadJSON(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	// nested object is a JSON number — not string and not object
	undo := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Follow",
			"object": 42
		}
	}`)
	err := p.Process(undo)
	assert.Error(t, err)
}

// failingFollowingRepo causes Delete to fail (non-NotFollowing path).
type failingFollowingRepo struct {
	*testutil.MockFollowingRepository
}

func (f *failingFollowingRepo) Delete(_ *model.Following) error {
	return errors.New("delete failed")
}

func TestProcess_UndoFollowDeleteError(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	mockFR := testutil.NewMockFollowingRepository()
	// pre-existing follow record so Unfollow finds it
	mockFR.Followings["f1"] = &model.Following{ID: "f1", FollowerID: "alice", FolloweeID: "bob"}
	followingRepo := &failingFollowingRepo{MockFollowingRepository: mockFR}

	idGen, _ := id.NewGenerator("aidx")
	resolver := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(aliceActor)}, idGen)
	followingSvc := corefollowing.NewService(repo, followingRepo, testutil.NewMockFollowRequestRepository(), idGen)
	p := federation.NewProcessor(resolver, followingSvc, nil, nil, repo, noteRepo)

	// Setup followee bob
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}
	// Pre-resolve alice to ID "alice" by first calling resolver
	aliceURI := "https://remote.example/users/alice"
	repo.Users["alice"] = &model.User{ID: "alice", Username: "alice", URI: &aliceURI}

	undo := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Follow",
			"object": "https://example.com/users/bob"
		}
	}`)
	err := p.Process(undo)
	assert.Error(t, err)
}

func TestProcess_FollowSelfFollow(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	// alice/bob 同IDのケース: alice as both actor and target via URI alias
	uri := "https://remote.example/users/alice"
	// alice already in repo (resolver populates) ... we register the same user
	// as a local "followee" by setting URI.
	repo.Users["alice-local"] = &model.User{ID: "alice-local", Username: "alice", URI: &uri}
	// 直接呼ぶことで follower==followee を発生させる
	body := []byte(`{
		"type": "Follow",
		"actor": "https://remote.example/users/alice",
		"object": "https://remote.example/users/alice"
	}`)
	err := p.Process(body)
	// SelfFollow がそのまま伝搬する
	assert.Error(t, err)
}

// --- Block ---

func newProcessorWithBlocking(t *testing.T) (*federation.Processor, *testutil.MockUserRepository, *testutil.MockBlockingRepository) {
	t.Helper()
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	blockingRepo := testutil.NewMockBlockingRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	resolver := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(aliceActor)}, idGen)
	followingSvc := corefollowing.NewService(repo, followingRepo, testutil.NewMockFollowRequestRepository(), idGen)
	blockingSvc := coreblocking.NewService(repo, blockingRepo, followingRepo, idGen)
	processor := federation.NewProcessor(resolver, followingSvc, nil, nil, repo, noteRepo)
	processor.SetBlockingService(blockingSvc)
	return processor, repo, blockingRepo
}

func TestProcess_BlockHappyPath(t *testing.T) {
	p, repo, blockingRepo := newProcessorWithBlocking(t)
	// 実本番のローカルユーザー row は user.uri が NULL なので、この条件で
	// test する (handleBlock が resolveTargetUser で ID 解決することを検証)。
	p.SetLocalBaseURL("https://example.com")
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}

	body := []byte(`{
		"type": "Block",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	require.NoError(t, p.Process(body))
	// resolverがaliceを解決した際に生成されたIDを取得
	var aliceID string
	for _, u := range repo.Users {
		if u.Username == "alice" {
			aliceID = u.ID
			break
		}
	}
	require.NotEmpty(t, aliceID)
	exists, _ := blockingRepo.Exists(aliceID, "bob")
	assert.True(t, exists)
}

func TestProcess_BlockAlreadyBlocking(t *testing.T) {
	p, repo, _ := newProcessorWithBlocking(t)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{
		"type": "Block",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	// 2回ブロックしてもエラーにならない（冪等）
	require.NoError(t, p.Process(body))
	require.NoError(t, p.Process(body))
}

func TestProcess_BlockRemoteUser_Ignored(t *testing.T) {
	p, repo, _ := newProcessorWithBlocking(t)
	host := "remote2.example"
	charlieURI := "https://remote2.example/users/charlie"
	repo.Users["charlie"] = &model.User{ID: "charlie", Username: "charlie", URI: &charlieURI, Host: &host}

	body := []byte(`{
		"type": "Block",
		"actor": "https://remote.example/users/alice",
		"object": "https://remote2.example/users/charlie"
	}`)
	// リモートユーザーへのブロックは無視
	require.NoError(t, p.Process(body))
}

func TestProcess_UndoBlock(t *testing.T) {
	p, repo, blockingRepo := newProcessorWithBlocking(t)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	// まずブロック
	body := []byte(`{
		"type": "Block",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	require.NoError(t, p.Process(body))

	// resolverが生成したaliceのIDを取得
	var aliceID string
	for _, u := range repo.Users {
		if u.Username == "alice" {
			aliceID = u.ID
			break
		}
	}

	// Undo Block
	undoBody := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Block",
			"actor": "https://remote.example/users/alice",
			"object": "https://example.com/users/bob"
		}
	}`)
	require.NoError(t, p.Process(undoBody))
	exists, _ := blockingRepo.Exists(aliceID, "bob")
	assert.False(t, exists)
}

func TestProcess_UndoBlock_NotBlocking(t *testing.T) {
	p, repo, _ := newProcessorWithBlocking(t)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	// ブロックしていない状態でUndo Blockしてもエラーにならない
	undoBody := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Block",
			"actor": "https://remote.example/users/alice",
			"object": "https://example.com/users/bob"
		}
	}`)
	require.NoError(t, p.Process(undoBody))
}

// --- Flag ---

func TestProcess_FlagHappyPath(t *testing.T) {
	p, repo, _ := newProcessorWithBlocking(t)
	abuseRepo := testutil.NewMockAbuseReportRepository()
	idGenFlag, _ := id.NewGenerator("aidx")
	p.SetAbuseReportRepo(abuseRepo, idGenFlag)

	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{
		"type": "Flag",
		"actor": "https://remote.example/users/alice",
		"object": ["https://example.com/users/bob"],
		"content": "spam"
	}`)
	require.NoError(t, p.Process(body))
	assert.Len(t, abuseRepo.Reports, 1)
	for _, r := range abuseRepo.Reports {
		// #1560: comment は content + "\n" + JSON(flagged URIs)。
		assert.Equal(t, "spam\n[\"https://example.com/users/bob\"]", r.Comment)
	}
}

func TestProcess_FlagSingleURI(t *testing.T) {
	p, repo, _ := newProcessorWithBlocking(t)
	abuseRepo := testutil.NewMockAbuseReportRepository()
	idGenFlag, _ := id.NewGenerator("aidx")
	p.SetAbuseReportRepo(abuseRepo, idGenFlag)

	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{
		"type": "Flag",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob",
		"content": "abuse"
	}`)
	require.NoError(t, p.Process(body))
	assert.Len(t, abuseRepo.Reports, 1)
}

func TestProcess_FlagFromNote(t *testing.T) {
	p, repo, _ := newProcessorWithBlocking(t)
	abuseRepo := testutil.NewMockAbuseReportRepository()
	idGenFlag, _ := id.NewGenerator("aidx")
	p.SetAbuseReportRepo(abuseRepo, idGenFlag)
	noteRepo := testutil.NewMockNoteRepository()

	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}
	noteURI := "https://example.com/notes/n1"
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "bob", URI: &noteURI}

	// noteRepoはProcessor内のものとは別なので、直接ユーザーURIで解決する
	body := []byte(`{
		"type": "Flag",
		"actor": "https://remote.example/users/alice",
		"object": ["https://example.com/users/bob"],
		"content": "reported note"
	}`)
	require.NoError(t, p.Process(body))
	assert.Len(t, abuseRepo.Reports, 1)
}

// --- Move ---

func TestProcess_Move(t *testing.T) {
	p, _, _ := newProcessorWithBlocking(t)
	// target を伴う Move は actor を再解決する (target-less の skip は
	// TestProcess_MoveWithoutTargetSkips で別途 pin、#1948-21)。
	body := []byte(`{
		"type": "Move",
		"actor": "https://remote.example/users/alice",
		"object": "https://remote.example/users/alice",
		"target": "https://remote.example/users/alice2"
	}`)
	require.NoError(t, p.Process(body))
}

// #1948-21: target が AS Link object ({href:...}) 形式でも getApHrefNullable 相当に
// href を読んで受理する (readObjectString の {id} 読みでは誤 skip していた)。
func TestProcess_MoveTargetHrefObject(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	body := []byte(`{
		"type": "Move",
		"actor": "https://remote.example/users/alice",
		"target": {"type": "Link", "href": "https://remote.example/users/alice2"}
	}`)
	require.NoError(t, p.Process(body))
	u, err := repo.FindByURI("https://remote.example/users/alice")
	require.NoError(t, err, "{href} 形式 target でも actor を force-resolve する (#1948-21)")
	assert.NotNil(t, u)
}

// --- EmojiReaction ---

func TestProcess_EmojiReaction(t *testing.T) {
	p, repo, _ := newProcessorWithBlocking(t)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	// EmojiReaction typeはLikeと同じハンドラにルーティングされる
	// reactionServiceがnilなので ErrUnsupportedActivity が返る
	body := []byte(`{
		"type": "EmojiReaction",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/notes/n1",
		"content": "👍"
	}`)
	err := p.Process(body)
	assert.ErrorIs(t, err, federation.ErrUnsupportedActivity)
}

func TestProcess_EmojiReact(t *testing.T) {
	p, _, _ := newProcessorWithBlocking(t)
	body := []byte(`{
		"type": "EmojiReact",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/notes/n1",
		"content": "❤️"
	}`)
	err := p.Process(body)
	assert.ErrorIs(t, err, federation.ErrUnsupportedActivity)
}

// --- Block nil service ---

func TestProcess_BlockWithoutService(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	body := []byte(`{
		"type": "Block",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob"
	}`)
	assert.ErrorIs(t, p.Process(body), federation.ErrUnsupportedActivity)
}

// --- Add/Remove ---

func newProcessorWithPinning(t *testing.T) (*federation.Processor, *testutil.MockUserRepository, *testutil.MockNoteRepository, *testutil.MockUserNotePiningRepository) {
	t.Helper()
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	resolver := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(aliceActor)}, idGen)
	followingSvc := corefollowing.NewService(repo, followingRepo, testutil.NewMockFollowRequestRepository(), idGen)
	processor := federation.NewProcessor(resolver, followingSvc, nil, nil, repo, noteRepo)
	processor.SetPinningRepo(piningRepo, idGen)
	return processor, repo, noteRepo, piningRepo
}

// resolveAliceAndSetFeatured はalice actorを事前解決してFeaturedフィールドを設定する。
func resolveAliceAndSetFeatured(t *testing.T, p *federation.Processor, repo *testutil.MockUserRepository) string {
	t.Helper()
	// Followでaliceを解決させる（ダミーのfollowee不要、ResolveActorが呼ばれれば良い）
	dummyURI := "https://example.com/users/dummy"
	repo.Users["dummy"] = &model.User{ID: "dummy", Username: "dummy", URI: &dummyURI}
	_ = p.Process([]byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/dummy"}`))

	var aliceID string
	featured := "https://remote.example/users/alice/collections/featured"
	for _, u := range repo.Users {
		if u.Username == "alice" {
			aliceID = u.ID
			u.Featured = &featured
			break
		}
	}
	require.NotEmpty(t, aliceID)
	return aliceID
}

func TestProcess_Add(t *testing.T) {
	p, repo, noteRepo, piningRepo := newProcessorWithPinning(t)
	_ = resolveAliceAndSetFeatured(t, p, repo)

	noteURI := "https://remote.example/notes/n1"
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "alice", URI: &noteURI}

	body := []byte(`{
		"type": "Add",
		"actor": "https://remote.example/users/alice",
		"object": "https://remote.example/notes/n1",
		"target": "https://remote.example/users/alice/collections/featured"
	}`)
	require.NoError(t, p.Process(body))
	assert.True(t, len(piningRepo.Pinings) > 0)
}

func TestProcess_Add_WrongTarget(t *testing.T) {
	p, _, _, piningRepo := newProcessorWithPinning(t)
	// targetがfeaturedでない場合は無視
	body := []byte(`{
		"type": "Add",
		"actor": "https://remote.example/users/alice",
		"object": "https://remote.example/notes/n1",
		"target": "https://remote.example/users/alice/collections/other"
	}`)
	require.NoError(t, p.Process(body))
	assert.Empty(t, piningRepo.Pinings)
}

func TestProcess_Remove(t *testing.T) {
	p, repo, noteRepo, piningRepo := newProcessorWithPinning(t)
	_ = resolveAliceAndSetFeatured(t, p, repo)

	noteURI := "https://remote.example/notes/n1"
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "alice", URI: &noteURI}

	// まずAddでピン留め
	addBody := []byte(`{
		"type": "Add",
		"actor": "https://remote.example/users/alice",
		"object": "https://remote.example/notes/n1",
		"target": "https://remote.example/users/alice/collections/featured"
	}`)
	require.NoError(t, p.Process(addBody))
	require.True(t, len(piningRepo.Pinings) > 0)

	// Remove
	removeBody := []byte(`{
		"type": "Remove",
		"actor": "https://remote.example/users/alice",
		"object": "https://remote.example/notes/n1",
		"target": "https://remote.example/users/alice/collections/featured"
	}`)
	require.NoError(t, p.Process(removeBody))
}

func TestProcess_Remove_NotPinned(t *testing.T) {
	p, _, noteRepo, _ := newProcessorWithPinning(t)
	noteURI := "https://remote.example/notes/n1"
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "alice", URI: &noteURI}

	// ピン留めしていないノートのRemoveはエラーにならない
	body := []byte(`{
		"type": "Remove",
		"actor": "https://remote.example/users/alice",
		"object": "https://remote.example/notes/n1",
		"target": "https://remote.example/users/alice/collections/featured"
	}`)
	require.NoError(t, p.Process(body))
}

func TestProcess_AddWithoutRepo(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	body := []byte(`{
		"type": "Add",
		"actor": "https://remote.example/users/alice",
		"object": "https://remote.example/notes/n1",
		"target": "https://remote.example/users/alice/collections/featured"
	}`)
	assert.ErrorIs(t, p.Process(body), federation.ErrUnsupportedActivity)
}

func TestProcess_RemoveWithoutRepo(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	body := []byte(`{
		"type": "Remove",
		"actor": "https://remote.example/users/alice",
		"object": "https://remote.example/notes/n1",
		"target": "https://remote.example/users/alice/collections/featured"
	}`)
	assert.ErrorIs(t, p.Process(body), federation.ErrUnsupportedActivity)
}

// --- Accept ---

func TestProcess_AcceptFollow(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	// ローカルユーザーbob（follower）を登録
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}

	// aliceからのAcceptを処理（bobからaliceへのFollow承認）
	// ExtractLocalUserIDで /users/bob → ID "bob" と解決される
	acceptBody := []byte(`{
		"type": "Accept",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Follow",
			"actor": "https://example.com/users/bob",
			"object": "https://remote.example/users/alice"
		}
	}`)
	// AcceptRequest自体はフォローリクエストが存在しなくてもエラーにならない（nil返却）
	require.NoError(t, p.Process(acceptBody))
}

// upstream Misskey #17340 (triage #999) の actor normalization は inner
// activity (Accept / Reject / Undo の object 内 activity) にも適用する設計。
// inner.actor が {"id": "..."} embedded object 形式で送られてきた場合に、
// inner.normalizeActor(act.Object) 経由で string ID に展開され、後段の
// readActorString(inner) で follower URI が抽出できることを実証する。
// TestProcess_AcceptFollow と対になる "inner side" の coverage。
func TestProcess_AcceptFollow_InnerActorEmbeddedObject(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}

	acceptBody := []byte(`{
		"type": "Accept",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Follow",
			"actor": {"id": "https://example.com/users/bob", "type": "Person"},
			"object": "https://remote.example/users/alice"
		}
	}`)
	// FollowRequest が存在しなくても nil 返却 (= TestProcess_AcceptFollow と同じ
	// 挙動)。重要なのは inner.actor の embedded object が原因で missing actor
	// error を吐かないこと。
	require.NoError(t, p.Process(acceptBody))
}

func TestProcess_AcceptNonFollow(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	// inner objectがFollowでない場合は無視
	body := []byte(`{
		"type": "Accept",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Like",
			"actor": "https://example.com/users/bob"
		}
	}`)
	require.NoError(t, p.Process(body))
}

func TestProcess_AcceptBadObject(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	// objectが文字列（Follow IDのURI）の場合は無視
	body := []byte(`{
		"type": "Accept",
		"actor": "https://remote.example/users/alice",
		"object": "https://remote.example/follows/123"
	}`)
	require.NoError(t, p.Process(body))
}

func TestProcess_AcceptUnknownFollower(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	// followerが見つからない場合は無視
	body := []byte(`{
		"type": "Accept",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Follow",
			"actor": "https://example.com/users/ghost",
			"object": "https://remote.example/users/alice"
		}
	}`)
	require.NoError(t, p.Process(body))
}

// --- Question/Poll ---

func TestProcess_CreateQuestion(t *testing.T) {
	p, repo, _, noteRepo := newProcessor(t, aliceActor)
	// resolverにpollRepoを注入するためresolverへのアクセスが必要
	// newProcessorで作成されたresolverにSetPollRepoできないため、
	// processor経由でresolverを取得する方法がない。代わりにfull setup。
	pollRepo := testutil.NewMockPollRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen2, _ := id.NewGenerator("aidx")
	resolver := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(aliceActor)}, idGen2)
	resolver.SetPollRepo(pollRepo)
	followingSvc := corefollowing.NewService(repo, followingRepo, testutil.NewMockFollowRequestRepository(), idGen2)
	p = federation.NewProcessor(resolver, followingSvc, nil, nil, repo, noteRepo)

	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Question",
			"id": "https://remote.example/notes/q1",
			"attributedTo": "https://remote.example/users/alice",
			"content": "Best language?",
			"oneOf": [
				{"type": "Note", "name": "Go", "replies": {"type": "Collection", "totalItems": 5}},
				{"type": "Note", "name": "Rust", "replies": {"type": "Collection", "totalItems": 3}}
			],
			"endTime": "2026-12-31T23:59:59Z",
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}
	}`)
	require.NoError(t, p.Process(body))
	// ノートが作成されていること
	assert.True(t, len(noteRepo.Notes) > 0)
	// Pollが作成されていること
	assert.Len(t, pollRepo.Polls, 1)
	for _, poll := range pollRepo.Polls {
		assert.False(t, poll.Multiple) // oneOf → single choice
		assert.Len(t, poll.Choices, 2)
		assert.Equal(t, "Go", poll.Choices[0])
		assert.Equal(t, "Rust", poll.Choices[1])
	}
}

func TestProcess_CreateQuestionAnyOf(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	pollRepo := testutil.NewMockPollRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen2, _ := id.NewGenerator("aidx")
	resolver := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(aliceActor)}, idGen2)
	resolver.SetPollRepo(pollRepo)
	followingSvc := corefollowing.NewService(repo, followingRepo, testutil.NewMockFollowRequestRepository(), idGen2)
	p := federation.NewProcessor(resolver, followingSvc, nil, nil, repo, noteRepo)

	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Question",
			"id": "https://remote.example/notes/q2",
			"attributedTo": "https://remote.example/users/alice",
			"content": "Pick all that apply",
			"anyOf": [
				{"type": "Note", "name": "A"},
				{"type": "Note", "name": "B"},
				{"type": "Note", "name": "C"}
			],
			"closed": "2026-12-31T23:59:59Z",
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}
	}`)
	require.NoError(t, p.Process(body))
	assert.Len(t, pollRepo.Polls, 1)
	for _, poll := range pollRepo.Polls {
		assert.True(t, poll.Multiple) // anyOf → multiple choice
		assert.Len(t, poll.Choices, 3)
		assert.NotNil(t, poll.ExpiresAt) // closedから設定
	}
}

// --- ExtractLocalUserID ---

func TestExtractLocalUserID(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	urls := activitypub.NewURLBuilder("https://example.com")
	idGen, _ := id.NewGenerator("aidx")
	resolver := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(aliceActor)}, idGen)

	assert.Equal(t, "abc123", resolver.ExtractLocalUserID("https://example.com/users/abc123"))
	assert.Equal(t, "", resolver.ExtractLocalUserID("https://remote.example/users/abc123"))
	assert.Equal(t, "", resolver.ExtractLocalUserID(""))
}

func TestProcess_FlagWithoutRepo(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	body := []byte(`{
		"type": "Flag",
		"actor": "https://remote.example/users/alice",
		"object": ["https://example.com/users/bob"]
	}`)
	assert.ErrorIs(t, p.Process(body), federation.ErrUnsupportedActivity)
}

// fakeRelayMarker records MarkAccepted/MarkRejected calls for assertion.
type fakeRelayMarker struct {
	accepted []string
	rejected []string
	err      error
}

func (f *fakeRelayMarker) MarkAccepted(_ context.Context, id string) error {
	f.accepted = append(f.accepted, id)
	return f.err
}

func (f *fakeRelayMarker) MarkRejected(_ context.Context, id string) error {
	f.rejected = append(f.rejected, id)
	return f.err
}

func TestProcess_AcceptFollowRelay_MarksAccepted(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	marker := &fakeRelayMarker{}
	p.SetRelayMarker(marker)

	body := []byte(`{
		"type": "Accept",
		"actor": "https://remote.example/users/alice",
		"object": {
			"id": "https://example.com/activities/follow-relay/rel123",
			"type": "Follow",
			"actor": "https://example.com/users/relay.actor",
			"object": "https://www.w3.org/ns/activitystreams#Public"
		}
	}`)
	require.NoError(t, p.Process(body))
	assert.Equal(t, []string{"rel123"}, marker.accepted)
	assert.Empty(t, marker.rejected)
}

func TestProcess_RejectFollowRelay_MarksRejected(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	marker := &fakeRelayMarker{}
	p.SetRelayMarker(marker)

	body := []byte(`{
		"type": "Reject",
		"actor": "https://remote.example/users/alice",
		"object": {
			"id": "https://example.com/activities/follow-relay/rel456",
			"type": "Follow"
		}
	}`)
	require.NoError(t, p.Process(body))
	assert.Equal(t, []string{"rel456"}, marker.rejected)
}

func TestProcess_AcceptNonRelay_IgnoresMarker(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	marker := &fakeRelayMarker{}
	p.SetRelayMarker(marker)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{
		"type": "Accept",
		"actor": "https://remote.example/users/alice",
		"object": {
			"id": "https://remote.example/activities/follow/xyz",
			"type": "Follow",
			"actor": "https://example.com/users/bob",
			"object": "https://remote.example/users/alice"
		}
	}`)
	// フォローリクエストが無いので内部的には ErrRequestNotFound を飲み込む
	require.NoError(t, p.Process(body))
	// relay marker は呼ばれない
	assert.Empty(t, marker.accepted)
}

// --- Chat federation (Misskey:ChatMessage) ---

type stubChatReceiver struct {
	called    int
	lastURI   string
	lastFrom  *model.User
	lastTo    string
	lastText  string
	returnErr error
}

func (s *stubChatReceiver) CreateMessageViaAP(_ context.Context, uri string, fromUser *model.User, toUserID, text string) (*model.ChatMessage, error) {
	s.called++
	s.lastURI = uri
	s.lastFrom = fromUser
	s.lastTo = toUserID
	s.lastText = text
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return &model.ChatMessage{ID: "cm_ap_1"}, nil
}

func TestProcess_ChatMessage_HappyPath(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	// ローカルユーザーはURI==nil (本番と同じ)。ExtractLocalUserID→FindByIDで解決。
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	chatSvc := &stubChatReceiver{}
	p.SetChatService(chatSvc)

	body := []byte(`{
		"id": "https://remote.example/chat-messages/1",
		"type": "Misskey:ChatMessage",
		"actor": "https://remote.example/users/alice",
		"attributedTo": "https://remote.example/users/alice",
		"to": "https://example.com/users/bob",
		"content": "hello via AP"
	}`)
	require.NoError(t, p.Process(body))
	assert.Equal(t, 1, chatSvc.called)
	assert.Equal(t, "https://remote.example/chat-messages/1", chatSvc.lastURI)
	assert.Equal(t, "bob", chatSvc.lastTo)
	assert.Equal(t, "hello via AP", chatSvc.lastText)
}

func TestProcess_ChatMessage_NoChatService(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	body := []byte(`{
		"id": "https://remote.example/chat-messages/2",
		"type": "Misskey:ChatMessage",
		"actor": "https://remote.example/users/alice",
		"attributedTo": "https://remote.example/users/alice",
		"to": "https://example.com/users/bob",
		"content": "hi"
	}`)
	err := p.Process(body)
	assert.ErrorIs(t, err, federation.ErrUnsupportedActivity)
}

func TestProcess_ChatMessage_ActorMismatch(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	chatSvc := &stubChatReceiver{}
	p.SetChatService(chatSvc)
	body := []byte(`{
		"id": "https://remote.example/chat-messages/3",
		"type": "Misskey:ChatMessage",
		"actor": "https://remote.example/users/alice",
		"attributedTo": "https://remote.example/users/mallory",
		"to": "https://example.com/users/bob",
		"content": "spoof"
	}`)
	err := p.Process(body)
	assert.Error(t, err)
	assert.Equal(t, 0, chatSvc.called)
}

func TestProcess_ChatMessage_MissingTo(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	chatSvc := &stubChatReceiver{}
	p.SetChatService(chatSvc)
	body := []byte(`{
		"id": "https://remote.example/chat-messages/4",
		"type": "Misskey:ChatMessage",
		"actor": "https://remote.example/users/alice",
		"attributedTo": "https://remote.example/users/alice",
		"content": "no to"
	}`)
	err := p.Process(body)
	assert.Error(t, err)
}

// --- Chat federation (CherryPick: Create + Note(_misskey_talk:true), #692) ---
//
// CherryPick / レガシー Misskey の chat 連合 wire format。Create に Note を
// 包んで `_misskey_talk: true` flag を立てる形。to は string[] で、handleCreate
// が IngestNote の前に分岐して chatService にルートする。

func TestProcess_CreateChat_HappyPath_ToArray(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	chatSvc := &stubChatReceiver{}
	p.SetChatService(chatSvc)

	body := []byte(`{
		"id": "https://remote.example/activities/create-chat-1",
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"id": "https://remote.example/chat-messages/cm1",
			"type": "Note",
			"attributedTo": "https://remote.example/users/alice",
			"content": "yo via cherrypick",
			"to": ["https://example.com/users/bob"],
			"_misskey_talk": true
		}
	}`)
	require.NoError(t, p.Process(body))
	assert.Equal(t, 1, chatSvc.called)
	assert.Equal(t, "https://remote.example/chat-messages/cm1", chatSvc.lastURI)
	assert.Equal(t, "bob", chatSvc.lastTo)
	assert.Equal(t, "yo via cherrypick", chatSvc.lastText)
}

func TestProcess_CreateChat_HappyPath_ToString(t *testing.T) {
	// to が array でなく単一 string でも受け付ける (legacy 実装互換)。
	p, repo, _, _ := newProcessor(t, aliceActor)
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	chatSvc := &stubChatReceiver{}
	p.SetChatService(chatSvc)

	body := []byte(`{
		"id": "https://remote.example/activities/create-chat-2",
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"id": "https://remote.example/chat-messages/cm2",
			"type": "Note",
			"attributedTo": "https://remote.example/users/alice",
			"content": "single recipient",
			"to": "https://example.com/users/bob",
			"_misskey_talk": true
		}
	}`)
	require.NoError(t, p.Process(body))
	assert.Equal(t, 1, chatSvc.called)
	assert.Equal(t, "single recipient", chatSvc.lastText)
}

func TestProcess_CreateChat_FlagFalseFallsThroughToIngest(t *testing.T) {
	// `_misskey_talk` flag が false / 無い場合は chat 経路に分岐せず通常の
	// IngestNote 処理に流れる。chatService は呼ばれないことだけを assert。
	// IngestNote の実 persist 動作は newFullProcessor 経由の既存
	// TestProcess_CreateNote でカバーしている。
	p, _, _, _ := newProcessor(t, aliceActor)
	chatSvc := &stubChatReceiver{}
	p.SetChatService(chatSvc)
	body := []byte(`{
		"id": "https://remote.example/activities/create-note-1",
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"id": "https://remote.example/notes/n1",
			"type": "Note",
			"attributedTo": "https://remote.example/users/alice",
			"content": "regular post",
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}
	}`)
	_ = p.Process(body)
	assert.Equal(t, 0, chatSvc.called, "chat service must NOT be hit for non-chat notes")
}

// #692: handleChatCreate / readRecipientURI のエッジケース。

func TestProcess_CreateChat_EmptyTo(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	chatSvc := &stubChatReceiver{}
	p.SetChatService(chatSvc)
	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"id": "https://remote.example/chat-messages/cm-empty",
			"type": "Note",
			"attributedTo": "https://remote.example/users/alice",
			"content": "no recipient",
			"to": "",
			"_misskey_talk": true
		}
	}`)
	err := p.Process(body)
	assert.Error(t, err)
	assert.Equal(t, 0, chatSvc.called)
}

func TestProcess_CreateChat_ToNumber_Rejected(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	chatSvc := &stubChatReceiver{}
	p.SetChatService(chatSvc)
	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"id": "https://remote.example/chat-messages/cm-num",
			"type": "Note",
			"attributedTo": "https://remote.example/users/alice",
			"content": "weird",
			"to": 42,
			"_misskey_talk": true
		}
	}`)
	err := p.Process(body)
	assert.Error(t, err)
	assert.Equal(t, 0, chatSvc.called)
}

func TestProcess_CreateChat_EmptyArrayTo(t *testing.T) {
	p, _, _, _ := newProcessor(t, aliceActor)
	chatSvc := &stubChatReceiver{}
	p.SetChatService(chatSvc)
	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"id": "https://remote.example/chat-messages/cm-empty-arr",
			"type": "Note",
			"attributedTo": "https://remote.example/users/alice",
			"content": "no recipient",
			"to": [""],
			"_misskey_talk": true
		}
	}`)
	err := p.Process(body)
	assert.Error(t, err)
	assert.Equal(t, 0, chatSvc.called)
}

func TestProcess_CreateChat_RecipientNotFound(t *testing.T) {
	// FindByURI が err、ExtractLocalUserID も local URI でない → resolve 失敗。
	p, _, _, _ := newProcessor(t, aliceActor)
	chatSvc := &stubChatReceiver{}
	p.SetChatService(chatSvc)
	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"id": "https://remote.example/chat-messages/cm-no-recip",
			"type": "Note",
			"attributedTo": "https://remote.example/users/alice",
			"content": "x",
			"to": "https://other.example/users/unknown",
			"_misskey_talk": true
		}
	}`)
	err := p.Process(body)
	assert.Error(t, err)
	assert.Equal(t, 0, chatSvc.called)
}

func TestProcess_CreateChat_RecipientNotLocal(t *testing.T) {
	// recipient が remote 解決されると loopback delivery として拒絶する。
	p, repo, _, _ := newProcessor(t, aliceActor)
	remoteURI := "https://remote2.example/users/charlie"
	remoteHost := "remote2.example"
	repo.Users["charlie"] = &model.User{ID: "charlie", Username: "charlie", URI: &remoteURI, Host: &remoteHost}
	chatSvc := &stubChatReceiver{}
	p.SetChatService(chatSvc)

	body := []byte(`{
		"id": "https://remote.example/activities/create-chat-3",
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"id": "https://remote.example/chat-messages/cm3",
			"type": "Note",
			"attributedTo": "https://remote.example/users/alice",
			"content": "wrong recipient",
			"to": ["https://remote2.example/users/charlie"],
			"_misskey_talk": true
		}
	}`)
	err := p.Process(body)
	assert.Error(t, err)
	assert.Equal(t, 0, chatSvc.called)
}

// --- FanoutHook ---

// fakeFanoutHook records OnNoteCreated calls for assertion. #569 で
// federation processor の hook が async (safeGo 経由) になったので、
// 並行 append safety のため mu を持つ。caller 側は assert.Eventually
// 等で hook 反映を待つ。
type fakeFanoutHook struct {
	mu    sync.Mutex
	calls []fakeFanoutCall
}

type fakeFanoutCall struct {
	noteID   string
	authorID string
	note     *model.Note
}

func (f *fakeFanoutHook) OnNoteCreated(note *model.Note, author *model.User) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeFanoutCall{noteID: note.ID, authorID: author.ID, note: note})
	f.mu.Unlock()
}

func (f *fakeFanoutHook) snapshot() []fakeFanoutCall {
	f.mu.Lock()
	out := make([]fakeFanoutCall, len(f.calls))
	copy(out, f.calls)
	f.mu.Unlock()
	return out
}

func TestProcess_CreateCallsFanoutHook(t *testing.T) {
	p, _, _, noteRepo := newProcessor(t, aliceActor)
	hook := &fakeFanoutHook{}
	p.SetFanoutHook(hook)

	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Note",
			"id": "https://remote.example/notes/n1",
			"attributedTo": "https://remote.example/users/alice",
			"content": "Hello from remote",
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}
	}`)
	require.NoError(t, p.Process(body))
	// ノートが取り込まれたこと
	assert.True(t, len(noteRepo.Notes) > 0)
	// fanoutHookが呼ばれたこと (#569 で async になったので Eventually で待つ)
	require.Eventually(t, func() bool {
		return len(hook.snapshot()) == 1
	}, time.Second, 10*time.Millisecond)
	calls := hook.snapshot()
	assert.NotEmpty(t, calls[0].noteID)
	assert.NotEmpty(t, calls[0].authorID)
}

func TestProcess_AnnounceCallsFanoutHook(t *testing.T) {
	p, repo, _, noteRepo := newProcessor(t, aliceActor)
	hook := &fakeFanoutHook{}
	p.SetFanoutHook(hook)

	// Announceのターゲットとなるローカルノートを用意
	noteURI := "https://example.com/notes/local1"
	noteRepo.Notes["local1"] = &model.Note{
		ID:         "local1",
		UserID:     "bob",
		URI:        &noteURI,
		Visibility: model.NoteVisibilityPublic,
	}
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{
		"type": "Announce",
		"id": "https://remote.example/activities/announce1",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/notes/local1"
	}`)
	require.NoError(t, p.Process(body))
	// fanoutHookが呼ばれたこと (#1158 で async になったので Eventually で待つ)
	require.Eventually(t, func() bool {
		return len(hook.snapshot()) == 1
	}, time.Second, 10*time.Millisecond)
	calls := hook.snapshot()
	// Announce で作成された renote の ID が渡される
	assert.NotEqual(t, "local1", calls[0].noteID)
	// hydrateNoteForFanout により Renote relation が preload された状態で
	// 渡される (#416: streaming payload で renote が null にならない保証)。
	require.NotNil(t, calls[0].note)
	require.NotNil(t, calls[0].note.Renote, "Renote relation must be hydrated before fanout")
	assert.Equal(t, "local1", calls[0].note.Renote.ID)
}

func TestProcess_CreateWithoutFanoutHook(t *testing.T) {
	// fanoutHookがnilでもCreate処理はpanicしない
	p, _, _, noteRepo := newProcessor(t, aliceActor)

	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Note",
			"id": "https://remote.example/notes/n2",
			"attributedTo": "https://remote.example/users/alice",
			"content": "No hook",
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}
	}`)
	require.NoError(t, p.Process(body))
	assert.True(t, len(noteRepo.Notes) > 0)
}

func TestProcess_AnnounceWithoutFanoutHook(t *testing.T) {
	// fanoutHookがnilでもAnnounce処理はpanicしない
	p, repo, _, noteRepo := newProcessor(t, aliceActor)

	noteURI := "https://example.com/notes/local2"
	noteRepo.Notes["local2"] = &model.Note{
		ID:         "local2",
		UserID:     "bob",
		URI:        &noteURI,
		Visibility: model.NoteVisibilityPublic,
	}
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{
		"type": "Announce",
		"id": "https://remote.example/activities/announce2",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/notes/local2"
	}`)
	require.NoError(t, p.Process(body))
}

// --- NotificationHook (#415) ---

// fakeNotificationHook records OnNoteCreated calls.
type fakeNotificationHook struct {
	mu    sync.Mutex
	calls []fakeNotificationCall
}

type fakeNotificationCall struct {
	noteID       string
	authorID     string
	replyTarget  *model.Note
	renoteTarget *model.Note
}

func (f *fakeNotificationHook) OnNoteCreated(note *model.Note, author *model.User, replyTarget, renoteTarget *model.Note) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeNotificationCall{
		noteID:       note.ID,
		authorID:     author.ID,
		replyTarget:  replyTarget,
		renoteTarget: renoteTarget,
	})
	f.mu.Unlock()
}

func (f *fakeNotificationHook) snapshot() []fakeNotificationCall {
	f.mu.Lock()
	out := make([]fakeNotificationCall, len(f.calls))
	copy(out, f.calls)
	f.mu.Unlock()
	return out
}

func TestProcess_AnnounceCallsNotificationHook(t *testing.T) {
	// remote user が local note を renote すると元投稿者 (bob) への renote
	// 通知が生成される経路を確認する (#415)。
	p, repo, _, noteRepo := newProcessor(t, aliceActor)
	hook := &fakeNotificationHook{}
	p.SetNotificationHook(hook)

	noteURI := "https://example.com/notes/local1"
	noteRepo.Notes["local1"] = &model.Note{
		ID:         "local1",
		UserID:     "bob",
		URI:        &noteURI,
		Visibility: model.NoteVisibilityPublic,
	}
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{
		"type": "Announce",
		"id": "https://remote.example/activities/announce-notify",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/notes/local1"
	}`)
	require.NoError(t, p.Process(body))

	// notificationHook が呼ばれたこと (#1158 で async になったので Eventually で待つ)
	require.Eventually(t, func() bool {
		return len(hook.snapshot()) == 1
	}, time.Second, 10*time.Millisecond)
	calls := hook.snapshot()
	// renoteTarget に local note が渡る。hook 内部の notifyLocalUser が
	// renoteTarget.UserID == "bob" を見て bob へ通知を送る。
	require.NotNil(t, calls[0].renoteTarget)
	assert.Equal(t, "local1", calls[0].renoteTarget.ID)
	assert.Equal(t, "bob", calls[0].renoteTarget.UserID)
	assert.Nil(t, calls[0].replyTarget, "Announce は reply ではない")
}

func TestProcess_CreateCallsNotificationHook(t *testing.T) {
	// remote user の普通の Note 作成では notificationHook を通過させるが、
	// reply / renote target が無いので hook 側で通知は発火しない。呼び出しが
	// 発生することだけを確認する (reply target 検証は別途)。
	p, _, _, noteRepo := newProcessor(t, aliceActor)
	hook := &fakeNotificationHook{}
	p.SetNotificationHook(hook)

	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Note",
			"id": "https://remote.example/notes/n-notify",
			"attributedTo": "https://remote.example/users/alice",
			"content": "Hello from remote",
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}
	}`)
	require.NoError(t, p.Process(body))

	assert.True(t, len(noteRepo.Notes) > 0)
	require.Eventually(t, func() bool {
		return len(hook.snapshot()) == 1
	}, time.Second, 10*time.Millisecond)
	calls := hook.snapshot()
	assert.Nil(t, calls[0].replyTarget)
	assert.Nil(t, calls[0].renoteTarget)
}

func TestProcess_CreateReplyPassesReplyTarget(t *testing.T) {
	// Remote reply to a local note → hook に replyTarget として local note が
	// 渡る (#415 reply 通知経路)。
	p, _, _, noteRepo := newProcessor(t, aliceActor)
	hook := &fakeNotificationHook{}
	p.SetNotificationHook(hook)

	localURI := "https://example.com/notes/local-parent"
	noteRepo.Notes["local-parent"] = &model.Note{
		ID:         "local-parent",
		UserID:     "bob",
		URI:        &localURI,
		Visibility: model.NoteVisibilityPublic,
	}

	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Note",
			"id": "https://remote.example/notes/reply1",
			"attributedTo": "https://remote.example/users/alice",
			"inReplyTo": "https://example.com/notes/local-parent",
			"content": "remote reply",
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}
	}`)
	require.NoError(t, p.Process(body))

	require.Eventually(t, func() bool {
		return len(hook.snapshot()) == 1
	}, time.Second, 10*time.Millisecond)
	calls := hook.snapshot()
	require.NotNil(t, calls[0].replyTarget, "replyTarget must be set for inReplyTo=local")
	assert.Equal(t, "local-parent", calls[0].replyTarget.ID)
}

// #1931: AP 経由で取り込んだ reply note も threadId を thread root に揃える
// (local create path #1928 と同 logic)。これが無いと remote thread の deep muting が効かない。
func TestProcess_CreateReplySetsThreadID(t *testing.T) {
	p, _, _, noteRepo := newProcessor(t, aliceActor)
	localURI := "https://example.com/notes/root-parent"
	// root-parent は threadId 無し (= thread root)。
	noteRepo.Notes["root-parent"] = &model.Note{
		ID: "root-parent", UserID: "bob", URI: &localURI, Visibility: model.NoteVisibilityPublic,
	}
	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Note",
			"id": "https://remote.example/notes/reply1",
			"attributedTo": "https://remote.example/users/alice",
			"inReplyTo": "https://example.com/notes/root-parent",
			"content": "remote reply",
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}
	}`)
	require.NoError(t, p.Process(body))
	require.Eventually(t, func() bool {
		n, err := noteRepo.FindByURI("https://remote.example/notes/reply1")
		return err == nil && n != nil
	}, time.Second, 10*time.Millisecond)
	created, err := noteRepo.FindByURI("https://remote.example/notes/reply1")
	require.NoError(t, err)
	require.NotNil(t, created.ThreadID)
	assert.Equal(t, "root-parent", *created.ThreadID, "AP reply の threadId は root.id を指す")
}

// #1931: 深い AP reply (親が既に threadId を持つ) は root threadId を継承する。
func TestProcess_CreateReplyInheritsThreadID(t *testing.T) {
	p, _, _, noteRepo := newProcessor(t, aliceActor)
	rootThread := "root"
	localURI := "https://example.com/notes/mid-parent"
	noteRepo.Notes["mid-parent"] = &model.Note{
		ID: "mid-parent", UserID: "bob", URI: &localURI, Visibility: model.NoteVisibilityPublic, ThreadID: &rootThread,
	}
	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Note",
			"id": "https://remote.example/notes/reply2",
			"attributedTo": "https://remote.example/users/alice",
			"inReplyTo": "https://example.com/notes/mid-parent",
			"content": "deep remote reply",
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}
	}`)
	require.NoError(t, p.Process(body))
	require.Eventually(t, func() bool {
		n, err := noteRepo.FindByURI("https://remote.example/notes/reply2")
		return err == nil && n != nil
	}, time.Second, 10*time.Millisecond)
	created, err := noteRepo.FindByURI("https://remote.example/notes/reply2")
	require.NoError(t, err)
	require.NotNil(t, created.ThreadID)
	assert.Equal(t, "root", *created.ThreadID, "深い AP reply は root threadId を継承")
}

func TestProcess_CreateWithoutNotificationHook(t *testing.T) {
	// notificationHook が nil でも Create は panic しない。
	p, _, _, noteRepo := newProcessor(t, aliceActor)

	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Note",
			"id": "https://remote.example/notes/no-notif",
			"attributedTo": "https://remote.example/users/alice",
			"content": "No notif",
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}
	}`)
	require.NoError(t, p.Process(body))
	assert.True(t, len(noteRepo.Notes) > 0)
}

func TestProcess_AnnounceWithoutNotificationHook(t *testing.T) {
	// notificationHook が nil でも Announce は panic しない。
	p, repo, _, noteRepo := newProcessor(t, aliceActor)

	noteURI := "https://example.com/notes/local3"
	noteRepo.Notes["local3"] = &model.Note{
		ID:         "local3",
		UserID:     "bob",
		URI:        &noteURI,
		Visibility: model.NoteVisibilityPublic,
	}
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{
		"type": "Announce",
		"id": "https://remote.example/activities/announce3",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/notes/local3"
	}`)
	require.NoError(t, p.Process(body))
}

// findIngestedRemoteNote returns the single non-seed note ingested by a Create
// Process call. Seed notes set up by a test live under their own keys; the
// freshly ingested one carries the given remote URI.
func findIngestedRemoteNote(t *testing.T, noteRepo *testutil.MockNoteRepository, uri string) *model.Note {
	t.Helper()
	n, err := noteRepo.FindByURI(uri)
	require.NoError(t, err, "ingested note %s should be persisted", uri)
	return n
}

// TestProcess_CreateNote_AudienceOnActivityDerivesVisibility seals the #1560
// fix: a Create whose to/cc audience lives on the activity (not on the Note
// object) must still derive the correct visibility. Before the fix, the Note
// object's empty to/cc yielded "specified" regardless of the activity audience.
func TestProcess_CreateNote_AudienceOnActivityDerivesVisibility(t *testing.T) {
	cases := []struct {
		name       string
		activityTo string
		activityCC string
		want       model.NoteVisibility
	}{
		{
			name:       "public_from_activity_to",
			activityTo: `["https://www.w3.org/ns/activitystreams#Public"]`,
			activityCC: `["https://remote.example/users/alice/followers"]`,
			want:       model.NoteVisibilityPublic,
		},
		{
			name:       "home_from_activity_to_cc",
			activityTo: `["https://remote.example/users/alice/followers"]`,
			activityCC: `["https://www.w3.org/ns/activitystreams#Public"]`,
			want:       model.NoteVisibilityHome,
		},
		{
			name:       "followers_from_activity_to",
			activityTo: `["https://remote.example/users/alice/followers"]`,
			activityCC: `[]`,
			want:       model.NoteVisibilityFollowers,
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _, _, noteRepo := newProcessor(t, aliceActor)
			noteURI := "https://remote.example/notes/aud" + string(rune('a'+i))
			// Note object 自身には to/cc を一切載せない。audience は activity 側のみ。
			body := []byte(`{
				"type": "Create",
				"actor": "https://remote.example/users/alice",
				"to": ` + tc.activityTo + `,
				"cc": ` + tc.activityCC + `,
				"object": {
					"type": "Note",
					"id": "` + noteURI + `",
					"attributedTo": "https://remote.example/users/alice",
					"content": "hi"
				}
			}`)
			require.NoError(t, p.Process(body))
			note := findIngestedRemoteNote(t, noteRepo, noteURI)
			assert.Equal(t, tc.want, note.Visibility)
		})
	}
}

// TestProcess_CreateNote_SpecifiedVisibleUserIDsFromActivityTo confirms that
// for a "specified" note whose recipient URI is carried only on the activity
// to, the unioned audience drives visibleUserIds resolution (#1560).
func TestProcess_CreateNote_SpecifiedVisibleUserIDsFromActivityTo(t *testing.T) {
	p, repo, _, noteRepo := newProcessor(t, aliceActor)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	noteURI := "https://remote.example/notes/spec1"
	// to に local user URI のみ (Public/followers 無し) → specified。
	// recipient URI も activity 側にしか無い。
	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"to": ["https://example.com/users/bob"],
		"object": {
			"type": "Note",
			"id": "` + noteURI + `",
			"attributedTo": "https://remote.example/users/alice",
			"content": "dm"
		}
	}`)
	require.NoError(t, p.Process(body))
	note := findIngestedRemoteNote(t, noteRepo, noteURI)
	assert.Equal(t, model.NoteVisibilitySpecified, note.Visibility)
	assert.Equal(t, []string{"bob"}, []string(note.VisibleUserIDs))
}

// TestProcess_CreateNote_AttributedToFilledFromActor verifies that a Note object
// lacking attributedTo is no longer rejected as invalid: the activity actor is
// used to fill it before ingest, matching upstream create() (#1560).
func TestProcess_CreateNote_AttributedToFilledFromActor(t *testing.T) {
	p, repo, _, noteRepo := newProcessor(t, aliceActor)
	noteURI := "https://remote.example/notes/noattr1"
	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"to": ["https://www.w3.org/ns/activitystreams#Public"],
		"object": {
			"type": "Note",
			"id": "` + noteURI + `",
			"content": "no attributedTo on object"
		}
	}`)
	require.NoError(t, p.Process(body))
	note := findIngestedRemoteNote(t, noteRepo, noteURI)
	// attributedTo を activity.actor から補ったので ingest は成功し、note は
	// 解決済み actor (alice, generated ID) に紐づく。
	require.NotEmpty(t, note.UserID)
	alice := repo.Users[note.UserID]
	require.NotNil(t, alice, "ingested note should belong to the resolved actor")
	assert.Equal(t, "alice", alice.Username)
	assert.Equal(t, model.NoteVisibilityPublic, note.Visibility)
}

// TestProcess_CreateNote_ObjectAudienceStillHonored is a non-regression guard:
// when the Note object already carries to/cc and the activity does not, the
// union is a no-op and visibility is unchanged (#1560).
func TestProcess_CreateNote_ObjectAudienceStillHonored(t *testing.T) {
	p, _, _, noteRepo := newProcessor(t, aliceActor)
	noteURI := "https://remote.example/notes/objaud1"
	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Note",
			"id": "` + noteURI + `",
			"attributedTo": "https://remote.example/users/alice",
			"to": ["https://www.w3.org/ns/activitystreams#Public"],
			"content": "object carries audience"
		}
	}`)
	require.NoError(t, p.Process(body))
	note := findIngestedRemoteNote(t, noteRepo, noteURI)
	assert.Equal(t, model.NoteVisibilityPublic, note.Visibility)
}

// #2106 L30: remote followee 宛の Undo(Follow) は skip される (handleFollow gate と対称)。
func TestProcess_UndoFollow_RemoteFolloweeSkipped(t *testing.T) {
	p, repo, _, _ := newProcessor(t, aliceActor)
	bobURI := "https://remote2.example/users/bob"
	bobHost := "remote2.example"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI, Host: &bobHost}

	undo := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Follow",
			"actor": "https://remote.example/users/alice",
			"object": "https://remote2.example/users/bob"
		}
	}`)
	require.NoError(t, p.Process(undo)) // remote followee なので skip (nil)
}

// #2106 L30: remote blockee 宛の Undo(Block) は skip される (handleBlock gate と対称)。
func TestProcess_UndoBlock_RemoteBlockeeSkipped(t *testing.T) {
	p, repo, _ := newProcessorWithBlocking(t)
	bobURI := "https://remote2.example/users/bob"
	bobHost := "remote2.example"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI, Host: &bobHost}

	undoBody := []byte(`{
		"type": "Undo",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Block",
			"actor": "https://remote.example/users/alice",
			"object": "https://remote2.example/users/bob"
		}
	}`)
	require.NoError(t, p.Process(undoBody)) // remote blockee なので skip (nil)
}

// upstream 2026.7.0 #17747: AP 受信の reply も reply 先より広い可視性には
// できない。upstream の ApNoteService は NoteCreateService.create 経由なので
// 同じクランプが掛かる。これが無いと「ローカルの followers 限定 note へ
// リモートが to:[Public] で返信」でローカル TL / 再配送に文脈が漏れる。
func TestProcess_CreateReplyClampsVisibilityToTarget(t *testing.T) {
	p, _, _, noteRepo := newProcessor(t, aliceActor)
	localURI := "https://example.com/notes/followers-parent"
	noteRepo.Notes["followers-parent"] = &model.Note{
		ID: "followers-parent", UserID: "bob", URI: &localURI, Visibility: model.NoteVisibilityFollowers,
	}
	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Note",
			"id": "https://remote.example/notes/reply-clamp",
			"attributedTo": "https://remote.example/users/alice",
			"inReplyTo": "https://example.com/notes/followers-parent",
			"content": "remote public reply",
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}
	}`)
	require.NoError(t, p.Process(body))
	require.Eventually(t, func() bool {
		n, err := noteRepo.FindByURI("https://remote.example/notes/reply-clamp")
		return err == nil && n != nil
	}, time.Second, 10*time.Millisecond)
	created, err := noteRepo.FindByURI("https://remote.example/notes/reply-clamp")
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityFollowers, created.Visibility,
		"followers 限定 note への public reply は followers に降格される")
}

// #17747 clamp で specified に降格したリモート reply は、reply 先の作者を
// visibleUserIds に含める必要がある (apNote.To が Public だけだと宛先が 1 件も
// 解決できず、DM の当事者すら本文を参照できない note が残る)。
func TestProcess_CreateReplyClampedToSpecifiedKeepsTargetVisible(t *testing.T) {
	p, _, _, noteRepo := newProcessor(t, aliceActor)
	localURI := "https://example.com/notes/specified-parent"
	noteRepo.Notes["specified-parent"] = &model.Note{
		ID: "specified-parent", UserID: "bob", URI: &localURI,
		Visibility: model.NoteVisibilitySpecified,
	}
	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Note",
			"id": "https://remote.example/notes/reply-specified",
			"attributedTo": "https://remote.example/users/alice",
			"inReplyTo": "https://example.com/notes/specified-parent",
			"content": "remote reply to DM",
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}
	}`)
	require.NoError(t, p.Process(body))
	require.Eventually(t, func() bool {
		n, err := noteRepo.FindByURI("https://remote.example/notes/reply-specified")
		return err == nil && n != nil
	}, time.Second, 10*time.Millisecond)
	created, err := noteRepo.FindByURI("https://remote.example/notes/reply-specified")
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilitySpecified, created.Visibility)
	assert.Contains(t, []string(created.VisibleUserIDs), "bob",
		"reply 先の作者が visibleUserIds に入っていないと当人が本文を読めない")
}

// 同じ Create(Note) が再配送されても通知は 1 度しか出ないこと。
//
// **通知は冪等ではない。** 呼ぶたびに新しい id で 1 件積まれるので、配送リトライや
// inbox / sharedInbox への二重投げがあると、受信者の通知一覧に同じメンションや
// DM が 2 件並ぶ。実運用のインスタンスでは日常的に起きる (実験環境では配送が
// 一度きりなので再現しない)。fanout は note id で冪等なので従来どおり発火する。
func TestProcess_CreateDoesNotRenotifyOnRedelivery(t *testing.T) {
	p, _, _, noteRepo := newProcessor(t, aliceActor)
	hook := &fakeNotificationHook{}
	p.SetNotificationHook(hook)

	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"object": {
			"type": "Note",
			"id": "https://remote.example/notes/n-redelivered",
			"attributedTo": "https://remote.example/users/alice",
			"content": "Hello again",
			"to": ["https://www.w3.org/ns/activitystreams#Public"]
		}
	}`)

	require.NoError(t, p.Process(body))
	require.Eventually(t, func() bool {
		return len(hook.snapshot()) == 1
	}, time.Second, 10*time.Millisecond)
	notesAfterFirst := len(noteRepo.Notes)

	// 同じ activity をもう一度受ける (= 配送リトライ / 二重投げ)。
	require.NoError(t, p.Process(body))

	// ノートは増えない (URI で dedup される)。
	assert.Equal(t, notesAfterFirst, len(noteRepo.Notes))
	// 通知も増えないこと。**ここが増えると一覧に同じ通知が 2 件並ぶ。**
	assert.Never(t, func() bool {
		return len(hook.snapshot()) > 1
	}, 200*time.Millisecond, 20*time.Millisecond)
}
