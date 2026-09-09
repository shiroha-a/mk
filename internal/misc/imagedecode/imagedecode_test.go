package imagedecode

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return data
}

// **インターレースの truecolor PNG が真っ黒にならないこと (#2925)。**
//
// `kovidgoyal/imaging` は colorType=2 / bitDepth=8 / tRNS 無しの PNG を独自の
// `*nrgb.Image` に読むが、その Adam7 の pass 合成に `*nrgb.Image` の case が
// 無く、**エラーを返さず全画素 0** を返す。resize 系の mode は「真っ黒な絵文字」
// を 200 で配ることになり、検出する手段が無い。
func TestDecode_InterlacedRGB8IsNotBlank(t *testing.T) {
	data := fixture(t, "interlaced-rgb8.png")
	require.True(t, IsBrokenInterlacedPNG(data), "fixture が該当条件でなくなっている")

	got, err := Decode(data)
	require.NoError(t, err)

	// **stdlib と画素単位で突き合わせる。** 「0 でないこと」だけだと、色が
	// 入れ替わるような別の壊れ方を見逃す。
	want, err := png.Decode(bytes.NewReader(data))
	require.NoError(t, err)

	b := got.Bounds()
	require.Equal(t, want.Bounds(), b)
	nonZero := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			wr, wg, wb, wa := want.At(x, y).RGBA()
			gr, gg, gb, ga := got.At(x, y).RGBA()
			require.Equal(t, [4]uint32{wr >> 8, wg >> 8, wb >> 8, wa >> 8},
				[4]uint32{gr >> 8, gg >> 8, gb >> 8, ga >> 8},
				"画素 (%d,%d)", x, y)
			if wr>>8 != 0 || wg>>8 != 0 || wb>>8 != 0 {
				nonZero++
			}
		}
	}
	require.Greater(t, nonZero, 0, "前提: fixture は全画素 0 ではない")
}

// **壊れない種別は imaging のまま通す (#2925)。**
//
// インターレースというだけで stdlib へ回すと、壊れていない PNG まで
// ICC→sRGB 変換を失う (Display P3 の PNG でチャンネル差が最大 31/255)。
func TestIsBrokenInterlacedPNG_Narrow(t *testing.T) {
	assert.True(t, IsBrokenInterlacedPNG(fixture(t, "interlaced-rgb8.png")))
	assert.False(t, IsBrokenInterlacedPNG(fixture(t, "interlaced-rgba8.png")),
		"RGBA は *image.NRGBA になり正しく読める")
	assert.False(t, IsBrokenInterlacedPNG(fixture(t, "plain-rgb8.png")),
		"非インターレースは壊れない")
	assert.False(t, IsBrokenInterlacedPNG(fixture(t, "interlaced-rgb8-trns.png")),
		"tRNS 付きは *image.NRGBA になり正しく読める")
}

// 判定の穴を 1 つずつ塞ぐ。**それぞれ別の変異に対応している。**
func TestIsBrokenInterlacedPNG_Guards(t *testing.T) {
	good := fixture(t, "interlaced-rgb8.png")

	// signature を見ないと、PNG でない入力の 28 バイト目を interlace method と
	// 読んでしまう。
	badSig := append([]byte(nil), good...)
	copy(badSig[:8], "NOTaPNG!")
	assert.False(t, IsBrokenInterlacedPNG(badSig), "signature を見ること")

	// 最初の chunk が IHDR でなければ、24/25/28 バイト目は IHDR の値ではない。
	badChunk := append([]byte(nil), good...)
	copy(badChunk[12:16], "IDAT")
	assert.False(t, IsBrokenInterlacedPNG(badChunk), "IHDR を見ること")

	// bit depth / colorType / interlace method のどれが違っても対象外。
	for _, tc := range []struct {
		name string
		off  int
		val  byte
	}{
		{"bit depth が 8 でない", 24, 16},
		{"colorType が 2 でない", 25, 6},
		{"interlace が 1 でない", 28, 0},
		{"interlace が 2 (不正値)", 28, 2},
	} {
		m := append([]byte(nil), good...)
		m[tc.off] = tc.val
		assert.False(t, IsBrokenInterlacedPNG(m), tc.name)
	}

	// **28 バイト境界。** `len(data) < 29` を `< 28` に緩めると範囲外を読む。
	assert.False(t, IsBrokenInterlacedPNG(good[:28]), "28 バイトで落ちないこと")
	// 29 バイトあれば IHDR は読めるので true。**そこで切れている PNG は
	// どのみち decode できず、stdlib がきちんとエラーを返す** (下のテスト)。
	assert.True(t, IsBrokenInterlacedPNG(good[:29]))
	assert.False(t, IsBrokenInterlacedPNG(nil))
	assert.False(t, IsBrokenInterlacedPNG([]byte("not a png")))
}

// **decode の失敗は握り潰さない。** nil を返すと呼び出し元が nil 参照で落ちる。
func TestDecode_TruncatedInterlacedPNGIsError(t *testing.T) {
	good := fixture(t, "interlaced-rgb8.png")
	img, err := Decode(good[:len(good)-20])
	require.Error(t, err)
	assert.Nil(t, img)
}

// tRNS 付きの truecolor は `*image.NRGBA` になり正しく読めるので対象外。
// **chunk の走査が length を信用して範囲外を読まないこと**も見る。
func TestHasChunk(t *testing.T) {
	good := fixture(t, "interlaced-rgb8.png")
	assert.True(t, hasChunk(good, "IHDR"))
	assert.False(t, hasChunk(good, "tRNS"))
	assert.True(t, hasChunk(fixture(t, "interlaced-rgb8-trns.png"), "tRNS"),
		"tRNS を見つけられること")
	assert.False(t, hasChunk(good, "zTXt"))

	// length を巨大にしても範囲外を読まない。
	broken := append([]byte(nil), good...)
	broken[8], broken[9], broken[10], broken[11] = 0x7f, 0xff, 0xff, 0xff
	assert.False(t, hasChunk(broken, "tRNS"))
	assert.NotPanics(t, func() { _ = IsBrokenInterlacedPNG(broken) })
}
