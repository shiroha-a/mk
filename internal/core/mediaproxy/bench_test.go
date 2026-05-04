package mediaproxy

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/gen2brain/avif"
	"github.com/gen2brain/webp"
	jpeg2000 "github.com/mrjoshuak/go-jpeg2000"
)

// #735 govips PoC: decode/encode/resize の現行 (gen2brain/* + std) 性能を
// 計測する benchmark suite。govips との直接比較は行わない (cgo 必須で
// project の static binary 方針 #618-620 と衝突するため。詳細は
// docs/mediaproxy-govips-evaluation.md)。
//
// 実行: go test -bench=. -benchmem -benchtime=3s ./internal/core/mediaproxy/...
//
// 計測ターゲット:
//   - Decode: JPEG / PNG / GIF / WebP / AVIF / JP2
//   - Encode: WebP / AVIF (proxy 出力 path)
//   - End-to-end: emoji (128x128) / avatar (320x320) リサイズフロー

const (
	benchEmojiSize  = 128 // ModeEmoji 出力高さ
	benchAvatarSize = 320 // ModeAvatar 出力高さ
	benchSourceSize = 1024
)

// makeBenchSrc は decode/encode 計測用の RGB 1024x1024 画像を生成する。
// pattern が単調にならないよう色相をピクセル位置で変える。
func makeBenchSrc(b *testing.B, w, h int) image.Image {
	b.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{
				R: uint8(x % 256),
				G: uint8(y % 256),
				B: uint8((x + y) % 256),
				A: 255,
			})
		}
	}
	return img
}

func makeBenchJPEG(b *testing.B) []byte {
	b.Helper()
	img := makeBenchSrc(b, benchSourceSize, benchSourceSize)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		b.Fatalf("encode JPEG: %v", err)
	}
	return buf.Bytes()
}

func makeBenchPNG(b *testing.B) []byte {
	b.Helper()
	img := makeBenchSrc(b, benchSourceSize, benchSourceSize)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		b.Fatalf("encode PNG: %v", err)
	}
	return buf.Bytes()
}

func makeBenchGIF(b *testing.B) []byte {
	b.Helper()
	img := makeBenchSrc(b, benchSourceSize, benchSourceSize)
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		b.Fatalf("encode GIF: %v", err)
	}
	return buf.Bytes()
}

func makeBenchWebP(b *testing.B) []byte {
	b.Helper()
	img := makeBenchSrc(b, benchSourceSize, benchSourceSize)
	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, webp.Options{Quality: 80}); err != nil {
		b.Fatalf("encode WebP: %v", err)
	}
	return buf.Bytes()
}

func makeBenchAVIF(b *testing.B) []byte {
	b.Helper()
	img := makeBenchSrc(b, benchSourceSize, benchSourceSize)
	var buf bytes.Buffer
	if err := avif.Encode(&buf, img); err != nil {
		b.Fatalf("encode AVIF: %v", err)
	}
	return buf.Bytes()
}

func makeBenchJP2(b *testing.B) []byte {
	b.Helper()
	img := makeBenchSrc(b, benchSourceSize, benchSourceSize)
	var buf bytes.Buffer
	if err := jpeg2000.Encode(&buf, img, nil); err != nil {
		b.Fatalf("encode JP2: %v", err)
	}
	return buf.Bytes()
}

// --- Decode benchmarks ---

func BenchmarkDecode_JPEG(b *testing.B) {
	data := makeBenchJPEG(b)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if _, err := decodeImage(data, "image/jpeg"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecode_PNG(b *testing.B) {
	data := makeBenchPNG(b)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if _, err := decodeImage(data, "image/png"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecode_GIF(b *testing.B) {
	data := makeBenchGIF(b)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if _, err := decodeImage(data, "image/gif"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecode_WebP(b *testing.B) {
	data := makeBenchWebP(b)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if _, err := decodeImage(data, "image/webp"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecode_AVIF(b *testing.B) {
	data := makeBenchAVIF(b)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if _, err := decodeImage(data, "image/avif"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecode_JP2(b *testing.B) {
	data := makeBenchJP2(b)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if _, err := decodeImage(data, "image/jp2"); err != nil {
			b.Fatal(err)
		}
	}
}

// --- Encode benchmarks (output path) ---

func BenchmarkEncode_WebP(b *testing.B) {
	img := makeBenchSrc(b, benchAvatarSize, benchAvatarSize) // typical avatar size
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if _, err := encodeWebP(img); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncode_AVIF(b *testing.B) {
	img := makeBenchSrc(b, benchAvatarSize, benchAvatarSize)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if _, err := encodeAVIF(img); err != nil {
			b.Fatal(err)
		}
	}
}

// --- End-to-end resize benchmarks (decode → resize → encode WebP) ---

func benchE2EResize(b *testing.B, data []byte, contentType string, height int) {
	b.Helper()
	s := testService(nil)
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		result, err := s.processResize(data, contentType, 0, height, FormatWebP)
		if err != nil {
			b.Fatal(err)
		}
		_ = result.Body.Close()
	}
}

func BenchmarkE2E_JPEGToWebP_Emoji(b *testing.B) {
	benchE2EResize(b, makeBenchJPEG(b), "image/jpeg", benchEmojiSize)
}

func BenchmarkE2E_JPEGToWebP_Avatar(b *testing.B) {
	benchE2EResize(b, makeBenchJPEG(b), "image/jpeg", benchAvatarSize)
}

func BenchmarkE2E_PNGToWebP_Avatar(b *testing.B) {
	benchE2EResize(b, makeBenchPNG(b), "image/png", benchAvatarSize)
}

func BenchmarkE2E_WebPToWebP_Avatar(b *testing.B) {
	benchE2EResize(b, makeBenchWebP(b), "image/webp", benchAvatarSize)
}

func BenchmarkE2E_JP2ToWebP_Avatar(b *testing.B) {
	benchE2EResize(b, makeBenchJP2(b), "image/jp2", benchAvatarSize)
}
