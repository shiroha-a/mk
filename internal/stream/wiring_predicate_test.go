package stream

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #2683: 起動時の配線検査が見る述語。**false を返す側を必ず固定する。**
//
// 反転させる変異は e2e が捕まえるが、`return true` のように**弱める**変異は
// 捕まらない (未配線を検出できなくなるのに起動は成功するため)。#2682 の
// レビューで実際に素通りした形なので、同じ型のテストを置く。
//
// **3 つは独立に消せる**ので、それぞれ別に固定する。
// `SetFollowingSnapshotLookup` / `SetNoteVisibilityChecker` は未配線で
// fail-closed なので検査対象外 (述語も持たせていない)。
func TestManager_WiringPredicates(t *testing.T) {
	m := &Manager{}
	assert.False(t, m.HasHardMuteLookup(), "未配線なら false")
	assert.False(t, m.HasMuteBlockSnapshotLookup(), "未配線なら false")
	assert.False(t, m.HasPolicyProvider(), "未配線なら false")
}
