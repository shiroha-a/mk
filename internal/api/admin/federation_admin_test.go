package admin_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	apiadmin "github.com/shiroha-a/mk/internal/api/admin"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFederationDeleteAllFiles(t *testing.T) {
	// repo 未配線 / host 未指定は 204 no-op
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.FederationDeleteAllFiles, `{}`, adminUser).Code)
}

// host 指定 + driveFileRepo 配線時は対象ホストの DriveFile が削除される。
// 他ホストおよびローカルファイルは残ること。本実装は #587 で追加。
func TestFederationDeleteAllFiles_DeletesByHost(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockDriveFileRepository()
	host := "remote.example"
	other := "other.example"
	require.NoError(t, repo.Create(&model.DriveFile{ID: "f1", UserHost: &host}))
	require.NoError(t, repo.Create(&model.DriveFile{ID: "f2", UserHost: &host}))
	require.NoError(t, repo.Create(&model.DriveFile{ID: "f3", UserHost: &other}))
	require.NoError(t, repo.Create(&model.DriveFile{ID: "f4"})) // local (UserHost = nil)
	h.SetDriveFileRepo(repo)

	rec := doPost(h.FederationDeleteAllFiles, `{"host":"remote.example"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.NotContains(t, repo.Files, "f1")
	assert.NotContains(t, repo.Files, "f2")
	assert.Contains(t, repo.Files, "f3", "他ホストのファイルは残る")
	assert.Contains(t, repo.Files, "f4", "ローカルファイルは残る")
}

func TestFederationRefreshRemoteInstanceMetadata(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// fetcher 未設定で host 未指定 (stub 相当の呼出) → 204 で no-op
	assert.Equal(t, http.StatusNoContent, doPost(h.FederationRefreshRemoteInstanceMetadata, `{}`, adminUser).Code)
}

// stubInstanceMetadataFetcher records Fetch calls for assertion.
type stubInstanceMetadataFetcher struct {
	calls []string
	err   error
}

func (s *stubInstanceMetadataFetcher) Fetch(host string) error {
	s.calls = append(s.calls, host)
	return s.err
}

func TestFederationRefreshRemoteInstanceMetadata_CallsFetcher(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	fetcher := &stubInstanceMetadataFetcher{}
	h.SetInstanceMetadataFetcher(fetcher)
	assert.Equal(t, http.StatusNoContent,
		doPost(h.FederationRefreshRemoteInstanceMetadata, `{"host":"remote.example"}`, adminUser).Code)
	assert.Equal(t, []string{"remote.example"}, fetcher.calls)
}

func TestFederationRefreshRemoteInstanceMetadata_EmptyHostNoCall(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	fetcher := &stubInstanceMetadataFetcher{}
	h.SetInstanceMetadataFetcher(fetcher)
	assert.Equal(t, http.StatusNoContent,
		doPost(h.FederationRefreshRemoteInstanceMetadata, `{}`, adminUser).Code)
	// host 未指定で fetcher は叩かれない
	assert.Empty(t, fetcher.calls)
}

func TestFederationRefreshRemoteInstanceMetadata_FetchError_Still204(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	fetcher := &stubInstanceMetadataFetcher{err: errors.New("net down")}
	h.SetInstanceMetadataFetcher(fetcher)
	// fetch 失敗してもクライアントには 204 を返す (ログのみ)
	assert.Equal(t, http.StatusNoContent,
		doPost(h.FederationRefreshRemoteInstanceMetadata, `{"host":"remote.example"}`, adminUser).Code)
	assert.Equal(t, []string{"remote.example"}, fetcher.calls)
}

func TestFederationRemoveAllFollowing(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.FederationRemoveAllFollowing, `{}`, adminUser).Code)
}

// stubUnfollowEnqueuer records every Unfollow payload so tests can assert
// which (follower, followee) pairs the admin handler scheduled. Misskey TS
// の queueService.createUnfollowJob 相当を mock するための test double。
type stubUnfollowEnqueuer struct {
	pairs [][2]string
	err   error
}

func (s *stubUnfollowEnqueuer) EnqueueUnfollow(p queue.UnfollowPayload) error {
	s.pairs = append(s.pairs, [2]string{p.FollowerID, p.FolloweeID})
	return s.err
}

// host 指定 + 依存全配線時、followerHost = host の Following row 全てに対して
// Unfollow ジョブが enqueue されること。実 row 削除と Reject(Follow) 配送は
// worker (processors.UnfollowProcessor) が担当する。本実装は #587 で追加。
func TestFederationRemoveAllFollowing_EnqueuesAllPairs(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	host := "remote.example"
	other := "other.example"
	repo := testutil.NewMockFollowingRepository()
	// followerHost = remote.example の row 2 件
	repo.Followings["f1"] = &model.Following{
		ID: "f1", FollowerID: "rA", FolloweeID: "localA", FollowerHost: &host,
	}
	repo.Followings["f2"] = &model.Following{
		ID: "f2", FollowerID: "rB", FolloweeID: "localB", FollowerHost: &host,
	}
	// 別ホスト + ローカル follower は対象外
	repo.Followings["f3"] = &model.Following{
		ID: "f3", FollowerID: "rC", FolloweeID: "localC", FollowerHost: &other,
	}
	repo.Followings["f4"] = &model.Following{
		ID: "f4", FollowerID: "localX", FolloweeID: "localY",
	}
	h.SetFollowingRepo(repo)

	enq := &stubUnfollowEnqueuer{}
	h.SetUnfollowEnqueuer(enq)

	rec := doPost(h.FederationRemoveAllFollowing, `{"host":"remote.example"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)

	// 対象 host の 2 ペアに対してのみ EnqueueUnfollow が呼ばれている
	require.Len(t, enq.pairs, 2)
	pairsSet := map[[2]string]bool{}
	for _, p := range enq.pairs {
		pairsSet[p] = true
	}
	assert.True(t, pairsSet[[2]string{"rA", "localA"}])
	assert.True(t, pairsSet[[2]string{"rB", "localB"}])
}

// 依存未配線 / host 未指定では no-op で 204
func TestFederationRemoveAllFollowing_NoOpWhenUnwired(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// repo は配線するが enqueuer は未配線
	h.SetFollowingRepo(testutil.NewMockFollowingRepository())
	rec := doPost(h.FederationRemoveAllFollowing, `{"host":"remote.example"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestFederationUpdateInstance(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.FederationUpdateInstance, `{}`, adminUser).Code)
}

// assertNoLogWritten は「指定期間内に log が 1 件も書かれない」ことを assert
// する。time.Sleep + assert.Empty より flaky になりにくい (期間中ずっと empty
// であることを poll する)。
// setupFederationUpdateInstance returns a configured handler + modlog repo
// pre-populated with the given instance row. before=nil なら instance を
// seed しない (host-not-found / no-instance-repo シナリオ用)。FederationUpdate
// Instance 系 test の boilerplate を 1 行にまとめる (#716)。
func setupFederationUpdateInstance(t *testing.T, before *model.Instance) (*apiadmin.Handler, *testutil.MockModerationLogRepository) {
	t.Helper()
	h, _, _, _ := newTestHandler(t)
	instRepo := testutil.NewMockInstanceRepository()
	if before != nil {
		require.NoError(t, instRepo.Create(before))
	}
	h.SetInstanceRepo(instRepo)
	return h, attachModLog(t, h)
}

func assertNoLogWritten(t *testing.T, repo *testutil.MockModerationLogRepository) {
	t.Helper()
	assert.Never(t, func() bool { return len(repo.Snapshot()) > 0 }, 100*time.Millisecond, 5*time.Millisecond,
		"moderation log を書いてはいけない")
}

// #676: instanceRepo 未配線なら request body の有無に関わらず 204 で no-op。
func TestFederationUpdateInstance_NoOpWithoutInstanceRepo(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := attachModLog(t, h)
	rec := doPost(h.FederationUpdateInstance, `{"host":"remote.example","isSuspended":true}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assertNoLogWritten(t, repo)
}

// #676: instance 行が存在しない host への update は no-op (FindByHost が err)。
func TestFederationUpdateInstance_HostNotFound(t *testing.T) {
	h, repo := setupFederationUpdateInstance(t, nil)
	rec := doPost(h.FederationUpdateInstance, `{"host":"ghost.example","isSuspended":true}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assertNoLogWritten(t, repo)
}

// #676: isSuspended=true への遷移で suspendRemoteInstance 1 件だけ書く。
func TestFederationUpdateInstance_WritesSuspendLog(t *testing.T) {
	h, repo := setupFederationUpdateInstance(t, &model.Instance{
		ID:              "inst-1",
		Host:            "remote.example",
		SuspensionState: model.SuspensionStateNone, // before: 未停止
	})

	rec := doPost(h.FederationUpdateInstance, `{"host":"remote.example","isSuspended":true}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)

	logs := repo.Snapshot()
	assert.Equal(t, "suspendRemoteInstance", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	assert.Equal(t, "inst-1", info["id"])
	assert.Equal(t, "remote.example", info["host"])
}

// #676: suspended → unsuspended 遷移で unsuspendRemoteInstance を書く。
func TestFederationUpdateInstance_WritesUnsuspendLog(t *testing.T) {
	h, repo := setupFederationUpdateInstance(t, &model.Instance{
		ID:              "inst-2",
		Host:            "remote.example",
		SuspensionState: model.SuspensionStateManuallySuspended, // before: 停止中
	})

	rec := doPost(h.FederationUpdateInstance, `{"host":"remote.example","isSuspended":false}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "unsuspendRemoteInstance", repo.Snapshot()[0].Type)
}

// #676: isSuspended が状態と一致 (no-change) なら log を書かない。
func TestFederationUpdateInstance_SuspendNoChange_NoLog(t *testing.T) {
	h, repo := setupFederationUpdateInstance(t, &model.Instance{
		ID:              "inst-3",
		Host:            "remote.example",
		SuspensionState: model.SuspensionStateNone,
	})

	rec := doPost(h.FederationUpdateInstance, `{"host":"remote.example","isSuspended":false}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assertNoLogWritten(t, repo)
}

// #676: moderationNote の before/after 差分で updateRemoteInstanceNote を書く。
func TestFederationUpdateInstance_WritesModerationNoteLog(t *testing.T) {
	h, repo := setupFederationUpdateInstance(t, &model.Instance{
		ID:             "inst-4",
		Host:           "remote.example",
		ModerationNote: "old note",
	})

	rec := doPost(h.FederationUpdateInstance, `{"host":"remote.example","moderationNote":"new note"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)

	logs := repo.Snapshot()
	assert.Equal(t, "updateRemoteInstanceNote", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	assert.Equal(t, "old note", info["before"])
	assert.Equal(t, "new note", info["after"])
	assert.Equal(t, "inst-4", info["id"])
}

// #676: moderationNote が同じ値なら log 無し (空文字列で clear のケースを除く)。
func TestFederationUpdateInstance_NoteSameValue_NoLog(t *testing.T) {
	h, repo := setupFederationUpdateInstance(t, &model.Instance{
		ID:             "inst-5",
		Host:           "remote.example",
		ModerationNote: "same",
	})

	rec := doPost(h.FederationUpdateInstance, `{"host":"remote.example","moderationNote":"same"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assertNoLogWritten(t, repo)
}

// #676: 1 リクエストで suspend + note を両方変更すると 2 log が出る。
func TestFederationUpdateInstance_WritesBothLogs(t *testing.T) {
	h, repo := setupFederationUpdateInstance(t, &model.Instance{
		ID:              "inst-6",
		Host:            "remote.example",
		SuspensionState: model.SuspensionStateNone,
		ModerationNote:  "old",
	})

	body := `{"host":"remote.example","isSuspended":true,"moderationNote":"new"}`
	rec := doPost(h.FederationUpdateInstance, body, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 2 }, 500*time.Millisecond, 5*time.Millisecond)

	types := []string{}
	for _, l := range repo.Snapshot() {
		types = append(types, l.Type)
	}
	assert.ElementsMatch(t, []string{"suspendRemoteInstance", "updateRemoteInstanceNote"}, types)
}
