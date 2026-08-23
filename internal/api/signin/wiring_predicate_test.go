package signin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #2682: 起動時の配線検査が見る述語。両側を固定する (#2682 review L-7)。
func TestHandler_HasTOTPReplayGuard(t *testing.T) {
	assert.False(t, (&Handler{}).HasTOTPReplayGuard(), "未配線なら false")

	h := &Handler{}
	h.SetTOTPReplayGuard(stubReplayGuard{})
	assert.True(t, h.HasTOTPReplayGuard(), "配線したら true")
}

type stubReplayGuard struct{}

func (stubReplayGuard) MarkUsed(context.Context, string, string) (bool, error) {
	return true, nil
}
