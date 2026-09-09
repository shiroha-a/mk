package mediaproxy

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// interlacedRGBPNG is an 8x8 red-to-blue gradient encoded as an **Adam7
// (interlaced) truecolor PNG** (IHDR colorType=2, interlace=1).
//
// **Go の image/png はインターレース出力に対応していない**ので fixture を
// 埋め込む。ImageMagick の `convert -interlace PNG` で生成した。
const interlacedRGBPNG = "iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAAE8ahlKAAAARUlEQVQI13XLwQ2AIBQE0fcTS6EYLMZibAZ6oZnvyYAJzm2yOxJJXG5EYlpTvUQxpqTJ0Zfb0Z0/zVDs+CQrUbX9QG6HByMGDwPazhZEAAAAAElFTkSuQmCC"

func mustInterlacedPNG(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(interlacedRGBPNG)
	require.NoError(t, err)
	require.True(t, isInterlacedPNG(data), "fixture が Adam7 でなくなっている")
	return data
}

// **インターレースの RGB PNG が真っ黒にならないこと (#2925)。**
//
// `kovidgoyal/imaging` は alpha を持たない PNG を独自の `*nrgb.Image` に読むが、
// その Adam7 の処理が壊れており **エラーを返さず全画素 0** を返す。resize 系の
// mode は「真っ黒な絵文字」を 200 で配ることになり、検出する手段が無い。
func TestDecodeImage_InterlacedRGBPNGIsNotBlank(t *testing.T) {
	data := mustInterlacedPNG(t)

	img, err := decodeImage(data, "image/png")
	require.NoError(t, err)

	// 参照実装として stdlib と突き合わせる。**「0 でないこと」だけでは弱い** —
	// 別の壊れ方 (色が入れ替わる等) を見逃す。
	want, err := png.Decode(bytes.NewReader(data))
	require.NoError(t, err)

	b := img.Bounds()
	require.Equal(t, want.Bounds(), b)
	nonZero := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			wr, wg, wb, wa := want.At(x, y).RGBA()
			gr, gg, gb, ga := img.At(x, y).RGBA()
			require.Equal(t, [4]uint32{wr >> 8, wg >> 8, wb >> 8, wa >> 8},
				[4]uint32{gr >> 8, gg >> 8, gb >> 8, ga >> 8},
				"画素 (%d,%d) が stdlib と違う", x, y)
			if wr>>8 != 0 || wg>>8 != 0 || wb>>8 != 0 {
				nonZero++
			}
		}
	}
	require.Greater(t, nonZero, 0, "前提: fixture は全画素 0 ではない")
}

// 非インターレースの PNG は従来どおり imaging で読む (回帰していないこと)。
func TestIsInterlacedPNG(t *testing.T) {
	assert.True(t, isInterlacedPNG(mustInterlacedPNG(t)))

	// stdlib が吐く PNG は常に非インターレース。
	assert.False(t, isInterlacedPNG(makeBadgePNG()))

	// PNG でないもの / 短すぎるものを誤検出しない。
	assert.False(t, isInterlacedPNG(nil))
	assert.False(t, isInterlacedPNG([]byte("not a png")))
	assert.False(t, isInterlacedPNG(bytes.Repeat([]byte{0}, 64)))

	// **signature はあるが最初の chunk が IHDR でない場合。**
	// interlace を読む位置に 1 を置いて、IHDR の確認が実際に効いていることを見る
	// (置かないと data[28] が 0 なので、chunk 名を見なくても false になる)。
	broken := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)
	copy(broken[12:16], "IDAT")
	broken[28] = 1
	assert.False(t, isInterlacedPNG(broken),
		"最初の chunk が IHDR でなければ、28 バイト目は interlace method ではない")
}
