package note

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
// 4 つは独立に消せるので、それぞれ別に固定する。
func TestCreateService_WiringPredicates(t *testing.T) {
	empty := &CreateService{}
	assert.False(t, empty.HasBlockingRepo(), "未配線なら false")
	assert.False(t, empty.HasMetaRepo(), "未配線なら false")
	assert.False(t, empty.HasSilencingProvider(), "未配線なら false")
	assert.False(t, empty.HasDriveFileRepo(), "未配線なら false")
}
