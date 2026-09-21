package twofactor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// **upstream と同じ 160bit にすること。**
//
// かつては 8 バイト (64bit) で、コメントは "long enough" と書きながら upstream
// 比で 96bit 落としていることに触れていなかった (`i/2fa/done.ts` は
// `new OTPAuth.Secret().base32` = 既定 20 バイト)。
func TestBackupCodeEntropy(t *testing.T) {
	t.Parallel()

	// 値はリテラルで書く (定数を参照すると、縮める変異と一緒に期待値が動く)。
	require.Equal(t, 20, BackupCodeBytes, "1 コードあたり 20 バイト (160bit)")

	codes, err := GenerateBackupCodes()
	require.NoError(t, err)
	require.NotEmpty(t, codes)
	for _, c := range codes {
		require.Len(t, c, 40, "hex 表現は 40 文字 (20 バイト)")
	}
}
