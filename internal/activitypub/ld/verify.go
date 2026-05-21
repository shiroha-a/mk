package ld

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
)

// LDSignatureType is the only LD-Signature algorithm Misskey TS / mk-go
// accept on the inbox path (= legacy 2017 RFC、URDNA2015 canonicalize +
// RSA-PKCS1v15 SHA-256)。
const LDSignatureType = "RsaSignature2017"

// Errors returned by VerifyRsaSignature2017.
var (
	ErrNoSignatureField    = errors.New("ld-sig: activity has no signature field")
	ErrInvalidSignature    = errors.New("ld-sig: signature shape invalid")
	ErrUnsupportedSigType  = errors.New("ld-sig: unsupported signature type")
	ErrInvalidSignaturePEM = errors.New("ld-sig: public key PEM invalid")
	ErrSignatureMismatch   = errors.New("ld-sig: signature verification failed")
)

// VerifyRsaSignature2017 verifies an RsaSignature2017 LD-Signature on an
// inbound activity. Mirrors upstream Misskey TS
// `JsonLdService.verifyRsaSignature2017` byte-for-byte (= createVerifyData →
// sha256 hex concat → RSA-PKCS1v15 SHA-256 verify)。
//
// `activity` は inbound JSON を unmarshal した map ([string]any)。`signature`
// field を含むこと。`publicKeyPEM` は signature creator (= activity.signature.
// creator が指す actor) の RSA 公開鍵 PEM。caller (= InboxProcessor) は
// `getApId(creator)` で user を resolve して user_publickey.keyPem を取得し、
// 本関数に渡す。
//
// Returns nil if verify passes, otherwise an error explaining the failure
// (sentinel errors: ErrNoSignatureField / ErrInvalidSignature /
// ErrUnsupportedSigType / ErrInvalidSignaturePEM / ErrSignatureMismatch)。
func (p *Processor) VerifyRsaSignature2017(activity map[string]any, publicKeyPEM string) error {
	sigRaw, ok := activity["signature"]
	if !ok {
		return ErrNoSignatureField
	}
	sig, ok := sigRaw.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: signature is not an object", ErrInvalidSignature)
	}
	sigType, _ := sig["type"].(string)
	if sigType != LDSignatureType {
		return fmt.Errorf("%w: type=%q (want %q)", ErrUnsupportedSigType, sigType, LDSignatureType)
	}
	sigValB64, _ := sig["signatureValue"].(string)
	if sigValB64 == "" {
		return fmt.Errorf("%w: signatureValue missing", ErrInvalidSignature)
	}

	verifyData, err := p.createVerifyData(activity, sig)
	if err != nil {
		return err
	}

	pub, err := parseRSAPublicKey(publicKeyPEM)
	if err != nil {
		return err
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sigValB64)
	if err != nil {
		return fmt.Errorf("%w: signatureValue base64 decode: %v", ErrInvalidSignature, err)
	}
	finalHash := sha256.Sum256([]byte(verifyData))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, finalHash[:], sigBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrSignatureMismatch, err)
	}
	return nil
}

// createVerifyData mirrors upstream `JsonLdService.createVerifyData`:
//
//   - options から `type` / `id` / `signatureValue` を削除し、
//     `@context: 'https://w3id.org/identity/v1'` を付加して URDNA2015 normalize
//   - data から `signature` を削除して URDNA2015 normalize
//   - sha256 hex concat (= options_hash_hex + document_hash_hex)
//
// 出力は 128 文字 hex 文字列で、上位で sha256.Sum256 した bytes を RSA verify
// に渡す。upstream の `verifier.update(verifyData)` + `verifier.verify(...,
// 'sha256')` の挙動と等価。
func (p *Processor) createVerifyData(data, options map[string]any) (string, error) {
	// options 側: clone + @context 付加 + type/id/signatureValue 削除
	optsCopy := make(map[string]any, len(options)+1)
	for k, v := range options {
		if k == "type" || k == "id" || k == "signatureValue" {
			continue
		}
		optsCopy[k] = v
	}
	optsCopy["@context"] = "https://w3id.org/identity/v1"
	canonOpts, err := p.Normalize(optsCopy)
	if err != nil {
		return "", fmt.Errorf("normalize options: %w", err)
	}
	optionsHash := hex.EncodeToString(sha256Sum([]byte(canonOpts)))

	// data 側: clone + signature 削除
	dataCopy := make(map[string]any, len(data))
	for k, v := range data {
		if k == "signature" {
			continue
		}
		dataCopy[k] = v
	}
	canonData, err := p.Normalize(dataCopy)
	if err != nil {
		return "", fmt.Errorf("normalize data: %w", err)
	}
	docHash := hex.EncodeToString(sha256Sum([]byte(canonData)))

	return optionsHash + docHash, nil
}

func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

// parseRSAPublicKey decodes a PEM-encoded RSA public key. Accepts both
// PKIX SubjectPublicKeyInfo ("BEGIN PUBLIC KEY") and legacy PKCS1
// ("BEGIN RSA PUBLIC KEY") encodings, matching Misskey TS と Mastodon /
// Pleroma の混在実装に対応するため。
func parseRSAPublicKey(pemStr string) (*rsa.PublicKey, error) {
	blk, _ := pem.Decode([]byte(pemStr))
	if blk == nil {
		return nil, fmt.Errorf("%w: pem decode failed", ErrInvalidSignaturePEM)
	}
	switch blk.Type {
	case "PUBLIC KEY":
		anyKey, err := x509.ParsePKIXPublicKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: PKIX parse: %v", ErrInvalidSignaturePEM, err)
		}
		rsaKey, ok := anyKey.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("%w: not an RSA public key", ErrInvalidSignaturePEM)
		}
		return rsaKey, nil
	case "RSA PUBLIC KEY":
		rsaKey, err := x509.ParsePKCS1PublicKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: PKCS1 parse: %v", ErrInvalidSignaturePEM, err)
		}
		return rsaKey, nil
	default:
		return nil, fmt.Errorf("%w: unsupported PEM block type %q", ErrInvalidSignaturePEM, blk.Type)
	}
}
