package repository

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// ErrDuplicateReaction is returned by NoteReactionRepository.Create when the
// (userId, noteId) unique constraint is violated.
var ErrDuplicateReaction = errors.New("user has already reacted to this note")

// NoteReactionRepository provides data access for note_reaction rows.
type NoteReactionRepository interface {
	Create(r *model.NoteReaction) error
	Delete(r *model.NoteReaction) error
	FindByPair(userID, noteID string) (*model.NoteReaction, error)
	// FindByUserAndNoteIDs returns reactions for a user across multiple notes.
	// noteIDをキーとしたmapを返す。
	FindByUserAndNoteIDs(userID string, noteIDs []string) (map[string]*model.NoteReaction, error)
	ListByNoteID(noteID string, untilID, sinceID string, limit int, reactions []string) ([]*model.NoteReaction, error)
	// ListByUserID returns reactions made by the given user, with User and Note
	// preloaded for packing. upstream Misskey TS の users/reactions endpoint
	// 用 (reactor 視点の reaction list)。
	ListByUserID(userID, untilID, sinceID string, limit int) ([]*model.NoteReaction, error)
}

type noteReactionRepository struct {
	db *gorm.DB
}

// NewNoteReactionRepository creates a new NoteReactionRepository.
func NewNoteReactionRepository(db *gorm.DB) NoteReactionRepository {
	return &noteReactionRepository{db: db}
}

func (r *noteReactionRepository) Create(rec *model.NoteReaction) error {
	if err := r.db.Create(rec).Error; err != nil {
		// PostgreSQLの一意制約違反 (23505) を専用エラーに変換する
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrDuplicateReaction
		}
		return err
	}
	return nil
}

func (r *noteReactionRepository) Delete(rec *model.NoteReaction) error {
	return r.db.Delete(rec).Error
}

func (r *noteReactionRepository) FindByPair(userID, noteID string) (*model.NoteReaction, error) {
	var rec model.NoteReaction
	if err := r.db.Where("\"userId\" = ? AND \"noteId\" = ?", userID, noteID).First(&rec).Error; err != nil {
		return nil, err
	}
	return &rec, nil
}

func (r *noteReactionRepository) FindByUserAndNoteIDs(userID string, noteIDs []string) (map[string]*model.NoteReaction, error) {
	if len(noteIDs) == 0 {
		return map[string]*model.NoteReaction{}, nil
	}
	var rows []*model.NoteReaction
	if err := r.db.Where("\"userId\" = ? AND \"noteId\" IN ?", userID, noteIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	m := make(map[string]*model.NoteReaction, len(rows))
	for _, row := range rows {
		m[row.NoteID] = row
	}
	return m, nil
}

// ListByNoteID returns reactions for the given noteID, optionally filtered by
// reaction string. Uses keyset pagination with paginationOrder (DESC by
// default; ASC when only sinceID is supplied, upstream parity).
func (r *noteReactionRepository) ListByNoteID(noteID string, untilID, sinceID string, limit int, reactions []string) ([]*model.NoteReaction, error) {
	var rows []*model.NoteReaction
	q := r.db.Preload("User").Where("\"noteId\" = ?", noteID)
	if len(reactions) == 1 {
		q = q.Where("reaction = ?", reactions[0])
	} else if len(reactions) > 1 {
		q = q.Where("reaction IN ?", reactions)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListByUserID returns reactions made by the given user. upstream Misskey TS
// の users/reactions endpoint で使う reactor 視点の list。User / Note を
// preload して pack 時に追加 query が走らないようにする。pagination は
// ListByNoteID と同じ keyset 方式 (paginationOrder で DESC 既定)。
func (r *noteReactionRepository) ListByUserID(userID, untilID, sinceID string, limit int) ([]*model.NoteReaction, error) {
	var rows []*model.NoteReaction
	// Note.User も preload する。users/reactions は note を完全 shape (PackNotes)
	// で返すため、note author が無いと misskey_dart の Note.user (UserLite) 解析が
	// null で落ちる (#1227)。
	q := r.db.Preload("User").Preload("Note").Preload("Note.User").Where("\"userId\" = ?", userID)
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
