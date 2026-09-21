package processors

import (
	"context"
	"github.com/shiroha-a/mk/internal/repository"
	"testing"

	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanRemoteNotes_Disabled(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	p := NewCleanRemoteNotesProcessor(noteRepo, func() CleanRemoteNotesConfig { return CleanRemoteNotesConfig{Enabled: false} })
	err := p.Handle(context.Background(), driver.RawTask{TypeName: "test"})
	require.NoError(t, err)
}

func TestCleanRemoteNotes_Enabled(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	p := NewCleanRemoteNotesProcessor(noteRepo, func() CleanRemoteNotesConfig {
		return CleanRemoteNotesConfig{
			Enabled:              true,
			ExpiryDays:           90,
			MaxProcessingMinutes: 1,
		}
	})
	err := p.Handle(context.Background(), driver.RawTask{TypeName: "test"})
	require.NoError(t, err)
}

func TestCleanRemoteNotes_DefaultValues(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	p := NewCleanRemoteNotesProcessor(noteRepo, func() CleanRemoteNotesConfig {
		return CleanRemoteNotesConfig{
			Enabled:              true,
			ExpiryDays:           0,
			MaxProcessingMinutes: 0,
		}
	})
	err := p.Handle(context.Background(), driver.RawTask{TypeName: "test"})
	require.NoError(t, err)
}

func TestCleanRemoteNotes_CancelledContext(t *testing.T) {
	noteRepo := testutil.NewMockNoteRepository()
	p := NewCleanRemoteNotesProcessor(noteRepo, func() CleanRemoteNotesConfig {
		return CleanRemoteNotesConfig{
			Enabled:              true,
			ExpiryDays:           90,
			MaxProcessingMinutes: 60,
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := p.Handle(ctx, driver.RawTask{TypeName: "test"})
	assert.NoError(t, err)
}

// **実行中に無効化したら止まること。**
//
// リモートノートの自動削除は不可逆なので、管理画面の停止スイッチが唯一の
// 止め方になる。起動時に固定していると、プロセスを再起動するまで走り続けた。
func TestCleanRemoteNotes_StopsWhenDisabledDuringRun(t *testing.T) {
	repo := &countingNoteRepo{batch: 100}
	enabled := true
	p := NewCleanRemoteNotesProcessor(repo, func() CleanRemoteNotesConfig {
		return CleanRemoteNotesConfig{Enabled: enabled, ExpiryDays: 90, MaxProcessingMinutes: 60}
	})
	// 1 バッチ目の直後に運営者が止める。
	repo.onDelete = func(calls int) {
		if calls >= 1 {
			enabled = false
		}
	}
	require.NoError(t, p.Handle(context.Background(), driver.RawTask{}))
	require.Equal(t, 1, repo.calls,
		"無効化した後はバッチを回さないこと (実測 %d 回)", repo.calls)
}

// countingNoteRepo は DeleteExpiredRemoteNotes の呼び出し回数を数える。
type countingNoteRepo struct {
	repository.NoteRepository
	calls    int
	batch    int64
	onDelete func(calls int)
}

func (r *countingNoteRepo) DeleteExpiredRemoteNotes(int, int) (int64, error) {
	r.calls++
	if r.onDelete != nil {
		r.onDelete(r.calls)
	}
	// 常に batchSize ちょうど返して「まだ残っている」と見せる。
	return r.batch, nil
}
