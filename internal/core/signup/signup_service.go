package signup

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// localUsernamePattern は upstream Misskey TS の `localUsernameSchema`
// (= third_party の models/User.ts:319) に整合する username 検証 regex。
// `^\w{1,20}$` 相当で `\w` = [a-zA-Z0-9_]、length 1-20。
//
// Go の regexp は RE2 default で `\w` が ASCII の [0-9A-Za-z_] に限定されるため
// upstream JS の `\w` と挙動が一致する。意図を明示するため明示的に文字クラスを
// 書き下している (Unicode property を含めない契約をコード上に固定する目的)。
//
// 旧 mk-go は length 128 文字まで許容していて drop-in で TS instance に
// 引き継いだ後 frontend (= TS の username schema) で reject される
// regression があった (#800)。
var localUsernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]{1,20}$`)

// normalizeAndValidateUsername trims surrounding whitespace and validates
// the username against upstream Misskey TS の `localUsernameSchema`. 返り値
// の username は trim 済みなので、caller はそのまま小文字化や DB 永続化に
// 利用してよい。validation 失敗時は ErrInvalidUsername を返す。
func normalizeAndValidateUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	if !localUsernamePattern.MatchString(username) {
		return "", ErrInvalidUsername
	}
	return username, nil
}

var (
	// ErrUsernameAlreadyExists is returned when the username is taken.
	ErrUsernameAlreadyExists = errors.New("username already exists")
	// ErrInvalidUsername is returned when the username is invalid.
	ErrInvalidUsername = errors.New("invalid username")
	// ErrUsernameReserved is returned when the username matches an entry in
	// meta.preservedUsernames (case-insensitive). 初回セットアップ時は root
	// ユーザー作成を妨げないため、このチェックはスキップする。
	ErrUsernameReserved = errors.New("username is reserved")
	// ErrPendingNotFound is returned when no user_pending row matches the code.
	ErrPendingNotFound = errors.New("pending signup not found")
	// ErrPendingExpired is returned when the pending signup is past its TTL.
	// TTL は ID (ULID) 由来 timestamp から算出する (createdAt カラム不在のため)。
	ErrPendingExpired = errors.New("pending signup expired")
	// ErrInvitationAlreadyUsed is returned when the invitation ticket linked to
	// a pending signup has already been consumed by another user. transaction
	// 経路で SELECT FOR UPDATE 後に usedById を確認することで concurrent burst
	// の race を完全に閉じる (#604)。
	ErrInvitationAlreadyUsed = errors.New("invitation already used")
	// ErrInvitationRevoked is returned when the invitation ticket linked to a
	// pending signup has been deleted (typically by an admin) before the user
	// completed the email verification. AlreadyUsed と区別することで frontend
	// が「使用済」「失効」をローカライズで分けられる (#610 item 2)。
	ErrInvitationRevoked = errors.New("invitation revoked")
	// ErrPasswordTooLong is returned when the supplied password exceeds the
	// 72-byte limit of bcrypt. Go の bcrypt は 73 byte 以上で
	// ErrPasswordTooLong を返すが Node の bcrypt は silent truncation する。
	// mk-go 側で 400 に変換しておかないと、空 hash が DB に書かれて user が
	// 永久 login 不能になる (#1075)。
	ErrPasswordTooLong = errors.New("password too long")
)

// PendingSignupTTL is the default lifetime of a pending signup row. Misskey TS
// 実装では明示的な TTL は無いが、放置 row の蓄積を避けるため 24h で運用する。
// ID (ULID) の timestamp と比較して PromotePending 時に判定する。
const PendingSignupTTL = 24 * time.Hour

// WebhookHook is invoked after a new local user has been created so that
// system webhooks subscribed to `userCreated` can fire. 循環依存を避けるため
// interface で受け取る (実装は core/webhook)。
type WebhookHook interface {
	OnUserCreated(user *model.User)
}

// Service handles user registration.
type Service struct {
	userRepo    repository.UserRepository
	metaRepo    repository.MetaRepository
	keypairRepo repository.UserKeypairRepository
	pendingRepo repository.UserPendingRepository
	ticketRepo  repository.RegistrationTicketRepository
	// db は PromotePending を transaction 化して partial failure rollback と
	// invitation ticket の SELECT FOR UPDATE ロックを実現する用途。未設定 (nil)
	// なら従来の repo-based 非 tx パスにフォールバックする (mock テスト用)。
	db          *gorm.DB
	webhookHook WebhookHook
	idGen       id.Generator
}

// NewService creates a new SignupService.
func NewService(userRepo repository.UserRepository, metaRepo repository.MetaRepository, idGen id.Generator) *Service {
	return &Service{userRepo: userRepo, metaRepo: metaRepo, idGen: idGen}
}

// SetDB wires the GORM database handle so PromotePending can run inside a
// transaction. production の router 配線時に呼ぶ。設定しない場合は repo-based
// 非 tx パスで動作する (mock テスト用)。
func (s *Service) SetDB(db *gorm.DB) {
	s.db = db
}

// SetTicketRepo wires the RegistrationTicketRepository so PromotePending tx
// path で invitation ticket を SELECT FOR UPDATE ロックして MarkUsed まで
// 同一 transaction で完結できる。emailRequiredForSignup + DisableRegistration
// 併用時の race fix (#604) に必須。
func (s *Service) SetTicketRepo(r repository.RegistrationTicketRepository) {
	s.ticketRepo = r
}

// SetUserPendingRepo wires the user_pending repository so CreatePending /
// PromotePending become available. emailRequiredForSignup フローを使う場合は
// 必須。未設定でも通常の Signup は動く。
func (s *Service) SetUserPendingRepo(r repository.UserPendingRepository) {
	s.pendingRepo = r
}

// SetKeypairRepo wires the user keypair repository. When set, Signup will
// generate a fresh RSA keypair for each newly created local user, which is
// required for ActivityPub federation (actor endpoints return publicKey).
func (s *Service) SetKeypairRepo(r repository.UserKeypairRepository) {
	s.keypairRepo = r
}

// SetWebhookHook attaches a WebhookHook invoked after user creation so that
// system webhooks subscribed to `userCreated` can fire.
func (s *Service) SetWebhookHook(h WebhookHook) {
	s.webhookHook = h
}

// SignupResult holds the created user and their native token.
type SignupResult struct {
	User  *model.User
	Token string
	// InvitationTicketID は PromotePending 経路で pending row から復元した
	// 招待コード ticket ID。通常 Signup や非招待 PromotePending では nil。
	InvitationTicketID *string
	// InvitationTicketConsumed は Service が tx 内で MarkUsed 済かどうか。
	// 非 tx 経路 (mock) で false なら handler 側で best-effort consume する。
	InvitationTicketConsumed bool
}

// Signup creates a new local user with the given username and password.
// isInitialSetup=true の場合、作成したユーザーを rootUser に設定する。
func (s *Service) Signup(username, password string, isInitialSetup bool) (*SignupResult, error) {
	username, err := normalizeAndValidateUsername(username)
	if err != nil {
		return nil, err
	}

	// ユーザー名の重複チェック
	lower := strings.ToLower(username)
	if _, err := s.userRepo.FindByUsernameLower(lower, nil); err == nil {
		return nil, ErrUsernameAlreadyExists
	}

	// meta.preservedUsernames チェック。初回セットアップ (root ユーザー作成) は
	// admin / root が予約ワードに含まれうるので除外する。meta fetch 失敗時は
	// ベストエフォートで通過させる (オンライン性を優先)。
	if !isInitialSetup {
		if meta, err := s.metaRepo.Fetch(); err == nil && isReservedUsername(lower, meta.PreservedUsernames) {
			return nil, ErrUsernameReserved
		}
	}

	// bcrypt は 73 byte 以上の password で ErrPasswordTooLong を返す。Node の
	// bcrypt が silent truncation するのと異なり、Go の bcrypt は error にする
	// ため、ここで拾わないと空 hash が DB に書かれて user が永久 login 不能に
	// なる (#1075)。
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		if errors.Is(err, bcrypt.ErrPasswordTooLong) {
			return nil, ErrPasswordTooLong
		}
		return nil, fmt.Errorf("signup: bcrypt hash password: %w", err)
	}

	// native token 生成 (16文字hex)
	token := generateToken()

	now := time.Now()
	userID := s.idGen.Generate(now)
	user := &model.User{
		ID:                userID,
		Username:          username,
		UsernameLower:     lower,
		Token:             &token,
		IsExplorable:      true,
		AvatarDecorations: []byte("[]"),
		// drop-in 互換 (#785): Misskey TS と同じ意味で初回 signup user を
		// isRoot=true でマークしておく。mk-go pure 経路では meta.rootUserId
		// で root を判定するので冗長だが、TS との bidirectional drop-in を
		// 担保するためマーカーを揃える。
		IsRoot: isInitialSetup,
	}
	if err := s.userRepo.Create(user); err != nil {
		return nil, err
	}

	// user_profile 作成
	hashStr := string(hash)
	profile := &model.UserProfile{
		UserID:             userID,
		Password:           &hashStr,
		AutoAcceptFollowed: true,
		PreventAiLearning:  true,
		PublicReactions:    true,
	}
	if err := s.userRepo.CreateProfile(profile); err != nil {
		return nil, err
	}

	// RSA keypair (federation 用)。keypairRepo が未設定の場合はスキップする
	// (従来の単体テストのため)。
	if s.keypairRepo != nil {
		privPEM, pubPEM, err := activitypub.GenerateRSAKeypair()
		if err != nil {
			return nil, err
		}
		if err := s.keypairRepo.Create(&model.UserKeypair{
			UserID:     userID,
			PublicKey:  pubPEM,
			PrivateKey: privPEM,
		}); err != nil {
			return nil, err
		}
	}

	// 初回セットアップの場合は rootUserId を設定
	if isInitialSetup {
		_ = s.metaRepo.Update(map[string]any{"rootUserId": userID})
	}

	// システムWebhook発火（`userCreated` 相当）。ベストエフォート。
	if s.webhookHook != nil {
		s.webhookHook.OnUserCreated(user)
	}

	return &SignupResult{User: user, Token: token}, nil
}

// generateToken creates a random 16-character token.
// crypto/rand.Read は実質的に失敗しない。
func generateToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// generatePendingCode creates a 32-char (16 byte) hex code used both as the
// user_pending.code column and the email confirmation link path segment.
// signup native token と区別するため長め。
func generatePendingCode() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CreatePending stores a pending signup row and returns it. Username重複と
// 予約名チェックは通常 Signup と揃え、password は bcrypt で hash 済を保管
// (PromotePending 時に再 hash しない)。emailRequiredForSignup=true 用。
//
// invitationTicketID が non-nil なら pending row に保存し、PromotePending
// 成功時に SignupResult.InvitationTicketID 経由で handler に伝える
// (招待制 + email 確認制併用時の MarkUsed 用)。
func (s *Service) CreatePending(username, email, password string, invitationTicketID *string) (*model.UserPending, error) {
	username, err := normalizeAndValidateUsername(username)
	if err != nil {
		return nil, err
	}
	lower := strings.ToLower(username)

	// 確定済 user との衝突チェック (この段階で押さえておかないと、後段の
	// PromotePending 直前まで気付けず無駄なメール送信になる)。
	if _, err := s.userRepo.FindByUsernameLower(lower, nil); err == nil {
		return nil, ErrUsernameAlreadyExists
	}
	if meta, err := s.metaRepo.Fetch(); err == nil && isReservedUsername(lower, meta.PreservedUsernames) {
		return nil, ErrUsernameReserved
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		if errors.Is(err, bcrypt.ErrPasswordTooLong) {
			return nil, ErrPasswordTooLong
		}
		return nil, fmt.Errorf("signup: bcrypt hash password: %w", err)
	}
	now := time.Now()
	row := &model.UserPending{
		ID:                 s.idGen.Generate(now),
		Code:               generatePendingCode(),
		Username:           username,
		Email:              email,
		Password:           string(hash),
		InvitationTicketID: invitationTicketID,
	}
	if err := s.pendingRepo.Create(row); err != nil {
		return nil, err
	}
	return row, nil
}

// PromotePending finalizes a pending signup: looks up by code, confirms the
// row hasn't expired, then creates the user using the stored hashed password
// (再 hash しない)。成功時に user_pending row を delete する。
//
// db が SetDB で wire されている場合は GORM transaction 内で処理し、partial
// failure を rollback で巻き戻す (#600 item 2)。さらに invitation 経由なら
// ticket を SELECT FOR UPDATE で lock して concurrent burst による「1 招待で
// 複数 user 作成」 race を完全に閉じる (#604)。MarkUsed も同 tx で完了する。
//
// db 未配線時は repo-based 非 tx パスに fallback (mock テスト互換)。
//
// 失敗パターン:
//   - ErrPendingNotFound: code が無い / DB error
//   - ErrPendingExpired: ID (ULID) timestamp が PendingSignupTTL を超過
//   - ErrUsernameAlreadyExists: 確認 link 待ちの間に同名 user が登録されたケース
//   - ErrInvitationAlreadyUsed: tx 経路で ticket がすでに別 user に消費済 (#604)
func (s *Service) PromotePending(code string) (*SignupResult, error) {
	pending, err := s.pendingRepo.FindByCode(code)
	if err != nil {
		return nil, ErrPendingNotFound
	}
	// ID (ULID) から作成時刻を引き、TTL を過ぎていれば拒否。row 自体は
	// 残しておく (cron での bulk cleanup を前提)。
	if t, err := s.idGen.ParseTime(pending.ID); err == nil {
		if time.Since(t) > PendingSignupTTL {
			return nil, ErrPendingExpired
		}
	}

	if s.db != nil && s.ticketRepo != nil {
		return s.promotePendingTx(pending)
	}
	return s.promotePendingNoTx(pending)
}

// promotePendingTx は db.Transaction 内で user 作成 / ticket 消費 / pending
// 削除を atomic に行う本番経路。
//
// IMPORTANT: promotePendingTx と promotePendingNoTx は **mirror で維持** する。
// username collision check / user-profile create / keypair gen / webhook hook /
// SignupResult build の順序や意味が両者でずれると挙動が divergence する。
// 片方を変更したら必ずもう片方も同じ shape に揃えること (#610 item 3)。
// tx 経路は raw GORM (tx.Create, tx.Where 等) を直接呼び、noTx 経路は repo
// 経由 (mock 互換のため)。
func (s *Service) promotePendingTx(pending *model.UserPending) (*SignupResult, error) {
	lower := strings.ToLower(pending.Username)
	token := generateToken()
	now := time.Now()
	userID := s.idGen.Generate(now)

	var resultUser *model.User
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		// invitation ticket がある場合は最初に SELECT FOR UPDATE で lock し、
		// 既に usedById がセットされていれば早期 return。これで concurrent
		// promote 試行は直列化される (#604 race fix)。
		var lockedTicketID string
		if pending.InvitationTicketID != nil {
			ticket, err := s.ticketRepo.FindByIDForUpdateTx(tx, *pending.InvitationTicketID)
			if err != nil {
				// gorm.ErrRecordNotFound は admin が ticket を revoke したケース
				// なので "Revoked" として明示。それ以外 (DB error) は internal
				// として上に伝える (handler が 500 を返す)。
				// 観測用 slog.Warn で運用側に signal を出す (#610 item 2)。
				if errors.Is(err, gorm.ErrRecordNotFound) {
					slog.Warn("promote pending: invitation ticket missing",
						"pendingId", pending.ID, "ticketId", *pending.InvitationTicketID)
					return ErrInvitationRevoked
				}
				return err
			}
			if ticket.UsedByID != nil {
				return ErrInvitationAlreadyUsed
			}
			lockedTicketID = ticket.ID
		}

		// username collision チェック (tx の visibility で in-flight 行も見える)
		var existing model.User
		switch err := tx.Where(`"usernameLower" = ? AND "host" IS NULL`, lower).First(&existing).Error; {
		case err == nil:
			return ErrUsernameAlreadyExists
		case errors.Is(err, gorm.ErrRecordNotFound):
			// 続行
		default:
			return err
		}

		// user 行作成
		user := &model.User{
			ID:                userID,
			Username:          pending.Username,
			UsernameLower:     lower,
			Token:             &token,
			IsExplorable:      true,
			AvatarDecorations: []byte("[]"),
		}
		if err := tx.Create(user).Error; err != nil {
			return err
		}

		// profile 作成 (failure 時は user 含めて tx rollback で消える)
		storedHash := pending.Password
		profile := &model.UserProfile{
			UserID:             userID,
			Email:              &pending.Email,
			EmailVerified:      true,
			Password:           &storedHash,
			AutoAcceptFollowed: true,
			PreventAiLearning:  true,
			PublicReactions:    true,
		}
		if err := tx.Create(profile).Error; err != nil {
			return err
		}

		// keypair (federation 用) — keypairRepo が wire されているときのみ
		if s.keypairRepo != nil {
			privPEM, pubPEM, err := activitypub.GenerateRSAKeypair()
			if err != nil {
				return err
			}
			if err := tx.Create(&model.UserKeypair{
				UserID:     userID,
				PublicKey:  pubPEM,
				PrivateKey: privPEM,
			}).Error; err != nil {
				return err
			}
		}

		// pending row 削除
		if err := tx.Where("id = ?", pending.ID).Delete(&model.UserPending{}).Error; err != nil {
			return err
		}

		// ticket 消費 (lock 中なので 100% 自分が最初)
		if lockedTicketID != "" {
			if err := s.ticketRepo.MarkUsedTx(tx, lockedTicketID, userID); err != nil {
				return err
			}
		}

		resultUser = user
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}

	// webhook hook は tx 完了後 (ベストエフォート、commit 後の non-critical 副作用)
	if s.webhookHook != nil {
		s.webhookHook.OnUserCreated(resultUser)
	}
	return &SignupResult{
		User:               resultUser,
		Token:              token,
		InvitationTicketID: pending.InvitationTicketID,
		// 招待 ticket がある場合のみ tx 内で MarkUsedTx 済 → consumed = true。
		// 非招待 pending では nil なので consumed = false で返し、handler 側でも
		// 何もしない (InvitationTicketID nil で早期 return)。
		InvitationTicketConsumed: pending.InvitationTicketID != nil,
	}, nil
}

// promotePendingNoTx は db 未配線の mock テスト経路 (transaction 無し)。
// production では使わない (router 配線時に SetDB 必須)。
//
// IMPORTANT: promotePendingTx と mirror で維持すること。詳細は
// promotePendingTx の doc コメント参照 (#610 item 3)。
// invitation ticket の lock / consume は tx 経路でしか行えないため、本経路
// では InvitationTicketID を SignupResult に伝搬するだけで MarkUsed しない
// (handler が non-tx 経路と判定して fallback consume する)。
func (s *Service) promotePendingNoTx(pending *model.UserPending) (*SignupResult, error) {
	lower := strings.ToLower(pending.Username)
	if _, err := s.userRepo.FindByUsernameLower(lower, nil); err == nil {
		return nil, ErrUsernameAlreadyExists
	}

	token := generateToken()
	now := time.Now()
	userID := s.idGen.Generate(now)
	user := &model.User{
		ID:                userID,
		Username:          pending.Username,
		UsernameLower:     lower,
		Token:             &token,
		IsExplorable:      true,
		AvatarDecorations: []byte("[]"),
	}
	if err := s.userRepo.Create(user); err != nil {
		return nil, err
	}
	storedHash := pending.Password
	profile := &model.UserProfile{
		UserID:             userID,
		Email:              &pending.Email,
		EmailVerified:      true,
		Password:           &storedHash,
		AutoAcceptFollowed: true,
		PreventAiLearning:  true,
		PublicReactions:    true,
	}
	if err := s.userRepo.CreateProfile(profile); err != nil {
		return nil, err
	}
	if s.keypairRepo != nil {
		privPEM, pubPEM, err := activitypub.GenerateRSAKeypair()
		if err != nil {
			return nil, err
		}
		if err := s.keypairRepo.Create(&model.UserKeypair{
			UserID:     userID,
			PublicKey:  pubPEM,
			PrivateKey: privPEM,
		}); err != nil {
			return nil, err
		}
	}
	_ = s.pendingRepo.Delete(pending.ID)

	if s.webhookHook != nil {
		s.webhookHook.OnUserCreated(user)
	}
	return &SignupResult{
		User:               user,
		Token:              token,
		InvitationTicketID: pending.InvitationTicketID,
	}, nil
}

// isReservedUsername reports whether lower (already lowercased) matches any
// entry in reserved case-insensitively. Entries in reserved are trimmed and
// lowercased before comparison so DB-side whitespace noise does not defeat
// the check.
func isReservedUsername(lower string, reserved []string) bool {
	for _, r := range reserved {
		if strings.EqualFold(strings.TrimSpace(r), lower) {
			return true
		}
	}
	return false
}
