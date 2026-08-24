package federation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 待ちの循環だけを弾き、循環でない待ちは通すこと (#2685 review HIGH-1)。
//
// **循環でない待ちまで弾くと意味が無い。** それは #2685 が直した
// 「無関係な goroutine を巻き込んで renoteId を落とす」状態に戻ることになる。
func TestResolveWaits(t *testing.T) {
	t.Run("競合が無ければ通る", func(t *testing.T) {
		var w resolveWaits
		w.hold(1, "A")
		assert.True(t, w.wait(2, "A"), "走っている側が何も待っていなければ待てる")
	})

	t.Run("誰も走らせていない鍵は待てる", func(t *testing.T) {
		var w resolveWaits
		assert.True(t, w.wait(1, "A"))
	})

	t.Run("自分が走らせている鍵は待てない", func(t *testing.T) {
		var w resolveWaits
		w.hold(1, "A")
		assert.False(t, w.wait(1, "A"), "同一チェーンの自己再入は即デッドロック")
	})

	t.Run("循環になる待ちは弾く", func(t *testing.T) {
		var w resolveWaits
		w.hold(1, "A")
		w.hold(2, "B")
		require.True(t, w.wait(1, "B"), "1 が B を待ち始める (まだ循環しない)")
		// 2 が A を待つと 1→B→2→A→1 で循環する。
		assert.False(t, w.wait(2, "A"), "循環になる待ちは弾くこと")
	})

	t.Run("3 者の循環も弾く", func(t *testing.T) {
		var w resolveWaits
		w.hold(1, "A")
		w.hold(2, "B")
		w.hold(3, "C")
		require.True(t, w.wait(1, "B"))
		require.True(t, w.wait(2, "C"))
		assert.False(t, w.wait(3, "A"))
	})

	t.Run("note と actor をまたぐ循環も弾く", func(t *testing.T) {
		// note を握ったまま actor を待つチェーンと、actor を握ったまま note を
		// 待つチェーン。**2 つの group を 1 つのグラフに載せていないと見えない**
		// 形 (#2685 review HIGH-1)。
		var w resolveWaits
		w.hold(1, noteWaitKey("N"))
		w.hold(2, actorWaitKey("U"))
		require.True(t, w.wait(1, actorWaitKey("U")))
		assert.False(t, w.wait(2, noteWaitKey("N")))
	})

	t.Run("名前空間が違えば別の鍵", func(t *testing.T) {
		var w resolveWaits
		w.hold(1, noteWaitKey("X"))
		assert.True(t, w.wait(1, actorWaitKey("X")),
			"同じ URI でも note と actor は別物として扱うこと")
	})

	t.Run("待ちが解けたら再び通る", func(t *testing.T) {
		var w resolveWaits
		w.hold(1, "A")
		w.hold(2, "B")
		require.True(t, w.wait(1, "B"))
		require.False(t, w.wait(2, "A"))

		w.unwait(1) // 1 が B の待ちをやめた
		assert.True(t, w.wait(2, "A"), "循環が解けたら通ること")
	})

	t.Run("走らせるのをやめたら待ちは通る", func(t *testing.T) {
		var w resolveWaits
		w.hold(1, "A")
		w.hold(2, "B")
		require.True(t, w.wait(1, "B"))
		require.False(t, w.wait(2, "A"))

		w.release(1, "A")
		assert.True(t, w.wait(2, "A"), "保持者が居なくなれば待てること")
	})

	t.Run("待ち側は保持者に混ざらない", func(t *testing.T) {
		// 同じ鍵を待っている者どうしを依存関係にすると、実在しない循環を
		// 作ってしまう。
		var w resolveWaits
		w.hold(3, "A")
		require.True(t, w.wait(1, "A"))
		require.True(t, w.wait(2, "A"))
		w.hold(1, "B")
		assert.True(t, w.wait(2, "B"), "A の待ち仲間である 1 経由で弾かれないこと")
	})

	t.Run("release と unwait で登録が消える", func(t *testing.T) {
		var w resolveWaits
		w.hold(1, "A")
		require.True(t, w.wait(1, "B"))
		w.release(1, "A")
		w.unwait(1)
		w.mu.Lock()
		_, stillChain := w.chains[1]
		_, stillHolder := w.holders["A"]
		w.mu.Unlock()
		assert.False(t, stillChain, "鍵も待ちも無くなったら entry ごと落とすこと")
		assert.False(t, stillHolder, "索引にも残さないこと")
	})

	t.Run("id 0 と空の鍵は素通し", func(t *testing.T) {
		var w resolveWaits
		w.hold(0, "A")
		w.hold(1, "")
		assert.True(t, w.wait(0, "A"), "採番前のチェーンは判定しない")
		assert.True(t, w.wait(1, ""))
		w.release(0, "A")
		w.release(1, "")
		w.unwait(0)
		assert.Empty(t, w.chains)
		assert.Empty(t, w.holders)
	})

	t.Run("空の鍵は名前空間を付けない", func(t *testing.T) {
		assert.Equal(t, "", noteWaitKey(""))
		assert.Equal(t, "", actorWaitKey(""))
	})
}

// unwaitAll は起こす側がまとめて印を落とすためのもの。**鍵を照合する** —
// 上限で自分から降りて別の鍵を待ち直したチェーンの新しい印を消さないため。
func TestResolveWaits_UnwaitAll(t *testing.T) {
	var w resolveWaits
	w.hold(9, "K")
	require.True(t, w.wait(1, "K"))
	require.True(t, w.wait(2, "K"))
	// 3 は一度 K を待って降り、別の鍵を待ち直した。
	require.True(t, w.wait(3, "OTHER"))

	w.unwaitAll(map[uint64]struct{}{1: {}, 2: {}, 3: {}}, "K")

	w.mu.Lock()
	defer w.mu.Unlock()
	assert.NotContains(t, w.chains, uint64(1), "待ちが解けた側は entry ごと落とすこと")
	assert.NotContains(t, w.chains, uint64(2))
	require.Contains(t, w.chains, uint64(3))
	assert.Equal(t, "OTHER", w.chains[3].waitingOn, "別の鍵の待ちは消さないこと")
}

// 空でも安全なこと。
func TestResolveWaits_UnwaitAllEmpty(t *testing.T) {
	var w resolveWaits
	w.unwaitAll(nil, "K")
	w.unwaitAll(map[uint64]struct{}{7: {}}, "K")
	assert.Empty(t, w.chains)
}
