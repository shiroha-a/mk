package ap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #2708: 起動時の配線検査が見る述語。**両側を固定する。**
//
// `assert.False` だけだと述語を `return true` に弱める変異が e2e でも捕まらず、
// `assert.True` を足しても**まとめて配線すると別の field を読む typo が
// 素通りする**。1 つずつ配線して他の述語が false のままであることも見る
// (#2683 review)。
func TestHandler_HasHostBlockChecker(t *testing.T) {
	assert.False(t, (&Handler{}).HasHostBlockChecker(), "未配線なら false")

	h := &Handler{}
	h.SetFederationGate(stubWiringHostBlock{}, "local.example")
	assert.True(t, h.HasHostBlockChecker(), "配線したら true")
}

type stubWiringHostBlock struct{}

func (stubWiringHostBlock) IsBlocked(string) bool    { return false }
func (stubWiringHostBlock) IsAllowed(string) bool    { return true }
func (stubWiringHostBlock) FederationDisabled() bool { return false }
