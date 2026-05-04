// Package hashtag implements the HashtagService.updateHashtag equivalent
// of Misskey TS — maintaining the per-tag mentionedUsersCount / userIds
// arrays so /api/hashtags/list ranking and /show counts work (#680)。
package hashtag

import (
	"log/slog"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// Service updates the `hashtag` table when a note is created and references
// hashtags. Wired into note_create_service via HashtagHook (local note path)
// and federation/resolver via the same hook (AP receive path)。
type Service struct {
	repo  repository.HashtagRepository
	idGen id.Generator
}

// NewService constructs a HashtagService.
func NewService(repo repository.HashtagRepository, idGen id.Generator) *Service {
	return &Service{repo: repo, idGen: idGen}
}

// OnNoteCreated is the HashtagHook implementation: for every tag in
// note.Tags, record the author as having mentioned that tag.
//
// 実装: 各 tag を順に repo.RecordMention で upsert + dedup する。tag が
// 多い note でもタグ数は通常 1 桁なので逐次で十分。失敗は best-effort
// (note 作成自体は成功扱い)、log を残して次へ。
//
// `note.Tags` は note_create_service / federation/resolver で
// hashtag.Extract 済みなのでここでは追加抽出しない。
func (s *Service) OnNoteCreated(note *model.Note, author *model.User) {
	if s == nil || s.repo == nil || note == nil || author == nil {
		return
	}
	if len(note.Tags) == 0 {
		return
	}
	isLocal := author.IsLocal()
	for _, name := range note.Tags {
		if name == "" {
			continue
		}
		hid := s.idGen.Generate(noteCreatedAt(note))
		if err := s.repo.RecordMention(hid, name, author.ID, isLocal); err != nil {
			slog.Warn("hashtag: RecordMention failed", "name", name, "userID", author.ID, "err", err)
		}
	}
}

// noteCreatedAt は note ID から作成時刻を再計算するためのヘルパ。idGen の
// ParseTime に依存するが、note 作成 hook なら ID は妥当な値が入っている
// 想定。失敗時はゼロ値時刻が返るが、Hashtag.ID は INSERT ON CONFLICT で
// 既存値が優先されるので新規 row 採番にしか影響しない (= 表示順への影響
// は無視できる)。
func noteCreatedAt(_ *model.Note) (_ time.Time) { return time.Now() }
