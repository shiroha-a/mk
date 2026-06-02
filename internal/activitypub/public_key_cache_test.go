package activitypub

import (
	"bytes"
	"crypto"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingParse wraps ParsePublicKey and records how many times the cache had
// to actually decode a PEM. メモ化が効いていれば同一 (keyId, PEM) では 1 回しか
// 増えない。
func countingParse(n *int) func(string) (crypto.PublicKey, KeyType, error) {
	return func(pemStr string) (crypto.PublicKey, KeyType, error) {
		*n++
		return ParsePublicKey(pemStr)
	}
}

func TestNewPublicKeyCache_Defaults(t *testing.T) {
	// size<=0 は default に丸めつつ lru / parse seam が配線される。
	for _, size := range []int{0, -5} {
		c := NewPublicKeyCache(size)
		require.NotNil(t, c)
		require.NotNil(t, c.lru, "size=%d should still allocate the LRU", size)
		require.NotNil(t, c.parse, "parse seam defaults to ParsePublicKey")
	}
	c := NewPublicKeyCache(8)
	require.NotNil(t, c.lru)
}

func TestPublicKeyCache_MemoizesRSAParse(t *testing.T) {
	_, pub := newTestKey(t)
	c := NewPublicKeyCache(8)
	var parses int
	c.parse = countingParse(&parses)

	p1, kt1, err := c.parsedKey("kid", pub)
	require.NoError(t, err)
	assert.Equal(t, KeyTypeRSA, kt1)

	p2, _, err := c.parsedKey("kid", pub)
	require.NoError(t, err)

	// 同一 (keyId, PEM) は 1 回だけパースされ、同じ *rsa.PublicKey ポインタが返る。
	assert.Equal(t, 1, parses, "same (keyId, PEM) must parse exactly once")
	assert.Same(t, p1, p2, "cache hit must return the identical parsed key")
}

func TestPublicKeyCache_ReparsesOnRotation(t *testing.T) {
	_, pub := newTestKey(t)
	c := NewPublicKeyCache(8)
	var parses int
	c.parse = countingParse(&parses)

	p1, _, err := c.parsedKey("kid", pub)
	require.NoError(t, err)

	// 同じ keyId でも PEM が変われば (= 鍵 rotation) cache key が変わり再パース。
	_, rotated, err := GenerateRSAKeypair()
	require.NoError(t, err)
	p2, _, err := c.parsedKey("kid", rotated)
	require.NoError(t, err)

	assert.Equal(t, 2, parses, "rotated PEM under same keyId must re-parse")
	assert.NotSame(t, p1, p2, "rotation must not return the stale parsed key")
}

func TestPublicKeyCache_MemoizesEd25519Parse(t *testing.T) {
	_, pub := newTestEd25519Key(t)
	c := NewPublicKeyCache(8)
	var parses int
	c.parse = countingParse(&parses)

	_, kt1, err := c.parsedKey("ed-kid", pub)
	require.NoError(t, err)
	assert.Equal(t, KeyTypeEd25519, kt1)

	_, _, err = c.parsedKey("ed-kid", pub)
	require.NoError(t, err)
	// ed25519.PublicKey は slice なので pointer 比較はできない。parse 回数で
	// メモ化を確認する。
	assert.Equal(t, 1, parses)
}

func TestPublicKeyCache_DistinctKeyIDsParseSeparately(t *testing.T) {
	_, pub := newTestKey(t)
	c := NewPublicKeyCache(8)
	var parses int
	c.parse = countingParse(&parses)

	_, _, err := c.parsedKey("kid-a", pub)
	require.NoError(t, err)
	// 同じ PEM でも keyId が異なれば別 entry (cache key は keyId+PEM の sha256)。
	_, _, err = c.parsedKey("kid-b", pub)
	require.NoError(t, err)
	assert.Equal(t, 2, parses)
}

func TestPublicKeyCache_ParseErrorNotCached(t *testing.T) {
	c := NewPublicKeyCache(8)
	var parses int
	c.parse = func(string) (crypto.PublicKey, KeyType, error) {
		parses++
		return nil, 0, errors.New("boom")
	}
	_, _, err := c.parsedKey("kid", "bad pem")
	require.Error(t, err)
	// 失敗はキャッシュされず、次回は再試行される。
	_, _, err = c.parsedKey("kid", "bad pem")
	require.Error(t, err)
	assert.Equal(t, 2, parses, "parse errors must not be memoized")
}

func TestPublicKeyCache_NilReceiverFallsBackToParse(t *testing.T) {
	_, pub := newTestKey(t)
	var c *PublicKeyCache
	// nil receiver でも都度パースで動く (防御経路)。
	_, kt, err := c.parsedKey("kid", pub)
	require.NoError(t, err)
	assert.Equal(t, KeyTypeRSA, kt)

	_, _, err = c.parsedKey("kid", "not pem")
	assert.Error(t, err)
}

func TestPublicKeyCache_NilLRUFallsBackToParse(t *testing.T) {
	_, pub := newTestKey(t)
	c := NewPublicKeyCache(8)
	c.lru = nil // lru.New 失敗を模した防御経路
	var parses int
	c.parse = countingParse(&parses)

	_, _, err := c.parsedKey("kid", pub)
	require.NoError(t, err)
	_, _, err = c.parsedKey("kid", pub)
	require.NoError(t, err)
	// lru が無いので memoize されず毎回パースされるが、エラーにはならない。
	assert.Equal(t, 2, parses)
}

func TestPublicKeyCache_ZeroValueDoesNotPanic(t *testing.T) {
	_, pub := newTestKey(t)
	// NewPublicKeyCache を経由しないゼロ値 (lru / parse とも nil) でも panic せず
	// 都度パースで動く (exported 型の誤用に対する防御)。
	c := &PublicKeyCache{}
	got, kt, err := c.parsedKey("kid", pub)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, KeyTypeRSA, kt)

	_, _, err = c.parsedKey("kid", "not pem")
	assert.Error(t, err)
}

func TestPublicKeyCache_VerifyRequestCached_Success(t *testing.T) {
	key, pub := newTestKey(t)
	body := []byte(`{"type":"Create"}`)
	req, err := http.NewRequest(http.MethodPost, "https://remote.example/inbox", bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, SignRequest(req, key, SHA256Digest(body), []string{"(request-target)", "date", "host", "digest"}))
	req.Header.Set("Host", "remote.example")

	c := NewPublicKeyCache(8)
	var parses int
	c.parse = countingParse(&parses)

	require.NoError(t, c.VerifyRequestCached(req, key.KeyID, pub))
	// 2 回目の verify (同一 keyId/PEM) でも x509 パースは増えない。
	require.NoError(t, c.VerifyRequestCached(req, key.KeyID, pub))
	assert.Equal(t, 1, parses, "VerifyRequestCached must reuse the memoized key")
}

func TestPublicKeyCache_VerifyRequestCached_ParseError(t *testing.T) {
	key, _ := newTestKey(t)
	req, _ := http.NewRequest(http.MethodGet, "https://remote.example/users/bob", nil)
	require.NoError(t, SignRequest(req, key, "", []string{"(request-target)", "date", "host"}))
	req.Header.Set("Host", "remote.example")

	c := NewPublicKeyCache(8)
	// PEM が壊れていれば parse error がそのまま返る。
	err := c.VerifyRequestCached(req, key.KeyID, "not a pem")
	assert.Error(t, err)
}

func TestPublicKeyCache_VerifyRequestCached_VerifyFails(t *testing.T) {
	key, _ := newTestKey(t)
	// 署名鍵とは別の鍵で verify すると失敗する。
	_, otherPub, err := GenerateRSAKeypair()
	require.NoError(t, err)
	req, _ := http.NewRequest(http.MethodGet, "https://remote.example/users/bob", nil)
	require.NoError(t, SignRequest(req, key, "", []string{"(request-target)", "date", "host"}))
	req.Header.Set("Host", "remote.example")

	c := NewPublicKeyCache(8)
	err = c.VerifyRequestCached(req, key.KeyID, otherPub)
	assert.Error(t, err)
}
