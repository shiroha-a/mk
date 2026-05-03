package twofactor

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateSecret(t *testing.T) {
	secret, uri, err := GenerateSecret("Misskey", "testuser")
	require.NoError(t, err)
	assert.NotEmpty(t, secret)
	assert.Contains(t, uri, "otpauth://totp/")
	assert.Contains(t, uri, "Misskey")
}

func TestValidate_Success(t *testing.T) {
	secret, _, err := GenerateSecret("Misskey", "testuser")
	require.NoError(t, err)
	// 正しいコードを生成して検証
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	assert.True(t, Validate(code, secret))
}

func TestValidate_WrongCode(t *testing.T) {
	secret, _, err := GenerateSecret("Misskey", "testuser")
	require.NoError(t, err)
	assert.False(t, Validate("000000", secret))
}

func TestValidate_EmptySecret(t *testing.T) {
	assert.False(t, Validate("123456", ""))
}

func TestGenerateSecret_EmptyIssuer(t *testing.T) {
	_, _, err := GenerateSecret("", "")
	// 空issuerはエラーになる → エラーパスをカバー
	assert.Error(t, err)
}

// TestQRDataURL guards #697: frontend が `<img :src=...>` で読むため
// `qr` は PNG data URL でなければならず、生 otpauth URI ではダメ。
func TestQRDataURL(t *testing.T) {
	_, uri, err := GenerateSecret("Misskey", "testuser")
	require.NoError(t, err)

	dataURL, err := QRDataURL(uri)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(dataURL, "data:image/png;base64,"),
		"qr field must be a base64 PNG data URL so the frontend <img src=...> works")

	// base64 部分は decodable であること (= 実際に画像 bytes が入っている)
	const prefix = "data:image/png;base64,"
	raw, err := base64.StdEncoding.DecodeString(dataURL[len(prefix):])
	require.NoError(t, err)
	// PNG signature は 8 byte 固定 (89 50 4E 47 0D 0A 1A 0A)
	require.GreaterOrEqual(t, len(raw), 8)
	assert.Equal(t, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, raw[:8],
		"decoded payload must be a valid PNG (signature mismatch)")
}

func TestQRDataURL_InvalidURI(t *testing.T) {
	// Malformed URL (`:` lone scheme prefix) でないと pquerna/otp の
	// NewKeyFromURL は寛容で error を返さない (string を Key.url に詰める
	// だけ) ので、url.Parse が失敗する形を渡す。
	_, err := QRDataURL(":invalid")
	assert.Error(t, err)
}

func TestQRDataURL_NonOtpAuthURIRendersImage(t *testing.T) {
	// Non-otpauth な URL でも Image() は QR を生成できるが、frontend は
	// otpauth URI であることを前提に保存しているので integration では
	// GenerateSecret 経由のみが使われる。本 test は「QR rendering 自体は
	// 任意 URL で動く」契約を guard する (将来 ext URL に変えても rendering
	// 経路は壊れない)。
	dataURL, err := QRDataURL("https://example.com/")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(dataURL, "data:image/png;base64,"))
}
