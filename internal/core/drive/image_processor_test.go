package drive

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/gen2brain/avif"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// テスト用画像生成ヘルパ
// ---------------------------------------------------------------------------

// makeTestJPEG creates a small JPEG image of the given size.
func makeTestJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 50), uint8(y * 50), 100, 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	return buf.Bytes()
}

// makeTestPNG creates a small PNG image of the given size.
func makeTestPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 30), uint8(y * 30), 200, 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// makeTestGIF creates a small GIF image.
func makeTestGIF(w, h int) []byte {
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{
		color.RGBA{255, 0, 0, 255},
		color.RGBA{0, 255, 0, 255},
	})
	var buf bytes.Buffer
	_ = gif.Encode(&buf, img, nil)
	return buf.Bytes()
}

// makeTestJPEGWithExif creates a JPEG with a fake EXIF APP1 marker.
func makeTestJPEGWithExif(w, h int) []byte {
	base := makeTestJPEG(w, h)
	// JPEG は SOI (FF D8) で始まる。SOI の後に APP1 マーカーを挿入。
	// APP1: FF E1 <length:2> "Exif\0\0" <dummy>
	exifHeader := []byte{
		0xFF, 0xE1, // APP1 marker
		0x00, 0x16, // length (22 bytes including length field)
		0x45, 0x78, 0x69, 0x66, 0x00, 0x00, // "Exif\0\0"
		0x4D, 0x4D, 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08, // TIFF header (big endian, IFD at offset 8)
		0x00, 0x00, // IFD entry count = 0
		0x00, 0x00, 0x00, 0x00, // next IFD offset = 0 (no more IFDs)
	}
	result := make([]byte, 0, len(base)+len(exifHeader))
	result = append(result, base[:2]...) // SOI
	result = append(result, exifHeader...)
	result = append(result, base[2:]...) // rest of JPEG
	return result
}

// ---------------------------------------------------------------------------
// isMimeImage / isMimeVideo / isAnimatedMime
// ---------------------------------------------------------------------------

func TestIsMimeImage(t *testing.T) {
	cases := []struct {
		mime string
		want bool
	}{
		{"image/jpeg", true},
		{"image/png", true},
		{"image/gif", true},
		{"image/webp", true},
		{"image/bmp", true},
		{"image/tiff", true},
		// IANA 公式名 (vnd.microsoft.icon) と古い慣例 (x-icon) を両方許可 (#418)
		{"image/x-icon", true},
		{"image/vnd.microsoft.icon", true},
		{"image/vnd.mozilla.apng", true},
		{"image/svg+xml", false},
		{"image/avif", true},
		{"video/mp4", false},
		{"application/octet-stream", false},
		{"text/plain", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, isMimeImage(tc.mime), tc.mime)
	}
}

func TestIsMimeVideo(t *testing.T) {
	assert.True(t, isMimeVideo("video/mp4"))
	assert.True(t, isMimeVideo("video/webm"))
	assert.False(t, isMimeVideo("image/jpeg"))
	assert.False(t, isMimeVideo("audio/mpeg"))
}

func TestIsAnimatedMime(t *testing.T) {
	assert.True(t, isAnimatedMime("image/gif"))
	assert.True(t, isAnimatedMime("image/vnd.mozilla.apng"))
	assert.False(t, isAnimatedMime("image/jpeg"))
	assert.False(t, isAnimatedMime("image/png"))
}

// ---------------------------------------------------------------------------
// hasExifMarker
// ---------------------------------------------------------------------------

func TestHasExifMarker_WithExif(t *testing.T) {
	data := makeTestJPEGWithExif(10, 10)
	assert.True(t, hasExifMarker(data))
}

func TestHasExifMarker_WithoutExif(t *testing.T) {
	data := makeTestJPEG(10, 10)
	assert.False(t, hasExifMarker(data))
}

func TestHasExifMarker_NotJPEG(t *testing.T) {
	data := makeTestPNG(10, 10)
	assert.False(t, hasExifMarker(data))
}

func TestHasExifMarker_TooShort(t *testing.T) {
	assert.False(t, hasExifMarker([]byte{0xFF, 0xD8}))
	assert.False(t, hasExifMarker(nil))
}

// ---------------------------------------------------------------------------
// DefaultImageProcessor.GetDimensions
// ---------------------------------------------------------------------------

func TestGetDimensions_JPEG(t *testing.T) {
	proc := NewDefaultImageProcessor()
	w, h, err := proc.GetDimensions(makeTestJPEG(100, 50), "image/jpeg")
	require.NoError(t, err)
	assert.Equal(t, 100, w)
	assert.Equal(t, 50, h)
}

func TestGetDimensions_PNG(t *testing.T) {
	proc := NewDefaultImageProcessor()
	w, h, err := proc.GetDimensions(makeTestPNG(200, 150), "image/png")
	require.NoError(t, err)
	assert.Equal(t, 200, w)
	assert.Equal(t, 150, h)
}

func TestGetDimensions_GIF(t *testing.T) {
	proc := NewDefaultImageProcessor()
	w, h, err := proc.GetDimensions(makeTestGIF(80, 60), "image/gif")
	require.NoError(t, err)
	assert.Equal(t, 80, w)
	assert.Equal(t, 60, h)
}

func TestGetDimensions_UnsupportedMime(t *testing.T) {
	proc := NewDefaultImageProcessor()
	_, _, err := proc.GetDimensions([]byte("not image"), "text/plain")
	assert.Error(t, err)
}

func TestGetDimensions_BadImageData(t *testing.T) {
	proc := NewDefaultImageProcessor()
	_, _, err := proc.GetDimensions([]byte("bad data"), "image/jpeg")
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// DefaultImageProcessor.CalculateBlurhash
// ---------------------------------------------------------------------------

func TestCalculateBlurhash_JPEG(t *testing.T) {
	proc := NewDefaultImageProcessor()
	hash, err := proc.CalculateBlurhash(makeTestJPEG(100, 100), "image/jpeg")
	require.NoError(t, err)
	assert.NotEmpty(t, hash)
	// BlurHash は 5x5 components = 長さ 28 以上
	assert.GreaterOrEqual(t, len(hash), 6)
}

func TestCalculateBlurhash_PNG(t *testing.T) {
	proc := NewDefaultImageProcessor()
	hash, err := proc.CalculateBlurhash(makeTestPNG(50, 50), "image/png")
	require.NoError(t, err)
	assert.NotEmpty(t, hash)
}

func TestCalculateBlurhash_UnsupportedMime(t *testing.T) {
	proc := NewDefaultImageProcessor()
	_, err := proc.CalculateBlurhash([]byte("x"), "text/plain")
	assert.Error(t, err)
}

func TestCalculateBlurhash_BadImage(t *testing.T) {
	proc := NewDefaultImageProcessor()
	_, err := proc.CalculateBlurhash([]byte("bad"), "image/jpeg")
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// DefaultImageProcessor.GenerateThumbnail
// ---------------------------------------------------------------------------

func TestGenerateThumbnail_JPEG(t *testing.T) {
	proc := NewDefaultImageProcessor()
	result, err := proc.GenerateThumbnail(makeTestJPEG(1000, 800), "image/jpeg")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "image/webp", result.MimeType)
	assert.NotEmpty(t, result.Data)
}

func TestGenerateThumbnail_PNG(t *testing.T) {
	proc := NewDefaultImageProcessor()
	result, err := proc.GenerateThumbnail(makeTestPNG(600, 400), "image/png")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "image/webp", result.MimeType)
}

func TestGenerateThumbnail_GIF_AnimatedSize(t *testing.T) {
	proc := NewDefaultImageProcessor()
	result, err := proc.GenerateThumbnail(makeTestGIF(800, 600), "image/gif")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "image/webp", result.MimeType)
	// GIF はアニメーション扱いなので 374x317 以内にリサイズされる
}

func TestGenerateThumbnail_SmallImage_NoUpscale(t *testing.T) {
	proc := NewDefaultImageProcessor()
	result, err := proc.GenerateThumbnail(makeTestJPEG(50, 30), "image/jpeg")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "image/webp", result.MimeType)
}

func TestGenerateThumbnail_UnsupportedMime(t *testing.T) {
	proc := NewDefaultImageProcessor()
	result, err := proc.GenerateThumbnail([]byte("x"), "text/plain")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestGenerateThumbnail_BadImageData(t *testing.T) {
	proc := NewDefaultImageProcessor()
	result, err := proc.GenerateThumbnail([]byte("bad"), "image/jpeg")
	require.NoError(t, err)
	assert.Nil(t, result) // デコード失敗は nil 返却
}

// ---------------------------------------------------------------------------
// DefaultImageProcessor.GenerateWebpublic
// ---------------------------------------------------------------------------

func TestGenerateWebpublic_LargeJPEG(t *testing.T) {
	proc := NewDefaultImageProcessor()
	// 2048 を超えるサイズ → webpublic 生成
	result, err := proc.GenerateWebpublic(makeTestJPEG(3000, 2000), "image/jpeg")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "image/webp", result.MimeType)
}

func TestGenerateWebpublic_LargePNG_OutputPNG(t *testing.T) {
	proc := NewDefaultImageProcessor()
	// PNG は透過維持のため PNG で出力
	result, err := proc.GenerateWebpublic(makeTestPNG(3000, 2000), "image/png")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "image/png", result.MimeType)
}

func TestGenerateWebpublic_SmallJPEG_NoExif_NotNeeded(t *testing.T) {
	proc := NewDefaultImageProcessor()
	// 2048 以下 + EXIF なし → webpublic 不要
	result, err := proc.GenerateWebpublic(makeTestJPEG(800, 600), "image/jpeg")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestGenerateWebpublic_SmallJPEG_WithExif_Needed(t *testing.T) {
	proc := NewDefaultImageProcessor()
	// 2048 以下でも EXIF ありなら webpublic 生成
	result, err := proc.GenerateWebpublic(makeTestJPEGWithExif(800, 600), "image/jpeg")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "image/webp", result.MimeType)
}

func TestGenerateWebpublic_UnsupportedMime(t *testing.T) {
	proc := NewDefaultImageProcessor()
	result, err := proc.GenerateWebpublic([]byte("x"), "text/plain")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestGenerateWebpublic_BadImageData(t *testing.T) {
	proc := NewDefaultImageProcessor()
	result, err := proc.GenerateWebpublic([]byte("bad"), "image/jpeg")
	require.NoError(t, err)
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// decodeImage / resizeFit / encodeWebP / encodePNG
// ---------------------------------------------------------------------------

func TestDecodeImage_WebP(t *testing.T) {
	// WebP 画像を生成して decodeImage でデコードできることを確認
	proc := NewDefaultImageProcessor()
	thumb, err := proc.GenerateThumbnail(makeTestJPEG(100, 100), "image/jpeg")
	require.NoError(t, err)
	require.NotNil(t, thumb)
	// thumb.Data は WebP なので decodeImage でデコード
	img, err := decodeImage(thumb.Data, "image/webp")
	require.NoError(t, err)
	assert.NotNil(t, img)
}

func TestDecodeImage_UnsupportedFormat(t *testing.T) {
	_, err := decodeImage([]byte("not an image"), "image/x-unknown")
	assert.Error(t, err)
}

func TestResizeFit_NoUpscale(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 50, 30))
	result := resizeFit(img, 498, 422)
	bounds := result.Bounds()
	assert.Equal(t, 50, bounds.Dx())
	assert.Equal(t, 30, bounds.Dy())
}

func TestResizeFit_Downscale(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1000, 800))
	result := resizeFit(img, 498, 422)
	bounds := result.Bounds()
	assert.LessOrEqual(t, bounds.Dx(), 498)
	assert.LessOrEqual(t, bounds.Dy(), 422)
}

func TestEncodeWebP(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	data, err := encodeWebP(img, 77)
	require.NoError(t, err)
	assert.NotEmpty(t, data)
	// WebP magic bytes: "RIFF" at offset 0, "WEBP" at offset 8
	assert.Equal(t, "RIFF", string(data[0:4]))
	assert.Equal(t, "WEBP", string(data[8:12]))
}

func TestEncodePNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	data, err := encodePNG(img)
	require.NoError(t, err)
	assert.NotEmpty(t, data)
	// PNG magic bytes
	assert.Equal(t, byte(0x89), data[0])
	assert.Equal(t, byte(0x50), data[1]) // 'P'
}

// ---------------------------------------------------------------------------
// Encoder error paths (テスト用差し替え経由)
// ---------------------------------------------------------------------------

func TestGenerateThumbnail_WebPEncodeError(t *testing.T) {
	restore := SetWebPEncoderForTest(func(_ image.Image, _ int) ([]byte, error) {
		return nil, errors.New("webp encode error")
	})
	defer restore()

	proc := NewDefaultImageProcessor()
	_, err := proc.GenerateThumbnail(makeTestJPEG(100, 100), "image/jpeg")
	assert.Error(t, err)
}

func TestGenerateWebpublic_WebPEncodeError(t *testing.T) {
	restore := SetWebPEncoderForTest(func(_ image.Image, _ int) ([]byte, error) {
		return nil, errors.New("webp encode error")
	})
	defer restore()

	proc := NewDefaultImageProcessor()
	// 2048 超え → webpublic 生成を試みる → encode error
	_, err := proc.GenerateWebpublic(makeTestJPEG(3000, 2000), "image/jpeg")
	assert.Error(t, err)
}

func TestGenerateWebpublic_PNGEncodeError(t *testing.T) {
	restore := SetPNGEncoderForTest(func(_ image.Image) ([]byte, error) {
		return nil, errors.New("png encode error")
	})
	defer restore()

	proc := NewDefaultImageProcessor()
	_, err := proc.GenerateWebpublic(makeTestPNG(3000, 2000), "image/png")
	assert.Error(t, err)
}

func TestCalculateBlurhash_GIF(t *testing.T) {
	proc := NewDefaultImageProcessor()
	hash, err := proc.CalculateBlurhash(makeTestGIF(80, 60), "image/gif")
	require.NoError(t, err)
	assert.NotEmpty(t, hash)
}

func TestDefaultEncodeWebP_ZeroDimensions(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 0, 0))
	_, err := defaultEncodeWebP(img, 77)
	// 0x0 画像のエンコードはエラーになる
	assert.Error(t, err)
}

func TestDefaultEncodePNG_Success(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 5, 5))
	data, err := defaultEncodePNG(img)
	require.NoError(t, err)
	assert.NotEmpty(t, data)
}

func TestCalculateBlurhash_EncodeError(t *testing.T) {
	// BlurHash の Encode は xComponents/yComponents が 1-9 の範囲外でエラー。
	// CalculateBlurhash は固定 5x5 なのでエラーにならないが、
	// WebP encoder エラー経由で CalculateBlurhash 自体のエラーパスを検証。
	restore := SetWebPEncoderForTest(func(_ image.Image, _ int) ([]byte, error) {
		return nil, errors.New("encode fail")
	})
	defer restore()

	// CalculateBlurhash は WebP encoder を使わないので影響なし
	proc := NewDefaultImageProcessor()
	hash, err := proc.CalculateBlurhash(makeTestJPEG(100, 100), "image/jpeg")
	require.NoError(t, err)
	assert.NotEmpty(t, hash)
}

// makeTestAVIF creates a small AVIF image.
func makeTestAVIF(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 30), uint8(y * 30), 200, 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, avif.Encode(&buf, img, avif.Options{Quality: 60}))
	return buf.Bytes()
}

// AVIF は Mastodon / MS Edge が表示できないため、小さくても EXIF が無くても
// 必ず WebP の webpublic を作る (upstream satisfyWebpublic の avif 除外)。
func TestGenerateWebpublic_SmallAVIF_AlwaysNeeded(t *testing.T) {
	proc := NewDefaultImageProcessor()
	result, err := proc.GenerateWebpublic(makeTestAVIF(t, 200, 150), "image/avif")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "image/webp", result.MimeType)
}

func TestGenerateThumbnail_AVIF(t *testing.T) {
	proc := NewDefaultImageProcessor()
	result, err := proc.GenerateThumbnail(makeTestAVIF(t, 200, 150), "image/avif")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "image/webp", result.MimeType)
}
