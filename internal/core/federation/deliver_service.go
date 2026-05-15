package federation

import (
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/repository"
)

// Errors returned by DeliverService.
var (
	// ErrSignerKeyMissing is returned when no keypair is found for the signer.
	ErrSignerKeyMissing = errors.New("signer keypair not found")
	// ErrNoLocalSigner is returned when DeliverService is asked to sign with a
	// remote user. リモートユーザーの代理署名は不可能。
	ErrNoLocalSigner = errors.New("cannot sign on behalf of a remote user")
)

// HostBlockChecker reports whether a host is on the blocked list and whether
// the local instance is willing to federate with it (allowlist + blocklist
// combined). federation package が core/instance に直接依存しないよう
// minimal interface だけ持つ (#536)。
type HostBlockChecker interface {
	IsBlocked(host string) bool
	// IsAllowed returns true when federation with host is permitted under
	// the current admin federation mode (none / specified / all) and the
	// host is not in blockedHosts.
	IsAllowed(host string) bool
}

// ed25519CacheTTL is how long recipient capability / sender Ed25519 keypair
// lookups stay in the in-memory cache. 5 分は actor refresh の actorTTL より
// 短く、Ed25519 鍵 rotation を検出する時間としては十分。capabilityCache に
// 入ったあとは同 recipient への deliver で DB を叩かない (#1080 review #2/#3)。
const ed25519CacheTTL = 5 * time.Minute

type ed25519CapEntry struct {
	capable   bool
	fetchedAt time.Time
}

type ed25519SignerEntry struct {
	keyID     string
	privPEM   string
	fetchedAt time.Time
}

// DeliverService computes recipient inboxes and enqueues HTTP-signed delivery
// jobs onto the asynq queue.
//
// 配信先計算と enqueue を分離するため、実際のHTTP送信は queue/processors の
// DeliverProcessor が担当する。
type DeliverService struct {
	enqueuer           queue.Enqueuer
	userRepo           repository.UserRepository
	followingRepo      repository.FollowingRepository
	keypairRepo        repository.UserKeypairRepository
	keypairExtraRepo   repository.UserKeypairExtraRepository   // optional: sender Ed25519 鍵
	publickeyExtraRepo repository.UserPublickeyExtraRepository // optional: recipient capability 判定
	urls               *activitypub.URLBuilder
	hostBlocker        HostBlockChecker
	// syncDeliverHook, when non-nil, replaces the asynq enqueue with an
	// inline call to the hook. test 専用で federation deliver の queue 経路
	// を bypass し、sign + HTTP POST を同期実行する e2e_federation 用。
	// production code から SetSyncDeliverHook を呼ばないこと (#780)。
	syncDeliverHook func(payload queue.DeliverPayload) error
	// capCache / signerCache は recipient capability / sender Ed25519 鍵の
	// 短期 in-memory cache (#1080 review #2/#3)。多 deliver で DB を叩く
	// N+1 を抑えるため、TTL 5min で lookup を memoize する。actor 削除 /
	// 鍵 rotation 後は TTL 経過で自動的に refresh される。
	capCache    sync.Map         // string (userID) -> ed25519CapEntry
	signerCache sync.Map         // string (userID) -> ed25519SignerEntry
	clock       func() time.Time // テストで差し替え可能
}

// NewDeliverService constructs a DeliverService.
func NewDeliverService(
	enqueuer queue.Enqueuer,
	userRepo repository.UserRepository,
	followingRepo repository.FollowingRepository,
	keypairRepo repository.UserKeypairRepository,
	urls *activitypub.URLBuilder,
) *DeliverService {
	return &DeliverService{
		enqueuer:      enqueuer,
		userRepo:      userRepo,
		followingRepo: followingRepo,
		keypairRepo:   keypairRepo,
		urls:          urls,
		clock:         time.Now,
	}
}

// SetHostBlockChecker attaches a HostBlockChecker. 配信時にこの checker が
// 呼ばれ、ブロック対象ホストの inbox にはエンキューしない。
func (s *DeliverService) SetHostBlockChecker(c HostBlockChecker) {
	s.hostBlocker = c
}

// SetKeypairExtraRepo wires the Ed25519 keypair repository so sender が
// Ed25519 鍵を持つ場合に DeliverActivityWithRecipient 経路で Ed25519 sign
// 情報を payload に詰めることができる。未配線なら全配送 RSA fallback
// (= 旧 deployment と完全互換 / drop-in 維持) (#1067 / #1071)。
func (s *DeliverService) SetKeypairExtraRepo(r repository.UserKeypairExtraRepository) {
	s.keypairExtraRepo = r
}

// SetPublickeyExtraRepo wires the remote Multikey repository so recipient の
// Ed25519 capability を user_publickey_extra で判定できる。未配線なら全配送
// RSA fallback (#1067 / #1071)。
func (s *DeliverService) SetPublickeyExtraRepo(r repository.UserPublickeyExtraRepository) {
	s.publickeyExtraRepo = r
}

// SetSyncDeliverHookForTest replaces the asynq enqueue with an inline
// synchronous call. Used by e2e_federation tests to bypass the queue layer
// and exercise the sign + POST + inbox handling path directly. Not for
// production use (#780).
//
// fn=nil restores the default (queue) path.
func (s *DeliverService) SetSyncDeliverHookForTest(fn func(payload queue.DeliverPayload) error) {
	s.syncDeliverHook = fn
}

// DeliverActivity enqueues a deliver job for each unique inbox in inboxes.
// signerUserID は署名に使うローカルユーザー。ブロック対象ホストの inbox は
// スキップする。受信者の actor 情報を caller が持たない経路 (= followers /
// inbox list ベース) は RSA only で配送される。受信者 actor URI が分かる
// 場合は DeliverActivityWithRecipient を使うと FEP-521a Multikey 対応サーバー
// 向けに Ed25519 sign が試行される (#1067 / #1071)。
func (s *DeliverService) DeliverActivity(signerUserID string, body []byte, inboxes []string) error {
	return s.deliverInternal(signerUserID, body, inboxes, nil)
}

// DeliverActivityWithRecipient is DeliverActivity + recipient capability based
// Ed25519 sign 試行。recipient が user_publickey_extra に Ed25519 鍵を持ち、
// sender が user_keypair_extra に Ed25519 鍵を持つ場合、payload に Ed25519
// 鍵情報も詰める (DeliverProcessor 側で実際の sign 分岐 + degrade safeguard
// を担う)。recipient == nil なら DeliverActivity と等価動作 (#1067 / #1071)。
func (s *DeliverService) DeliverActivityWithRecipient(signerUserID string, body []byte, inboxes []string, recipient *model.User) error {
	return s.deliverInternal(signerUserID, body, inboxes, recipient)
}

func (s *DeliverService) deliverInternal(signerUserID string, body []byte, inboxes []string, recipient *model.User) error {
	if len(inboxes) == 0 {
		return nil
	}
	keyID, keyPEM, err := s.signerCredentials(signerUserID)
	if err != nil {
		return err
	}
	// Ed25519 sign 経路を試行できるのは recipient が Ed25519 capable
	// (assertionMethod を持つ) + sender が Ed25519 keypair を持つ両方が
	// 真のときのみ。どちらかが false なら edKeyID/edPrivPEM は空のまま payload
	// に詰められ、DeliverProcessor は従来通り RSA で sign する。
	var edKeyID, edPrivPEM string
	if recipient != nil && s.recipientIsEd25519Capable(recipient) {
		edKeyID, edPrivPEM = s.signerEd25519Credentials(signerUserID)
	}
	seen := make(map[string]struct{}, len(inboxes))
	for _, inbox := range inboxes {
		if inbox == "" {
			continue
		}
		if _, dup := seen[inbox]; dup {
			continue
		}
		if s.isBlockedInbox(inbox) {
			continue
		}
		seen[inbox] = struct{}{}
		payload := queue.DeliverPayload{
			Inbox:          inbox,
			Body:           body,
			KeyID:          keyID,
			KeyPEM:         keyPEM,
			Ed25519KeyID:   edKeyID,
			Ed25519PrivPEM: edPrivPEM,
		}
		if s.syncDeliverHook != nil {
			// test 経路 (#780): queue を経由せず inline で sign + POST。
			if err := s.syncDeliverHook(payload); err != nil {
				return fmt.Errorf("sync deliver to %s: %w", inbox, err)
			}
			continue
		}
		if err := s.enqueuer.EnqueueDeliver(payload); err != nil {
			return fmt.Errorf("enqueue deliver to %s: %w", inbox, err)
		}
	}
	return nil
}

// recipientIsEd25519Capable reports whether the recipient actor has at least
// one Ed25519 entry in user_publickey_extra (= 受信側が FEP-521a Multikey で
// Ed25519 を expose している)。未配線時 / DB error / 行なしのいずれも false
// (= 安全側で RSA fallback)。capCache で TTL 5min 結果を memoize し、N+1
// query を抑える (#1080 review #2)。
func (s *DeliverService) recipientIsEd25519Capable(recipient *model.User) bool {
	if s.publickeyExtraRepo == nil || recipient == nil {
		return false
	}
	if cached, ok := s.capCache.Load(recipient.ID); ok {
		entry := cached.(ed25519CapEntry)
		if s.clock().Sub(entry.fetchedAt) <= ed25519CacheTTL {
			return entry.capable
		}
	}
	rows, err := s.publickeyExtraRepo.ListByUserID(recipient.ID)
	if err != nil {
		// DB error は cache せず次回 retry させる
		return false
	}
	capable := len(rows) > 0
	s.capCache.Store(recipient.ID, ed25519CapEntry{
		capable:   capable,
		fetchedAt: s.clock(),
	})
	return capable
}

// signerEd25519Credentials returns (keyId, PEM) for the local signer's
// Ed25519 keypair. 未配線時 / 鍵未発行 (= P5 backfill 前の旧 user) のいずれも
// 空文字列を返し、caller 側で RSA only にフォールバックする。signerCache で
// TTL 5min 結果を memoize し、N+1 query を抑える (#1080 review #3)。
func (s *DeliverService) signerEd25519Credentials(userID string) (string, string) {
	if s.keypairExtraRepo == nil {
		return "", ""
	}
	if cached, ok := s.signerCache.Load(userID); ok {
		entry := cached.(ed25519SignerEntry)
		if s.clock().Sub(entry.fetchedAt) <= ed25519CacheTTL {
			return entry.keyID, entry.privPEM
		}
	}
	kp, err := s.keypairExtraRepo.FindByUserID(userID)
	if err != nil {
		// 鍵未発行は cache に空 entry を入れて再 query を抑える
		s.signerCache.Store(userID, ed25519SignerEntry{fetchedAt: s.clock()})
		return "", ""
	}
	keyID := s.urls.UserURI(userID) + "#ed25519-key"
	s.signerCache.Store(userID, ed25519SignerEntry{
		keyID:     keyID,
		privPEM:   kp.Ed25519PrivateKey,
		fetchedAt: s.clock(),
	})
	return keyID, kp.Ed25519PrivateKey
}

// isBlockedInbox returns true when the inbox URL points at a host the local
// instance refuses to federate with — either because it is blocked or the
// federation mode (none / specified) excludes it (#536). blocker が未設定なら
// 常に false。
func (s *DeliverService) isBlockedInbox(inbox string) bool {
	if s.hostBlocker == nil {
		return false
	}
	u, err := url.Parse(inbox)
	if err != nil || u.Host == "" {
		return false
	}
	if s.hostBlocker.IsBlocked(u.Host) {
		return true
	}
	return !s.hostBlocker.IsAllowed(u.Host)
}

// DeliverToFollowers enqueues delivery to all remote followers of signerUserID.
// sharedInbox は repository 側で集約済み。
func (s *DeliverService) DeliverToFollowers(signerUserID string, body []byte) error {
	inboxes, err := s.followingRepo.ListRemoteFollowerInboxes(signerUserID)
	if err != nil {
		return err
	}
	return s.DeliverActivity(signerUserID, body, inboxes)
}

// DeliverToUser enqueues a delivery to a single recipient user. Local users
// are skipped (no AP delivery needed). リモートユーザーの sharedInbox があれば
// 優先する。recipient actor が既知なので FEP-521a Multikey による Ed25519
// sign を試行する経路 (DeliverActivityWithRecipient) を使う (#1067 / #1071)。
func (s *DeliverService) DeliverToUser(signerUserID string, recipient *model.User, body []byte) error {
	if recipient == nil || recipient.IsLocal() {
		return nil
	}
	inbox := preferredInbox(recipient)
	if inbox == "" {
		return nil
	}
	return s.DeliverActivityWithRecipient(signerUserID, body, []string{inbox}, recipient)
}

// signerCredentials returns the keyId URI and PEM private key for a local
// signer user.
func (s *DeliverService) signerCredentials(userID string) (string, string, error) {
	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		return "", "", err
	}
	if !user.IsLocal() {
		return "", "", ErrNoLocalSigner
	}
	kp, err := s.keypairRepo.FindByUserID(userID)
	if err != nil {
		return "", "", ErrSignerKeyMissing
	}
	keyID := s.urls.UserKeyURI(userID)
	return keyID, kp.PrivateKey, nil
}

// preferredInbox returns the sharedInbox of u when present, otherwise the
// individual inbox.
func preferredInbox(u *model.User) string {
	if u.SharedInbox != nil && *u.SharedInbox != "" {
		return *u.SharedInbox
	}
	if u.Inbox != nil {
		return *u.Inbox
	}
	return ""
}
