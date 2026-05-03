package repository

import (
	"fmt"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// PollRepository provides data access for polls.
type PollRepository interface {
	Create(poll *model.Poll) error
	FindByNoteID(noteID string) (*model.Poll, error)
	IncrementVote(noteID string, choice int, delta int) error
	// ListExpiredUnnotified returns polls whose expiresAt has passed and have
	// not yet been pollEnded-notified. Used by core/poll.ExpiryWorker (#690)。
	// limit caps the batch so a long backlog (e.g. after restart) doesn't
	// dominate one tick.
	ListExpiredUnnotified(now time.Time, limit int) ([]*model.Poll, error)
	// MarkNotified stamps poll.notifiedAt for the given note so subsequent
	// ticker scans skip it. idempotent — calling twice with the same time is
	// harmless.
	MarkNotified(noteID string, t time.Time) error
}

type pollRepository struct {
	db *gorm.DB
}

// NewPollRepository creates a new PollRepository.
func NewPollRepository(db *gorm.DB) PollRepository {
	return &pollRepository{db: db}
}

func (r *pollRepository) Create(poll *model.Poll) error {
	return r.db.Create(poll).Error
}

// FindByNoteID returns the poll attached to noteID.
func (r *pollRepository) FindByNoteID(noteID string) (*model.Poll, error) {
	var p model.Poll
	if err := r.db.Where("\"noteId\" = ?", noteID).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// IncrementVote bumps poll.votes[choice] by delta. PostgreSQLの配列は1始まりのため
// SQLでは choice+1 を用いる。GORMの UpdateColumn は配列インデックスを取れない
// ため、ここではraw SQLで更新する。choiceは整数なので文字列展開でも安全。
func (r *pollRepository) IncrementVote(noteID string, choice int, delta int) error {
	pgChoice := choice + 1
	q := fmt.Sprintf(`UPDATE "poll" SET votes[%d] = votes[%d] + ? WHERE "noteId" = ?`, pgChoice, pgChoice)
	return r.db.Exec(q, delta, noteID).Error
}

// ListExpiredUnnotified returns up to `limit` polls with expiresAt < now and
// notifiedAt IS NULL. partial index IDX_poll_expired_unnotified が migration
// で作られている前提。
func (r *pollRepository) ListExpiredUnnotified(now time.Time, limit int) ([]*model.Poll, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []*model.Poll
	if err := r.db.
		Where(`"expiresAt" IS NOT NULL AND "expiresAt" < ? AND "notifiedAt" IS NULL`, now).
		Order(`"expiresAt" ASC`).
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// MarkNotified stamps poll.notifiedAt = t for the given note. concurrent
// ticker や retry でも単純な UPDATE 1 行なので冪等。
func (r *pollRepository) MarkNotified(noteID string, t time.Time) error {
	return r.db.Model(&model.Poll{}).
		Where(`"noteId" = ?`, noteID).
		Update("notifiedAt", t).Error
}
