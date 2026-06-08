package processors

import (
	"context"
	"log/slog"
	"time"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// Narrow interfaces the daily clean job depends on. Each is satisfied by the
// corresponding repository; a nil dependency makes that sub-task a no-op so
// callers can wire a partial subset.
type (
	// UserIPPruner removes old user_ip rows (repository.UserIPRepository).
	UserIPPruner interface {
		DeleteOlderThan(t time.Time) (int64, error)
	}
	// RoleAssignmentPruner removes expired role_assignment rows
	// (repository.RoleAssignmentRepository).
	RoleAssignmentPruner interface {
		DeleteExpired(now time.Time) (int64, error)
	}
	// OutdatedGamePruner removes abandoned reversi games
	// (repository.ReversiRepository).
	OutdatedGamePruner interface {
		DeleteOutdatedGames(thresholdID string) (int64, error)
	}
	// CleanIDGenerator generates the reversi threshold id (misc/id.Generator).
	CleanIDGenerator interface {
		Generate(t time.Time) string
	}
)

const (
	// userIPRetention は user_ip 行を保持する期間。upstream CleanProcessorService
	// と同じく 90 日。
	userIPRetention = 90 * 24 * time.Hour
	// reversiOutdatedAfter は開始されないまま放置された reversi game を outdated
	// と見なす猶予。upstream cleanOutdatedGames は now-10min の id を閾値にする。
	reversiOutdatedAfter = 10 * time.Minute
)

// CleanProcessor implements the upstream generic `clean` systemQueue cron
// (`0 0 * * *`, #1563): (1) prune user_ip older than 90 days, (2) delete
// expired role assignments, (3) delete not-yet-started reversi games older
// than ~10 minutes. antenna auto-deactivate は mk-go が lastUsedAt を read 時に
// bump しないため faithful port が全 antenna を無効化してしまう。別 feature
// として対象外 (#1563 follow-up)。
type CleanProcessor struct {
	userIP     UserIPPruner
	roleAssign RoleAssignmentPruner
	reversi    OutdatedGamePruner
	idGen      CleanIDGenerator
}

// NewCleanProcessor constructs the processor. Any nil dependency disables its
// sub-task (no-op) rather than panicking.
func NewCleanProcessor(userIP UserIPPruner, roleAssign RoleAssignmentPruner, reversi OutdatedGamePruner, idGen CleanIDGenerator) *CleanProcessor {
	return &CleanProcessor{userIP: userIP, roleAssign: roleAssign, reversi: reversi, idGen: idGen}
}

// Handle implements the driver handler contract. Each sub-task logs and
// swallows its own error so one failure does not abort the others; the job is
// always reported as success (MaxRetry(0)).
func (p *CleanProcessor) Handle(_ context.Context, _ driver.Task) error {
	now := time.Now()

	if p.userIP != nil {
		if n, err := p.userIP.DeleteOlderThan(now.Add(-userIPRetention)); err != nil {
			slog.Warn("clean: prune user_ip failed", "err", err)
		} else if n > 0 {
			slog.Info("clean: pruned user_ip", "count", n)
		}
	}

	if p.roleAssign != nil {
		if n, err := p.roleAssign.DeleteExpired(now); err != nil {
			slog.Warn("clean: delete expired role assignments failed", "err", err)
		} else if n > 0 {
			slog.Info("clean: deleted expired role assignments", "count", n)
		}
	}

	if p.reversi != nil && p.idGen != nil {
		threshold := p.idGen.Generate(now.Add(-reversiOutdatedAfter))
		if n, err := p.reversi.DeleteOutdatedGames(threshold); err != nil {
			slog.Warn("clean: delete outdated reversi games failed", "err", err)
		} else if n > 0 {
			slog.Info("clean: deleted outdated reversi games", "count", n)
		}
	}

	return nil
}
