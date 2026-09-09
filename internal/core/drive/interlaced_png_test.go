package drive

import (
	"bytes"
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// **drive 側の decodeImage も共有の規則を通ること (#2925)。**
//
// **ここが要点** — media proxy 側だけ直しても、ローカルにアップロードされた
// インターレースの truecolor PNG は**真っ黒なサムネイルを storage に焼く**。
// proxy はその variant を優先して返す (`swapToVariant`) ので原本を読み直さず、
// 絵文字やアバターが真っ黒なまま 200 で配られ続ける。
func TestGenerateThumbnail_InterlacedRGB8IsNotBlank(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "misc", "imagedecode", "testdata", "interlaced-rgb8.png"))
	require.NoError(t, err)

	p := NewDefaultImageProcessor()
	out, err := p.GenerateThumbnail(data, "image/png")
	require.NoError(t, err)
	require.NotEmpty(t, out.Data)

	img, _, err := image.Decode(bytes.NewReader(out.Data))
	require.NoError(t, err)

	b := img.Bounds()
	nonZero := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 > 24 || g>>8 > 24 || bl>>8 > 24 {
				nonZero++
			}
		}
	}
	require.Greater(t, nonZero, 0, "サムネイルが真っ黒になっている (imaging の Adam7 バグ)")
}
