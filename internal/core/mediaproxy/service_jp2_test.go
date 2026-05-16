//go:build !race

// JPEG 2000 input 経路の round-trip test を `-race` build から除外する。
// `mrjoshuak/go-jpeg2000` v1.2.1 の `(*encoder).encodeTile` が起動する
// 子 goroutine が同 `*T1` の field を mutex 無しで read/write しており、
// Go race detector に検出される (#1088 / lib 内部 bug)。mk-go 側で
// 修正できないため、`-race` build (= CI shard 1) ではこの test を
// compile から外し、通常 build では引き続き走らせて回帰検出する。
//
// 上流 library が race fix を出したら本 build tag を撤去できる。

package mediaproxy

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	jpeg2000 "github.com/mrjoshuak/go-jpeg2000"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecodeImage_JP2 は #734 で追加した JPEG 2000 pure Go decoder
// (mrjoshuak/go-jpeg2000) の input 経路を確認する。Encode → Decode の
// round-trip で bounds が保たれれば OK。標準 image.Decode 経由で動作する。
func TestDecodeImage_JP2(t *testing.T) {
	// 2x2 RGB image を JP2 で encode してから decode で読み直す
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	src.Set(0, 0, color.RGBA{R: 255, A: 255})
	src.Set(1, 0, color.RGBA{G: 255, A: 255})
	src.Set(0, 1, color.RGBA{B: 255, A: 255})
	src.Set(1, 1, color.RGBA{R: 255, G: 255, A: 255})

	var buf bytes.Buffer
	require.NoError(t, jpeg2000.Encode(&buf, src, nil), "encode JP2")
	require.NotEmpty(t, buf.Bytes())

	img, err := decodeImage(buf.Bytes(), "image/jp2")
	require.NoError(t, err)
	assert.Equal(t, 2, img.Bounds().Dx())
	assert.Equal(t, 2, img.Bounds().Dy())
}
