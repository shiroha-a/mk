package move

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/testutil"
)

// stubBlockEnqueuer is a non-nil BlockEnqueuer for the wiring predicate test.
type stubBlockEnqueuer struct{}

func (stubBlockEnqueuer) EnqueueBlockBulk([]queue.BlockPayload) error { return nil }

// #2683: 起動時の配線検査が見る述語。**false を返す側を必ず固定する。**
//
// 反転させる変異は e2e が捕まえるが、`return true` のように**弱める**変異は
// 捕まらない (未配線を検出できなくなるのに起動は成功するため)。#2682 の
// レビューで実際に素通りした形なので、同じ型のテストを置く。
//
// **1 つでも欠ければ false。** 引き継ぎは repo 3 つと idGen と blockQueue が
// 揃って初めて成立する。特に `blockQueue` は別 setter なので、そこだけ消えた
// 状態を通してしまうと effect に書いたブロック引き継ぎが黙って止まる
// (#2683 review L-3)。
func TestService_HasCarryOverRepos(t *testing.T) {
	assert.False(t, (&Service{}).HasCarryOverRepos(),
		"未配線なら false を返すこと (常に true だと検査が無意味になる)")

	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)

	t.Run("blockQueue だけ欠けても false", func(t *testing.T) {
		s := &Service{}
		s.SetCarryOverRepos(testutil.NewMockBlockingRepository(),
			testutil.NewMockMutingRepository(), testutil.NewMockUserListRepository(), idGen)
		assert.False(t, s.HasCarryOverRepos(),
			"SetBlockQueue が消えるとブロックの引き継ぎが止まるので false であること")
	})

	t.Run("全て揃えば true", func(t *testing.T) {
		s := &Service{}
		s.SetCarryOverRepos(testutil.NewMockBlockingRepository(),
			testutil.NewMockMutingRepository(), testutil.NewMockUserListRepository(), idGen)
		s.SetBlockQueue(stubBlockEnqueuer{})
		assert.True(t, s.HasCarryOverRepos())
	})

	t.Run("repo が 1 つ欠けても false", func(t *testing.T) {
		s := &Service{}
		s.SetCarryOverRepos(nil, testutil.NewMockMutingRepository(),
			testutil.NewMockUserListRepository(), idGen)
		s.SetBlockQueue(stubBlockEnqueuer{})
		assert.False(t, s.HasCarryOverRepos())
	})
}
