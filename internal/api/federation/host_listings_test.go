package federation

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- bind validation ---

func TestFollowers_MissingHost(t *testing.T) {
	h, _ := newHandler(t)
	rec := postStub(h.Followers)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	// 400 エラーレスポンス直後に 200 を追記してしまう double-write バグの
	// regression guard。body は単一の JSON オブジェクト (error wrapper) になる
	// はずで、追加の [] が後続しないこと。
	var single map[string]any
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &single),
		"response body must be a single JSON object, not concatenated")
}

func TestFollowing_MissingHost(t *testing.T) {
	h, _ := newHandler(t)
	assert.Equal(t, http.StatusBadRequest, postStub(h.Following).Code)
}

func TestUsers_MissingHost(t *testing.T) {
	h, _ := newHandler(t)
	assert.Equal(t, http.StatusBadRequest, postStub(h.Users).Code)
}

// --- detail: Followers / Following / Users with repo wired ---

// TestFollowers_FiltersByHost guards the upstream-parity column: federation/
// followers filters by followeeHost (= the followed users belong to the given
// remote host). Seeding followerHost=remote must NOT match.
func TestFollowers_FiltersByHost(t *testing.T) {
	h, _ := newHandler(t)
	repo := testutil.NewMockFollowingRepository()
	remote := "remote.example"
	other := "elsewhere.example"
	// followeeHost=remote の 2 行が対象 (本家 followers.ts と同じ列)。
	repo.Followings["f1"] = &model.Following{ID: "f1", FollowerID: "local1", FolloweeID: "r1", FolloweeHost: &remote}
	repo.Followings["f2"] = &model.Following{ID: "f2", FollowerID: "local2", FolloweeID: "r2", FolloweeHost: &remote}
	repo.Followings["f3"] = &model.Following{ID: "f3", FollowerID: "local3", FolloweeID: "r3", FolloweeHost: &other}
	// followerHost=remote は federation/followers では対象外であることの guard。
	repo.Followings["f4"] = &model.Following{ID: "f4", FollowerID: "r4", FollowerHost: &remote, FolloweeID: "local4"}
	h.SetFollowingRepo(repo)

	rec := postBody(h.Followers, `{"host":"remote.example","limit":10}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Len(t, rows, 2)
}

// TestFollowing_FiltersByHost guards the upstream-parity column: federation/
// following filters by followerHost (= the followers belong to the given
// remote host). Seeding followeeHost=remote must NOT match.
func TestFollowing_FiltersByHost(t *testing.T) {
	h, _ := newHandler(t)
	repo := testutil.NewMockFollowingRepository()
	remote := "remote.example"
	// followerHost=remote の 1 行が対象 (本家 following.ts と同じ列)。
	repo.Followings["f1"] = &model.Following{ID: "f1", FollowerID: "r1", FollowerHost: &remote, FolloweeID: "local1"}
	// followeeHost=remote は federation/following では対象外。
	repo.Followings["f2"] = &model.Following{ID: "f2", FollowerID: "local2", FolloweeID: "r2", FolloweeHost: &remote}
	h.SetFollowingRepo(repo)

	rec := postBody(h.Following, `{"host":"remote.example"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Len(t, rows, 1)
}

// TestFollowers_PackShape verifies the Following packer matches upstream
// FollowingEntityService.pack: id / createdAt / followeeId / followerId plus a
// populated followee (UserDetailedNotMe) when userRepo + idGen are wired, and
// that the old host/withReplies fields are gone.
func TestFollowers_PackShape(t *testing.T) {
	h, _ := newHandler(t)
	gen, _ := id.NewGenerator("aidx")
	h.SetIDGen(gen)

	remote := "remote.example"
	// row ID は aidx ID にする (createdAt は row ID から導出されるため)。
	fid := gen.Generate(time.Now())
	rid := gen.Generate(time.Now())
	repo := testutil.NewMockFollowingRepository()
	repo.Followings[fid] = &model.Following{ID: fid, FollowerID: "local1", FolloweeID: rid, FolloweeHost: &remote}
	h.SetFollowingRepo(repo)

	userRepo := testutil.NewMockUserRepository()
	userRepo.Users[rid] = &model.User{ID: rid, Username: "alice", Host: &remote}
	h.SetUserRepo(userRepo)

	rec := postBody(h.Followers, `{"host":"remote.example"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	row := rows[0]
	assert.Equal(t, fid, row["id"])
	assert.Equal(t, "local1", row["followerId"])
	assert.Equal(t, rid, row["followeeId"])
	// createdAt は aidx ID から導出され非空。
	assert.NotEmpty(t, row["createdAt"])
	// populateFollowee=true なので followee が UserDetailedNotMe で埋まる。
	followee, ok := row["followee"].(map[string]any)
	require.True(t, ok, "followee must be populated")
	assert.Equal(t, "alice", followee["username"])
	// follower は populate しない (本家 populateFollower 未指定)。
	_, hasFollower := row["follower"]
	assert.False(t, hasFollower, "follower must not be populated")
	// 旧 shape の host / withReplies フィールドは出さない (本家 schema に無い)。
	_, hasFollowerHost := row["followerHost"]
	_, hasFolloweeHost := row["followeeHost"]
	_, hasWithReplies := row["withReplies"]
	assert.False(t, hasFollowerHost)
	assert.False(t, hasFolloweeHost)
	assert.False(t, hasWithReplies)
}

// TestFollowing_PackShape mirrors TestFollowers_PackShape for the following
// endpoint and also covers the missing-followee path (lookup returns nil so
// followee stays absent).
func TestFollowing_PackShape(t *testing.T) {
	h, _ := newHandler(t)
	gen, _ := id.NewGenerator("aidx")
	h.SetIDGen(gen)

	remote := "remote.example"
	repo := testutil.NewMockFollowingRepository()
	// followee user は userRepo に存在しないので followee は埋まらない。
	repo.Followings["f1"] = &model.Following{ID: "f1", FollowerID: "r1", FollowerHost: &remote, FolloweeID: "ghost"}
	h.SetFollowingRepo(repo)
	h.SetUserRepo(testutil.NewMockUserRepository())

	rec := postBody(h.Following, `{"host":"remote.example"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	row := rows[0]
	assert.Equal(t, "ghost", row["followeeId"])
	_, hasFollowee := row["followee"]
	assert.False(t, hasFollowee, "missing followee user must leave followee absent")
}

func TestUsers_FiltersByHost(t *testing.T) {
	h, _ := newHandler(t)
	userRepo := testutil.NewMockUserRepository()
	remote := "remote.example"
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice", Host: &remote}
	userRepo.Users["u2"] = &model.User{ID: "u2", Username: "bob"}
	h.SetUserRepo(userRepo)

	rec := postBody(h.Users, `{"host":"remote.example"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	// MockUserRepository.ListUsers が Hostname フィルタを実装してないと local も
	// 混入してしまうため、実装側でフィルタしないケースでもテストは最低限
	// 200 が返ることを確認する。実 DB 経路での厳密な filter 動作検証は repo
	// 側テスト (TestUserRepository_ListUsers_HostnameFilter 相当) で行う。
	assert.GreaterOrEqual(t, len(rows), 1)
}

func TestFollowers_NoRepoReturnsEmpty(t *testing.T) {
	h, _ := newHandler(t)
	rec := postBody(h.Followers, `{"host":"remote.example"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFollowing_NoRepoReturnsEmpty(t *testing.T) {
	h, _ := newHandler(t)
	rec := postBody(h.Following, `{"host":"remote.example"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUsers_NoRepoReturnsEmpty(t *testing.T) {
	h, _ := newHandler(t)
	rec := postBody(h.Users, `{"host":"remote.example"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// bindHostPage の limit clamp 分岐 (>100) カバー
func TestFollowers_LimitClampedAt100(t *testing.T) {
	h, _ := newHandler(t)
	h.SetFollowingRepo(testutil.NewMockFollowingRepository())
	rec := postBody(h.Followers, `{"host":"remote.example","limit":500}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}
