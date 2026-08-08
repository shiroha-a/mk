package activitypub

import (
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	lru "github.com/hashicorp/golang-lru/v2"
)

// DefaultPublicKeyCacheSize bounds the parsed-public-key LRU used by the
// inbound HTTP Signature verify path. 受信側 actor key は実質「自分が交流
// している remote actor 数」に比例し、relay 経由でも数千 order なので 4096 で
// 大半の instance の working set を賄える。超過分は LRU が evict する。
const DefaultPublicKeyCacheSize = 4096

// parsedPublicKey bundles a decoded public key with its discriminated type so
// the verify path can dispatch RSA / Ed25519 without re-parsing.
type parsedPublicKey struct {
	pub crypto.PublicKey
	kt  KeyType
}

// PublicKeyCache memoizes the PEM -> parsed-key (pub, KeyType) decode so the
// inbound HTTP Signature verify hot path skips pem.Decode +
// x509.ParsePKIXPublicKey on every activity (#1426). It is the inbound
// analogue of DeliverProcessor.signingKey on the outbound side (#1425).
//
// cache key は (keyId, PEM) の sha256 hex。PEM が変わる (= 鍵 rotation) と cache
// key も変わるため stale な鍵を返さない。これにより resolver が常に「永続層の
// 最新 PEM」を返す既存セマンティクス (Resolver.PublicKeyForKeyID の doc) を壊さ
// ずに、同一 PEM に対する x509 パースだけを 1 回に集約できる。
//
// 並行安全: lru.Cache は内部 mutex を持つため複数 inbox worker から同時に呼んで
// よい。parse seam (parse field) はテストでパース回数を観測するためのもので、
// 本番では ParsePublicKey 固定。
type PublicKeyCache struct {
	lru *lru.Cache[string, parsedPublicKey]
	// parse はパースの seam。本番は ParsePublicKey、テストではパース回数を数える
	// closure を差し込む。NewPublicKeyCache が ParsePublicKey で初期化する。
	parse func(pemStr string) (crypto.PublicKey, KeyType, error)
}

// NewPublicKeyCache constructs a PublicKeyCache with the given LRU size.
// size <= 0 は DefaultPublicKeyCacheSize に丸める。lru.New は size>0 で error を
// 返さない実装だが、防御的に握って lru==nil (= 都度パース) にフォールバックする。
func NewPublicKeyCache(size int) *PublicKeyCache {
	if size <= 0 {
		size = DefaultPublicKeyCacheSize
	}
	c := &PublicKeyCache{parse: ParsePublicKey}
	if l, err := lru.New[string, parsedPublicKey](size); err == nil {
		c.lru = l
	}
	return c
}

// publicKeyCacheKey folds (keyID, PEM) into a sha256 hex string. map key を
// ~1.5KB の PEM から 64B に縮め、PEM 本体が key 経由でログ等に漏れるリスクも
// 下げる (deliver.go と同じ方式)。null-byte separator で keyID/PEM 境界の曖昧
// さを避ける。
func publicKeyCacheKey(keyID, pemStr string) string {
	sum := sha256.Sum256([]byte(keyID + "\x00" + pemStr))
	return hex.EncodeToString(sum[:])
}

// parsedKey returns the parsed (pub, kt) for (keyID, pem), memoizing the x509
// parse. nil receiver / lru 未設定 (= New 失敗の防御経路) では都度パースに
// フォールバックする。パース失敗はキャッシュしない (次回再試行できる)。
func (c *PublicKeyCache) parsedKey(keyID, pemStr string) (crypto.PublicKey, KeyType, error) {
	if c == nil {
		return ParsePublicKey(pemStr)
	}
	// parse seam が未設定 (= NewPublicKeyCache を経由せずゼロ値の PublicKeyCache を
	// 直接生成した場合) でも panic させず ParsePublicKey にフォールバックする。
	// PublicKeyCache は exported 型なので、他パッケージが `&PublicKeyCache{}` を
	// 作って呼ぶ誤用に備える (本番経路は必ず NewPublicKeyCache で parse 配線済み)。
	parse := c.parse
	if parse == nil {
		parse = ParsePublicKey
	}
	if c.lru == nil {
		return parse(pemStr)
	}
	ck := publicKeyCacheKey(keyID, pemStr)
	if v, ok := c.lru.Get(ck); ok {
		return v.pub, v.kt, nil
	}
	pub, kt, err := parse(pemStr)
	if err != nil {
		return nil, 0, err
	}
	c.lru.Add(ck, parsedPublicKey{pub: pub, kt: kt})
	return pub, kt, nil
}

// VerifyRequestCached verifies req's HTTP Signature using a memoized parse of
// (keyID, pem). 挙動は activitypub.VerifyRequest と等価で、(keyId, PEM) 単位で
// x509 パースを 1 回に集約する inbound hot path 用 drop-in (#1426)。pem は呼び
// 出し側 (resolver) が返す「永続層の最新値」前提で、stale PEM は cache key 不一致
// で自動的に再パースされる。
//
// VerifyRequest と同じく signature header parse + algorithm guard を公開鍵パース
// (ここでは cache 参照) より前に行うため、未対応 algorithm のリクエストでは鍵を
// パース / cache 参照せず短絡し、エラー優先順位も一致する (#1426 review)。
//
// 戻り値の KeyType は検証に成功した鍵の種別で、err == nil のときのみ意味を持つ
// (#2393)。err != nil の戻り値は KeyTypeRSA と同じゼロ値なので、鍵種別を読む側は
// 必ず先に err を判定すること。cache が既に種別を保持しているため、この値を返す
// ために追加のパースは発生しない。
func (c *PublicKeyCache) VerifyRequestCached(req *http.Request, keyID, pemStr string) (KeyType, error) {
	parsed, err := parseSignatureForVerify(req)
	if err != nil {
		return 0, err
	}
	pub, kt, err := c.parsedKey(keyID, pemStr)
	if err != nil {
		return 0, err
	}
	if err := verifyParsed(req, parsed, pub, kt); err != nil {
		return 0, err
	}
	return kt, nil
}
