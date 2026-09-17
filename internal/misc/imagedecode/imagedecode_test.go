package imagedecode

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/kovidgoyal/imaging"
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

// 対象外の入力は imaging に回る (既定の経路が生きていること)。
func TestDecode_FallsBackToImaging(t *testing.T) {
	for _, name := range []string{"plain-rgb8.png", "interlaced-rgba8.png"} {
		data := fixture(t, name)
		require.False(t, IsBrokenInterlacedPNG(data))
		img, err := Decode(data)
		require.NoError(t, err, name)
		require.NotNil(t, img, name)
		assert.Equal(t, 8, img.Bounds().Dx(), name)
	}
}

// **chunk の走査が範囲外を読まないこと。** 細工した length や途中で切れた
// 入力で index out of range にならず false を返す。
func TestHasChunk_Bounds(t *testing.T) {
	good := fixture(t, "interlaced-rgb8.png")

	// chunk header の途中で切れている (pos+8 > len)。
	assert.False(t, hasChunk(good[:12], "tRNS"))
	assert.False(t, hasChunk(good[:8], "tRNS"))

	// length を巨大にしても範囲外を読まない (`next > len(data)` で止まる)。
	huge := append([]byte(nil), good...)
	huge[8] = 0xff
	assert.False(t, hasChunk(huge, "tRNS"))
	assert.NotPanics(t, func() { _ = hasChunk(huge, "tRNS") })
}

// **wasm のデコーダへ渡す入力を縛る (#3037)。**
//
// `gen2brain/*` は wazero の runtime を package 内で 1 度だけ作り、
// `WithMemoryLimitPages` も `WithCloseOnContextDone` も指定しない。設定を
// 差し込む口が無いので、mk-go 側で握れるのは入力の大きさ・宣言寸法・
// 同時実行数だけ。
func TestDecode_RefusesOversizedSandboxedInput(t *testing.T) {
	big := SandboxedDecoderMaxBytes + 1

	// ISOBMFF (`ftyp` + brand) を名乗るバイト列。中身は問わない —
	// **デコーダに渡す前に断る**ことが要点。
	isobmff := func(brand string) []byte {
		b := make([]byte, big)
		copy(b, []byte{0, 0, 0, 0x20})
		copy(b[4:], "ftyp")
		copy(b[8:], brand)
		return b
	}

	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"AVIF", isobmff("avif")},
		{"AVIF sequence", isobmff("avis")},
		{"HEIC", isobmff("heic")},
		{"HEIF generic brand", isobmff("mif1")},
		{"JPEG XL codestream", append([]byte{0xFF, 0x0A}, make([]byte, big)...)},
		{"WebP", func() []byte {
			b := make([]byte, big)
			copy(b, "RIFF")
			copy(b[8:], "WEBP")
			return b
		}()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode(tt.data)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrEncodedTooLarge)
		})
	}
}

// **上限ちょうどは通す。** 判定が off-by-one で 1 バイト厳しくなると、
// 「通る画像の集合を変えない」という前提が崩れる。
func TestDecode_SandboxedCapIsInclusive(t *testing.T) {
	data := make([]byte, SandboxedDecoderMaxBytes)
	copy(data, []byte{0, 0, 0, 0x20})
	copy(data[4:], "ftyp")
	copy(data[8:], "avif")

	_, err := Decode(data)
	require.Error(t, err, "中身は AVIF ではないのでデコードは失敗する")
	assert.NotErrorIs(t, err, ErrEncodedTooLarge, "上限ちょうどで断っている")
}

// **wasm を使わない形式には掛けない。** PNG / JPEG / GIF は Go のデコーダで、
// 宣言寸法の cap が既に効いている。ここまで縛ると大きな PNG の
// サムネイル生成が落ち、webpublic が作られず EXIF が公開側へ出る
// (#3037 で塞いだばかりの経路)。
func TestDecode_SandboxedCapDoesNotApplyToNativeFormats(t *testing.T) {
	for _, tt := range []struct {
		name string
		head []byte
	}{
		{"PNG", []byte("\x89PNG\r\n\x1a\n")},
		{"JPEG", []byte{0xFF, 0xD8, 0xFF, 0xE0}},
		{"GIF", []byte("GIF89a")},
		// `ftyp` だが画像ではない brand (MP4)。
		{"MP4", append([]byte{0, 0, 0, 0x20}, []byte("ftypisom")...)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := make([]byte, SandboxedDecoderMaxBytes+1)
			copy(data, tt.head)

			_, err := Decode(data)
			require.Error(t, err, "中身が壊れているのでデコードは失敗する")
			assert.NotErrorIs(t, err, ErrEncodedTooLarge)
		})
	}
}

// **画素数だけでは確保量が決まらない (#3037)。**
//
// 16bit の PNG は `image.NRGBA64` へ展開されるので 1 画素 8 バイト。cap
// ちょうどの 64MP なら 512MB で、同時実行枠が 4 あれば 2GB を超える。
// cap を通っているので今までは何も止めなかった。
func TestDecodeWithPixelCap_CountsBytesNotOnlyPixels(t *testing.T) {
	// 4x4 = 16 画素。予算は maxPixels*4 バイト。
	const w, h = 4, 4
	deep := encode16BitPNG(t, w, h)
	shallow := encode8BitPNG(t, w, h)

	// maxPixels=20 → 予算 80 バイト。8bit は 64 バイトで通り、16bit は
	// 128 バイトで落ちる。**画素数はどちらも 16 で cap 内**。
	_, err := DecodeWithPixelCap(shallow, 20)
	require.NoError(t, err, "8bit の判定が変わっている")

	_, err = DecodeWithPixelCap(deep, 20)
	require.Error(t, err, "16bit がバイト予算を超えても通っている")
	assert.ErrorIs(t, err, ErrTooManyPixels)

	// 予算を倍にすれば 16bit も通る (「16bit を一律で拒否」ではない)。
	_, err = DecodeWithPixelCap(deep, 40)
	assert.NoError(t, err, "16bit を一律で拒否している")
}

// **8bit の画像にとっては従来と同じ判定。** 通る集合が変わると、cap を
// 通っていた実写真がサムネイル・webpublic を失い、EXIF が公開側へ出る
// (#3037 で塞いだばかりの経路)。
func TestDecodeWithPixelCap_EightBitBoundaryUnchanged(t *testing.T) {
	data := encode8BitPNG(t, 4, 4) // 16 画素

	_, err := DecodeWithPixelCap(data, 16)
	assert.NoError(t, err, "cap ちょうどの 8bit 画像を落としている")

	_, err = DecodeWithPixelCap(data, 15)
	assert.ErrorIs(t, err, ErrTooManyPixels)
}

func TestDecodedBytesPerPixel(t *testing.T) {
	for _, tt := range []struct {
		name  string
		model color.Model
		want  int64
	}{
		{"NRGBA64", color.NRGBA64Model, 8},
		{"RGBA64", color.RGBA64Model, 8},
		{"Gray16", color.Gray16Model, 2},
		{"Gray", color.GrayModel, 1},
		{"NRGBA", color.NRGBAModel, 4},
		// 分からないものは 4 に倒す (どれも 4 を超えない)。
		{"YCbCr", color.YCbCrModel, 4},
		{"CMYK", color.CMYKModel, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, decodedBytesPerPixel(tt.model))
		})
	}
}

func encode16BitPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA64(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	// png.Encode が 16bit で書いていることを確かめる (8bit に落ちていたら
	// このテストは何も検査していない)。
	cfg, err := png.DecodeConfig(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	require.Equal(t, int64(8), decodedBytesPerPixel(cfg.ColorModel), "16bit で書けていない")
	return buf.Bytes()
}

func encode8BitPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	cfg, err := png.DecodeConfig(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	require.Equal(t, int64(4), decodedBytesPerPixel(cfg.ColorModel))
	return buf.Bytes()
}

// **`image.DecodeConfig` は PNG の `tRNS` を読まない (#3037 レビューで実測)。**
//
// 非パレット画像では `tRNS` に当たる前に break するので、グレースケールは
// `tRNS` の有無に関わらず `Gray` / `Gray16` と報告される。ところがデコーダは
// `tRNS` があるとアルファ付きへ展開する — 16bit グレースケールは
// `image.NRGBA64` (1 画素 8 バイト、報告値の 4 倍)。**バイト予算を入れた意味が
// そこだけ消えていた。**
func TestDecodeWithPixelCap_Gray16WithTransparencyCostsEightBytes(t *testing.T) {
	plain := encodeGray16PNG(t, 4, 4)
	withTRNS := insertPNGChunk(t, plain, "tRNS", []byte{0x00, 0x00})

	// どちらも DecodeConfig は Gray16 と報告する (= 報告値は当てにならない)。
	for _, data := range [][]byte{plain, withTRNS} {
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		require.NoError(t, err)
		require.Equal(t, color.Gray16Model, cfg.ColorModel)
	}

	// 16 画素。予算は maxPixels*4 バイト。maxPixels=16 なら予算 64 バイト。
	// tRNS 無し (2 B/px = 32 バイト) は通る。
	_, err := DecodeWithPixelCap(plain, 16)
	assert.NoError(t, err, "tRNS 無しの Gray16 の判定が変わっている")

	// tRNS 有り (8 B/px = 128 バイト) は同じ予算では落ちる。
	_, err = DecodeWithPixelCap(withTRNS, 16)
	require.Error(t, err, "tRNS 付き Gray16 が 2 B/px と見積もられている")
	assert.ErrorIs(t, err, ErrTooManyPixels)

	// 予算を増やせば通る (一律拒否ではない)。
	_, err = DecodeWithPixelCap(withTRNS, 32)
	assert.NoError(t, err)
}

// **見積もりが実際の確保と合っていること。** ここが合っていないと、予算の
// 判定そのものが意味を持たない。
func TestDecodedBytesPerPixelFor_MatchesTheDecodedImage(t *testing.T) {
	gray16 := encodeGray16PNG(t, 4, 4)

	for _, tt := range []struct {
		name string
		data []byte
		want int64
	}{
		{"Gray16", gray16, 2},
		{"Gray16 + tRNS", insertPNGChunk(t, gray16, "tRNS", []byte{0x00, 0x00}), 8},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := png.DecodeConfig(bytes.NewReader(tt.data))
			require.NoError(t, err)
			assert.Equal(t, tt.want, decodedBytesPerPixelFor(tt.data, cfg.ColorModel))

			// 実際にデコードして、見積もりが下回っていないことを確かめる。
			img, err := imaging.Decode(bytes.NewReader(tt.data), imaging.AutoOrientation(true))
			require.NoError(t, err)
			assert.LessOrEqual(t, actualBytesPerPixel(img), tt.want,
				"見積もりが実際の確保量を下回っている (%T)", img)
		})
	}
}

// **JXL の判定は登録側の magic に合わせる。** `gen2brain/jpegxl` が登録するのは
// `????JXL` (7 バイト) なので、`JXL ` + `0D0A870A` まで要求すると 1 バイト違う
// 入力が guard を抜けて wasm へ全量渡る。
func TestUsesSandboxedDecoder_JXLContainerMatchesRegisteredMagic(t *testing.T) {
	data := make([]byte, 16)
	copy(data, []byte{0, 0, 0, 0x0C})
	copy(data[4:], "JXL")
	data[7] = 0x00 // 末尾スペースではない形
	assert.True(t, usesSandboxedDecoder(data), "登録 magic と同じ形を取り逃がしている")
}

func actualBytesPerPixel(img image.Image) int64 {
	switch img.(type) {
	case *image.NRGBA64, *image.RGBA64:
		return 8
	case *image.NRGBA, *image.RGBA, *image.CMYK:
		return 4
	case *image.Gray16:
		return 2
	case *image.Gray, *image.Paletted:
		return 1
	default:
		return 4
	}
}

func encodeGray16PNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray16(image.Rect(0, 0, w, h))))
	return buf.Bytes()
}

// insertPNGChunk splices a chunk in just before IDAT.
func insertPNGChunk(t *testing.T, src []byte, typ string, data []byte) []byte {
	t.Helper()
	idx := bytes.Index(src, []byte("IDAT"))
	require.Greater(t, idx, 4)
	pos := idx - 4

	body := append([]byte(typ), data...)
	out := make([]byte, 0, len(src)+len(body)+8)
	out = append(out, src[:pos]...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(data)))
	out = append(out, body...)
	out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(body))
	return append(out, src[pos:]...)
}
