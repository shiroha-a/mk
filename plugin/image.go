package plugin

import (
	"image"

	"github.com/shiroha-a/mk/internal/misc/imagedecode"
)

// Image decoding limits exposed to plugins.
//
// **プラグインは別 module なので `internal/` を import できない。** 本体は
// #3037 で「宣言寸法の cap → バイト予算 → wasm へ渡す入力の上限 → 同時実行枠」を
// `internal/misc/imagedecode` に集約したが、その入口が公開されていないため、
// プラグインが取得した画像は cap 無しでデコードされていた。取得元が細工した
// 画像を返すと、ヘッダの寸法だけで巨大なラスタを確保できる (Go の大確保失敗は
// `throw("out of memory")` で recover 不能なのでプロセスごと落ちる)。
// MaxImagePixels is the declared width*height ceiling used for images a
// plugin fetched from a third party. 本体の media proxy と同じ値。
var MaxImagePixels = imagedecode.MaxPixels

// DecodeImage decodes an image with the same limits the server applies to
// remote media.
//
// **取得元からのバイト数を絞るだけでは足りない。** 圧縮された入力が小さくても、
// 展開後のラスタは宣言寸法で決まる。`io.LimitReader` で読み込みを止めていても
// このデコードを通さなければ確保は起きる。
func DecodeImage(data []byte) (image.Image, error) {
	return imagedecode.DecodeWithPixelCap(data, imagedecode.MaxPixels)
}

// DecodeImageWithPixelCap is DecodeImage with an explicit ceiling, for plugins
// that want a tighter limit than the server default.
func DecodeImageWithPixelCap(data []byte, maxPixels int64) (image.Image, error) {
	return imagedecode.DecodeWithPixelCap(data, maxPixels)
}
