// Package signupapplication implements the state machine behind approval-based
// registration (#2554 / #2555).
//
// 申請は「他の Misskey サーバーのアカウント」を連絡先として作られ、管理者の審査を
// 経て登録に至る。**承認待ちを user 行として持たない**のが設計の核で、user が
// 作られるのは承認後の登録時 (#2556) だけ。
//
//	無し       -> pending                (Apply)
//	pending    -> approved / rejected    (Approve / Reject)
//	approved   -> completed              (MarkCompleted、#2556 の登録経路)
//	pending    -> expired                (期限切れ)
//	approved   -> expired                (期限切れ)
//
// rejected / expired / completed は終端。再申請は別レコードになる。
package signupapplication

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"gorm.io/gorm"
)

// DefaultTTL is how long an application stays usable, counted from creation.
//
// **短くしないこと。** 承認から登録までの間に相手インスタンスが一時的に落ちる
// ことは珍しくない。1 日にすると、復旧すれば通ったはずの人が期限切れで落ちる。
const DefaultTTL = 7 * 24 * time.Hour

// MaxReasonLength bounds the free-text application reason. `reason` 列の
// varchar(2048) に合わせる (超過は DB エラーではなく検証エラーで返す)。
const MaxReasonLength = 2048

var (
	// ErrLiveApplicationExists is returned when the contact already has a
	// pending or approved application.
	ErrLiveApplicationExists = errors.New("signupapplication: a live application already exists for this contact")
	// ErrNotFound is returned when the application does not exist.
	ErrNotFound = errors.New("signupapplication: not found")
	// ErrNotPending is returned when approving / rejecting an application that
	// is no longer awaiting review.
	ErrNotPending = errors.New("signupapplication: application is not pending")
	// ErrNotApproved is returned when completing an application that is not in
	// the approved state.
	ErrNotApproved = errors.New("signupapplication: application is not approved")
	// ErrExpired is returned when acting on an application past its deadline.
	//
	// **ErrNotPending と分けるのが要点。** 期限切れの行は status が pending の
	// まま残りうる (掃除は遅延反映) ので、まとめて「審査待ちではない」と返すと
	// 管理者には「審査待ちに見えるのに審査待ちではない」という説明になる。
	ErrExpired = errors.New("signupapplication: application has expired")
	// ErrInvalidContact is returned when the contact identity is incomplete.
	ErrInvalidContact = errors.New("signupapplication: invalid contact")
	// ErrReasonTooLong is returned when the free-text reason exceeds the limit.
	ErrReasonTooLong = errors.New("signupapplication: reason is too long")
)

// Contact identifies the remote account used as the applicant's contact.
//
// **Host と RemoteID が一致判定の鍵。** Username は表示専用で、相手サーバーでの
// 改名により変わりうる。MiAuth の `check` 応答は相手サーバーのローカルユーザーを
// 返すため `uri` が null であり、安定して使えるのはこの組だけ。
type Contact struct {
	Host     string
	RemoteID string
	Username string
}

// Service manages signup applications.
type Service struct {
	repo  repository.SignupApplicationRepository
	idGen id.Generator
	clock func() time.Time
	ttl   time.Duration
}

// NewService constructs a Service.
func NewService(repo repository.SignupApplicationRepository, idGen id.Generator) *Service {
	return &Service{
		repo:  repo,
		idGen: idGen,
		clock: time.Now,
		ttl:   DefaultTTL,
	}
}

// SetClock replaces the clock, primarily for tests.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.clock = now
	}
}

// SetTTL replaces how long an application stays usable. Non-positive values are
// ignored so a misconfiguration cannot make every application expire instantly.
func (s *Service) SetTTL(d time.Duration) {
	if d > 0 {
		s.ttl = d
	}
}

// Apply records a new application for the contact.
func (s *Service) Apply(contact Contact, reason string) (*model.SignupApplication, error) {
	if err := validateContact(contact); err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	// rune 単位で数える。varchar(2048) は文字数なので、byte で見ると日本語が
	// 通らなくなる。
	if len([]rune(reason)) > MaxReasonLength {
		return nil, ErrReasonTooLong
	}

	now := s.clock()
	// 期限切れの申請が席を占めたままだと、本人が申請し直せない。**参照の
	// たびに掃除する**ことで、掃除ジョブが無くても回るようにする。
	if _, err := s.expireIfStale(contact, now); err != nil {
		return nil, err
	}

	app := &model.SignupApplication{
		ID:              s.idGen.Generate(now),
		ContactHost:     contact.Host,
		ContactRemoteID: contact.RemoteID,
		ContactUsername: contact.Username,
		Status:          model.SignupApplicationPending,
		CreatedAt:       now,
		UpdatedAt:       now,
		ExpiresAt:       now.Add(s.ttl),
	}
	if reason != "" {
		app.Reason = &reason
	}

	if err := s.repo.Create(app); err != nil {
		if errors.Is(err, repository.ErrSignupApplicationLiveExists) {
			return nil, ErrLiveApplicationExists
		}
		return nil, fmt.Errorf("signupapplication: create: %w", err)
	}
	return app, nil
}

// Current returns the contact's live application, lazily expiring it when it is
// past its deadline. Returns nil (and no error) when there is none.
//
// 登録ページが「いまこの人はどの状態か」を出すための入口。
func (s *Service) Current(contact Contact) (*model.SignupApplication, error) {
	if err := validateContact(contact); err != nil {
		return nil, err
	}
	return s.expireIfStale(contact, s.clock())
}

// Latest returns the contact's most recent application regardless of status.
// 却下・期限切れの結果を申請者に見せるために使う。
func (s *Service) Latest(contact Contact) (*model.SignupApplication, error) {
	if err := validateContact(contact); err != nil {
		return nil, err
	}
	app, err := s.repo.FindLatestByContact(contact.Host, contact.RemoteID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("signupapplication: find latest: %w", err)
	}
	return app, nil
}

// expireIfStale returns the contact's live application, flipping it to expired
// (and returning nil) when it is past its deadline.
func (s *Service) expireIfStale(contact Contact, now time.Time) (*model.SignupApplication, error) {
	app, err := s.repo.FindLiveByContact(contact.Host, contact.RemoteID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("signupapplication: find live: %w", err)
	}
	if now.Before(app.ExpiresAt) {
		return app, nil
	}
	if err := s.transition(app.ID, now, func(cur *model.SignupApplication) decision {
		if !cur.IsLive() || now.Before(cur.ExpiresAt) {
			// 別の経路が先に処理していたら何もしない。
			return decision{}
		}
		// 掃除の経路では ErrExpired を呼び出し側に返さない (期限切れは
		// 「申請が無い」と同じ意味になる)。
		return decision{fields: map[string]any{"status": model.SignupApplicationExpired}}
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

// Approve marks the application as approved.
//
// **ここでは registration_ticket を発行しない。** 発行を承認時にすると、登録
// までの数日間、利用者に渡していない bearer 相当の credential が DB に置き
// っぱなしになる。チケットは登録経路 (#2556) で発行して即消費する。
func (s *Service) Approve(applicationID, moderatorID string) error {
	now := s.clock()
	return s.transition(applicationID, now, func(cur *model.SignupApplication) decision {
		if cur.Status != model.SignupApplicationPending {
			return decision{err: ErrNotPending}
		}
		// 期限切れを承認できてしまうと、掃除の前後で結果が変わる。ついでに
		// 実態へ寄せておく (掃除は遅延反映なので pending のまま残っている)。
		if !now.Before(cur.ExpiresAt) {
			return expireDecision()
		}
		return decision{fields: map[string]any{
			"status":        model.SignupApplicationApproved,
			"processedById": moderatorID,
			"processedAt":   now,
		}}
	})
}

// Reject marks the application as rejected.
func (s *Service) Reject(applicationID, moderatorID string) error {
	now := s.clock()
	return s.transition(applicationID, now, func(cur *model.SignupApplication) decision {
		if cur.Status != model.SignupApplicationPending {
			return decision{err: ErrNotPending}
		}
		// 期限切れは却下として記録しない。**審査していないものを「審査して
		// 落とした」と残すと、監査の意味が壊れる。**
		if !now.Before(cur.ExpiresAt) {
			return expireDecision()
		}
		return decision{fields: map[string]any{
			"status":        model.SignupApplicationRejected,
			"processedById": moderatorID,
			"processedAt":   now,
		}}
	})
}

// MarkCompleted records that the approved application produced a local account.
// ticketID は実際に消費した registration_ticket (監査用)。
//
// 登録経路 (#2556) から、ユーザー作成と同じ流れで呼ぶ。
func (s *Service) MarkCompleted(applicationID, userID, ticketID string) error {
	now := s.clock()
	return s.transition(applicationID, now, func(cur *model.SignupApplication) decision {
		if cur.Status != model.SignupApplicationApproved {
			return decision{err: ErrNotApproved}
		}
		if !now.Before(cur.ExpiresAt) {
			return expireDecision()
		}
		fields := map[string]any{
			"status":   model.SignupApplicationCompleted,
			"usedById": userID,
		}
		if ticketID != "" {
			fields["ticketId"] = ticketID
		}
		return decision{fields: fields}
	})
}

// decision is what a guarded transition wants to do with the locked row.
//
// **fields と err は同時に返せる。** 期限切れの検出は「expired に落とす」と
// 「呼び出し側にはエラーを返す」を同時に行う必要があるため。
type decision struct {
	fields map[string]any
	err    error
}

// transition applies a guarded state change under a row-level write lock.
//
// **ロックを取るのが要点。** 審査は管理者が、完了は登録経路が触るので、素の
// read-modify-write だと「却下と同時に登録が通る」ような取り違えが起きる。
// decide が nil の fields を返したら何もしない (競合して既に処理済み)。
func (s *Service) transition(
	applicationID string,
	now time.Time,
	decide func(cur *model.SignupApplication) decision,
) error {
	// **decide の err をトランザクションの外まで持ち越す。** gorm の
	// Transaction はコールバックがエラーを返すとロールバックするので、
	// 「期限切れに落としつつ ErrExpired を返す」を素直に書くと更新が巻き戻る。
	var outcome error
	if err := s.repo.DB().Transaction(func(tx *gorm.DB) error {
		cur, err := s.repo.FindByIDForUpdateTx(tx, applicationID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("signupapplication: lock: %w", err)
		}
		d := decide(cur)
		if len(d.fields) > 0 {
			d.fields["updatedAt"] = now
			if uerr := s.repo.UpdateFieldsTx(tx, applicationID, d.fields); uerr != nil {
				return fmt.Errorf("signupapplication: update: %w", uerr)
			}
		}
		outcome = d.err
		return nil
	}); err != nil {
		return err
	}
	return outcome
}

// expireDecision marks a live-but-overdue row as expired and reports ErrExpired.
// 期限切れは審査の結果ではないので、processedBy / processedAt は立てない。
func expireDecision() decision {
	return decision{
		fields: map[string]any{"status": model.SignupApplicationExpired},
		err:    ErrExpired,
	}
}

// Get returns a single application by ID.
func (s *Service) Get(applicationID string) (*model.SignupApplication, error) {
	app, err := s.repo.FindByID(applicationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("signupapplication: find: %w", err)
	}
	return app, nil
}

// List returns applications for the admin review screen, newest first.
func (s *Service) List(filter string, limit, offset int) ([]*model.SignupApplication, error) {
	rows, err := s.repo.List(filter, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("signupapplication: list: %w", err)
	}
	return rows, nil
}

// Count returns how many applications match the filter.
func (s *Service) Count(filter string) (int, error) {
	n, err := s.repo.Count(filter)
	if err != nil {
		return 0, fmt.Errorf("signupapplication: count: %w", err)
	}
	return n, nil
}

// ExpireStale flips every overdue live application to `expired`.
// 遅延反映 (expireIfStale) だけでも運用は回るが、管理画面の一覧を実態に
// 合わせるために一括でも掃除できるようにしておく。
func (s *Service) ExpireStale() (int, error) {
	n, err := s.repo.ExpireStale(s.clock())
	if err != nil {
		return 0, fmt.Errorf("signupapplication: expire stale: %w", err)
	}
	return n, nil
}

func validateContact(c Contact) error {
	if strings.TrimSpace(c.Host) == "" || strings.TrimSpace(c.RemoteID) == "" {
		return ErrInvalidContact
	}
	return nil
}
