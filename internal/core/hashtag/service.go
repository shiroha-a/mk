// Package hashtag implements the HashtagService.updateHashtag equivalent
// of Misskey TS — maintaining the per-tag mentionedUsersCount / userIds
// arrays so /api/hashtags/list ranking and /show counts work (#680)。
package hashtag

import (
	"log/slog"
	"sync"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// Service updates the `hashtag` table when a note is created and references
// hashtags. Wired into note_create_service via HashtagHook (local note path)
// and federation/resolver via the same hook (AP receive path)。
//
// hook 内部で goroutine を spawn する fire-and-forget 設計 (#719)。caller は
// 同期/非同期の差を意識せずに OnNoteCreated を呼べる。
type Service struct {
	repo  repository.HashtagRepository
	idGen id.Generator
	// pending は in-flight worker goroutine 数を追跡する。production では
	// 参照されないが、unit test が WaitForPendingWrites() 経由で完了を
	// 待つために使う。
	pending sync.WaitGroup
}

// NewService constructs a HashtagService.
func NewService(repo repository.HashtagRepository, idGen id.Generator) *Service {
	return &Service{repo: repo, idGen: idGen}
}

// OnNoteCreated is the HashtagHook implementation: for every tag in
// note.Tags, record the author as having mentioned that tag.
//
// 振る舞い: caller への return を即座に行い、実際の repo 書き込みは
// goroutine で行う (#719)。caller (note_create_service / federation/resolver)
// が hook 完了を待たずに後続処理に進めるので、tag が多い note の inbox 受信
// でも drain time が直列に伸びない。失敗は best-effort で log するのみ。
//
// 実装: 各 tag を順に repo.RecordMention で upsert + dedup する。tag が
// 多い note でもタグ数は通常 1 桁なので逐次で十分。
//
// `note.Tags` は note_create_service / federation/resolver で
// hashtag.Extract 済みなのでここでは追加抽出しない。
//
// テスト用: 内部 sync.WaitGroup を Wait() で待てるようにしているので、unit
// test は `svc.WaitForPendingWrites()` で goroutine の完了を観測してから
// repo state を assert する。
func (s *Service) OnNoteCreated(note *model.Note, author *model.User) {
	if s == nil || s.repo == nil || note == nil || author == nil {
		return
	}
	if len(note.Tags) == 0 {
		return
	}
	isLocal := author.IsLocal()
	// 入力を goroutine 起動前に複製して capture race を防ぐ。
	// (caller が同 *model.Note を後段で mutate しても safe)
	tags := make([]string, 0, len(note.Tags))
	for _, name := range note.Tags {
		if name != "" {
			tags = append(tags, name)
		}
	}
	if len(tags) == 0 {
		return
	}
	authorID := author.ID
	createdAt := noteCreatedAt(note)

	s.pending.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("hashtag: panic in OnNoteCreated worker", "panic", r)
			}
		}()
		for _, name := range tags {
			hid := s.idGen.Generate(createdAt)
			if err := s.repo.RecordMention(hid, name, authorID, isLocal); err != nil {
				slog.Warn("hashtag: RecordMention failed", "name", name, "userID", authorID, "err", err)
			}
		}
	})
}

// WaitForPendingWrites blocks until all in-flight OnNoteCreated goroutines
// finish. Production code does not call this — it exists so unit tests can
// observe the post-write repo state deterministically without polling.
func (s *Service) WaitForPendingWrites() {
	if s == nil {
		return
	}
	s.pending.Wait()
}

// noteCreatedAt は note ID から作成時刻を再計算するためのヘルパ。idGen の
// ParseTime に依存するが、note 作成 hook なら ID は妥当な値が入っている
// 想定。失敗時はゼロ値時刻が返るが、Hashtag.ID は INSERT ON CONFLICT で
// 既存値が優先されるので新規 row 採番にしか影響しない (= 表示順への影響
// は無視できる)。
func noteCreatedAt(_ *model.Note) (_ time.Time) { return time.Now() }
