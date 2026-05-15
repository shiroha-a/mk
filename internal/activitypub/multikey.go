package activitypub

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/multiformats/go-multibase"
)

// FEP-521a / W3C VC Data Integrity (Multikey) で Ed25519 public key を
// publicKeyMultibase 値として表現するための encode/decode ヘルパ。
//
// publicKeyMultibase の形式:
//
//	"z" + base58btc(multicodec_varint_prefix || raw_ed25519_public_key)
//
// - multibase prefix `z` = base58btc
// - multicodec varint prefix for Ed25519 public key = 0xed 0x01 (2 bytes)
// - raw Ed25519 public key = 32 bytes
// → 結果は必ず `z6Mk...` 形式の 48 文字程度の文字列になる。
//
// 参考: https://w3c-ccg.github.io/security-vocab/#Multikey
//
// Fedibird / Mastodon glitch-soc など FEP-521a 対応サーバーが actor JSON の
// `assertionMethod[]` で Ed25519 公開鍵を expose する際に使う標準形式。
// mk-go は同形式で expose し、リモートから受信したものを parse する。

// ed25519MulticodecPrefix is the varint-encoded multicodec for an Ed25519
// public key (table value 0xed). Multibase / Multikey 仕様に基づく 2 byte の
// 固定プレフィックスで、varint encode 結果は `0xed 0x01` になる。
var ed25519MulticodecPrefix = []byte{0xed, 0x01}

// ed25519PublicKeySize is the canonical size of an Ed25519 public key in bytes.
const ed25519PublicKeySize = 32

// ErrInvalidMultikey is returned when a publicKeyMultibase string is malformed
// or does not represent an Ed25519 public key (wrong multibase, wrong
// multicodec, wrong key length, etc).
var ErrInvalidMultikey = errors.New("invalid Ed25519 multikey")

// EncodeEd25519Multikey returns the publicKeyMultibase representation of pub
// (e.g. "z6Mk..."). pub の長さが 32 bytes でない場合 error を返す。
func EncodeEd25519Multikey(pub ed25519.PublicKey) (string, error) {
	if len(pub) != ed25519PublicKeySize {
		return "", fmt.Errorf("%w: public key size %d != %d", ErrInvalidMultikey, len(pub), ed25519PublicKeySize)
	}
	payload := make([]byte, 0, len(ed25519MulticodecPrefix)+ed25519PublicKeySize)
	payload = append(payload, ed25519MulticodecPrefix...)
	payload = append(payload, pub...)
	return multibase.Encode(multibase.Base58BTC, payload)
}

// DecodeEd25519Multikey parses a publicKeyMultibase string and returns the
// embedded Ed25519 public key. multibase prefix が `z` (base58btc) でない、
// multicodec prefix が Ed25519 ではない、key 長が 32 byte でない、いずれの
// 場合も ErrInvalidMultikey をラップした error を返す。
func DecodeEd25519Multikey(s string) (ed25519.PublicKey, error) {
	enc, raw, err := multibase.Decode(s)
	if err != nil {
		return nil, fmt.Errorf("%w: multibase decode: %v", ErrInvalidMultikey, err)
	}
	if enc != multibase.Base58BTC {
		return nil, fmt.Errorf("%w: unexpected multibase encoding %v (want base58btc)", ErrInvalidMultikey, enc)
	}
	if len(raw) < len(ed25519MulticodecPrefix) {
		return nil, fmt.Errorf("%w: payload too short", ErrInvalidMultikey)
	}
	for i, b := range ed25519MulticodecPrefix {
		if raw[i] != b {
			return nil, fmt.Errorf("%w: not an Ed25519 multicodec prefix", ErrInvalidMultikey)
		}
	}
	body := raw[len(ed25519MulticodecPrefix):]
	if len(body) != ed25519PublicKeySize {
		return nil, fmt.Errorf("%w: key size %d != %d", ErrInvalidMultikey, len(body), ed25519PublicKeySize)
	}
	// ed25519.PublicKey は []byte の type alias なので copy で隔離する
	// (multibase.Decode の返す raw slice は内部 buffer の view の可能性)。
	pub := make(ed25519.PublicKey, ed25519PublicKeySize)
	copy(pub, body)
	return pub, nil
}
