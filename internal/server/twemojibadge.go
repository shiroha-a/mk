package server

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	xdraw "golang.org/x/image/draw"
)

// twemojiBadgeNamePattern mirrors upstream の `/^[0-9a-f-]+\.png$/`。
// 拡張子が .png でないもの (.svg 等) は 404 にする。
var twemojiBadgeNamePattern = regexp.MustCompile(`^[0-9a-f-]+\.png$`)

const (
	// badgeRenderSize is the size the SVG is rasterised at before processing.
	// upstream は density:1000 で読み込んでから 488x488 に resize する。
	badgeRenderSize = 488
	// badgePadding は upstream の extend({top:12, ...}) 相当。
	badgePadding = 12
	// badgeCanvasSize は 488 + 12*2 = 512。upstream の create({width:512}) と一致。
	badgeCanvasSize = badgeRenderSize + badgePadding*2
	// badgeOutputSize は最終的な出力サイズ (upstream: resize(96, 96))。
	badgeOutputSize = 96
	// badgeContrast は upstream の linear(1.75, -(128*1.75)+128) の傾き。
	badgeContrast = 1.75
)

// twemojiBadgeHandler serves /twemoji-badge/:name.
type twemojiBadgeHandler struct {
	dir string
}

func newTwemojiBadgeHandler(dir string) *twemojiBadgeHandler {
	return &twemojiBadgeHandler{dir: dir}
}

// Serve renders a Twemoji SVG into the monochrome badge PNG used by Web Push.
//
// upstream ClientServerService の sharp パイプラインを再現する:
//
//	resize(488) -> greyscale -> normalise -> linear(1.75 コントラスト)
//	-> flatten(#000) -> extend(12px, #000) -> b-w
//	-> 512x512 の透明キャンバスと XOR 合成 -> resize(96) -> png
//
// 結果は「絵文字の形が抜けた黒い四角」で、通知バッジとして使われる。
func (h *twemojiBadgeHandler) Serve(c echo.Context) error {
	name := c.Param("*")
	if name == "" {
		name = c.Param("name")
	}
	if !twemojiBadgeNamePattern.MatchString(name) {
		return echo.NewHTTPError(http.StatusNotFound)
	}

	// パターンで [0-9a-f-]+ に制限済みだが、念のため path traversal を潰す。
	base := strings.TrimSuffix(name, ".png")
	if base == "" || strings.Contains(base, "..") || strings.ContainsRune(base, filepath.Separator) {
		return echo.NewHTTPError(http.StatusNotFound)
	}

	svg, err := os.ReadFile(filepath.Join(h.dir, base+".svg"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound)
	}

	out, err := renderTwemojiBadge(svg)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError)
	}

	c.Response().Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	c.Response().Header().Set("Cache-Control", "max-age=2592000")
	return c.Blob(http.StatusOK, "image/png", out)
}

// renderTwemojiBadge rasterises the SVG and applies the upstream pipeline.
func renderTwemojiBadge(svg []byte) ([]byte, error) {
	rendered, err := rasterizeSVG(svg, badgeRenderSize)
	if err != nil {
		return nil, err
	}

	// greyscale -> normalise -> contrast -> flatten(#000) を 1 パスで行う。
	// flatten は「透明部分を黒で塗る」なので、alpha で黒へ寄せれば等価。
	mask := image.NewGray(image.Rect(0, 0, badgeCanvasSize, badgeCanvasSize))
	lo, hi := greyRange(rendered)
	for y := 0; y < badgeRenderSize; y++ {
		for x := 0; x < badgeRenderSize; x++ {
			r, g, b, a := rendered.At(x, y).RGBA()
			// Rec.601 luma。sharp の greyscale と同じ係数。
			lum := (299*float64(r>>8) + 587*float64(g>>8) + 114*float64(b>>8)) / 1000
			lum = normalise(lum, lo, hi)
			// linear(1.75, -(128*1.75)+128)
			v := badgeContrast*lum - 128*badgeContrast + 128
			// flatten({background:'#000'}): 透明なほど黒に倒す。
			v *= float64(a>>8) / 255
			mask.SetGray(x+badgePadding, y+badgePadding, color.Gray{Y: clamp8(v)})
		}
	}
	// extend の余白は #000 のまま (Gray のゼロ値が黒)。

	// upstream は透明キャンバスと mask を boolean 'eor' (XOR) で合成する。
	// キャンバスは全ビット 0 なので、XOR の結果は mask の反転ではなく mask
	// そのもの… ではなく、b-w colorspace 上で 0 との XOR = 各ビット反転無し。
	// 実際に必要なのは「絵文字の形が抜けた黒い板」なので、明度を反転して
	// alpha に載せ替える。
	badge := image.NewNRGBA(image.Rect(0, 0, badgeCanvasSize, badgeCanvasSize))
	for y := 0; y < badgeCanvasSize; y++ {
		for x := 0; x < badgeCanvasSize; x++ {
			v := mask.GrayAt(x, y).Y
			badge.SetNRGBA(x, y, color.NRGBA{R: 0, G: 0, B: 0, A: 255 - v})
		}
	}

	small := image.NewNRGBA(image.Rect(0, 0, badgeOutputSize, badgeOutputSize))
	xdraw.CatmullRom.Scale(small, small.Bounds(), badge, badge.Bounds(), xdraw.Over, nil)

	var buf bytes.Buffer
	if err := png.Encode(&buf, small); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// rasterizeSVG renders an SVG document into an RGBA image of the given size.
func rasterizeSVG(svg []byte, size int) (*image.RGBA, error) {
	icon, err := oksvg.ReadIconStream(bytes.NewReader(svg))
	if err != nil {
		return nil, err
	}
	icon.SetTarget(0, 0, float64(size), float64(size))

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	scanner := rasterx.NewScannerGV(size, size, img, img.Bounds())
	icon.Draw(rasterx.NewDasher(size, size, scanner), 1.0)
	return img, nil
}

// greyRange returns the min/max luma of the opaque pixels, used to reproduce
// sharp's normalise() (stretch to full range).
func greyRange(img *image.RGBA) (lo, hi float64) {
	lo, hi = 255, 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if a>>8 == 0 {
				continue
			}
			lum := (299*float64(r>>8) + 587*float64(g>>8) + 114*float64(bl>>8)) / 1000
			if lum < lo {
				lo = lum
			}
			if lum > hi {
				hi = lum
			}
		}
	}
	if hi <= lo {
		return 0, 255
	}
	return lo, hi
}

func normalise(v, lo, hi float64) float64 {
	if hi <= lo {
		return v
	}
	return (v - lo) * 255 / (hi - lo)
}

func clamp8(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v)
}
