package note

import (
	"errors"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// Errors returned by DeleteService.
var (
	// ErrNoteNotFound is returned when the target note does not exist.
	ErrNoteNotFound = errors.New("note not found")
	// ErrNoteAccessDenied is returned when the user is not allowed to delete the note.
	ErrNoteAccessDenied = errors.New("not the author of this note")
)

// DeleteFederationHook is invoked after a note is deleted so that the
// ActivityPub layer can broadcast a Delete activity. パッケージ間の循環依存を
// 避けるためinterfaceで受け取る (実装は core/federation)。
type DeleteFederationHook interface {
	OnNoteDeleted(author *model.User, note *model.Note)
}

// DeleteChartHook is invoked after a note is deleted so the chart
// subsystem can decrement the relevant counters. 失敗はベストエフォート
// で握り潰す前提なので戻り値を持たない。
type DeleteChartHook interface {
	OnNoteDeleted(note *model.Note)
}

// DeleteTimelineHook is invoked after a note is deleted so Redis fanout
// timelines (home/local/global/user/userList) can have the note ID
// purged via LREM (#379)。inbound Delete activity 経由でも local
// `notes/delete` 経由でも同じ DeleteService を通るので、ここに hook を
// 1 つ生やせば両方の経路で timeline 残留を防げる。実装は core/timeline。
type DeleteTimelineHook interface {
	OnNoteDeleted(note *model.Note, author *model.User)
}

// DeleteNoteStreamHook is invoked after a note is deleted so the stream
// layer can publish a `deleted` event to `noteStream:<noteID>` for any
// WebSocket clients that have called subNote on this note (#700)。
// 失敗はベストエフォート扱いなので戻り値を持たない。
type DeleteNoteStreamHook interface {
	OnNoteDeleted(noteID string, deletedAt time.Time)
}

// DeleteService provides note deletion logic.
type DeleteService struct {
	noteRepo       repository.NoteRepository
	federationHook DeleteFederationHook
	indexHook      IndexHook
	chartHook      DeleteChartHook
	timelineHook   DeleteTimelineHook
	noteStreamHook DeleteNoteStreamHook
}

// NewDeleteService creates a new DeleteService.
func NewDeleteService(noteRepo repository.NoteRepository) *DeleteService {
	return &DeleteService{noteRepo: noteRepo}
}

// SetFederationHook attaches a DeleteFederationHook invoked after Delete.
func (s *DeleteService) SetFederationHook(h DeleteFederationHook) {
	s.federationHook = h
}

// SetIndexHook attaches an IndexHook invoked after Delete so the search
// backend can drop the note from its index.
func (s *DeleteService) SetIndexHook(h IndexHook) {
	s.indexHook = h
}

// SetChartHook attaches a chart hook invoked after a note has been
// deleted so the chart subsystem can decrement the relevant counters.
func (s *DeleteService) SetChartHook(h DeleteChartHook) {
	s.chartHook = h
}

// SetTimelineHook attaches a DeleteTimelineHook invoked after Delete so
// Redis fanout timelines can be purged of the note ID (#379)。
func (s *DeleteService) SetTimelineHook(h DeleteTimelineHook) {
	s.timelineHook = h
}

// SetNoteStreamHook attaches a DeleteNoteStreamHook invoked after Delete so
// the stream layer can publish `deleted` events on `noteStream:<noteID>`
// (#700)。
func (s *DeleteService) SetNoteStreamHook(h DeleteNoteStreamHook) {
	s.noteStreamHook = h
}

// Delete removes a note authored by the given user. It returns
// ErrNoteNotFound when the note does not exist and ErrNoteAccessDenied when
// the user is not the author.
func (s *DeleteService) Delete(user *model.User, noteID string) error {
	if user == nil {
		return errors.New("user is required")
	}

	note, err := s.noteRepo.FindByID(noteID)
	if err != nil {
		return ErrNoteNotFound
	}

	if note.UserID != user.ID {
		return ErrNoteAccessDenied
	}

	if err := s.noteRepo.Delete(note); err != nil {
		return err
	}
	// 返信を削除するときは親ノートの repliesCount を減算する。本家 Misskey の
	// NoteDeleteService と同じく親の local/remote を問わず一律に減算する。
	// DeleteService は local/remote どちらの削除経路でも共通で呼ばれるため、
	// ここに置くと federation 側でも同じ動作になる。失敗はベストエフォート。
	if note.ReplyID != nil {
		_ = s.noteRepo.IncrementCount(*note.ReplyID, "repliesCount", -1)
	}
	if s.federationHook != nil {
		s.federationHook.OnNoteDeleted(user, note)
	}
	// 検索インデックスの撤去もベストエフォート。失敗してもメインのDelete操作は成功扱い。
	if s.indexHook != nil {
		s.indexHook.OnNoteDeleted(note)
	}
	// チャート集計もベストエフォート。
	if s.chartHook != nil {
		s.chartHook.OnNoteDeleted(note)
	}
	// Redis fanout timeline 残留を防ぐ (#379)。ベストエフォート。
	if s.timelineHook != nil {
		s.timelineHook.OnNoteDeleted(note, user)
	}
	// noteStream に deleted を publish して subNote 購読中の WebSocket
	// クライアントへ即時反映する (#700)。失敗はベストエフォート。
	if s.noteStreamHook != nil {
		s.noteStreamHook.OnNoteDeleted(note.ID, time.Now())
	}
	return nil
}
