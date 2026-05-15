package activitypub

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/multiformats/go-multibase"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeEd25519Multikey_RoundTrip(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	mb, err := EncodeEd25519Multikey(pub)
	require.NoError(t, err)
	// FEP-521a / W3C VC Data Integrity の Ed25519 Multikey は必ず `z6Mk` で始まる
	// (`z` = base58btc, `6Mk` = `0xed01` multicodec prefix を 32 byte zero key と
	// concat した結果の prefix)。具体的な byte 列によらず prefix は同じ。
	assert.True(t, strings.HasPrefix(mb, "z6Mk"), "want z6Mk... prefix, got %s", mb)

	got, err := DecodeEd25519Multikey(mb)
	require.NoError(t, err)
	assert.Equal(t, pub, got)
}

// 既知のオールゼロ Ed25519 公開鍵 (32 byte の 0) の Multikey 表現を hardcode で
// 検証する。multicodec 0xed01 + 32 zero-byte で base58btc encode した結果は
// 下記の固定文字列になり、go-multibase / multicodec ライブラリの encode 出力が
// 変化したことを CI で早期検出するための regression guard。
func TestEncodeEd25519Multikey_KnownVector_ZeroKey(t *testing.T) {
	zero := make(ed25519.PublicKey, ed25519PublicKeySize) // all zero
	mb, err := EncodeEd25519Multikey(zero)
	require.NoError(t, err)
	assert.Equal(t, "z6MkeTG3bFFSLYVU7VqhgZxqr6YzpaGrQtFMh1uvqGy1vDnP", mb)

	// round-trip でも zero key が復元される
	back, err := DecodeEd25519Multikey(mb)
	require.NoError(t, err)
	assert.Equal(t, zero, back)
}

func TestEncodeEd25519Multikey_WrongSize(t *testing.T) {
	short := ed25519.PublicKey(make([]byte, 16))
	_, err := EncodeEd25519Multikey(short)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMultikey)
}

func TestDecodeEd25519Multikey_InvalidPrefix(t *testing.T) {
	// 全く異なる multibase prefix を使う (例: base16 prefix `f`)
	_, err := DecodeEd25519Multikey("fdeadbeef")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMultikey)
}

func TestDecodeEd25519Multikey_NonEd25519Multicodec(t *testing.T) {
	// secp256k1 public key multicodec prefix (0xe7 0x01) + 33 dummy bytes を
	// base58btc encode した正しい multibase 文字列を作成 → Ed25519 decoder で
	// "not an Ed25519 multicodec prefix" 経路で reject されることを確認。
	payload := append([]byte{0xe7, 0x01}, make([]byte, 33)...)
	mb, err := multibase.Encode(multibase.Base58BTC, payload)
	require.NoError(t, err)

	_, err = DecodeEd25519Multikey(mb)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMultikey)
}

func TestDecodeEd25519Multikey_WrongKeyLength(t *testing.T) {
	// Ed25519 multicodec prefix + 16 byte body (短すぎる) を base58btc encode し
	// → "key size 16 != 32" 経路で reject されることを確認。
	payload := append([]byte{0xed, 0x01}, make([]byte, 16)...)
	mb, err := multibase.Encode(multibase.Base58BTC, payload)
	require.NoError(t, err)

	_, err = DecodeEd25519Multikey(mb)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMultikey)
}

func TestDecodeEd25519Multikey_PayloadTooShort(t *testing.T) {
	// multicodec prefix の途中で切れた payload (1 byte のみ) → "payload too short"
	// 経路で reject されることを確認。
	payload := []byte{0xed}
	mb, err := multibase.Encode(multibase.Base58BTC, payload)
	require.NoError(t, err)

	_, err = DecodeEd25519Multikey(mb)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMultikey)
}

func TestDecodeEd25519Multikey_EmptyString(t *testing.T) {
	_, err := DecodeEd25519Multikey("")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMultikey)
}
