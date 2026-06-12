package repository

import (
	"strings"

	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// ClipNoteRepository provides data access for the `clip_note` table.
type ClipNoteRepository interface {
	Create(cn *model.ClipNote) error
	Delete(cn *model.ClipNote) error
	FindByPair(clipID, noteID string) (*model.ClipNote, error)
	ListByClip(clipID string, untilID, sinceID string, limit int) ([]*model.ClipNote, error)
	// ListByClipVisible is ListByClip with a visibility push-down: clip entries
	// whose referenced note the viewer cannot see (per core/note.CanSeeNote) are
	// excluded before LIMIT. viewerID 空文字は匿名 (public/home のみ)。clips/notes
	// で post-fetch filter によるページ過少充填と per-note の follow 判定 N+1 を
	// 避けるため (#1418 review)。export 経路は全件必要なので ListByClip を使う。
	// searchWords は各語を note.text / note.cw への ILIKE 部分一致 (OR) として
	// AND 結合する (#1562、upstream clips/notes の search param 相当)。空 slice
	// は無条件。
	ListByClipVisible(clipID, viewerID, untilID, sinceID string, limit int, searchWords []string) ([]*model.ClipNote, error)
	// CountByClip returns the number of notes in the given clip. Used by
	// noteEachClipsLimit policy gate (#1029 PR-1 follow-up).
	CountByClip(clipID string) (int64, error)
}

type clipNoteRepository struct {
	db *gorm.DB
}

// NewClipNoteRepository creates a new ClipNoteRepository.
func NewClipNoteRepository(db *gorm.DB) ClipNoteRepository {
	return &clipNoteRepository{db: db}
}

func (r *clipNoteRepository) Create(cn *model.ClipNote) error {
	return r.db.Create(cn).Error
}

func (r *clipNoteRepository) Delete(cn *model.ClipNote) error {
	return r.db.Delete(cn).Error
}

func (r *clipNoteRepository) FindByPair(clipID, noteID string) (*model.ClipNote, error) {
	var cn model.ClipNote
	if err := r.db.Where("\"clipId\" = ? AND \"noteId\" = ?", clipID, noteID).First(&cn).Error; err != nil {
		return nil, err
	}
	return &cn, nil
}

// CountByClip returns the number of notes in the given clip.
func (r *clipNoteRepository) CountByClip(clipID string) (int64, error) {
	var count int64
	if err := r.db.Model(&model.ClipNote{}).
		Where(`"clipId" = ?`, clipID).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// ListByClip returns the entries for a clip with since/until pagination on
// the clip_note id. Order flips to ASC when only sinceID is supplied,
// matching paginationOrder (upstream QueryService.makePaginationQuery parity).
func (r *clipNoteRepository) ListByClip(clipID string, untilID, sinceID string, limit int) ([]*model.ClipNote, error) {
	return r.listByClip(clipID, "", false, untilID, sinceID, limit, nil)
}

// ListByClipVisible is ListByClip with the visibility push-down enabled.
func (r *clipNoteRepository) ListByClipVisible(clipID, viewerID, untilID, sinceID string, limit int, searchWords []string) ([]*model.ClipNote, error) {
	return r.listByClip(clipID, viewerID, true, untilID, sinceID, limit, searchWords)
}

func (r *clipNoteRepository) listByClip(clipID, viewerID string, filterVisibility bool, untilID, sinceID string, limit int, searchWords []string) ([]*model.ClipNote, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	q := r.db.Where("\"clipId\" = ?", clipID)
	if filterVisibility {
		// clip_note は author 混在のため、note を相関 EXISTS で join して
		// visibility を LIMIT 前に絞る。条件は core/note.CanSeeNote と一致 (#1454)。
		q = applyViewerVisibilityExists(q, `"clip_note"."noteId"`, viewerID)
	}
	// search 各語を text / cw への ILIKE 部分一致 (語内 OR、語間 AND) で絞る
	// (#1562、upstream clips/notes.ts:103-110)。LIMIT 前に適用しないと
	// ページ過少充填になるため visibility と同じく push down する。同一 note
	// 行への相関なので、語ごとに EXISTS を重ねず単一 EXISTS 内で AND する
	// (subplan が語数分走るのを避ける)。
	if len(searchWords) > 0 {
		var cond strings.Builder
		args := make([]any, 0, len(searchWords)*2)
		cond.WriteString(`EXISTS (SELECT 1 FROM "note" sn WHERE sn."id" = "clip_note"."noteId"`)
		for _, word := range searchWords {
			like := "%" + escapeLike(word) + "%"
			cond.WriteString(` AND (sn."text" ILIKE ? OR sn."cw" ILIKE ?)`)
			args = append(args, like, like)
		}
		cond.WriteString(`)`)
		q = q.Where(cond.String(), args...)
	}
	if untilID != "" {
		q = q.Where("id < ?", untilID)
	}
	if sinceID != "" {
		q = q.Where("id > ?", sinceID)
	}
	var rows []*model.ClipNote
	if err := q.Order(paginationOrder(sinceID, untilID, "id")).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// escapeLike escapes LIKE/ILIKE metacharacters so user input matches
// literally (upstream sqlLikeEscape 相当)。PostgreSQL の既定 escape 文字は
// backslash なので \ 自身もエスケープする。
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
