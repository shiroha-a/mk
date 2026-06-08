package processors

import (
	"context"
	"log/slog"
	"time"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// ExpiredMutingPruner is the narrow interface the checkExpiredMutings job
// needs. repository.MutingRepository satisfies it (DeleteExpired).
type ExpiredMutingPruner interface {
	DeleteExpired(now time.Time) (int64, error)
}

// CheckExpiredMutingsProcessor physically removes user- and channel-muting
// rows whose expiresAt has passed, mirroring upstream
// CheckExpiredMutingsProcessorService (cron `*/5 * * * *`, #1563 / #1603)。
// read filter は既に期限切れ行を除外するので利用者影響は無く、DB hygiene の
// ための能動 prune。mk-go には redis mute cache が無いため cache refresh は不要。
type CheckExpiredMutingsProcessor struct {
	userMutes    ExpiredMutingPruner
	channelMutes ExpiredMutingPruner
}

// NewCheckExpiredMutingsProcessor constructs a processor. Either pruner may be
// nil to disable that half (no-op) so callers can wire a subset.
func NewCheckExpiredMutingsProcessor(userMutes, channelMutes ExpiredMutingPruner) *CheckExpiredMutingsProcessor {
	return &CheckExpiredMutingsProcessor{userMutes: userMutes, channelMutes: channelMutes}
}

// Handle implements the driver handler contract.
func (p *CheckExpiredMutingsProcessor) Handle(_ context.Context, _ driver.Task) error {
	now := time.Now()
	// 失敗しても nil を返して success 扱い。MaxRetry(0) で retry させない方針で、
	// 次回 cron (5 分後) を待てば自然に再 prune される。
	prune := func(kind string, r ExpiredMutingPruner) {
		if r == nil {
			return
		}
		n, err := r.DeleteExpired(now)
		if err != nil {
			slog.Warn("checkExpiredMutings: prune failed", "kind", kind, "err", err)
			return
		}
		if n > 0 {
			slog.Info("checkExpiredMutings: pruned expired mutings", "kind", kind, "count", n)
		}
	}
	prune("user", p.userMutes)
	prune("channel", p.channelMutes)
	return nil
}
