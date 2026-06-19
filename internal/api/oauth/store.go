package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// cacheTTL is how long an authorization transaction / grant code lives, matching
// upstream's MemoryKVCache 5-minute expiry.
const cacheTTL = 5 * time.Minute

// ErrNotFound is returned by the Store when a transaction / grant is missing or
// expired.
var ErrNotFound = errors.New("oauth: not found")

// transaction holds the validated /oauth/authorize parameters between the
// authorize step (store) and the decision step (take). Single-use.
type transaction struct {
	ClientID      string   `json:"clientId"`
	ClientName    string   `json:"clientName"`
	ClientLogo    string   `json:"clientLogo"`
	RedirectURI   string   `json:"redirectUri"`
	Scopes        []string `json:"scopes"`
	CodeChallenge string   `json:"codeChallenge"`
	State         string   `json:"state"`
}

// grant binds an issued authorization code to its consented parameters. Used by
// the token exchange to validate the code_verifier / client_id / redirect_uri
// and to mint the access token with the consented scopes.
type grant struct {
	ClientID      string   `json:"clientId"`
	UserID        string   `json:"userId"`
	RedirectURI   string   `json:"redirectUri"`
	CodeChallenge string   `json:"codeChallenge"`
	Scopes        []string `json:"scopes"`
}

// Store persists OAuth transactions and grant codes with TTL. Backed by Redis
// in production (so authorize/decision/token can hit different instances); a
// test fake implements the same single-use / atomic-claim semantics.
type Store interface {
	// PutTransaction stores a validated authorize transaction under id.
	PutTransaction(ctx context.Context, id string, t *transaction) error
	// TakeTransaction atomically loads and removes a transaction (single use).
	// Returns ErrNotFound if absent/expired.
	TakeTransaction(ctx context.Context, id string) (*transaction, error)
	// PutGrant stores an authorization code's bound parameters.
	PutGrant(ctx context.Context, code string, g *grant) error
	// GetGrant loads a grant without consuming it. ErrNotFound if absent.
	GetGrant(ctx context.Context, code string) (*grant, error)
	// ClaimGrant atomically marks the code as exchanged. firstUse is true only
	// for the caller that won the claim; a later call (replay) returns false.
	ClaimGrant(ctx context.Context, code string) (firstUse bool, err error)
	// PutGrantToken records the access-token id minted for a code so a later
	// replay can revoke it (upstream RFC6749 §4.1.2 behavior).
	PutGrantToken(ctx context.Context, code, tokenID string) error
	// GetGrantToken returns the recorded token id for a code ("" if none).
	GetGrantToken(ctx context.Context, code string) (string, error)
}

// RedisStore is the production Store backed by a Redis client.
type RedisStore struct {
	rdb *redis.Client
}

// NewRedisStore constructs a RedisStore.
func NewRedisStore(rdb *redis.Client) *RedisStore {
	return &RedisStore{rdb: rdb}
}

func txnKey(id string) string     { return "oauth:txn:" + id }
func grantKey(code string) string { return "oauth:grant:" + code }
func claimKey(code string) string { return "oauth:grant:" + code + ":claimed" }
func tokenKey(code string) string { return "oauth:grant:" + code + ":token" }

func (s *RedisStore) PutTransaction(ctx context.Context, id string, t *transaction) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, txnKey(id), b, cacheTTL).Err()
}

func (s *RedisStore) TakeTransaction(ctx context.Context, id string) (*transaction, error) {
	// GETDEL で atomic に load + 消費する (transaction は単回使用)。
	v, err := s.rdb.GetDel(ctx, txnKey(id)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var t transaction
	if err := json.Unmarshal([]byte(v), &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *RedisStore) PutGrant(ctx context.Context, code string, g *grant) error {
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, grantKey(code), b, cacheTTL).Err()
}

func (s *RedisStore) GetGrant(ctx context.Context, code string) (*grant, error) {
	v, err := s.rdb.Get(ctx, grantKey(code)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var g grant
	if err := json.Unmarshal([]byte(v), &g); err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *RedisStore) ClaimGrant(ctx context.Context, code string) (bool, error) {
	// SETNX で「最初に exchange を試みた呼び出し」だけが claim を獲得する。
	// 既に claim 済み (= replay) なら false。validation 成否に関わらず claim は
	// 残るため、誤った code_verifier での exchange でも code は単回で焼き切れる
	// (upstream が validation 前に used=true にするのと同じ)。
	err := s.rdb.SetArgs(ctx, claimKey(code), "1", redis.SetArgs{Mode: "NX", TTL: cacheTTL}).Err()
	if errors.Is(err, redis.Nil) {
		// key が既に存在 (= 既に claim 済み) → replay。
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *RedisStore) PutGrantToken(ctx context.Context, code, tokenID string) error {
	return s.rdb.Set(ctx, tokenKey(code), tokenID, cacheTTL).Err()
}

func (s *RedisStore) GetGrantToken(ctx context.Context, code string) (string, error) {
	v, err := s.rdb.Get(ctx, tokenKey(code)).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return v, err
}
