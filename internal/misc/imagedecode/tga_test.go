package imagedecode

import (
	"encoding/binary"
	"errors"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// tgaHeader builds the 18-byte TARGA header for an uncompressed true-color
// image of the declared size. body is appended verbatim.
func tgaHeader(w, h int, body []byte) []byte {
	b := make([]byte, 18)
	b[2] = 2 // uncompressed true-color
	binary.LittleEndian.PutUint16(b[12:14], uint16(w))
	binary.LittleEndian.PutUint16(b[14:16], uint16(h))
	b[16] = 32 // bits per pixel
	b[17] = 8  // alpha bits
	return append(b, body...)
}

// 宣言寸法だけが巨大な TGA は、ラスタを確保する前に拒否されること。
func TestDecodeTGAWithPixelCap_RefusesOversizedHeader(t *testing.T) {
	t.Parallel()

	// 82 バイト。画素データは無い。
	data := tgaHeader(16384, 16384, make([]byte, 64))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	img, err := DecodeTGAWithPixelCap(data, MaxPixels)

	runtime.ReadMemStats(&after)

	require.Error(t, err)
	require.Nil(t, img)
	require.True(t, errors.Is(err, ErrTooManyPixels), "got %v", err)

	// 16384*16384*4 = 1 GiB。確保されていればここに現れる。
	// 判定そのものが使う分に余裕を見て 64 MiB を上限にする。
	const budget = 64 << 20
	allocated := after.TotalAlloc - before.TotalAlloc
	require.Less(t, allocated, uint64(budget),
		"raster must not be allocated before the cap is checked (allocated %d bytes)", allocated)
}

// cap ちょうどは通り、1 画素超えると落ちること。
func TestDecodeTGAWithPixelCap_Boundary(t *testing.T) {
	t.Parallel()

	// 小さな cap を渡して境界を見る (実データを 4 画素ぶんだけ用意する)。
	ok := tgaHeader(2, 2, make([]byte, 2*2*4))
	img, err := DecodeTGAWithPixelCap(ok, 4)
	require.NoError(t, err)
	require.NotNil(t, img)
	require.Equal(t, 2, img.Bounds().Dx())
	require.Equal(t, 2, img.Bounds().Dy())

	_, err = DecodeTGAWithPixelCap(ok, 3)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrTooManyPixels))
}

// 壊れたヘッダ / 0 寸法は素直に失敗すること (通してはいけない)。
func TestDecodeTGAWithPixelCap_RejectsInvalid(t *testing.T) {
	t.Parallel()

	_, err := DecodeTGAWithPixelCap([]byte{0, 1, 2}, MaxPixels)
	require.Error(t, err, "truncated header")

	// **`tga.DecodeConfig` は 0 寸法をエラーにしない** (実測) ので、ここで
	// 落ちるのは自前の判定。メッセージまで見ないと Decode 側の失敗と区別が
	// つかず、判定を外す変異が素通りする。
	_, err = DecodeTGAWithPixelCap(tgaHeader(0, 0, make([]byte, 64)), MaxPixels)
	require.Error(t, err, "zero dimensions")
	require.Contains(t, err.Error(), "invalid dimensions")

	_, err = DecodeTGAWithPixelCap(tgaHeader(16, 0, make([]byte, 64)), MaxPixels)
	require.Error(t, err, "zero height")
	require.Contains(t, err.Error(), "invalid dimensions")
}
