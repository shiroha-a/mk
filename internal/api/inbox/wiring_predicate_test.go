package inbox

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shiroha-a/mk/internal/queue"
)

// #2682: 起動時の配線検査が見る述語。false 側を固定する。弱める変異
// (常に true) は e2e でも捕まらないので、ここで縛る。
func TestHandler_HasExpectedHost(t *testing.T) {
	assert.False(t, (&Handler{}).HasExpectedHost(), "未設定なら false")
	h := &Handler{}
	h.SetExpectedHost("example.test")
	assert.True(t, h.HasExpectedHost())
}

func TestHandler_HasEnqueuer(t *testing.T) {
	assert.False(t, (&Handler{}).HasEnqueuer(), "未配線なら false")

	h := &Handler{}
	h.SetEnqueuer(stubInboxEnqueuer{})
	assert.True(t, h.HasEnqueuer(), "配線したら true")
}

type stubInboxEnqueuer struct{}

func (stubInboxEnqueuer) EnqueueInbox(context.Context, queue.InboxPayload) error { return nil }
