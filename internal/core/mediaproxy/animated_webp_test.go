package mediaproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// animatedWebP builds a RIFF/WEBP container that declares animation.
//
// **デコードできる必要は無い。** pass-through はバイト列をそのまま返すので、
// 判定に使うヘッダだけあればよい。
func animatedWebP() []byte {
	var body bytes.Buffer
	body.WriteString("WEBP")

	vp8xPayload := make([]byte, 10)
	vp8xPayload[0] = 0x02 // ANIMATION_FLAG
	body.WriteString("VP8X")
	_ = binary.Write(&body, binary.LittleEndian, uint32(len(vp8xPayload)))
	body.Write(vp8xPayload)

	animPayload := make([]byte, 6)
	body.WriteString("ANIM")
	_ = binary.Write(&body, binary.LittleEndian, uint32(len(animPayload)))
	body.Write(animPayload)

	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

// stillWebP builds a plain (non-animated) RIFF/WEBP container.
// **判定にしか使わない** ので、デコードできる必要は無い。
func stillWebP() []byte {
	var body bytes.Buffer
	body.WriteString("WEBP")
	body.WriteString("VP8 ")
	_ = binary.Write(&body, binary.LittleEndian, uint32(4))
	body.Write([]byte{1, 2, 3, 4})

	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

func serveWebP(t *testing.T, data []byte) (*httptest.Server, *Service, string) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write(data)
	}))
	t.Cleanup(ts.Close)
	url := ts.URL + "/anim.webp"
	return ts, testService(map[string]bool{url: true}), url
}

// **アニメーション WebP を静止画にしない (#3128)。** `isAnimatedFormat` は MIME
// 判定なので WebP を拾えず、resize 経路 (先頭 1 コマだけ decode) に乗って
// 潰れていた。GIF / APNG と同じく pass-through で保つ。
//
// **フィクスチャはデコードできるものを使う。** ヘッダだけの合成バイト列だと
// `processResize` が「デコード失敗時は元データをそのまま返す」枝に落ち、
// pass-through と**バイト列も Content-Type も一致**してしまうので、
// 判定を外しても落ちない (実際にそうなっていた)。
func TestFetch_AnimatedWebP_PassThroughOnAnimatedModes(t *testing.T) {
	data := animatedWebPFixture(t, 300, 8)
	_, s, url := serveWebP(t, data)

	for _, mode := range []ProxyMode{ModeEmoji, ModeAvatar, ModePreview} {
		result, err := s.Fetch(context.Background(), url, mode, FormatWebP, true)
		require.NoError(t, err, "mode=%v", mode)
		assert.Equal(t, "image/webp", result.ContentType, "mode=%v", mode)

		var got bytes.Buffer
		_, err = got.ReadFrom(result.Body)
		require.NoError(t, err)
		result.Body.Close()
		// **バイト列がそのまま返ること**まで見る。resize 経路に乗ると
		// 再エンコードされて別のバイト列になる。
		assert.Equal(t, data, got.Bytes(), "mode=%v はバイト列をそのまま返す", mode)
	}
}

// 静止画設定 (`static=1`) のときは従来どおり静止化する。ここを通すと
// 「アニメーションを再生しない」設定が効かなくなる。
func TestFetch_AnimatedWebP_StaticRequestIsResized(t *testing.T) {
	data := animatedWebPFixture(t, 300, 8)
	_, s, url := serveWebP(t, data)

	result, err := s.Fetch(context.Background(), url, ModeEmoji, FormatWebP, false)
	require.NoError(t, err)
	var got bytes.Buffer
	_, err = got.ReadFrom(result.Body)
	require.NoError(t, err)
	result.Body.Close()
	assert.NotEqual(t, data, got.Bytes(), "static のときは素通ししない")
}

// 素通しするかの判定を表で固定する。**`Fetch` を通すとデコードまで走る**ので、
// 「素通しするか」だけを見るにはここで見るしかない。
func TestShouldPassThroughAnimated(t *testing.T) {
	still := stillWebP()
	anim := animatedWebP()
	png := []byte("\x89PNG\r\n\x1a\n")

	cases := []struct {
		name        string
		contentType string
		data        []byte
		mode        ProxyMode
		animated    bool
		want        bool
	}{
		// #3128 の本題。MIME は静止画と同じなのでバイト列で判定する。
		{"アニメ WebP / emoji", "image/webp", anim, ModeEmoji, true, true},
		{"アニメ WebP / avatar", "image/webp", anim, ModeAvatar, true, true},
		{"アニメ WebP / preview", "image/webp", anim, ModePreview, true, true},

		// 静止画設定 (`static=1`) が来ているなら静止化するのが正しい。
		{"アニメ WebP / static 要求", "image/webp", anim, ModeEmoji, false, false},
		// static / badge は明示的に静止画を要求する mode。
		{"アニメ WebP / static mode", "image/webp", anim, ModeStatic, true, false},
		{"アニメ WebP / badge mode", "image/webp", anim, ModeBadge, true, false},

		// 静止 WebP は従来どおり resize 経路へ。
		{"静止 WebP / emoji", "image/webp", still, ModeEmoji, true, false},

		// GIF / APNG の既存挙動は変えない (MIME 判定のまま)。
		{"GIF / emoji", "image/gif", png, ModeEmoji, true, true},
		{"APNG / emoji", "image/apng", png, ModeEmoji, true, true},
		{"GIF / static 要求", "image/gif", png, ModeEmoji, false, false},
		{"PNG / emoji", "image/png", png, ModeEmoji, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldPassThroughAnimated(tc.contentType, tc.data, tc.mode, tc.animated)
			assert.Equal(t, tc.want, got)
		})
	}
}
