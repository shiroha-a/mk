package miauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrSessionNotFound is returned when the token is unknown or has expired.
var ErrSessionNotFound = errors.New("miauth: session not found")

const (
	// PendingTTL bounds how long an unfinished MiAuth flow stays resumable.
	// 相手のサーバーで承認する分の余裕があればよい。
	PendingTTL = 15 * time.Minute
	// VerifiedTTL bounds how long a verified contact stays usable without
	// re-authenticating.
	//
	// **状態確認のたびに MiAuth を踏ませないためのもの。** 長くすると、端末を
	// 離れた後に他人が続きを操作できる窓が広がる。
	VerifiedTTL = 30 * time.Minute

	pendingPrefix  = "miauth:pending:"
	verifiedPrefix = "miauth:verified:"
)

// PendingSession is a MiAuth flow that has been started but not yet checked.
type PendingSession struct {
	Host    string `json:"host"`
	Session string `json:"session"`
}

// SessionStore keeps the browser-bound state of in-flight MiAuth flows.
//
// **ブラウザに渡すのは自前のトークンで、MiAuth の session UUID ではない。**
// これが「フローを始めたブラウザだけが続きを実行できる」という束縛になる。
// upstream の check が単回であることはリプレイを防ぐだけで、受け取る主体は
// 縛らない — 攻撃者が開始したフローを被害者に承認させ、攻撃者のブラウザで
// コールバックを踏む筋が残る。
type SessionStore struct {
	redis *redis.Client
	clock func() time.Time
}

// NewSessionStore constructs a SessionStore.
func NewSessionStore(r *redis.Client) *SessionStore {
	return &SessionStore{redis: r, clock: time.Now}
}

// StartPending records a freshly created MiAuth flow and returns the token the
// browser must present to continue.
func (s *SessionStore) StartPending(ctx context.Context, host, session string) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(PendingSession{Host: host, Session: session})
	if err != nil {
		return "", fmt.Errorf("miauth: marshal pending: %w", err)
	}
	if err := s.redis.Set(ctx, pendingPrefix+token, payload, PendingTTL).Err(); err != nil {
		return "", fmt.Errorf("miauth: store pending: %w", err)
	}
	return token, nil
}

// TakePending returns the pending flow for the token and removes it.
//
// **取り出しと削除を分けない。** 残しておくと、同じトークンで check を何度も
// 叩かせることになる (相手側は単回なので 2 度目以降は必ず失敗するが、こちらから
// 無駄な外部リクエストを撃てる口になる)。
func (s *SessionStore) TakePending(ctx context.Context, token string) (*PendingSession, error) {
	if token == "" {
		return nil, ErrSessionNotFound
	}
	raw, err := s.redis.GetDel(ctx, pendingPrefix+token).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("miauth: load pending: %w", err)
	}
	var out PendingSession
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, ErrSessionNotFound
	}
	return &out, nil
}

// SaveVerified records a verified contact and returns the token that stands for
// it. 以降の申請・登録はこのトークンだけで進められる。
func (s *SessionStore) SaveVerified(ctx context.Context, c *Contact) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("miauth: marshal verified: %w", err)
	}
	if err := s.redis.Set(ctx, verifiedPrefix+token, payload, VerifiedTTL).Err(); err != nil {
		return "", fmt.Errorf("miauth: store verified: %w", err)
	}
	return token, nil
}

// Verified returns the contact behind a verified token, leaving it in place.
//
// 申請 → 状態確認 → 登録と複数回使うので、ここでは消さない。登録が完了したら
// DropVerified で明示的に落とす。
func (s *SessionStore) Verified(ctx context.Context, token string) (*Contact, error) {
	if token == "" {
		return nil, ErrSessionNotFound
	}
	raw, err := s.redis.Get(ctx, verifiedPrefix+token).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("miauth: load verified: %w", err)
	}
	var out Contact
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, ErrSessionNotFound
	}
	return &out, nil
}

// DropVerified invalidates a verified token.
func (s *SessionStore) DropVerified(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := s.redis.Del(ctx, verifiedPrefix+token).Err(); err != nil {
		return fmt.Errorf("miauth: drop verified: %w", err)
	}
	return nil
}

// newToken returns a 256-bit random token.
//
// **推測されると、他人の検証済みセッションを乗っ取れる。** UUID ではなく
// crypto/rand を使う。
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("miauth: generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// NewSessionID returns the identifier handed to the remote MiAuth endpoint.
//
// 相手のサーバーで一意であればよいが、**推測できると第三者が check を先に
// 叩けてしまう**ので、こちらも crypto/rand から作る。
func NewSessionID() (string, error) {
	return newToken()
}
