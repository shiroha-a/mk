package processors

import (
	"context"
	"log/slog"
	"time"

	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/repository"
)

// CleanRemoteNotesConfig holds the settings for the cleaning job.
type CleanRemoteNotesConfig struct {
	Enabled              bool
	ExpiryDays           int
	MaxProcessingMinutes int
}

// CleanRemoteNotesProcessor handles the periodic remote notes cleaning task.
type CleanRemoteNotesProcessor struct {
	noteRepo repository.NoteRepository
	// cfg は毎回読み直す。**起動時に固定すると、運営者が管理画面で止めても
	// プロセスを再起動するまで削除が走り続ける** — 不可逆な操作に対する唯一の
	// 停止手段が効かない。姉妹ジョブ (`orphan_user_cleanup.go`) は同じ理由で
	// 既にこの形になっている。upstream もループの各反復で再評価する。
	cfg func() CleanRemoteNotesConfig
}

// NewCleanRemoteNotesProcessor creates the processor.
func NewCleanRemoteNotesProcessor(noteRepo repository.NoteRepository, cfg func() CleanRemoteNotesConfig) *CleanRemoteNotesProcessor {
	if cfg == nil {
		cfg = func() CleanRemoteNotesConfig { return CleanRemoteNotesConfig{} }
	}
	return &CleanRemoteNotesProcessor{noteRepo: noteRepo, cfg: cfg}
}

// Handle implements driver.HandlerFunc. バッチ削除を maxProcessingMinutes の
// 範囲で繰り返し実行する。100件/バッチで DB 負荷を平準化。
func (p *CleanRemoteNotesProcessor) Handle(ctx context.Context, _ driver.Task) error {
	cfg := p.cfg()
	if !cfg.Enabled {
		slog.Debug("clean-remote-notes: disabled, skipping")
		return nil
	}

	expiryDays := cfg.ExpiryDays
	if expiryDays <= 0 {
		expiryDays = 90
	}
	maxDuration := time.Duration(cfg.MaxProcessingMinutes) * time.Minute
	if maxDuration <= 0 {
		maxDuration = 60 * time.Minute
	}

	deadline := time.Now().Add(maxDuration)
	const batchSize = 100
	var totalDeleted int64

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		// **各反復で有効/無効を読み直す。** 実行中に運営者が止めたら、そこで
		// 止まる (upstream の CleanRemoteNotesProcessorService と同じ)。
		if !p.cfg().Enabled {
			slog.Info("clean-remote-notes: disabled during run, stopping")
			break
		}
		deleted, err := p.noteRepo.DeleteExpiredRemoteNotes(expiryDays, batchSize)
		if err != nil {
			slog.Error("clean-remote-notes: batch delete failed", "error", err)
			return err
		}
		totalDeleted += deleted
		if deleted < int64(batchSize) {
			break
		}
		// I/O 平準化のため短い sleep を挟む
		time.Sleep(500 * time.Millisecond)
	}

	if totalDeleted > 0 {
		slog.Info("clean-remote-notes: completed", "deleted", totalDeleted)
	}
	return nil
}
