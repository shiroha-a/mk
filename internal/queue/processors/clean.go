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
	// AntennaDeactivator deactivates antennas unused since a cutoff
	// (repository.AntennaRepository).
	AntennaDeactivator interface {
		DeactivateUnusedSince(cutoff time.Time) (int64, error)
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
// than ~10 minutes, (4) deactivate antennas unused for longer than
// antennaThreshold (#1604; relies on antenna.lastUsedAt being bumped on
// read/update so active antennas are not wrongly deactivated).
type CleanProcessor struct {
	userIP           UserIPPruner
	roleAssign       RoleAssignmentPruner
	reversi          OutdatedGamePruner
	idGen            CleanIDGenerator
	antenna          AntennaDeactivator
	antennaThreshold time.Duration
}

// NewCleanProcessor constructs the processor. Any nil dependency disables its
// sub-task (no-op) rather than panicking. antennaThreshold <= 0 also disables
// the antenna deactivate sub-task (matching upstream's `> 0` guard).
func NewCleanProcessor(userIP UserIPPruner, roleAssign RoleAssignmentPruner, reversi OutdatedGamePruner, idGen CleanIDGenerator, antenna AntennaDeactivator, antennaThreshold time.Duration) *CleanProcessor {
	return &CleanProcessor{
		userIP:           userIP,
		roleAssign:       roleAssign,
		reversi:          reversi,
		idGen:            idGen,
		antenna:          antenna,
		antennaThreshold: antennaThreshold,
	}
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

	if p.antenna != nil && p.antennaThreshold > 0 {
		if n, err := p.antenna.DeactivateUnusedSince(now.Add(-p.antennaThreshold)); err != nil {
			slog.Warn("clean: deactivate unused antennas failed", "err", err)
		} else if n > 0 {
			slog.Info("clean: deactivated unused antennas", "count", n)
		}
	}

	return nil
}
