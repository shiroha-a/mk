package imagedecode

import (
	"bytes"
	"fmt"
	"image"

	"github.com/blezek/tga"
)

// DecodeTGAWithPixelCap decodes a TGA image, refusing it before the raster is
// allocated when the header declares more pixels than maxPixels.
//
// **TGA だけは `image.DecodeConfig` では判定できない。** magic bytes を持たない
// ので `image.RegisterFormat` の自動判別に載せられず (載せると他形式の判定を
// 壊す)、MIME で明示 dispatch する必要がある。`DecodeWithPixelCap` の
// 「ヘッダを読めなかったら通す」枝に落ちるため、**そこを通すだけでは cap が
// 効かない**。
//
// blezek/tga は 18 バイトのヘッダの width/height をそのまま使って
// `image.NewNRGBA` を確保し、**寸法を一切検証しない**。寸法は uint16 なので
// 最大 65535x65535 = 16 GiB の単一確保になる。Go の大確保失敗は
// `throw("out of memory")` で recover 不能なのでプロセスごと落ちる。
//
// ここでは同 package の `DecodeConfig` (ヘッダだけを読み、ラスタを確保しない)
// で寸法を先に取り、cap を通してから本体をデコードする。
func DecodeTGAWithPixelCap(data []byte, maxPixels int64) (image.Image, error) {
	cfg, err := tga.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("tga: decode config: %w", err)
	}
	// **`DecodeConfig` は 0 寸法をエラーにしない** (実測: 0x0 / 16x0 いずれも
	// err=nil で返る)。そのまま進めても `Decode` 側が失敗するが、cap の判定を
	// 「寸法が妥当である」前提で書けるようにここで落とす。
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("tga: invalid dimensions %dx%d", cfg.Width, cfg.Height)
	}
	// **blezek/tga は宣言された深度によらず 4 バイト/画素を確保する**
	// (NRGBA か RGBA のどちらかを作る)。`rasterBytesPerPixelBaseline` が
	// 同じ 4 なので、画素数の cap がそのままバイト予算の cap でもある
	// — ここに予算の判定を重ねると、片方を外す変異をもう片方が吸収して
	// テストが弱くなるだけなので置かない。
	px := int64(cfg.Width) * int64(cfg.Height)
	if px > maxPixels {
		return nil, fmt.Errorf("%w: %dx%d", ErrTooManyPixels, cfg.Width, cfg.Height)
	}
	img, err := tga.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("tga: decode: %w", err)
	}
	return img, nil
}
