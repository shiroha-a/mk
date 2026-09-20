package imagedecode

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
)

// riffWebP builds a RIFF/WEBP container from raw chunks.
func riffWebP(chunks ...[]byte) []byte {
	var body bytes.Buffer
	body.WriteString("WEBP")
	for _, c := range chunks {
		body.Write(c)
	}
	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

// chunk builds one RIFF chunk (payload is padded to an even boundary).
func chunk(typ string, payload []byte) []byte {
	var b bytes.Buffer
	b.WriteString(typ)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(payload)))
	b.Write(payload)
	if len(payload)%2 == 1 {
		b.WriteByte(0)
	}
	return b.Bytes()
}

// vp8x builds a VP8X chunk with the given flags byte.
func vp8x(flags byte) []byte {
	p := make([]byte, 10)
	p[0] = flags
	return chunk("VP8X", p)
}

func TestIsAnimatedWebP(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{
			// libwebp の ANIMATION_FLAG は 0x02。
			name: "VP8X の ANIMATION フラグ",
			data: riffWebP(vp8x(0x02), chunk("ANMF", []byte{1, 2})),
			want: true,
		},
		{
			// **ANIM / ANMF を伴わない形**。併記したケースだけだと、フラグを
			// 見なくなる変異をチャンク側の判定が拾ってしまい区別が付かない。
			name: "VP8X の ANIMATION フラグだけ (ANIM チャンク無し)",
			data: riffWebP(vp8x(0x02), chunk("VP8 ", []byte{1, 2, 3, 4})),
			want: true,
		},
		{
			// VP8X を持たない / フラグが立っていなくても ANIM があればアニメ。
			name: "ANIM チャンク",
			data: riffWebP(chunk("ANIM", []byte{0, 0, 0, 0, 0, 0})),
			want: true,
		},
		{
			name: "ANMF チャンクだけ",
			data: riffWebP(chunk("ANMF", []byte{1})),
			want: true,
		},
		{
			// ALPHA(0x10) / EXIF(0x08) / XMP(0x04) / ICC(0x20) は立てても
			// アニメではない。**隣のビットを拾わないこと**を見る。
			name: "VP8X の他のフラグだけ",
			data: riffWebP(vp8x(0x20|0x10|0x08|0x04), chunk("VP8 ", []byte{9})),
			want: false,
		},
		{
			name: "素の静止 WebP (VP8)",
			data: riffWebP(chunk("VP8 ", []byte{1, 2, 3})),
			want: false,
		},
		{
			name: "可逆の静止 WebP (VP8L)",
			data: riffWebP(chunk("VP8L", []byte{1, 2, 3})),
			want: false,
		},
		{name: "RIFF でない", data: []byte("\x89PNG\r\n\x1a\nxxxx"), want: false},
		{name: "RIFF だが WEBP でない", data: append([]byte("RIFF\x00\x00\x00\x00WAVE"), 0), want: false},
		{
			// **中に ANIM を持つ非 WebP の RIFF**。短いケースだけだと、
			// 先頭の WEBP 判定を外す変異が「どのみち長さ不足で false」に
			// 隠れてしまい区別が付かない。
			name: "RIFF/WAVE の中に ANIM チャンクがある",
			data: append(append([]byte("RIFF"), []byte{0, 0, 0, 0}...),
				append([]byte("WAVE"), chunk("ANIM", []byte{0, 0, 0, 0, 0, 0})...)...),
			want: false,
		},
		{name: "空", data: nil, want: false},
		{name: "ヘッダ途中で切れている", data: []byte("RIFF\x00\x00\x00\x00WEB"), want: false},
		{
			// チャンク長が container を越えたら打ち切る (無限ループにしない)。
			name: "チャンク長が壊れている",
			data: append(riffWebP(), []byte("VP8X\xff\xff\xff\x7f")...),
			want: false,
		},
		{
			// 長さ 0 のチャンクで pos が進まないと無限ループになる。
			name: "長さ 0 のチャンクが続く",
			data: riffWebP(chunk("XXXX", nil), chunk("ANIM", []byte{0})),
			want: true,
		},
		{
			// VP8X の payload が切れていてフラグを読めないケース。
			name: "VP8X の payload が無い",
			data: riffWebP(chunk("VP8X", nil)),
			want: false,
		},
		{
			// **後ろにチャンクが続く形**。`size >= 1` を要求しないと
			// `data[pos+8]` が次の 4CC の 1 バイト目 (`'V'` = 0x56、0x02 の
			// ビットが立つ) になり、静止 WebP を誤検出する。
			name: "VP8X の宣言長が 0 で VP8 が続く",
			data: riffWebP(chunkSized("VP8X", nil, 0), chunk("VP8 ", []byte{1, 2, 3, 4})),
			want: false,
		},
		{
			name: "VP8X の宣言長が 0 で VP8L が続く",
			data: riffWebP(chunkSized("VP8X", nil, 0), chunk("VP8L", []byte{1, 2, 3, 4})),
			want: false,
		},
		{
			// 奇数長チャンクの**後ろ**を正しく探せること。パディングを
			// 落とすと次のチャンクの境界がずれて ANIM を見失う。
			name: "奇数長チャンクの後ろに ANIM がある",
			data: riffWebP(chunk("ICCP", []byte{1, 2, 3}), chunk("ANIM", []byte{0, 0, 0, 0, 0, 0})),
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsAnimatedWebP(tc.data))
		})
	}
}

// chunkSized builds a RIFF chunk whose declared size can differ from the payload.
func chunkSized(typ string, payload []byte, declared int) []byte {
	var b bytes.Buffer
	b.WriteString(typ)
	_ = binary.Write(&b, binary.LittleEndian, uint32(declared))
	b.Write(payload)
	if len(payload)%2 == 1 {
		b.WriteByte(0)
	}
	return b.Bytes()
}
