package repository

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cleanupInstanceSecret(t *testing.T, key string) {
	t.Helper()
	testDB.Exec(`DELETE FROM "instance_secret" WHERE key = ?`, key)
}

func TestInstanceSecretRepository_GetOrCreate(t *testing.T) {
	repo := NewInstanceSecretRepository(testDB)
	const key = "test-generate"
	cleanupInstanceSecret(t, key)
	t.Cleanup(func() { cleanupInstanceSecret(t, key) })

	secret, err := repo.GetOrCreate(key)
	require.NoError(t, err)
	assert.Len(t, secret, generatedSecretBytes, "生成された鍵の長さ")
	assert.NotEqual(t, make([]byte, generatedSecretBytes), secret, "ゼロ値ではない")
}

// 同じ key は常に同じ値を返す。ここが揺れると、署名した URL を再起動後に
// 検証できなくなる (URL 由来の弱い鍵を捨てた目的そのものが崩れる)。
func TestInstanceSecretRepository_GetOrCreateIsStable(t *testing.T) {
	repo := NewInstanceSecretRepository(testDB)
	const key = "test-stable"
	cleanupInstanceSecret(t, key)
	t.Cleanup(func() { cleanupInstanceSecret(t, key) })

	first, err := repo.GetOrCreate(key)
	require.NoError(t, err)
	second, err := repo.GetOrCreate(key)
	require.NoError(t, err)
	assert.Equal(t, first, second, "同じ key は同じ値を返す")

	// 別 instance からも同じ値が読めること (プロセスが分かれても共有される)。
	other, err := NewInstanceSecretRepository(testDB).GetOrCreate(key)
	require.NoError(t, err)
	assert.Equal(t, first, other)
}

func TestInstanceSecretRepository_GetOrCreateIsUniquePerKey(t *testing.T) {
	repo := NewInstanceSecretRepository(testDB)
	const keyA, keyB = "test-unique-a", "test-unique-b"
	for _, k := range []string{keyA, keyB} {
		cleanupInstanceSecret(t, k)
	}
	t.Cleanup(func() {
		for _, k := range []string{keyA, keyB} {
			cleanupInstanceSecret(t, k)
		}
	})

	a, err := repo.GetOrCreate(keyA)
	require.NoError(t, err)
	b, err := repo.GetOrCreate(keyB)
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "key ごとに独立した値")
}

// 既存行があればそれを返す (生成し直さない)。
func TestInstanceSecretRepository_GetOrCreateReadsExistingRow(t *testing.T) {
	repo := NewInstanceSecretRepository(testDB)
	const key = "test-existing"
	cleanupInstanceSecret(t, key)
	t.Cleanup(func() { cleanupInstanceSecret(t, key) })

	want := []byte{0xde, 0xad, 0xbe, 0xef}
	require.NoError(t, testDB.Exec(
		`INSERT INTO "instance_secret" (key, value) VALUES (?, ?)`,
		key, hex.EncodeToString(want),
	).Error)

	got, err := repo.GetOrCreate(key)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// 壊れた行 (hex でない / 空) は黙って使わずエラーにする。ここで空の鍵を
// 返すと、全員が同じ「空鍵」で署名を検証できてしまう。
func TestInstanceSecretRepository_GetOrCreateRejectsCorruptRow(t *testing.T) {
	repo := NewInstanceSecretRepository(testDB)

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"non-hex value", "test-corrupt-hex", "zzzz"},
		{"empty value", "test-corrupt-empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanupInstanceSecret(t, tt.key)
			t.Cleanup(func() { cleanupInstanceSecret(t, tt.key) })
			require.NoError(t, testDB.Exec(
				`INSERT INTO "instance_secret" (key, value) VALUES (?, ?)`,
				tt.key, tt.value,
			).Error)

			_, err := repo.GetOrCreate(tt.key)
			assert.Error(t, err, "壊れた行はエラーにする")
		})
	}
}
