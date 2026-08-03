package drive

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"strings"

	"github.com/bbrks/go-blurhash"
	"github.com/gen2brain/webp"
	"github.com/kovidgoyal/imaging"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// ProcessedImage holds the result of image transformation.
type ProcessedImage struct {
	Data     []byte
	MimeType string // e.g. "image/webp", "image/png"
}

// ImageProcessor handles thumbnail/webpublic generation and metadata
// extraction. テスト時はモックに差し替え可能。
type ImageProcessor interface {
	// GenerateThumbnail creates a thumbnail from the input image bytes.
	// Returns nil, nil if the input format is not supported.
	GenerateThumbnail(body []byte, mimeType string) (*ProcessedImage, error)

	// GenerateWebpublic creates a web-optimised version if needed.
	// Returns nil, nil if webpublic is not required.
	GenerateWebpublic(body []byte, mimeType string) (*ProcessedImage, error)

	// GetDimensions returns the pixel width and height of the image.
	GetDimensions(body []byte, mimeType string) (width, height int, err error)

	// CalculateBlurhash computes the BlurHash string for the image.
	CalculateBlurhash(body []byte, mimeType string) (string, error)
}

// VideoProcessor extracts thumbnails from video files.
type VideoProcessor interface {
	// GenerateThumbnail extracts a frame from the video and returns a
	// WebP thumbnail. Returns nil, nil if extraction fails or FFmpeg is
	// unavailable.
	GenerateThumbnail(body []byte, mimeType string) (*ProcessedImage, error)
}

// Thumbnail / webpublic 生成パラメータ (Misskey TS 準拠)
const (
	thumbnailWidth  = 498
	thumbnailHeight = 422
	animThumbWidth  = 374
	animThumbHeight = 317
	webpublicMax    = 2048
	webpQuality     = 77
	blurhashSize    = 64
	blurhashXComp   = 5
	blurhashYComp   = 5
)

// isMimeImage returns true if the MIME type is a supported image format for
// thumbnail/webpublic generation.
func isMimeImage(mime string) bool {
	switch mime {
	case "image/jpeg", "image/png", "image/gif",
		"image/webp", "image/bmp", "image/tiff",
		// IANA 公式名 (image/vnd.microsoft.icon) と古い慣例 (image/x-icon)
		// を両方許可。mediaproxy 側 isConvertibleImage と同じ alias を扱う
		// (#418)。
		"image/x-icon", "image/vnd.microsoft.icon",
		// APNG は 2 綴りある。判定器が改善されて `image/apng` を返すように
		// なった (#2319) ので両方受ける。片方だけだとサムネイルが生成されなく
		// なる (APNG の 1 フレーム目は valid な PNG なので stdlib で decode できる)。
		"image/vnd.mozilla.apng", "image/apng":
		return true
	default:
		return false
	}
}

// isMimeVideo returns true if the MIME type represents a video format.
func isMimeVideo(mime string) bool {
	return strings.HasPrefix(mime, "video/")
}

// isAnimatedMime returns true if the MIME type is an animated image format
// (GIF or APNG). アニメーション画像はより小さいサムネイルを生成する。
func isAnimatedMime(mime string) bool {
	return mime == "image/gif" || mime == "image/vnd.mozilla.apng" || mime == "image/apng"
}

// ---------------------------------------------------------------------------
// DefaultImageProcessor — pure Go + gen2brain/webp (libwebp on wazero/WASM)
// ---------------------------------------------------------------------------

// DefaultImageProcessor implements ImageProcessor using pure Go libraries
// (disintegration/imaging) and gen2brain/webp for WebP encoding.
//
// gen2brain/webp は libwebp を WASM 化して wazero で実行するため cgo-free。
// Quality は int (0-100)、ただし 100 は lossless 扱いになる罠あり。本パッケージ
// は webpQuality = 77 固定なのでこの罠には踏み込まないが、定数を変える際は注意。
type DefaultImageProcessor struct{}

// NewDefaultImageProcessor creates a new DefaultImageProcessor.
func NewDefaultImageProcessor() *DefaultImageProcessor {
	return &DefaultImageProcessor{}
}

// decodeImage decodes image bytes into an image.Image. Auto-orients JPEG
// images using EXIF orientation data. golang.org/x/image の webp/bmp/tiff
// デコーダは import _ で init 登録済み。
func decodeImage(body []byte, mimeType string) (image.Image, error) {
	// imaging.Decode は EXIF orientation を自動補正し、
	// import _ で登録済みの webp/bmp/tiff も処理できる。
	img, err := imaging.Decode(bytes.NewReader(body), imaging.AutoOrientation(true))
	if err != nil {
		return nil, fmt.Errorf("unsupported image format: %s: %w", mimeType, err)
	}
	return img, nil
}

// webpEncoderFunc / pngEncoderFunc はテスト時に差し替え可能なエンコーダ。
var webpEncoderFunc = defaultEncodeWebP
var pngEncoderFunc = defaultEncodePNG

func defaultEncodeWebP(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, webp.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func defaultEncodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	// bytes.Buffer.Write は常に成功するため png.Encode はエラーを返さない
	_ = png.Encode(&buf, img)
	return buf.Bytes(), nil
}

// encodeWebP encodes an image.Image to WebP bytes with the given quality.
func encodeWebP(img image.Image, quality int) ([]byte, error) {
	return webpEncoderFunc(img, quality)
}

// encodePNG encodes an image.Image to PNG bytes.
func encodePNG(img image.Image) ([]byte, error) {
	return pngEncoderFunc(img)
}

// resizeFit resizes img to fit within maxW x maxH while preserving aspect
// ratio. 元画像が maxW x maxH 以下の場合は拡大しない。
func resizeFit(img image.Image, maxW, maxH int) image.Image {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= maxW && h <= maxH {
		return img
	}
	return imaging.Fit(img, maxW, maxH, imaging.Lanczos)
}

func (p *DefaultImageProcessor) GenerateThumbnail(body []byte, mimeType string) (*ProcessedImage, error) {
	if !isMimeImage(mimeType) {
		return nil, nil
	}
	img, err := decodeImage(body, mimeType)
	if err != nil {
		return nil, nil // デコード失敗はサムネイルなしで続行
	}

	tw, th := thumbnailWidth, thumbnailHeight
	if isAnimatedMime(mimeType) {
		tw, th = animThumbWidth, animThumbHeight
	}

	thumb := resizeFit(img, tw, th)
	data, err := encodeWebP(thumb, webpQuality)
	if err != nil {
		return nil, err
	}
	return &ProcessedImage{Data: data, MimeType: "image/webp"}, nil
}

func (p *DefaultImageProcessor) GenerateWebpublic(body []byte, mimeType string) (*ProcessedImage, error) {
	if !isMimeImage(mimeType) {
		return nil, nil
	}
	img, err := decodeImage(body, mimeType)
	if err != nil {
		return nil, nil
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	hasExif := hasExifMarker(body)

	// SVG / AVIF は現在未対応なのでここには来ない (isMimeImage で弾かれる)
	// webpublic 不要の条件: メタデータなし & 2048以下 & SVG/AVIFでない
	if !hasExif && w <= webpublicMax && h <= webpublicMax {
		return nil, nil
	}

	resized := resizeFit(img, webpublicMax, webpublicMax)

	// PNG は透過を維持するため PNG で出力、それ以外は WebP
	if mimeType == "image/png" {
		data, err := encodePNG(resized)
		if err != nil {
			return nil, err
		}
		return &ProcessedImage{Data: data, MimeType: "image/png"}, nil
	}

	data, err := encodeWebP(resized, webpQuality)
	if err != nil {
		return nil, err
	}
	return &ProcessedImage{Data: data, MimeType: "image/webp"}, nil
}

func (p *DefaultImageProcessor) GetDimensions(body []byte, mimeType string) (int, int, error) {
	if !isMimeImage(mimeType) {
		return 0, 0, fmt.Errorf("not an image: %s", mimeType)
	}
	img, err := decodeImage(body, mimeType)
	if err != nil {
		return 0, 0, err
	}
	bounds := img.Bounds()
	return bounds.Dx(), bounds.Dy(), nil
}

func (p *DefaultImageProcessor) CalculateBlurhash(body []byte, mimeType string) (string, error) {
	if !isMimeImage(mimeType) {
		return "", fmt.Errorf("not an image: %s", mimeType)
	}
	img, err := decodeImage(body, mimeType)
	if err != nil {
		return "", err
	}

	// BlurHash 用に 64x64 にリサイズ
	small := imaging.Fit(img, blurhashSize, blurhashSize, imaging.Lanczos)

	// go-blurhash は image.Image を受け取る。NRGBA に変換しておく。
	bounds := small.Bounds()
	nrgba := image.NewNRGBA(bounds)
	draw.Draw(nrgba, bounds, small, bounds.Min, draw.Src)

	// blurhash.Encode は xComp/yComp が 1-9 の範囲内であれば常に成功する
	hash, _ := blurhash.Encode(blurhashXComp, blurhashYComp, nrgba)
	return hash, nil
}

// hasExifMarker checks if the image bytes contain an EXIF marker.
// JPEG の APP1 (0xFF 0xE1) + "Exif\0\0" パターンを検索する。
func hasExifMarker(body []byte) bool {
	if len(body) < 12 {
		return false
	}
	// JPEG 先頭の SOI マーカー確認
	if body[0] != 0xFF || body[1] != 0xD8 {
		return false // JPEG でなければ EXIF なしとみなす
	}
	// APP1 マーカー + Exif ヘッダを探す (先頭 64KB 以内)
	limit := min(len(body), 65536)
	exifSig := []byte{0x45, 0x78, 0x69, 0x66, 0x00, 0x00} // "Exif\0\0"
	return bytes.Contains(body[:limit], exifSig)
}
