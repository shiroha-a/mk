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

// CheckExpiredMutingsProcessor physically removes user-muting rows whose
// expiresAt has passed, mirroring upstream CheckExpiredMutingsProcessorService
// (cron `*/5 * * * *`, #1563)。read filter (ListMuteeIDs/Exists) は既に期限切れ
// 行を除外するので user 影響は無く、DB hygiene のための能動 prune。mk-go には
// redis user-mute cache が無いため cache refresh は不要。channel-mute erase は
// channel_muting に expiresAt 列が無く別 feature のため対象外。
type CheckExpiredMutingsProcessor struct {
	repo ExpiredMutingPruner
}

// NewCheckExpiredMutingsProcessor constructs a processor. A nil repo makes the
// handler a no-op so callers can wire it before the repo is ready.
func NewCheckExpiredMutingsProcessor(repo ExpiredMutingPruner) *CheckExpiredMutingsProcessor {
	return &CheckExpiredMutingsProcessor{repo: repo}
}

// Handle implements the driver handler contract.
func (p *CheckExpiredMutingsProcessor) Handle(_ context.Context, _ driver.Task) error {
	if p.repo == nil {
		return nil
	}
	n, err := p.repo.DeleteExpired(time.Now())
	if err != nil {
		// retention 同様、失敗しても nil を返して success 扱い。MaxRetry(0) で
		// retry させない方針で、次回 cron (5 分後) を待てば自然に再 prune される。
		slog.Warn("checkExpiredMutings: prune failed", "err", err)
		return nil
	}
	if n > 0 {
		slog.Info("checkExpiredMutings: pruned expired mutings", "count", n)
	}
	return nil
}
