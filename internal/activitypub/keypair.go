package activitypub

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"time"
)

// randReader is the entropy source used by GenerateRSAKeypair. テストで差し替え
// 可能なように package-level の var にしている。
var randReader io.Reader = rand.Reader

// nowFunc returns the current time. テストで差し替え可能。
var nowFunc = time.Now

// KeyType discriminates between supported HTTP Signature key algorithms.
// RSAはMastodon/Misskeyの事実上の標準、Ed25519は新世代Fediverse実装で
// 採用されつつある軽量・高速な署名方式。
type KeyType int

const (
	// KeyTypeRSA is used for RSA-SHA256 / RSA-SHA512 / hs2019(RSA) signatures.
	KeyTypeRSA KeyType = iota
	// KeyTypeEd25519 is used for ed25519 / hs2019(Ed25519) signatures.
	KeyTypeEd25519
)

// GenerateRSAKeypair returns a fresh 2048-bit RSA keypair encoded as PEM
// strings (private + public). 失敗時はエラーを返す。MarshalPKIXPublicKey は
// RSAキーに対しては常に成功するため、その後のエラーチェックは省略している。
func GenerateRSAKeypair() (privatePEM string, publicPEM string, err error) {
	priv, err := rsa.GenerateKey(randReader, 2048)
	if err != nil {
		return "", "", err
	}
	privBytes := x509.MarshalPKCS1PrivateKey(priv)
	privBlock := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: privBytes}

	pubBytes, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	pubBlock := &pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes}

	return string(pem.EncodeToMemory(privBlock)), string(pem.EncodeToMemory(pubBlock)), nil
}

// GenerateEd25519Keypair returns a fresh Ed25519 keypair encoded as PEM
// strings (private + public). 鍵生成はioエラーの時だけ失敗するため、
// MarshalPKCS8PrivateKey / MarshalPKIXPublicKey側のエラーチェックは省略。
func GenerateEd25519Keypair() (privatePEM string, publicPEM string, err error) {
	pub, priv, err := ed25519.GenerateKey(randReader)
	if err != nil {
		return "", "", err
	}
	privBytes, _ := x509.MarshalPKCS8PrivateKey(priv)
	privBlock := &pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}

	pubBytes, _ := x509.MarshalPKIXPublicKey(pub)
	pubBlock := &pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes}

	return string(pem.EncodeToMemory(privBlock)), string(pem.EncodeToMemory(pubBlock)), nil
}

// ParseRSAPrivateKey decodes a PEM-encoded RSA private key.
// PKCS1 ("BEGIN RSA PRIVATE KEY") と PKCS8 ("BEGIN PRIVATE KEY") の両形式を
// 受け付ける。Misskey TS は PKCS8 で RSA 鍵を保存するため、drop-in 切替で
// mk-go が TS 生成の鍵を読む場合に PKCS8 fallback が必要 (#369 関連)。
func ParseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("invalid PEM block")
	}
	// 1. まず PKCS1 (mk-go が自分で生成した鍵はこの形式) を試す。
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	// 2. PKCS1 失敗時は PKCS8 を試す。TS Misskey は openssl 3.x デフォルトで
	//    `-----BEGIN PRIVATE KEY-----` の PKCS8 形式を生成する。
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse RSA private key (tried PKCS1 and PKCS8): %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("PKCS8 key is not RSA: %T", parsed)
	}
	return rsaKey, nil
}

// ParseEd25519PrivateKey decodes a PEM-encoded Ed25519 private key (PKCS8).
func ParseEd25519PrivateKey(pemStr string) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("invalid PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("not an Ed25519 private key")
	}
	return priv, nil
}

// ParseRSAPublicKey decodes a PEM-encoded RSA public key.
// 既存呼び出し側の互換のため残す。新規コードはParsePublicKeyを使うこと。
func ParseRSAPublicKey(pemStr string) (*rsa.PublicKey, error) {
	pub, kt, err := ParsePublicKey(pemStr)
	if err != nil {
		return nil, err
	}
	if kt != KeyTypeRSA {
		return nil, errors.New("not an RSA public key")
	}
	return pub.(*rsa.PublicKey), nil
}

// MarshalEd25519PublicKeyPEM encodes an Ed25519 public key into the PKIX
// SubjectPublicKeyInfo PEM form so it can be stored in user_publickey_extra
// alongside RSA keys (= 同一テーブルで ParsePublicKey が type-dispatch 可能)。
// 入力 key の長さは ed25519.PublicKeySize (= 32 byte) でなければならない。
func MarshalEd25519PublicKeyPEM(pub ed25519.PublicKey) (string, error) {
	if len(pub) != ed25519.PublicKeySize {
		return "", fmt.Errorf("invalid ed25519 public key size %d", len(pub))
	}
	pubBytes, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	block := &pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes}
	return string(pem.EncodeToMemory(block)), nil
}

// ParseEd25519PublicKeyPEM decodes a PEM-encoded Ed25519 public key
// (PKIX SubjectPublicKeyInfo). Errors are returned when the PEM is missing,
// malformed, or contains a non-Ed25519 key (= caller can fall back to RSA).
func ParseEd25519PublicKeyPEM(pemStr string) (ed25519.PublicKey, error) {
	pub, kt, err := ParsePublicKey(pemStr)
	if err != nil {
		return nil, err
	}
	if kt != KeyTypeEd25519 {
		return nil, errors.New("not an Ed25519 public key")
	}
	return pub.(ed25519.PublicKey), nil
}

// ParsePublicKey decodes a PEM-encoded public key and reports its type.
// RSA / Ed25519を識別できる。それ以外は "unsupported public key type" で
// 拒否する (Misskeyフェデレーションでは ECDSA等は事実上使われていない)。
func ParsePublicKey(pemStr string) (crypto.PublicKey, KeyType, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, 0, errors.New("invalid PEM block")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, 0, err
	}
	switch k := pub.(type) {
	case *rsa.PublicKey:
		return k, KeyTypeRSA, nil
	case ed25519.PublicKey:
		return k, KeyTypeEd25519, nil
	default:
		return nil, 0, fmt.Errorf("unsupported public key type: %T", pub)
	}
}
