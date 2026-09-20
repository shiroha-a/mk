package mediaproxy

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/require"
)

// animatedWebPFixture builds a **decodable** animated WebP of the given size.
//
// **合成したヘッダだけのバイト列を使わないこと。** デコードできない入力は
// `GenerateWebpublic` が `decodeImage` の失敗で `nil, nil` を返すため、
// アニメーション判定を外しても同じ結果になり、変異を殺せない (実際に
// そうなっていた)。ここでは in-tree のエンコーダが作った実在の VP8L /
// VP8 チャンクを ANIM / ANMF で包むので、`imaging` が本物として読める。
//
// `internal/core/drive` にも同じものがある。共有するには
// `gen2brain/webp` を testutil へ持ち込むことになり、testutil を import する
// 全パッケージが wasm ランタイムを抱えるので置いていない。
func animatedWebPFixture(t *testing.T, w, h int) []byte {
	t.Helper()

	src := image.NewNRGBA(image.Rect(0, 0, w, h))
	src.Set(0, 0, color.NRGBA{R: 200, G: 30, B: 30, A: 255})
	still, err := encodeWebP(src)
	require.NoError(t, err)

	frame := webpImageChunk(t, still)

	put3 := func(b *bytes.Buffer, v int) {
		b.WriteByte(byte(v))
		b.WriteByte(byte(v >> 8))
		b.WriteByte(byte(v >> 16))
	}

	var body bytes.Buffer
	body.WriteString("WEBP")

	vp8x := make([]byte, 10)
	vp8x[0] = 0x02 // ANIMATION_FLAG
	vp8x[4], vp8x[5], vp8x[6] = byte(w-1), byte((w-1)>>8), byte((w-1)>>16)
	vp8x[7], vp8x[8], vp8x[9] = byte(h-1), byte((h-1)>>8), byte((h-1)>>16)
	writeChunk(&body, "VP8X", vp8x)

	writeChunk(&body, "ANIM", make([]byte, 6)) // bgcolor + loop

	for i := 0; i < 2; i++ {
		var af bytes.Buffer
		put3(&af, 0)   // x/2
		put3(&af, 0)   // y/2
		put3(&af, w-1) // width-1
		put3(&af, h-1) // height-1
		put3(&af, 100) // duration ms
		af.WriteByte(0)
		af.Write(frame)
		writeChunk(&body, "ANMF", af.Bytes())
	}

	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

// webpImageChunk returns the VP8 / VP8L chunk (header included) of a still WebP.
func webpImageChunk(t *testing.T, still []byte) []byte {
	t.Helper()
	pos := 12
	for pos+8 <= len(still) {
		typ := string(still[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(still[pos+4 : pos+8]))
		end := pos + 8 + size + (size & 1)
		if end > len(still) {
			break
		}
		if typ == "VP8 " || typ == "VP8L" {
			return still[pos:end]
		}
		pos = end
	}
	t.Fatalf("静止 WebP に画像チャンクが無い")
	return nil
}

func writeChunk(b *bytes.Buffer, typ string, payload []byte) {
	b.WriteString(typ)
	_ = binary.Write(b, binary.LittleEndian, uint32(len(payload)))
	b.Write(payload)
	if len(payload)%2 == 1 {
		b.WriteByte(0)
	}
}
