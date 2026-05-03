package twofactor

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"github.com/shiroha-a/mk/internal/model"
)

// Errors returned by WebAuthnService.
var (
	// ErrWebAuthnSessionNotFound is returned when the begin/finish session
	// cannot be located in Redis (expired, never created, or wrong id).
	ErrWebAuthnSessionNotFound = errors.New("twofactor: webauthn session not found")
	// ErrWebAuthnNotConfigured is returned when WebAuthnService methods are
	// called on a zero-value or nil-Redis instance.
	ErrWebAuthnNotConfigured = errors.New("twofactor: webauthn service not configured")
)

// webAuthnSessionTTL bounds how long a registration / authentication challenge
// stays valid. 5 minutes matches the typical user interaction window for
// inserting a security key and tapping it.
const webAuthnSessionTTL = 5 * time.Minute

// WebAuthnService wraps a *webauthn.WebAuthn instance with the bits the rest
// of the codebase needs: Redis-backed challenge sessions and a model adapter
// that converts between go-webauthn and our DB shapes.
type WebAuthnService struct {
	wa    *webauthn.WebAuthn
	redis redis.Cmdable
}

// NewWebAuthnService constructs a WebAuthnService from the public URL of the
// instance and an optional Redis client. The instance URL is parsed to derive
// `RPID` (host) and `RPOrigins` (scheme + host) — these are required by the
// WebAuthn spec to bind credentials to the relying party domain.
//
// Returns ErrWebAuthnNotConfigured if instanceURL cannot be parsed.
func NewWebAuthnService(instanceURL, displayName string, redisClient redis.Cmdable) (*WebAuthnService, error) {
	if instanceURL == "" {
		return nil, ErrWebAuthnNotConfigured
	}
	u, err := url.Parse(instanceURL)
	if err != nil || u.Host == "" {
		return nil, ErrWebAuthnNotConfigured
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          u.Hostname(),
		RPDisplayName: displayName,
		RPOrigins:     []string{strings.TrimRight(instanceURL, "/")},
	})
	if err != nil {
		return nil, fmt.Errorf("webauthn config: %w", err)
	}
	return &WebAuthnService{wa: wa, redis: redisClient}, nil
}

// --- webauthn.User adapter ------------------------------------------------

// userAdapter implements webauthn.User for one of our model.User rows + their
// existing security keys. The same adapter is reused on register (where the
// existing keys list helps the authenticator avoid duplicate enrollments) and
// on login (where it provides the allowedCredentials list).
type userAdapter struct {
	user *model.User
	keys []*model.UserSecurityKey
}

// WebAuthnID returns the user handle as raw bytes. We use the ULID-style
// User.ID directly since it is already a stable opaque value <= 64 bytes.
func (u *userAdapter) WebAuthnID() []byte { return []byte(u.user.ID) }

// WebAuthnName returns the username — used by the authenticator UI to
// disambiguate accounts.
func (u *userAdapter) WebAuthnName() string { return u.user.Username }

// WebAuthnDisplayName returns the display name (falls back to username when
// the user has not set one).
func (u *userAdapter) WebAuthnDisplayName() string {
	if u.user.Name != nil && *u.user.Name != "" {
		return *u.user.Name
	}
	return u.user.Username
}

// WebAuthnCredentials converts our DB rows into the go-webauthn shape so that
// BeginRegistration / BeginLogin can build the excludeCredentials /
// allowedCredentials lists, and FinishLogin can find the right key to verify.
func (u *userAdapter) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(u.keys))
	for _, k := range u.keys {
		credID, err := base64.RawURLEncoding.DecodeString(k.ID)
		if err != nil {
			continue
		}
		pubKey, err := base64.RawURLEncoding.DecodeString(k.PublicKey)
		if err != nil {
			continue
		}
		cred := webauthn.Credential{
			ID:        credID,
			PublicKey: pubKey,
			Authenticator: webauthn.Authenticator{
				SignCount: uint32(k.Counter),
			},
		}
		for _, t := range k.Transports {
			cred.Transport = append(cred.Transport, protocol.AuthenticatorTransport(t))
		}
		out = append(out, cred)
	}
	return out
}

// --- session storage ------------------------------------------------------

// sessionKey returns the Redis key used to stash a SessionData blob between
// begin and finish. The user id binds the session to one account so a stolen
// sessionID alone cannot be replayed against a different victim.
func sessionKey(userID, sessionID string) string {
	return "twofa:webauthn:" + userID + ":" + sessionID
}

// primarySessionKey is the Redis key for the upstream-compatible
// single-in-flight-per-user mode used by /api/i/2fa/{register-key,key-done}.
// Misskey TS の WebAuthnService と同じく client は session id を round-trip
// しないため user 単位で 1 件の challenge だけ保持する。新規 challenge は
// 既存を上書きする (#698)。login flow など session id round-trip が必要な
// 経路は引き続き sessionKey() の方を使う。
func primarySessionKey(userID string) string {
	return "twofa:webauthn:" + userID + ":primary"
}

// putSession serializes the SessionData and stores it under a freshly generated
// random session id with TTL.
func (s *WebAuthnService) putSession(ctx context.Context, userID string, sd *webauthn.SessionData) (string, error) {
	if s == nil || s.redis == nil {
		return "", ErrWebAuthnNotConfigured
	}
	raw, err := json.Marshal(sd)
	if err != nil {
		return "", err
	}
	id, err := newSessionID()
	if err != nil {
		return "", err
	}
	if err := s.redis.Set(ctx, sessionKey(userID, id), raw, webAuthnSessionTTL).Err(); err != nil {
		return "", err
	}
	return id, nil
}

// takeSession loads-and-deletes a SessionData blob (single-use).
func (s *WebAuthnService) takeSession(ctx context.Context, userID, sessionID string) (*webauthn.SessionData, error) {
	if s == nil || s.redis == nil {
		return nil, ErrWebAuthnNotConfigured
	}
	key := sessionKey(userID, sessionID)
	raw, err := s.redis.GetDel(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrWebAuthnSessionNotFound
		}
		return nil, err
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal(raw, &sd); err != nil {
		return nil, err
	}
	return &sd, nil
}

// newSessionID generates a 24-byte URL-safe random id (192 bits of entropy).
func newSessionID() (string, error) {
	const n = 24
	buf := make([]byte, n)
	if _, err := readRandom(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// readRandom is a thin wrapper around crypto/rand.Read. Defined as a var so
// tests can stub it for deterministic id generation.
var readRandom = cryptorand.Read

// --- public API -----------------------------------------------------------

// BeginRegistration starts a new credential registration. Returns the
// CredentialCreation options (to be JSON-marshalled and shipped to the
// browser) and a sessionID that the caller must round-trip back on the
// finish step.
func (s *WebAuthnService) BeginRegistration(ctx context.Context, user *model.User, existing []*model.UserSecurityKey) (*protocol.CredentialCreation, string, error) {
	if s == nil || s.wa == nil {
		return nil, "", ErrWebAuthnNotConfigured
	}
	adapter := &userAdapter{user: user, keys: existing}
	creation, sd, err := s.wa.BeginRegistration(adapter)
	if err != nil {
		return nil, "", err
	}
	sid, err := s.putSession(ctx, user.ID, sd)
	if err != nil {
		return nil, "", err
	}
	return creation, sid, nil
}

// BeginRegistrationPrimary is the upstream-compatible variant of
// BeginRegistration that stores the SessionData under a fixed per-user key
// (no session id round-trip). Used by /api/i/2fa/register-key (#698).
// Returns only the CredentialCreation options; no opaque sessionID for the
// caller to track.
//
// authenticatorSelection は Misskey TS upstream (`@simplewebauthn/server`) と
// 同じく residentKey=required / userVerification=preferred を要求する。これが
// 無いと一部の authenticator + browser 組合せ (特に passkey 経由の platform
// authenticator) でダイアログは表示されるが credential が返らないケースがある。
func (s *WebAuthnService) BeginRegistrationPrimary(ctx context.Context, user *model.User, existing []*model.UserSecurityKey) (*protocol.CredentialCreation, error) {
	if s == nil || s.wa == nil {
		return nil, ErrWebAuthnNotConfigured
	}
	adapter := &userAdapter{user: user, keys: existing}
	creation, sd, err := s.wa.BeginRegistration(adapter, webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
		ResidentKey:      protocol.ResidentKeyRequirementRequired,
		UserVerification: protocol.VerificationPreferred,
	}))
	if err != nil {
		return nil, err
	}
	if err := s.putPrimarySession(ctx, user.ID, sd); err != nil {
		return nil, err
	}
	return creation, nil
}

// FinishRegistrationPrimary completes the upstream-compatible registration
// flow started by BeginRegistrationPrimary. The SessionData is loaded by
// user id alone (no client-supplied sessionID), matching Misskey TS
// WebAuthnService (#698).
func (s *WebAuthnService) FinishRegistrationPrimary(ctx context.Context, user *model.User, existing []*model.UserSecurityKey, req *http.Request) (*webauthn.Credential, error) {
	if s == nil || s.wa == nil {
		return nil, ErrWebAuthnNotConfigured
	}
	sd, err := s.takePrimarySession(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	adapter := &userAdapter{user: user, keys: existing}
	cred, err := s.wa.FinishRegistration(adapter, *sd, req)
	if err != nil {
		return nil, err
	}
	return cred, nil
}

// putPrimarySession overwrites any existing primary challenge so a fresh
// register-key call after an abandoned attempt always works.
func (s *WebAuthnService) putPrimarySession(ctx context.Context, userID string, sd *webauthn.SessionData) error {
	if s == nil || s.redis == nil {
		return ErrWebAuthnNotConfigured
	}
	raw, err := json.Marshal(sd)
	if err != nil {
		return err
	}
	return s.redis.Set(ctx, primarySessionKey(userID), raw, webAuthnSessionTTL).Err()
}

// takePrimarySession loads-and-deletes the primary session blob (single-use).
func (s *WebAuthnService) takePrimarySession(ctx context.Context, userID string) (*webauthn.SessionData, error) {
	if s == nil || s.redis == nil {
		return nil, ErrWebAuthnNotConfigured
	}
	raw, err := s.redis.GetDel(ctx, primarySessionKey(userID)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrWebAuthnSessionNotFound
		}
		return nil, err
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal(raw, &sd); err != nil {
		return nil, err
	}
	return &sd, nil
}

// FinishRegistration verifies the registration response from the browser and
// returns a Credential ready to be persisted as a UserSecurityKey row.
func (s *WebAuthnService) FinishRegistration(ctx context.Context, user *model.User, existing []*model.UserSecurityKey, sessionID string, req *http.Request) (*webauthn.Credential, error) {
	if s == nil || s.wa == nil {
		return nil, ErrWebAuthnNotConfigured
	}
	sd, err := s.takeSession(ctx, user.ID, sessionID)
	if err != nil {
		return nil, err
	}
	adapter := &userAdapter{user: user, keys: existing}
	cred, err := s.wa.FinishRegistration(adapter, *sd, req)
	if err != nil {
		return nil, err
	}
	return cred, nil
}

// BeginLogin starts an authentication assertion challenge for the given user.
// Used during the 2FA step of /api/signin-flow when a security key is the
// preferred second factor.
func (s *WebAuthnService) BeginLogin(ctx context.Context, user *model.User, existing []*model.UserSecurityKey) (*protocol.CredentialAssertion, string, error) {
	if s == nil || s.wa == nil {
		return nil, "", ErrWebAuthnNotConfigured
	}
	adapter := &userAdapter{user: user, keys: existing}
	assertion, sd, err := s.wa.BeginLogin(adapter)
	if err != nil {
		return nil, "", err
	}
	sid, err := s.putSession(ctx, user.ID, sd)
	if err != nil {
		return nil, "", err
	}
	return assertion, sid, nil
}

// FinishLogin verifies the assertion response. The returned Credential carries
// an updated counter; callers should persist it via UserSecurityKeyRepository
// to detect cloned authenticators on subsequent logins.
func (s *WebAuthnService) FinishLogin(ctx context.Context, user *model.User, existing []*model.UserSecurityKey, sessionID string, req *http.Request) (*webauthn.Credential, error) {
	if s == nil || s.wa == nil {
		return nil, ErrWebAuthnNotConfigured
	}
	sd, err := s.takeSession(ctx, user.ID, sessionID)
	if err != nil {
		return nil, err
	}
	adapter := &userAdapter{user: user, keys: existing}
	cred, err := s.wa.FinishLogin(adapter, *sd, req)
	if err != nil {
		return nil, err
	}
	return cred, nil
}

// --- persistence helpers --------------------------------------------------

// CredentialToModel converts a freshly-registered webauthn Credential into
// the model.UserSecurityKey shape persisted by the repository. The caller
// supplies userID and the user-chosen `name`.
func CredentialToModel(cred *webauthn.Credential, userID, name string) *model.UserSecurityKey {
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	out := &model.UserSecurityKey{
		ID:        base64.RawURLEncoding.EncodeToString(cred.ID),
		UserID:    userID,
		Name:      name,
		PublicKey: base64.RawURLEncoding.EncodeToString(cred.PublicKey),
		Counter:   int64(cred.Authenticator.SignCount),
		LastUsed:  time.Now(),
	}
	if len(transports) > 0 {
		out.Transports = pq.StringArray(transports)
	}
	return out
}
