// Package twofactor provides TOTP-based two-factor authentication.
package twofactor

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// totpSkew は許容する前後ステップ数。upstream Misskey 2026.6.0 (#17580) で
// UserAuthService の validationWindow が 5→1 に絞られた (replay surface 縮小)。
// drop-in 互換のため mk-go も ±1 ステップ (±30s) に揃える。Misskey TS 側も
// window:1 になったため両者 ±30s で時計ずれ許容も一致する (旧 #1555 の window:5
// 追従は upstream 変更で陳腐化)。
const totpSkew = 1

// qrImageSize は frontend MkPoll 系コンポーネントが想定する QR コード画像
// の 1 辺ピクセル数。Misskey TS upstream の `QRCode.toDataURL(url)` は
// default 4x scale + 8px margin (= 約 200px 程度) を返すため、SP / desktop
// どちらでも視認できるよう 256px で固定する。
const qrImageSize = 256

// GenerateSecret creates a new TOTP secret for a user.
// Returns the secret key and the otpauth:// URI for QR code generation.
func GenerateSecret(issuer, accountName string) (secret string, uri string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: accountName,
	})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

// QRDataURL renders the given otpauth:// URI into a base64-encoded PNG
// `data:` URL suitable for direct use as `<img src="...">`. Misskey TS の
// `i/2fa/register` endpoint は `QRCode.toDataURL(url)` で同形式の文字列を
// 返しており、frontend (settings/2fa.qrdialog.vue) が `<img :src=
// "twoFactorData.qr">` で読み込む契約 (#697)。
//
// 戻り値は "data:image/png;base64,..." 形式。pquerna/otp の Key.Image は
// internally rsc.io/qr を使い QR モードを ALPHANUM か BYTE で自動選択する。
func QRDataURL(uri string) (string, error) {
	key, err := otp.NewKeyFromURL(uri)
	if err != nil {
		return "", fmt.Errorf("twofactor: parse otpauth uri: %w", err)
	}
	img, err := key.Image(qrImageSize, qrImageSize)
	if err != nil {
		return "", fmt.Errorf("twofactor: render qr image: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", fmt.Errorf("twofactor: encode qr png: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// Validate checks if a TOTP code is valid for the given secret. The acceptance
// window matches upstream Misskey (window:5 = ±5 steps ≈ ±150s)。
func Validate(code, secret string) bool {
	ok, err := totp.ValidateCustom(code, secret, time.Now().UTC(), totp.ValidateOpts{
		Period:    30,
		Skew:      totpSkew,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	return err == nil && ok
}
