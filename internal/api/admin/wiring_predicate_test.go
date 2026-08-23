package admin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #2682: 起動時の配線検査が見る述語。**両側を固定する。**
//
// false 側だけだと `return true` に弱める変異 (未配線を検出できなくなる) を
// 逃すし、true 側だけだと `return false` に倒す変異を逃す。後者は起動を
// 落とすだけで安全側ではあるが、DB を持たない手元実行で緑になるのは
// 誤解を招く (#2682 review L-7)。
func TestHandler_HasUserTokenInvalidator(t *testing.T) {
	assert.False(t, (&Handler{}).HasUserTokenInvalidator(), "未配線なら false")

	h := &Handler{}
	h.SetUserTokenInvalidator(stubUserTokenInvalidator{})
	assert.True(t, h.HasUserTokenInvalidator(), "配線したら true")
}

type stubUserTokenInvalidator struct{}

func (stubUserTokenInvalidator) InvalidateTokensForUser(string) {}
