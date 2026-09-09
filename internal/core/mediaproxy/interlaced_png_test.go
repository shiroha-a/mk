package mediaproxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// **media proxy の decodeImage が共有の規則を通ること (#2925)。**
//
// 規則そのものは `internal/misc/imagedecode` のテストが固定する。ここで見るのは
// **結線** — `decodeImage` を素の `imaging.Decode` に戻すと、インターレースの
// truecolor PNG が全画素 0 になり、resize 系の mode が「真っ黒な絵文字」を
// 200 で配る。
func TestDecodeImage_InterlacedRGB8IsNotBlank(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "misc", "imagedecode", "testdata", "interlaced-rgb8.png"))
	require.NoError(t, err)

	img, err := decodeImage(data, "image/png")
	require.NoError(t, err)

	b := img.Bounds()
	nonZero := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 != 0 || g>>8 != 0 || bl>>8 != 0 {
				nonZero++
			}
		}
	}
	require.Greater(t, nonZero, 0, "全画素 0 になっている (imaging の Adam7 バグ)")
}
