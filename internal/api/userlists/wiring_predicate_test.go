package userlists

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #2683: 起動時の配線検査が見る述語。**false を返す側を必ず固定する。**
//
// 反転させる変異は e2e が捕まえるが、`return true` のように**弱める**変異は
// 捕まらない (未配線を検出できなくなるのに起動は成功するため)。#2682 の
// レビューで実際に素通りした形なので、同じ型のテストを置く。
func TestHandler_HasRolePolicyProvider(t *testing.T) {
	assert.False(t, (&Handler{}).HasRolePolicyProvider(),
		"未配線なら false を返すこと (常に true だと検査が無意味になる)")

	// **配線したら true も固定する。** false 側だけだと、述語が別の field を
	// 読む typo (copy-paste の付け替え漏れ) が単体・e2e とも素通りする
	// (#2683 review LOW-1)。
	x := &Handler{}
	x.SetRolePolicyProvider(stubWiringPolicyProvider{})
	assert.True(t, x.HasRolePolicyProvider(), "配線したら true")
}

type stubWiringPolicyProvider struct{}

func (stubWiringPolicyProvider) GetUserPolicies(string) map[string]any { return nil }
