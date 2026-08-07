package antenna

import (
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
)

// 同じ note を全アンテナで評価するので、owner ごとの follow 判定は 1 回だけ
// 引いて使い回す。アンテナ数に比例して DB を叩くと fan-out が note 作成に
// 対して遅れ、直後の antennas/notes が取りこぼす。
func TestMatchMemo_OwnerFollowsIsCachedPerNote(t *testing.T) {
	svc, _ := newSvc(t)
	followingRepo := testutil.NewMockFollowingRepository()
	followingRepo.Followings["f1"] = &model.Following{ID: "f1", FollowerID: "owner", FolloweeID: "author"}
	svc.SetFollowingRepo(followingRepo)

	memo := newMatchMemo()
	assert.True(t, memo.ownerFollows(svc, "owner", "author"))
	assert.True(t, memo.ownerFollows(svc, "owner", "author"), "2 回目はキャッシュから返す")
	assert.False(t, memo.ownerFollows(svc, "stranger", "author"), "owner ごとに独立して判定する")
}

// memo が nil でも (単体テスト等の直接呼び出し) 素通しで動く。
func TestMatchMemo_NilFallsBackToDirectLookup(t *testing.T) {
	svc, _ := newSvc(t)
	followingRepo := testutil.NewMockFollowingRepository()
	followingRepo.Followings["f1"] = &model.Following{ID: "f1", FollowerID: "owner", FolloweeID: "author"}
	svc.SetFollowingRepo(followingRepo)

	var memo *matchMemo
	assert.True(t, memo.ownerFollows(svc, "owner", "author"))
	assert.False(t, memo.listContains(svc, "missing-list", "author"))
}

// list メンバーシップも note 単位でキャッシュする。
func TestMatchMemo_ListMembership(t *testing.T) {
	svc, _ := newSvc(t)
	listRepo := testutil.NewMockUserListRepository()
	listRepo.Lists["l1"] = &model.UserList{ID: "l1", UserID: "owner", Name: "l"}
	listRepo.Members = []*model.UserListMembership{{ID: "m1", UserListID: "l1", UserID: "author"}}
	svc.SetUserListRepo(listRepo)

	memo := newMatchMemo()
	assert.True(t, memo.listContains(svc, "l1", "author"))
	assert.True(t, memo.listContains(svc, "l1", "author"), "2 回目はキャッシュから返す")
	assert.False(t, memo.listContains(svc, "l1", "stranger"))
}
