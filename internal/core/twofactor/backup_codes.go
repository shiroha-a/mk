package twofactor

import (
	"encoding/hex"
	"errors"
)

// BackupCodeCount is the number of recovery codes generated when 2FA is set
// up. Matches Misskey upstream's default of 5; users typically print or save
// these and use them when their TOTP device is unavailable.
const BackupCodeCount = 5

// BackupCodeBytes is the number of random bytes per code.
//
// **upstream と同じ 20 バイト (160bit)。** かつては 8 バイト (64bit) で、
// コメントは "long enough" と書いていたが upstream 比で 96bit 落としている
// ことに触れていなかった (`i/2fa/done.ts` は `new OTPAuth.Secret().base32`
// = 既定 20 バイト)。オンライン総当たりはレート制限で防げるが、ハッシュ化
// されていない値をこの長さで持つ理由も無い。
//
// **既存のコードは引き続き検証できる。** 照合は保存済みの値との比較なので、
// 長さを変えても古い 16 hex 文字のコードはそのまま使える。次に再発行した
// ときから新しい長さになる。
const BackupCodeBytes = 20

// ErrBackupCodeMismatch is returned when ConsumeBackupCode does not find the
// supplied code in the user's backup list.
var ErrBackupCodeMismatch = errors.New("twofactor: backup code does not match")

// GenerateBackupCodes returns BackupCodeCount fresh hex-encoded recovery
// codes. Codes are stored as plain text in user_profile.twoFactorBackupSecret;
// since they are single-use and high-entropy, hashing them at rest gives only
// marginal benefit and complicates the verify path. Users see these once
// during 2FA setup and must save them.
func GenerateBackupCodes() ([]string, error) {
	codes := make([]string, BackupCodeCount)
	for i := range codes {
		buf := make([]byte, BackupCodeBytes)
		// readRandom is a package-level var defined in webauthn.go so tests
		// can inject a deterministic source for both call sites.
		if _, err := readRandom(buf); err != nil {
			return nil, err
		}
		codes[i] = hex.EncodeToString(buf)
	}
	return codes, nil
}

// ConsumeBackupCode looks up `code` in `stored` and, if found, returns the
// remaining list with that code removed (single-use semantics). Returns
// ErrBackupCodeMismatch when the code is not present.
//
// Note: comparison is intentionally simple equality. Backup codes are
// generated server-side and the keyspace is large enough that timing-attack
// hardening (constant-time compare per code) does not change the practical
// security posture meaningfully — but the per-code work also stays small.
func ConsumeBackupCode(stored []string, code string) ([]string, error) {
	if code == "" {
		return nil, ErrBackupCodeMismatch
	}
	for i, c := range stored {
		if c == code {
			out := make([]string, 0, len(stored)-1)
			out = append(out, stored[:i]...)
			out = append(out, stored[i+1:]...)
			return out, nil
		}
	}
	return nil, ErrBackupCodeMismatch
}
