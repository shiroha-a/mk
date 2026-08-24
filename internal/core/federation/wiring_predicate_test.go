package federation

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
// Resolver と DeliverService は別オブジェクトの別 field なので、片方の述語で
// もう片方を代替できない。
func TestFederation_WiringPredicates(t *testing.T) {
	assert.False(t, (&Resolver{}).HasHostBlockChecker(), "resolver: 未配線なら false")
	assert.False(t, (&DeliverService{}).HasHostBlockChecker(), "deliver: 未配線なら false")
}
