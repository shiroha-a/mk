package mediaproxy

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"
)

// animatedGIF builds a 2-frame GIF so isAnimatedFormat sees an animated source.
func animatedGIF(t *testing.T) []byte {
	t.Helper()
	pal := color.Palette{color.RGBA{0, 0, 0, 255}, color.RGBA{255, 255, 255, 255}}
	frames := make([]*image.Paletted, 0, 2)
	for i := 0; i < 2; i++ {
		img := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
		img.SetColorIndex(0, 0, uint8(i))
		frames = append(frames, img)
	}
	var buf bytes.Buffer
	require.NoError(t, gif.EncodeAll(&buf, &gif.GIF{Image: frames, Delay: []int{0, 0}}))
	return buf.Bytes()
}

// #2905: `?emoji=1&static=1` はアニメーションを静止画にする。
//
// **mode だけを見ていたので効いていなかった。** parseMode は emoji を static より
// 先に見るので mode は ModeEmoji のままで、pass-through 判定が mode しか見て
// いなかったため GIF がそのまま返っていた。利用者の
// disableShowingAnimatedImages 設定が無視される。
func TestProcessAndReturn_StaticStopsAnimation(t *testing.T) {
	s := &Service{}
	src := animatedGIF(t)
	require.True(t, isAnimatedFormat("image/gif"), "テストの前提: GIF は animated 形式")

	// animated=true (既定) では pass-through され、GIF のまま返る。
	got, err := s.processAndReturn(context.Background(), src, "image/gif", ModeEmoji, FormatWebP, "https://example.com/a.gif", true)
	require.NoError(t, err)
	require.Equal(t, "image/gif", got.ContentType, "animated=true では元の形式を保つこと")

	// animated=false では decode 経路に乗り、静止画形式へ変換される。
	got, err = s.processAndReturn(context.Background(), src, "image/gif", ModeEmoji, FormatWebP, "https://example.com/a.gif", false)
	require.NoError(t, err)
	require.NotEqual(t, "image/gif", got.ContentType,
		"animated=false でも GIF のまま返っている (静止画設定が無視される)")
}

// avatar / preview も同じ扱い (upstream と揃える)。
func TestProcessAndReturn_StaticAppliesToAvatarAndPreview(t *testing.T) {
	s := &Service{}
	src := animatedGIF(t)
	for _, mode := range []ProxyMode{ModeAvatar, ModePreview} {
		got, err := s.processAndReturn(context.Background(), src, "image/gif", mode, FormatWebP, "https://example.com/a.gif", false)
		require.NoError(t, err)
		require.NotEqual(t, "image/gif", got.ContentType, "mode=%v で静止画化されていない", mode)
	}
}

// アニメーションでない形式は animated に関係なく従来どおり。
//
// **GIF では確かめられない。** isAnimatedFormat は content-type で判定するので、
// 1 フレームの GIF も animated 扱いになる。形式そのものが非アニメの PNG で見る。
func TestProcessAndReturn_StaticDoesNotAffectStillImages(t *testing.T) {
	s := &Service{}
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	require.NoError(t, png.Encode(&buf, img))
	require.False(t, isAnimatedFormat("image/png"), "テストの前提: PNG は非アニメ形式")

	for _, animated := range []bool{true, false} {
		got, err := s.processAndReturn(context.Background(), buf.Bytes(), "image/png", ModeEmoji, FormatWebP, "https://example.com/a.png", animated)
		require.NoError(t, err)
		require.Equal(t, "image/webp", got.ContentType, "animated=%v で出力形式が変わっている", animated)
	}
}
