package ld

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"time"
)

// ldNonceRand is the randomness source for the signature nonce. テスト時に
// 差し替え可能にするためパッケージ変数として公開。
var ldNonceRand io.Reader = rand.Reader

// SignRsaSignature2017 attaches an RsaSignature2017 LD-Signature to activity,
// authored by the holder of privateKeyPEM. creator is the signing key URI
// (e.g. "https://example.com/users/<id>#main-key").
//
// Mirrors upstream Misskey TS `JsonLdService.signRsaSignature2017` byte-for-byte
// (= options{type,creator,nonce,created} → createVerifyData → sha256 →
// RSA-PKCS1v15 SHA-256 sign → signatureValue base64)。出力は
// VerifyRsaSignature2017 および remote Misskey/Mastodon の LD-Signature verifier で
// 検証可能。
//
// activity は変更せず、`signature` field を加えたコピーを返す。
func (p *Processor) SignRsaSignature2017(activity map[string]any, privateKeyPEM, creator string, created time.Time) (map[string]any, error) {
	nonceBytes := make([]byte, 16)
	if _, err := io.ReadFull(ldNonceRand, nonceBytes); err != nil {
		return nil, fmt.Errorf("ld-sig: nonce generation failed: %w", err)
	}
	// createVerifyData は options から type/id/signatureValue を除き、nonce/created/
	// creator は残して normalize する。verify 側も同じ処理をするので、これらを
	// 後段で attach する signature object に同値で含めれば検証が通る。
	options := map[string]any{
		"type":    LDSignatureType,
		"creator": creator,
		"nonce":   hex.EncodeToString(nonceBytes),
		"created": created.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}

	verifyData, err := p.createVerifyData(activity, options)
	if err != nil {
		return nil, err
	}
	priv, err := parseRSAPrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	finalHash := sha256.Sum256([]byte(verifyData))
	sigBytes, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, finalHash[:])
	if err != nil {
		return nil, fmt.Errorf("ld-sig: sign: %w", err)
	}

	signature := make(map[string]any, len(options)+1)
	for k, v := range options {
		signature[k] = v
	}
	signature["signatureValue"] = base64.StdEncoding.EncodeToString(sigBytes)

	out := make(map[string]any, len(activity)+1)
	for k, v := range activity {
		out[k] = v
	}
	out["signature"] = signature
	return out, nil
}

// parseRSAPrivateKey decodes a PEM-encoded RSA private key, accepting both
// PKCS1 ("BEGIN RSA PRIVATE KEY") and PKCS8 ("BEGIN PRIVATE KEY") encodings.
// Kept local to the ld package to avoid an import cycle with the activitypub
// package (which depends on ld for inbox verify).
func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode([]byte(pemStr))
	if blk == nil {
		return nil, fmt.Errorf("%w: pem decode failed", ErrInvalidSignaturePEM)
	}
	if key, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: private key parse: %v", ErrInvalidSignaturePEM, err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%w: not an RSA private key", ErrInvalidSignaturePEM)
	}
	return rsaKey, nil
}
