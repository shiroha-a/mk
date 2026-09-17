package imagedecode

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"

	"github.com/kovidgoyal/imaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// アニメーションは 1 コマだけ読む。
//
// **pixel cap はコマ数を見ない。** 1 コマぶんの寸法しか見ないので、全コマを
// 載せる decoder に渡すとコマ数で掛け算できる。GIF のコマは LZW 圧縮なので、
// 透明な全画面コマは極端に縮み、小さなファイルから数十 GB を要求できる。
//
// **検出は「後続のコマが壊れていても読めるか」で行う。** メモリ使用量の測定は
// GC のタイミングに左右されるが、これは決定的に分かれる — `gif.DecodeAll` は
// 途中で切れた GIF を `gif: not enough image data` で丸ごと失敗させ、
// `gif.Decode` は 1 コマ目を返して止まる (実測)。

// multiFrameGIF builds a GIF with n identical frames of w x h.
func multiFrameGIF(t *testing.T, n, w, h int) []byte {
	t.Helper()
	pal := color.Palette{color.Black, color.White}
	g := &gif.GIF{Config: image.Config{ColorModel: pal, Width: w, Height: h}}
	for i := 0; i < n; i++ {
		f := image.NewPaletted(image.Rect(0, 0, w, h), pal)
		f.SetColorIndex(0, 0, uint8(i%2))
		g.Image = append(g.Image, f)
		g.Delay = append(g.Delay, 10)
	}
	var buf bytes.Buffer
	require.NoError(t, gif.EncodeAll(&buf, g))
	return buf.Bytes()
}

func TestDecode_GIFReadsOnlyTheFirstFrame(t *testing.T) {
	full := multiFrameGIF(t, 3, 64, 64)
	// 2 コマ目の途中で切る。全コマを読む実装はここで失敗する。
	truncated := full[:len(full)*2/3]

	// 前提の確認: 切った GIF は DecodeAll では読めない。
	_, err := gif.DecodeAll(bytes.NewReader(truncated))
	require.Error(t, err, "この前提が崩れるとテストが空虚になる")
	// imaging 経由も同じく失敗する = 差が観測できる。
	_, err = imaging.Decode(bytes.NewReader(truncated), imaging.AutoOrientation(true))
	require.Error(t, err, "imaging が全コマを読まなくなったならこのテストは作り直す")

	img, err := Decode(truncated)
	require.NoError(t, err, "2 コマ目まで読みに行っている")
	assert.Equal(t, 64, img.Bounds().Dx())
	assert.Equal(t, 64, img.Bounds().Dy())
}

// **普通の GIF はこれまでどおり読める。** これが無いと「GIF を常に拒否する」
// 実装でも上のテストが通る。返る像も imaging 経由と一致すること。
func TestDecode_OrdinaryGIFMatchesImaging(t *testing.T) {
	data := multiFrameGIF(t, 2, 32, 24)

	got, err := Decode(data)
	require.NoError(t, err)
	want, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	require.NoError(t, err)

	require.Equal(t, want.Bounds(), got.Bounds(), "経路によって Bounds が変わっている")
	for y := got.Bounds().Min.Y; y < got.Bounds().Max.Y; y++ {
		for x := got.Bounds().Min.X; x < got.Bounds().Max.X; x++ {
			if got.At(x, y) != want.At(x, y) {
				t.Fatalf("画素が一致しない at (%d,%d): got %v want %v", x, y, got.At(x, y), want.At(x, y))
			}
		}
	}
}

// 1 コマ目が論理画面よりずれた位置にある GIF でも、原点を揃えて返す。
//
// imaging の `SingleFrame()` は `NormalizeOrigin` を通すので、揃えないと
// **返る像の Bounds.Min が経路によって変わる**。
func TestDecode_GIFFrameOriginIsNormalized(t *testing.T) {
	pal := color.Palette{color.Black, color.White}
	f := image.NewPaletted(image.Rect(4, 6, 20, 22), pal)
	f.SetColorIndex(4, 6, 1)
	g := &gif.GIF{
		Config: image.Config{ColorModel: pal, Width: 40, Height: 40},
		Image:  []*image.Paletted{f},
		Delay:  []int{10},
	}
	var buf bytes.Buffer
	require.NoError(t, gif.EncodeAll(&buf, g))

	got, err := Decode(buf.Bytes())
	require.NoError(t, err)
	assert.Equal(t, image.Point{}, got.Bounds().Min, "原点が揃っていない")
	assert.Equal(t, 16, got.Bounds().Dx())
	assert.Equal(t, 16, got.Bounds().Dy())
}

// pngChunk renders one PNG chunk with its CRC.
func pngChunk(typ string, payload []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(payload)))
	b.WriteString(typ)
	b.Write(payload)
	crc := crc32.NewIEEE()
	crc.Write([]byte(typ))
	crc.Write(payload)
	_ = binary.Write(&b, binary.BigEndian, crc.Sum32())
	return b.Bytes()
}

// apngWithBrokenSecondFrame builds an APNG whose 2nd frame's fdAT is garbage.
//
// **CRC は正しく付ける。** stdlib は読み飛ばす ancillary チャンクでも CRC を
// 検証するので、壊すのは中身だけにする。
func apngWithBrokenSecondFrame(t *testing.T, w, h int) []byte {
	t.Helper()
	src := image.NewNRGBA(image.Rect(0, 0, w, h))
	src.Set(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, src))
	base := buf.Bytes()

	// IDAT の開始位置と IEND の開始位置を探す。
	idat := bytes.Index(base, []byte("IDAT"))
	iend := bytes.Index(base, []byte("IEND"))
	require.Positive(t, idat)
	require.Positive(t, iend)
	idatStart, iendStart := idat-4, iend-4

	fcTL := func(seq uint32) []byte {
		p := make([]byte, 26)
		binary.BigEndian.PutUint32(p[0:], seq)
		binary.BigEndian.PutUint32(p[4:], uint32(w))
		binary.BigEndian.PutUint32(p[8:], uint32(h))
		// x/y offset = 0、delay 1/10 秒、dispose/blend = 0。
		binary.BigEndian.PutUint16(p[20:], 1)
		binary.BigEndian.PutUint16(p[22:], 10)
		return p
	}
	acTL := make([]byte, 8)
	binary.BigEndian.PutUint32(acTL[0:], 2) // num_frames
	binary.BigEndian.PutUint32(acTL[4:], 0) // num_plays

	fdAT := make([]byte, 4+16)
	binary.BigEndian.PutUint32(fdAT[0:], 3) // sequence number
	copy(fdAT[4:], "not a zlib stream")     // **中身だけ壊す**

	var out bytes.Buffer
	out.Write(base[:idatStart])
	out.Write(pngChunk("acTL", acTL))
	out.Write(pngChunk("fcTL", fcTL(0)))
	out.Write(base[idatStart:iendStart])
	out.Write(pngChunk("fcTL", fcTL(2)))
	out.Write(pngChunk("fdAT", fdAT))
	out.Write(base[iendStart:])
	return out.Bytes()
}

func TestDecode_APNGReadsOnlyTheDefaultImage(t *testing.T) {
	data := apngWithBrokenSecondFrame(t, 48, 32)
	require.True(t, isAnimatedPNG(data), "acTL を検出できていない")

	// 前提の確認: 全コマを読む経路はここで失敗する。
	_, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	require.Error(t, err, "imaging が全コマを読まなくなったならこのテストは作り直す")

	img, err := Decode(data)
	require.NoError(t, err, "2 コマ目まで読みに行っている")
	assert.Equal(t, 48, img.Bounds().Dx())
	assert.Equal(t, 32, img.Bounds().Dy())
	r, g, b, a := img.At(0, 0).RGBA()
	assert.Equal(t, [4]uint32{10 * 257, 20 * 257, 30 * 257, 65535}, [4]uint32{r, g, b, a})
}

// **アニメーションでない PNG は stdlib 経路へ落とさない。** あちらは
// ICC→sRGB 変換を失うので、`acTL` の無い PNG まで回すと色が変わる。
func TestIsAnimatedPNG_OnlyMatchesACTL(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, src))

	assert.False(t, isAnimatedPNG(buf.Bytes()), "ただの PNG を APNG と判定している")
	assert.False(t, isAnimatedPNG([]byte("GIF89a....")), "PNG でない値")
	assert.False(t, isAnimatedPNG(nil))
	assert.True(t, isAnimatedPNG(apngWithBrokenSecondFrame(t, 8, 8)))
}

func TestIsGIF(t *testing.T) {
	assert.True(t, isGIF([]byte("GIF87a-rest")))
	assert.True(t, isGIF([]byte("GIF89a-rest")))
	assert.False(t, isGIF([]byte("GIF")))
	assert.False(t, isGIF([]byte("\x89PNG\r\n\x1a\n")))
	assert.False(t, isGIF(nil))
}

// exifOrientationPNG builds a non-square PNG carrying an `eXIf` chunk that
// declares orientation 6 (90 度回転)、optionally as an APNG.
//
// **imaging の pipeline を通ったかどうかを、色ではなく向きで見る。** ICC
// プロファイルを組むより軽く、通ったかどうかが Bounds に出るので判定が確実。
func exifOrientationPNG(t *testing.T, animated bool) []byte {
	t.Helper()
	src := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	src.Set(0, 0, color.NRGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, src))
	base := buf.Bytes()

	idat := bytes.Index(base, []byte("IDAT"))
	require.Positive(t, idat)
	idatStart := idat - 4

	// TIFF header + IFD 1 件 (tag 0x0112 Orientation / type SHORT / value 6)。
	exif := make([]byte, 0, 26)
	exif = append(exif, 'I', 'I', 0x2a, 0x00)
	exif = binary.LittleEndian.AppendUint32(exif, 8)
	exif = binary.LittleEndian.AppendUint16(exif, 1)
	exif = binary.LittleEndian.AppendUint16(exif, 0x0112)
	exif = binary.LittleEndian.AppendUint16(exif, 3)
	exif = binary.LittleEndian.AppendUint32(exif, 1)
	exif = binary.LittleEndian.AppendUint16(exif, 6)
	exif = append(exif, 0, 0)
	exif = binary.LittleEndian.AppendUint32(exif, 0)

	var out bytes.Buffer
	out.Write(base[:idatStart])
	out.Write(pngChunk("eXIf", exif))
	if animated {
		acTL := make([]byte, 8)
		binary.BigEndian.PutUint32(acTL[0:], 1)
		out.Write(pngChunk("acTL", acTL))
		fcTL := make([]byte, 26)
		binary.BigEndian.PutUint32(fcTL[4:], 8)
		binary.BigEndian.PutUint32(fcTL[8:], 4)
		binary.BigEndian.PutUint16(fcTL[20:], 1)
		binary.BigEndian.PutUint16(fcTL[22:], 10)
		out.Write(pngChunk("fcTL", fcTL))
	}
	out.Write(base[idatStart:])
	return out.Bytes()
}

// **APNG でも imaging の pipeline を通すこと。**
//
// 1 稿目は `png.Decode` で 1 コマ目を取っていたが、それだと EXIF の向き補正も
// **ICC / CICP → sRGB 変換も落ちる** (敵対的レビューで Display P3 の APNG が
// R チャンネル 32/255 ずれることを実測された)。カスタム絵文字は APNG が
// よく使われるので、静止画化のたびに色や向きが変わるのは受け入れられない。
//
// いまは `acTL` / `fcTL` / `fdAT` だけ落として静止 PNG として imaging へ渡す
// ので、フレームの増幅は起きないまま扱いが普通の PNG と揃う。
func TestDecode_APNGKeepsTheImagingPipeline(t *testing.T) {
	still := exifOrientationPNG(t, false)
	anim := exifOrientationPNG(t, true)
	require.False(t, isAnimatedPNG(still))
	require.True(t, isAnimatedPNG(anim))

	want, err := imaging.Decode(bytes.NewReader(still), imaging.AutoOrientation(true))
	require.NoError(t, err)
	got, err := Decode(anim)
	require.NoError(t, err)
	assert.Equal(t, want.Bounds(), got.Bounds(), "APNG だけ向き補正が落ちている")

	// **補正が実際に走っていること**まで見る。これが無いと「どちらも無補正」でも
	// テストが通り、回帰を検出できない。8x4 が 4x8 になる。
	raw, err := png.Decode(bytes.NewReader(still))
	require.NoError(t, err)
	assert.NotEqual(t, raw.Bounds(), want.Bounds(),
		"eXIf の向き補正が走っていない (この細工では経路を踏めていない)")
	assert.Equal(t, 4, got.Bounds().Dx())
	assert.Equal(t, 8, got.Bounds().Dy())
}

// アニメーション用チャンクだけを落とすこと。
func TestStripAPNGAnimation(t *testing.T) {
	anim := exifOrientationPNG(t, true)
	still := stripAPNGAnimation(anim)
	require.NotNil(t, still)

	assert.False(t, bytes.Contains(still, []byte("acTL")), "acTL が残っている")
	assert.False(t, bytes.Contains(still, []byte("fcTL")), "fcTL が残っている")
	assert.True(t, bytes.Contains(still, []byte("eXIf")), "eXIf まで落としている")
	assert.True(t, bytes.Contains(still, []byte("IDAT")))
	assert.True(t, bytes.Contains(still, []byte("IEND")))

	// アニメーションでない PNG は書き換えない (nil を返して呼び出し側に任せる)。
	assert.Nil(t, stripAPNGAnimation(exifOrientationPNG(t, false)))
	// 壊れた入力でも panic せず nil。
	assert.NotPanics(t, func() {
		assert.Nil(t, stripAPNGAnimation([]byte("\x89PNG\r\n\x1a\n\x00\x00")))
	})
}
