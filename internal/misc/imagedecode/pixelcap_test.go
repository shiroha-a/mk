package imagedecode

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 巨大な canvas を宣言したヘッダは、ラスタを確保する前に弾く。
//
// **デコード後に測っても遅い。** デコーダはヘッダの寸法だけで確保するので、
// 判定が後ろにあると確保そのものが起きてしまう。**Go の大確保失敗は
// `throw("out of memory")` で recover できない**ため、echo の Recover は
// 効かずプロセスごと落ちる。
//
// テストは **BMP** で書く。54 バイトのヘッダだけで任意の寸法を宣言でき、
// 実際にその大きさのピクセルデータを用意しなくてよいので、
// 「ヘッダだけ見て弾いている」ことを直接確かめられる。

// bmpHeader builds a 54-byte BITMAPINFOHEADER BMP declaring w x h at 24bpp.
//
// ピクセルデータは付けない。**付けないことが要点** — 弾けていればデータを
// 読みに行かないので、ここで止まったことが分かる。
func bmpHeader(w, h int32) []byte {
	var b bytes.Buffer
	b.WriteString("BM")
	_ = binary.Write(&b, binary.LittleEndian, uint32(54)) // file size (嘘でよい)
	_ = binary.Write(&b, binary.LittleEndian, uint32(0))  // reserved
	_ = binary.Write(&b, binary.LittleEndian, uint32(54)) // pixel data offset
	_ = binary.Write(&b, binary.LittleEndian, uint32(40)) // DIB header size
	_ = binary.Write(&b, binary.LittleEndian, w)
	_ = binary.Write(&b, binary.LittleEndian, h)
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))  // planes
	_ = binary.Write(&b, binary.LittleEndian, uint16(24)) // bpp
	_ = binary.Write(&b, binary.LittleEndian, uint32(0))  // compression
	_ = binary.Write(&b, binary.LittleEndian, uint32(0))  // image size
	_ = binary.Write(&b, binary.LittleEndian, int32(2835))
	_ = binary.Write(&b, binary.LittleEndian, int32(2835))
	_ = binary.Write(&b, binary.LittleEndian, uint32(0)) // palette colours
	_ = binary.Write(&b, binary.LittleEndian, uint32(0)) // important colours
	return b.Bytes()
}

func TestDecode_RefusesOversizedHeaderBeforeAllocating(t *testing.T) {
	// 54 バイトで 8.6GB (46341^2 * 4 byte) を要求するヘッダ。
	data := bmpHeader(46341, 46341)
	require.Len(t, data, 54, "ヘッダだけで宣言できることが前提")

	_, err := Decode(data)
	require.ErrorIs(t, err, ErrTooManyPixels)
	assert.Contains(t, err.Error(), "46341x46341", "どの寸法で弾いたかを残す")
}

func TestDecode_PixelCapBoundary(t *testing.T) {
	// MaxPixels ちょうどは通す判定であること (ヘッダ段階の判定のみ検査。
	// 実データを伴わないので Decode 自体は別の理由で失敗してよい)。
	t.Run("上限ちょうどは pixel cap では弾かない", func(t *testing.T) {
		_, err := Decode(bmpHeader(8192, 8192))
		assert.NotErrorIs(t, err, ErrTooManyPixels)
	})

	t.Run("上限を 1 画素超えたら弾く", func(t *testing.T) {
		// 8192*8192 + 8192 画素 = MaxPixels + 8192。
		_, err := Decode(bmpHeader(8192, 8193))
		assert.ErrorIs(t, err, ErrTooManyPixels)
	})
}

// 普通の画像はこれまでどおり読める。**通る集合を変えていない**ことの確認。
func TestDecode_NormalImageStillDecodes(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))

	got, err := Decode(buf.Bytes())
	require.NoError(t, err)
	assert.Equal(t, 8, got.Bounds().Dx())
	assert.Equal(t, 8, got.Bounds().Dy())
}

// 寸法を読めない入力は通す (従来どおりデコード後の cap が受け止める)。
//
// **判定できないことを理由に拒否しない。** TGA のように magic bytes を持たない
// 形式や未登録の形式がここで落ちると、これまで読めていた画像が読めなくなる。
func TestDecode_UnreadableHeaderIsNotRefusedByTheCap(t *testing.T) {
	_, err := Decode([]byte("not an image at all"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrTooManyPixels)
}
