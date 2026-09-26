package imagedecode

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"testing"

	gwebp "github.com/gen2brain/webp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vp8lHeader builds a VP8L bitstream that carries only a header declaring
// w x h. デコード前に拒否されることを見るテスト用なので、画素データは要らない。
func vp8lHeader(w, h int) []byte {
	p := make([]byte, 5)
	p[0] = 0x2f
	binary.LittleEndian.PutUint32(p[1:5], uint32(w-1)|uint32(h-1)<<14)
	return p
}

// vp8xCanvas builds a VP8X chunk with flags and canvas size.
func vp8xCanvas(flags byte, w, h int) []byte {
	p := make([]byte, 10)
	p[0] = flags
	putU24le(p[4:7], uint32(w-1))
	putU24le(p[7:10], uint32(h-1))
	return chunk("VP8X", p)
}

// anmf builds an ANMF chunk placing a frame of w x h at (x, y).
func anmf(x, y, w, h int, sub ...[]byte) []byte {
	p := make([]byte, 16)
	putU24le(p[0:3], uint32(x/2))
	putU24le(p[3:6], uint32(y/2))
	putU24le(p[6:9], uint32(w-1))
	putU24le(p[9:12], uint32(h-1))
	putU24le(p[12:15], 100)
	for _, s := range sub {
		p = append(p, s...)
	}
	return chunk("ANMF", p)
}

func animHeader() []byte { return chunk("ANIM", make([]byte, 6)) }

// encodeFrameChunks encodes img as a still WebP and returns its image chunks
// (ALPH / VP8 / VP8L), ready to be placed inside an ANMF.
func encodeFrameChunks(t *testing.T, img image.Image, lossless bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, gwebp.Encode(&buf, img, gwebp.Options{Lossless: lossless, Quality: 100}))
	chunks, err := webpChunks(buf.Bytes())
	require.NoError(t, err)
	var out []byte
	for _, c := range chunks {
		switch c.typ {
		case "ALPH", "VP8 ", "VP8L":
			out = append(out, chunk(c.typ, c.payload)...)
		}
	}
	require.NotEmpty(t, out)
	return out
}

func solid(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

// TestDecode_AnimatedWebPFrameLargerThanCanvasIsRefused pins the decode-bomb
// shape: a 1x1 canvas carrying huge frames. 修正前は imaging が全コマを
// デコードして 1.2GiB 確保していた。
func TestDecode_AnimatedWebPFrameLargerThanCanvasIsRefused(t *testing.T) {
	const big = 12000
	cases := []struct {
		name string
		data []byte
	}{
		{
			name: "ANMF declares a frame larger than the canvas",
			data: riffWebP(vp8xCanvas(webpFlagAnimation, 1, 1), animHeader(),
				anmf(0, 0, big, big, chunk("VP8L", vp8lHeader(big, big))),
				anmf(0, 0, big, big, chunk("VP8L", vp8lHeader(big, big)))),
		},
		{
			name: "ANMF fits the canvas but the bitstream is huge",
			data: riffWebP(vp8xCanvas(webpFlagAnimation, 1, 1), animHeader(),
				anmf(0, 0, 1, 1, chunk("VP8L", vp8lHeader(big, big)))),
		},
		{
			name: "frame offset pushes it outside the canvas",
			data: riffWebP(vp8xCanvas(webpFlagAnimation, 16, 16), animHeader(),
				anmf(8, 8, 16, 16, chunk("VP8L", vp8lHeader(16, 16)))),
		},
		{
			name: "still VP8X with a bitstream larger than the canvas",
			data: riffWebP(vp8xCanvas(0, 1, 1), chunk("VP8L", vp8lHeader(big, big))),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img, err := Decode(tc.data)
			require.Error(t, err)
			assert.Nil(t, img)
			assert.True(t, errors.Is(err, ErrMalformedWebP), "got %v", err)
		})
	}
}

func TestDecode_WebPBitstreamOverPixelCapIsRefused(t *testing.T) {
	data := riffWebP(chunk("VP8L", vp8lHeader(16000, 16000)))
	_, err := DecodeWithPixelCap(data, 1000)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTooManyPixels), "got %v", err)
}

func TestDecode_AnimatedWebPReturnsFirstFrame(t *testing.T) {
	red := color.NRGBA{R: 255, A: 255}
	blue := color.NRGBA{B: 255, A: 255}

	t.Run("lossless, full canvas", func(t *testing.T) {
		data := riffWebP(vp8xCanvas(webpFlagAnimation, 16, 16), animHeader(),
			anmf(0, 0, 16, 16, encodeFrameChunks(t, solid(16, 16, red), true)),
			anmf(0, 0, 16, 16, encodeFrameChunks(t, solid(16, 16, blue), true)))
		img, err := Decode(data)
		require.NoError(t, err)
		assert.Equal(t, image.Rect(0, 0, 16, 16), img.Bounds())
		r, g, b, a := img.At(5, 5).RGBA()
		assert.Equal(t, [4]uint32{0xffff, 0, 0, 0xffff}, [4]uint32{r, g, b, a})
	})

	t.Run("first frame offset on a larger canvas", func(t *testing.T) {
		data := riffWebP(vp8xCanvas(webpFlagAnimation, 32, 32), animHeader(),
			anmf(8, 8, 16, 16, encodeFrameChunks(t, solid(16, 16, red), true)))
		img, err := Decode(data)
		require.NoError(t, err)
		assert.Equal(t, image.Rect(0, 0, 32, 32), img.Bounds())
		_, _, _, a := img.At(1, 1).RGBA()
		assert.Zero(t, a, "outside the first frame must stay transparent")
		r, _, _, a := img.At(10, 10).RGBA()
		assert.Equal(t, uint32(0xffff), r)
		assert.Equal(t, uint32(0xffff), a)
	})

	t.Run("lossy with ALPH", func(t *testing.T) {
		half := solid(16, 16, color.NRGBA{G: 255, A: 128})
		data := riffWebP(vp8xCanvas(webpFlagAnimation|webpFlagAlpha, 16, 16), animHeader(),
			anmf(0, 0, 16, 16, encodeFrameChunks(t, half, false)))
		img, err := Decode(data)
		require.NoError(t, err)
		_, _, _, a := img.At(4, 4).RGBA()
		assert.InDelta(t, 128*257, a, 4*257, "alpha must survive the still rebuild")
	})
}

func TestDecode_MalformedWebPDoesNotPanic(t *testing.T) {
	valid := riffWebP(vp8xCanvas(webpFlagAnimation, 16, 16), animHeader(),
		anmf(0, 0, 16, 16, chunk("VP8L", vp8lHeader(16, 16))))
	for i := 12; i < len(valid); i++ {
		assert.NotPanics(t, func() { _, _ = Decode(valid[:i]) }, "truncated at %d", i)
	}
	// ANMF の中身が 16 バイトの header に満たない。
	short := riffWebP(vp8xCanvas(webpFlagAnimation, 16, 16), chunk("ANMF", []byte{1, 2, 3}))
	_, err := Decode(short)
	assert.True(t, errors.Is(err, ErrMalformedWebP), "got %v", err)
}

func FuzzDecodeWebP(f *testing.F) {
	f.Add(riffWebP(vp8xCanvas(webpFlagAnimation, 16, 16), animHeader(),
		anmf(0, 0, 16, 16, chunk("VP8L", vp8lHeader(16, 16)))))
	f.Add(riffWebP(chunk("VP8L", vp8lHeader(4, 4))))
	f.Fuzz(func(t *testing.T, data []byte) {
		if !isWebP(data) {
			return
		}
		_, _ = decodeWebP(data, 1<<16)
	})
}

// TestDecodeWebP_Branches exercises decodeWebP directly so that each guard is
// hit on its own (Decode の前段の DecodeConfig が先に弾くケースも含めて)。
func TestDecodeWebP_Branches(t *testing.T) {
	red := solid(8, 8, color.NRGBA{R: 255, A: 255})
	var lossless, lossy bytes.Buffer
	require.NoError(t, gwebp.Encode(&lossless, red, gwebp.Options{Lossless: true}))
	require.NoError(t, gwebp.Encode(&lossy, solid(8, 8, color.NRGBA{G: 255, A: 100}), gwebp.Options{Quality: 90}))
	lossyChunks, err := webpChunks(lossy.Bytes())
	require.NoError(t, err)
	require.Equal(t, "VP8X", lossyChunks[0].typ, "lossy with alpha is expected to use the extended format")

	vp8 := func(w, h int) []byte {
		p := make([]byte, 10)
		p[3], p[4], p[5] = 0x9d, 0x01, 0x2a
		binary.LittleEndian.PutUint16(p[6:8], uint16(w))
		binary.LittleEndian.PutUint16(p[8:10], uint16(h))
		return p
	}

	ok := []struct {
		name string
		data []byte
	}{
		{"simple VP8L", lossless.Bytes()},
		{"extended still with ALPH", lossy.Bytes()},
		{"trailing garbage after RIFF size", append(append([]byte{}, lossless.Bytes()...), 'X', 'Y', 'Z')},
		{"animated with ICCP and EXIF kept", riffWebP(
			vp8xCanvas(webpFlagAnimation|webpFlagICC|webpFlagEXIF, 8, 8),
			chunk("ICCP", []byte{0}), animHeader(),
			anmf(0, 0, 8, 8, encodeFrameChunks(t, red, true)),
			chunk("EXIF", []byte{0}))},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			// ICCP / EXIF の中身はダミーなので imaging が読めない可能性はあるが、
			// 検査段階 (ErrMalformedWebP / ErrTooManyPixels) では弾かれないこと。
			_, err := decodeWebP(tc.data, 1<<20)
			if err != nil {
				assert.False(t, errors.Is(err, ErrMalformedWebP), "got %v", err)
				assert.False(t, errors.Is(err, ErrTooManyPixels), "got %v", err)
			}
		})
	}

	bad := []struct {
		name string
		data []byte
		want error
	}{
		{"not webp", []byte("RIFF\x00\x00\x00\x00JUNK"), ErrMalformedWebP},
		{"no chunks", []byte("RIFF\x04\x00\x00\x00WEBP"), ErrMalformedWebP},
		{"unexpected first chunk", riffWebP(chunk("ICCP", []byte{0})), ErrMalformedWebP},
		{"short VP8X", riffWebP(chunk("VP8X", []byte{0, 0})), ErrMalformedWebP},
		{"canvas over cap", riffWebP(vp8xCanvas(0, 2000, 2000), chunk("VP8L", vp8lHeader(2000, 2000))), ErrTooManyPixels},
		{"simple VP8 over cap", riffWebP(chunk("VP8 ", vp8(2000, 2000))), ErrTooManyPixels},
		{"simple VP8 bad start code", riffWebP(chunk("VP8 ", make([]byte, 10))), ErrMalformedWebP},
		{"simple VP8 zero size", riffWebP(chunk("VP8 ", vp8(0, 4))), ErrMalformedWebP},
		{"simple VP8L bad signature", riffWebP(chunk("VP8L", []byte{0, 0, 0, 0, 0})), ErrMalformedWebP},
		{"still VP8X without bitstream", riffWebP(vp8xCanvas(0, 8, 8), chunk("EXIF", []byte{0})), ErrMalformedWebP},
		{"still VP8X with broken bitstream", riffWebP(vp8xCanvas(0, 8, 8), chunk("VP8L", []byte{1})), ErrMalformedWebP},
		{"animated without ANMF", riffWebP(vp8xCanvas(webpFlagAnimation, 8, 8), animHeader()), ErrMalformedWebP},
		{"frame without bitstream", riffWebP(vp8xCanvas(webpFlagAnimation, 8, 8), animHeader(),
			anmf(0, 0, 8, 8, chunk("ALPH", []byte{0}))), ErrMalformedWebP},
		{"frame with broken sub-chunks", riffWebP(vp8xCanvas(webpFlagAnimation, 8, 8), animHeader(),
			anmf(0, 0, 8, 8, []byte("VP8L\xff\xff\xff\xff"))), ErrMalformedWebP},
		{"frame with broken bitstream", riffWebP(vp8xCanvas(webpFlagAnimation, 8, 8), animHeader(),
			anmf(0, 0, 8, 8, chunk("VP8 ", []byte{1, 2}))), ErrMalformedWebP},
		{"unknown bitstream type", riffWebP(vp8xCanvas(webpFlagAnimation, 8, 8), animHeader(),
			anmf(0, 0, 8, 8, chunk("VP8L", vp8lHeader(8, 8))[:0])), ErrMalformedWebP},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeWebP(tc.data, 1<<20)
			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.want), "got %v", err)
		})
	}

	t.Run("unknown bitstream fourcc", func(t *testing.T) {
		_, _, err := webpBitstreamDims("VP9 ", nil)
		assert.True(t, errors.Is(err, ErrMalformedWebP))
	})
}
