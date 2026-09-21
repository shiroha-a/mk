package plugin

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"
)

// **プラグインにも本体と同じデコード上限を公開すること。**
//
// プラグインは別 module なので `internal/misc/imagedecode` を import できない。
// 入口が無いと、取得元が細工した画像でヘッダの寸法どおりに巨大なラスタを
// 確保させられる (recover 不能な OOM になる)。
func TestDecodeImage_AppliesPixelCap(t *testing.T) {
	t.Parallel()

	// 小さな PNG は通ること。
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))))
	img, err := DecodeImage(buf.Bytes())
	require.NoError(t, err)
	require.NotNil(t, img)

	// 明示的な cap を下回る画像は落ちること。
	_, err = DecodeImageWithPixelCap(buf.Bytes(), 4)
	require.Error(t, err, "宣言寸法が cap を超えたら拒否すること")

	require.Positive(t, MaxImagePixels, "既定の上限が公開されていること")
}
