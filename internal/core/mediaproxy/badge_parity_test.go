package mediaproxy

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encodePNG is a small helper for the badge fixtures below.
func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// decodeBadge decodes a badge PNG produced by processBadge.
func decodeBadge(t *testing.T, res *ProxyResult) *image.NRGBA {
	t.Helper()
	var buf bytes.Buffer
	_, err := buf.ReadFrom(res.Body)
	require.NoError(t, err)
	img, err := png.Decode(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	nrgba, ok := img.(*image.NRGBA)
	require.True(t, ok, "badge は NRGBA で出す (alpha=輝度 の silhouette)")
	return nrgba
}

// wideGradient builds a 200x100 image whose left half is dark and right half is
// bright. **横長なのが要点** — contain なら縦に黒帯が付いて内容は残り、
// cover (旧実装の imaging.Fill) なら左右が切り落とされる。
func wideGradient(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			v := uint8(x * 255 / 199)
			img.Set(x, y, color.NRGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return encodePNG(t, img)
}

// **contain であって cover ではない (#2920)。**
//
// 旧実装は `imaging.Fill` (cover + 中央クロップ) だったので、200x100 の絵文字は
// 横幅の半分が失われていた。contain なら縦横比を保って 96x96 に収め、余った上下は
// 黒帯になる。帯の存在と、左右端の内容が残っていることの両方で判定する。
func TestProcessBadge_ContainKeepsFullWidth(t *testing.T) {
	s := testService(nil)
	res, err := s.processBadge(wideGradient(t), "image/png")
	require.NoError(t, err)
	defer res.Body.Close()

	img := decodeBadge(t, res)
	assert.Equal(t, 96, img.Bounds().Dx())
	assert.Equal(t, 96, img.Bounds().Dy())

	at := func(x, y int) uint8 { return img.Pix[(y*96+x)*4] }
	// 200x100 は 96x48 に収まるので、上下 24 行が黒帯になる。
	assert.EqualValues(t, 0, at(48, 2), "上に黒帯ができること (contain)")
	assert.EqualValues(t, 0, at(48, 93), "下に黒帯ができること (contain)")
	// 帯の内側では左が暗く右が明るいまま。cover だと中央だけが残るので、
	// 左端が暗く右端が明るいという関係が崩れる。
	assert.Less(t, at(2, 48), uint8(64), "左端の暗さが残ること")
	assert.Greater(t, at(93, 48), uint8(192), "右端の明るさが残ること")
}

// **透明部分は黒に落ちる (#2920)。** upstream の `flatten({background: '#000'})`。
// あわせて、透明画素の RGB を normalise に含めないことも見る — 含めると
// 見えている部分のコントラストが潰れる (実測で sharp の半分の値になっていた)。
func TestProcessBadge_FlattensTransparentToBlack(t *testing.T) {
	s := testService(nil)
	// 左半分は「白いが完全に透明」、右半分は不透明のグラデーション。
	img := image.NewNRGBA(image.Rect(0, 0, 96, 96))
	for y := 0; y < 96; y++ {
		for x := 0; x < 96; x++ {
			if x < 48 {
				img.Set(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 0})
			} else {
				v := uint8((x - 48) * 255 / 47)
				img.Set(x, y, color.NRGBA{R: v, G: v, B: v, A: 255})
			}
		}
	}
	res, err := s.processBadge(encodePNG(t, img), "image/png")
	require.NoError(t, err)
	defer res.Body.Close()

	out := decodeBadge(t, res)
	at := func(x, y int) uint8 { return out.Pix[(y*96+x)*4] }
	assert.EqualValues(t, 0, at(10, 48), "完全に透明な部分は黒になること")
	assert.Greater(t, at(93, 48), uint8(192),
		"透明画素の白を normalise に含めると、見えている側のコントラストが潰れる")
}

// **暗いところが透明な silhouette になる (#2920)。**
//
// upstream の最後の `boolean(mask, 'eor')` は透明な RGBA canvas との XOR なので、
// 結果は R=G=B=A=mask になる。no-op ではない — これが無いと通知 UI の背景に
// 乗せたときの見え方が変わる。
func TestProcessBadge_AlphaEqualsLuminance(t *testing.T) {
	s := testService(nil)
	res, err := s.processBadge(wideGradient(t), "image/png")
	require.NoError(t, err)
	defer res.Body.Close()

	img := decodeBadge(t, res)
	for i := 0; i < 96*96; i++ {
		r, g, b, a := img.Pix[i*4], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3]
		require.True(t, r == g && g == b && b == a,
			"画素 %d が R=G=B=A になっていない: [%d %d %d %d]", i, r, g, b, a)
	}
}

// **ほぼ単色なら 404 (#2920)。** upstream の
// `stats().entropy < 0.1` → `StatusError('Skip to provide badge', 404)`。
// SW の create-notification.ts は `res.status !== 200` で iconUrl('plus') へ
// 落ちるので、判別できないバッジを配るより 404 の方が親切。
func TestProcessBadge_BlankImageIs404(t *testing.T) {
	s := testService(nil)
	img := image.NewNRGBA(image.Rect(0, 0, 96, 96))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	_, err := s.processBadge(encodePNG(t, img), "image/png")
	require.ErrorIs(t, err, ErrNotFound, "単色のバッジは配らない")
}

// 中身のある画像は 404 にならない (guard が過剰に効いていないこと)。
func TestProcessBadge_ContentfulImageIsNot404(t *testing.T) {
	s := testService(nil)
	res, err := s.processBadge(wideGradient(t), "image/png")
	require.NoError(t, err)
	res.Body.Close()
}

// shannonEntropy の境界。単色は 0、2 値の半々は 1 bit。
func TestShannonEntropy(t *testing.T) {
	assert.Equal(t, 0.0, shannonEntropy(nil))
	assert.Equal(t, 0.0, shannonEntropy(bytes.Repeat([]byte{7}, 100)))
	half := append(bytes.Repeat([]byte{0}, 50), bytes.Repeat([]byte{255}, 50)...)
	assert.InDelta(t, 1.0, shannonEntropy(half), 1e-9)
}

// **normalise / 1.75x コントラスト / flatten が実際に効いていること (#2920)。**
//
// **fixture の作りが要点。** フルレンジのグラデーションだと normalise もコントラストも
// 恒等変換に近くなり、外しても結果が変わらない (実際、最初のテストはその形で
// 4 つの変異を素通ししていた)。狭いレンジ・完全透明・半透明を 1 枚に入れて、
// それぞれの段が結果を動かすことを別々の座標で見る。
//
// 実測値 (変異を入れたときの値):
//
//	                        (34,48)  (62,48)  (80,48)
//	基準                        132      212      176
//	透明画素の RGB を 0 にしない   0       13       96
//	flatten を外す               132      212      255
//	normalise を外す              82      146      127
//	コントラストを外す            130      176      128
func TestProcessBadge_NormaliseContrastAndFlattenAllApply(t *testing.T) {
	s := testService(nil)
	img := image.NewNRGBA(image.Rect(0, 0, 96, 96))
	for y := 0; y < 96; y++ {
		for x := 0; x < 96; x++ {
			switch {
			case x < 32:
				// 白いが完全に透明。normalise に含めると狭レンジ側が潰れる。
				img.Set(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 0})
			case x < 64:
				// 100..140 の狭いレンジ。normalise とコントラストで伸びる。
				v := uint8(100 + (x-32)*40/31)
				img.Set(x, y, color.NRGBA{R: v, G: v, B: v, A: 255})
			default:
				// 半透明の一定色。flatten で黒に半分寄る。
				img.Set(x, y, color.NRGBA{R: 200, G: 200, B: 200, A: 128})
			}
		}
	}
	res, err := s.processBadge(encodePNG(t, img), "image/png")
	require.NoError(t, err)
	defer res.Body.Close()

	out := decodeBadge(t, res)
	at := func(x, y int) int { return int(out.Pix[(y*96+x)*4]) }

	assert.EqualValues(t, 0, at(10, 48), "完全に透明な部分は黒")
	assert.InDelta(t, 132, at(34, 48), 12,
		"狭レンジの暗い側。透明画素を normalise に含めると 0 まで落ちる / normalise を外すと 82 に留まる")
	assert.InDelta(t, 212, at(62, 48), 12,
		"狭レンジの明るい側。1.75x コントラストを外すと 176 に留まる")
	assert.InDelta(t, 176, at(80, 48), 12,
		"半透明の一定色。flatten を外すと 255 になる / normalise を外すと 127")
}
