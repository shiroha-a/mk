package misc

import (
	"crypto/rand"
	"encoding/hex"
	"io"
)

// randReader is the source of cryptographic randomness.
// テスト時に差し替え可能にするためパッケージ変数として公開
var randReader io.Reader = rand.Reader

// SecureRandomHex returns a hex-encoded random string of n characters.
func SecureRandomHex(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := io.ReadFull(randReader, b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)[:n]
}

// AlphanumericChars mirrors upstream secureRndstr's default LU_CHARS
// (digits + lower + upper, 62 chars)。admin-issued temporary password 等で
// upstream と shape を揃えるために使う (#1825)。
const AlphanumericChars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// SecureRandomString returns a cryptographically random string of length
// characters drawn from chars. Mirrors upstream secureRndstr (62-char
// alphanumeric set では modulo bias は無視できる程度で、upstream の
// floor(byte/0xFF*len) と同程度)。RNG 失敗時は panic する。
func SecureRandomString(length int, chars string) string {
	if length <= 0 || len(chars) == 0 {
		return ""
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(randReader, buf); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	// 乱数バイト列をそのまま charset の index に写像して in-place で書き戻す。
	for i := range buf {
		buf[i] = chars[int(buf[i])%len(chars)]
	}
	return string(buf)
}
