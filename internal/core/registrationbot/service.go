// Package registrationbot owns the dedicated local account that announces
// approval-based registration results (#2554 / #2557).
//
// **システムアカウントは使わない。** `instance.actor` 等はユーザー名にドットを
// 含むことで判定されており、AP 上 `Application` として配信される。実装によって
// 特別扱いされうるうえ、アバターがインスタンスアイコン固定でプロフィールに用途を
// 書けず、`instance.actor` は AP のオブジェクト取得と通報にも使われる共用
// アカウントでもある。
//
// ここで作るのはドットを含まない通常の bot なので `Service` として配信される。
package registrationbot

import (
	"errors"
	"fmt"
	"time"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// Username is the account's fixed local username.
//
// **ドットを含めないこと。** システムアカウント判定は `username.includes('.')`
// なので、ドットがあると `Application` として配信されてしまう。
const Username = "registration_service"

// ErrUsernameTaken is returned when a human account already holds the name.
//
// 黙って別名にすると、運営者は「作ったつもりの bot」と実際に喋るアカウントが
// 食い違ったことに気づけない。
var ErrUsernameTaken = errors.New("registrationbot: the username is already taken by another account")

// Service creates and fetches the announcement bot.
type Service struct {
	userRepo    repository.UserRepository
	keypairRepo repository.UserKeypairRepository
	usedRepo    repository.UsedUsernameRepository
	idGen       id.Generator
	clock       func() time.Time
}

// NewService constructs a Service.
func NewService(
	userRepo repository.UserRepository,
	keypairRepo repository.UserKeypairRepository,
	usedRepo repository.UsedUsernameRepository,
	idGen id.Generator,
) *Service {
	return &Service{
		userRepo:    userRepo,
		keypairRepo: keypairRepo,
		usedRepo:    usedRepo,
		idGen:       idGen,
		clock:       time.Now,
	}
}

// SetClock replaces the clock, primarily for tests.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.clock = now
	}
}

// Ensure returns the bot account, creating it when missing.
//
// 冪等。既にあれば作り直さない — **作り直すと actor URI が変わり、それまでの
// フォロワーが失われて通知が相手の受信設定を通らなくなる。**
func (s *Service) Ensure() (*model.User, error) {
	existing, err := s.userRepo.FindByUsernameLower(Username, nil)
	if err == nil && existing != nil {
		if !existing.IsBot {
			// 人間のアカウントが同じ名前を持っている。黙って使わない。
			return nil, ErrUsernameTaken
		}
		return s.completeFromOrphan(existing)
	}

	now := s.clock()
	user := &model.User{
		ID:            s.idGen.Generate(now),
		Username:      Username,
		UsernameLower: Username,
		Host:          nil,
		IsBot:         true,
		// **false にすること。** 承認 DM を相手の通知フィルタに通すため、
		// 申請者にはこのアカウントのフォローを案内する。true だとフォローが
		// 承認待ちのまま滞留し、承認する手段が無いので永久に成立しない
		// (実際に instance.actor には滞留した follow request がある)。
		IsLocked: false,
		// 一覧に出す必要は無い。
		IsExplorable: false,
	}
	if err := s.userRepo.Create(user); err != nil {
		return nil, fmt.Errorf("registrationbot: create user: %w", err)
	}
	// ユーザー名を予約する。**予約しないと、bot を消したときに他人が同じ名前を
	// 取れてしまう。**
	if s.usedRepo != nil {
		if uerr := s.usedRepo.Create(Username); uerr != nil {
			// 既に予約済みなら成功として扱う (冪等)。
			if exists, cerr := s.usedRepo.Exists(Username); cerr != nil || !exists {
				return nil, fmt.Errorf("registrationbot: reserve username: %w", uerr)
			}
		}
	}
	return s.completeFromOrphan(user)
}

// completeFromOrphan fills in whatever rows are missing, so a partial failure
// on a previous attempt completes on the next call.
func (s *Service) completeFromOrphan(user *model.User) (*model.User, error) {
	if _, err := s.userRepo.FindProfileByUserID(user.ID); err != nil {
		// パスワードは入れない。**通常の認証経路を作らない。**
		if perr := s.userRepo.CreateProfile(&model.UserProfile{UserID: user.ID}); perr != nil {
			return nil, fmt.Errorf("registrationbot: create profile: %w", perr)
		}
	}
	if _, err := s.keypairRepo.FindByUserID(user.ID); err != nil {
		privPEM, pubPEM, kerr := activitypub.GenerateRSAKeypair()
		if kerr != nil {
			return nil, fmt.Errorf("registrationbot: generate keypair: %w", kerr)
		}
		if kerr := s.keypairRepo.Create(&model.UserKeypair{
			UserID:     user.ID,
			PublicKey:  pubPEM,
			PrivateKey: privPEM,
		}); kerr != nil {
			return nil, fmt.Errorf("registrationbot: create keypair: %w", kerr)
		}
	}
	return user, nil
}

// IsBotAccount reports whether the user is this bot.
//
// reset-password / アカウント削除のガードが使う。**通常のシステムアカウントの
// ガードはユーザー名のドットで判定するので、この bot は対象外**になる。
func IsBotAccount(u *model.User) bool {
	return u != nil && u.Host == nil && u.UsernameLower == Username
}
