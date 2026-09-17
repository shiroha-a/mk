// Package imagedecode centralises the image decoding rules shared by the media
// proxy and the drive image processor.
//
// **2 箇所で同じ decode をしているので、規則はここに 1 つだけ置く (#2925)。**
// `internal/core/mediaproxy` は `internal/core/drive` を import しているため
// 逆向きには依存できず、片方だけ直すと**もう片方に同じバグが残る**。実際
// #2925 の初版がそれで、リモート URL の直 fetch だけが直り、ローカルに
// アップロードされた画像は真っ黒なサムネイルを storage に焼いたままだった。
package imagedecode

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"

	"github.com/kovidgoyal/imaging"
)

// ErrTooManyPixels reports that the header declares more pixels than
// MaxPixels, so the image was refused **before** allocating its raster.
var ErrTooManyPixels = errors.New("imagedecode: declared dimensions exceed the pixel cap")

// MaxPixels caps width*height as declared by the image header.
//
// **デコードの前に見るのが要点。** デコーダはヘッダの寸法だけでラスタを確保する
// ので、デコード後に測っても遅い。BMP は 54 バイトのヘッダで 8.6GB を要求でき、
// PNG は zlib の圧縮率 1032:1 で 12MB から 65535x65535 (NRGBA 17GB) を作れる。
// **Go の大確保失敗は `throw("out of memory")` で recover できない**ので、
// echo の Recover ミドルウェアは効かずプロセスごと落ちる。
//
// 値は mediaproxy が従来デコード後に使っていた cap (8192x8192 = 64MP) と同じ。
// 変えていないのは**判定の位置だけ**で、通る画像の集合は変わらない。
var MaxPixels int64 = 8192 * 8192

// Decode decodes image bytes, working around a decoder bug for a narrow class
// of PNG.
//
// 既定は `imaging.Decode` — EXIF の向きを補正し、ICC/CICP を sRGB へ変換し、
// `import _` で登録済みの webp/bmp/tiff も読める。
func Decode(data []byte) (image.Image, error) {
	// **ラスタを確保する前にヘッダの寸法を見る。** `image.DecodeConfig` は
	// 登録済みデコーダのヘッダだけを読むので、ここで弾けば巨大な確保が起きない。
	//
	// **読めなかったら通す。** TGA のように magic bytes を持たない形式や、
	// `image.RegisterFormat` されていない形式はここで判定できない。判定できない
	// ことを理由に拒否すると、これまで読めていた画像が読めなくなる (この変更は
	// 通る集合を変えないという前提で入れている)。その場合は従来どおり
	// デコード後の cap が受け止める。
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		if int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
			return nil, fmt.Errorf("%w: %dx%d", ErrTooManyPixels, cfg.Width, cfg.Height)
		}
	}
	if IsBrokenInterlacedPNG(data) {
		// **stdlib で読む。** 下記の条件の PNG は imaging が全画素 0 にする。
		// **EXIF の向きと ICC→sRGB 変換は失われる**が、代わりに得られるのは
		// 「真っ黒な画像」なので、そちらの方がましという判断。
		return png.Decode(bytes.NewReader(data))
	}
	return imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
}

// IsBrokenInterlacedPNG reports whether data is a PNG that
// `kovidgoyal/imaging` v1.8.21 decodes to all-zero pixels **without returning
// an error**.
//
// **条件は 3 つすべて。** colorType=2 (truecolor) / bit depth=8 / tRNS 無し、
// かつインターレース (Adam7)。このときだけ imaging は独自の `*nrgb.Image` を
// 選ぶ (`apng/reader.go` の `cbTC8 && !useTransparent`) が、その Adam7 の
// pass 合成に `*nrgb.Image` の case が無く、stride が 0 のまま no-op copy に
// なる。gray / gray+alpha / palette / RGBA / 16bit RGB / tRNS 付き truecolor は
// いずれも別の型になり正しく読める (34 通りの組み合わせで実測)。
//
// **条件を広げてはいけない。** インターレースだけで判定すると、壊れていない
// 種別まで stdlib へ回して ICC→sRGB 変換を落とす (Display P3 の PNG で
// チャンネル差が最大 31/255 出る、実測)。
func IsBrokenInterlacedPNG(data []byte) bool {
	const sig = "\x89PNG\r\n\x1a\n"
	// signature 8 + length 4 + type 4 + IHDR 13 = 29 バイト。
	// IHDR は bitDepth(24) / colorType(25) / interlace(28)。
	if len(data) < 29 || string(data[:8]) != sig || string(data[12:16]) != "IHDR" {
		return false
	}
	if data[24] != 8 || data[25] != 2 || data[28] != 1 {
		return false
	}
	return !hasChunk(data, "tRNS")
}

// hasChunk walks the PNG chunk list looking for typ. **長さを信用して読み飛ばす
// のではなく、毎回残りバイト数を確かめる** — 細工した length で範囲外を読まない
// ようにするため。
func hasChunk(data []byte, typ string) bool {
	pos := 8 // signature の後ろから
	for {
		// length 4 + type 4 + data + CRC 4
		if pos+8 > len(data) {
			return false
		}
		// **`length < 0` の検査は要らない。** byte を 24 bit 左シフトしても
		// int (64bit) では正のままなので到達しない。範囲外は下の
		// `next > len(data)` で止める。
		length := int(data[pos])<<24 | int(data[pos+1])<<16 | int(data[pos+2])<<8 | int(data[pos+3])
		if string(data[pos+4:pos+8]) == typ {
			return true
		}
		if string(data[pos+4:pos+8]) == "IDAT" {
			// tRNS は IDAT より前に来る (PNG 仕様)。以降は読む必要が無い。
			return false
		}
		next := pos + 12 + length
		if next <= pos || next > len(data) {
			return false
		}
		pos = next
	}
}
