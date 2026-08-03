package drive

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ftypHeader builds a minimal ISO-BMFF header with the given major brand and
// compatible-brands list. .mov / .heic / .avif / .m4a / .m4v / .3gp はすべて
// この形で、ブランドだけが違う。
func ftypHeader(brand, compat string) []byte {
	body := []byte("ftyp")
	body = append(body, []byte(brand)...)
	body = append(body, 0, 0, 0, 0) // minor version
	body = append(body, []byte(compat)...)
	size := make([]byte, 4)
	binary.BigEndian.PutUint32(size, uint32(len(body)+4))
	return padTo512(append(size, body...))
}

func padTo512(b []byte) []byte {
	for len(b) < 512 {
		b = append(b, 0)
	}
	return b
}

// #2319: `http.DetectContentType` 単体では判定できず application/octet-stream に
// 落ちていた形式。落ちると (1) ブラウザで再生できない (2) サムネイルが生成され
// ない (3) uploadableFileTypes policy に一致しない、の 3 つが同時に起きる。
func TestDetectMIME_FormatsGoCannotSniff(t *testing.T) {
	cases := []struct {
		name string
		want string
		data []byte
	}{
		{".mov (QuickTime)", "video/quicktime", ftypHeader("qt  ", "qt  ")},
		{".heic", "image/heic", ftypHeader("heic", "heicmif1")},
		{".avif", "image/avif", ftypHeader("avif", "avifmif1miaf")},
		{".m4a", "audio/x-m4a", ftypHeader("M4A ", "M4A mp42isom")},
		{".m4v", "video/x-m4v", ftypHeader("M4V ", "M4V mp42isom")},
		{".3gp", "video/3gpp", ftypHeader("3gp4", "3gp4")},
		{".3g2", "video/3gpp2", ftypHeader("3g2a", "3g2a")},
		{".flac", "audio/flac", padTo512([]byte("fLaC\x00\x00\x00\x22"))},
		{".aac (ADTS)", "audio/aac", padTo512([]byte{0xFF, 0xF1, 0x50, 0x80})},
		{".tif", "image/tiff", padTo512([]byte{0x49, 0x49, 0x2A, 0x00})},
		{".mpg", "video/mpeg", padTo512([]byte{0x00, 0x00, 0x01, 0xBA})},
		{".mp3 without ID3", "audio/mpeg", padTo512([]byte{0xFF, 0xFB, 0x90, 0x00})},
		{".apng", "image/apng", padTo512([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\x08acTL"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, DetectMIME(tc.data))
		})
	}
}

// mimetype は ID3 タグ付き MP3 を判定できないので、標準 sniffer への
// フォールバックが要る。ここが退行すると ID3 付き MP3 (= 実世界の大半) が
// 再生できなくなる。
func TestDetectMIME_FallsBackToStdlibSniffer(t *testing.T) {
	withID3 := padTo512([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"))
	assert.Equal(t, "audio/mpeg", DetectMIME(withID3))
}

// 既に正しく判定できていた形式が退行していないこと。
func TestDetectMIME_KnownGoodFormats(t *testing.T) {
	cases := []struct {
		name string
		want string
		data []byte
	}{
		{"png", "image/png", padTo512([]byte("\x89PNG\r\n\x1a\n"))},
		{"jpeg", "image/jpeg", padTo512([]byte{0xFF, 0xD8, 0xFF, 0xE0})},
		{"gif", "image/gif", padTo512([]byte("GIF89a"))},
		{"webp", "image/webp", padTo512([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "))},
		{"webm", "video/webm", padTo512([]byte{0x1A, 0x45, 0xDF, 0xA3})},
		{"mp4", "video/mp4", ftypHeader("isom", "isomiso2avc1mp41")},
		{"bmp", "image/bmp", padTo512([]byte("BM\x00\x00\x00\x00"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, DetectMIME(tc.data))
		})
	}
}

// upstream fixMime 相当の正規化。`audio/wave` は `http.DetectContentType` が
// WAV に対して返す綴りで、正規化しないと allowlist に無いため octet-stream に
// 矯正されて再生できない。
func TestDetectMIME_NormalisesAliases(t *testing.T) {
	assert.Equal(t, "audio/wav", DetectMIME(padTo512([]byte("RIFF\x24\x00\x00\x00WAVEfmt "))))

	// 正規化そのものの単体確認 (fallback 経路や将来の判定器で現れうる綴り)。
	assert.Equal(t, "audio/flac", fixMIME("audio/x-flac"))
	assert.Equal(t, "audio/wav", fixMIME("audio/vnd.wave"))
	assert.Equal(t, "audio/wav", fixMIME("audio/wave"))
	assert.Equal(t, "audio/wav", fixMIME("audio/x-wav"))
	assert.Equal(t, "image/png", fixMIME("image/png"), "対象外はそのまま")

	// 正規化後の綴りはすべて browser-safe allowlist に載っていること
	// (載っていないと配信時に octet-stream へ矯正され再生できない)。
	for _, m := range []string{"audio/flac", "audio/wav"} {
		assert.Equal(t, m, BrowserSafeContentType(m), "%s must stay browser-safe", m)
	}
}

// SVG は XML として判定されるので、upstream の checkSvg 相当が要る。
// 記録する type は image/svg+xml だが、**inline 配信はさせない** (active content)。
func TestDetectMIME_SVG(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"xml 宣言つき", `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`},
		{"コメント先行", `<!-- hello --><svg xmlns="http://www.w3.org/2000/svg"/>`},
		{"doctype 先行", `<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" ""><svg xmlns="http://www.w3.org/2000/svg"/>`},
		{"要素名が大文字", `<SVG xmlns="http://www.w3.org/2000/svg"/>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, MIMESVG, DetectMIME([]byte(tc.body)))
			assert.Equal(t, MIMEOctetStream, BrowserSafeContentType(DetectMIME([]byte(tc.body))),
				"SVG must never be served inline")
		})
	}
}

// 本文に "<svg" を含むだけの別文書を SVG と誤認しないこと。誤認すると
// image として扱われ、サムネイル生成やセンシティブ判定の経路が変わる。
func TestDetectMIME_NotSVG(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"HTML", `<!DOCTYPE html><html><body>hi</body></html>`, "text/html"},
		{"svg を含む HTML", `<!DOCTYPE html><html><body><svg></svg></body></html>`, "text/html"},
		{"svg と書いてあるテキスト", strings.Repeat("this file mentions <svg> but is plain text\n", 8), "text/plain"},
		{"ルートが svg でない XML", `<?xml version="1.0"?><root><svg/></root>`, "text/xml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, DetectMIME([]byte(tc.body)))
			assert.Equal(t, MIMEOctetStream, BrowserSafeContentType(DetectMIME([]byte(tc.body))))
		})
	}
}

// 巨大な XML を SVG 判定でパースし続けない (upstream checkSvg と同じ 1MiB 上限)。
func TestDetectMIME_SVGSizeLimit(t *testing.T) {
	head := `<svg xmlns="http://www.w3.org/2000/svg">`
	body := head + strings.Repeat("<rect/>", (svgSizeLimit/7)+16) + "</svg>"
	require.Greater(t, len(body), svgSizeLimit)
	assert.NotEqual(t, MIMESVG, DetectMIME([]byte(body)), "1MiB 超は SVG 判定しない")
}

func TestDetectMIME_Empty(t *testing.T) {
	assert.Equal(t, MIMEOctetStream, DetectMIME(nil))
	assert.Equal(t, MIMEOctetStream, DetectMIME([]byte{}))
}

// 拡張子は一切見ない。見てしまうと `.png` と名付けた HTML で allowlist を
// 通り抜けられ、#2106 H3/H4 の stored XSS 対策が無効化される。
func TestDetectMIME_IgnoresFilename(t *testing.T) {
	htmlBody := []byte(`<!DOCTYPE html><html><script>alert(1)</script></html>`)
	// 呼び出し口 (AnalyseFile) はファイル名を受け取らない。中身だけで判定される。
	info, err := AnalyseFile(strings.NewReader(string(htmlBody)))
	require.NoError(t, err)
	assert.Equal(t, "text/html", info.MimeType)
	assert.Equal(t, MIMEOctetStream, BrowserSafeContentType(info.MimeType))
}

func TestStripMIMEParams(t *testing.T) {
	assert.Equal(t, "text/xml", stripMIMEParams("text/xml; charset=utf-8"))
	assert.Equal(t, "image/png", stripMIMEParams("  IMAGE/PNG  "))
	assert.Equal(t, "", stripMIMEParams(""))
}

// AnalyseFile が新しい判定を通ること (drive_file.type に載る値)。
func TestAnalyseFile_UsesDetectMIME(t *testing.T) {
	mov := ftypHeader("qt  ", "qt  ")
	info, err := AnalyseFile(strings.NewReader(string(mov)))
	require.NoError(t, err)
	assert.Equal(t, "video/quicktime", info.MimeType)
	assert.Equal(t, len(mov), info.Size)
	assert.NotEmpty(t, info.MD5)
}

// 先頭 MIMESniffLen バイトだけでも同じ判定になること。chunked upload の初回
// append と S3 の Put はこの前提で prefix しか渡さない。
func TestDetectMIME_PrefixIsEnough(t *testing.T) {
	full := append(ftypHeader("qt  ", "qt  "), make([]byte, 4*1024*1024)...)
	prefix := full[:MIMESniffLen]
	assert.Equal(t, DetectMIME(full), DetectMIME(prefix))
	assert.Equal(t, "video/quicktime", DetectMIME(prefix))
}

// #2319 で判定器を差し替えた結果、APNG は `image/png` ではなく `image/apng` で
// 返るようになった。下流の分岐が旧綴り (`image/vnd.mozilla.apng`) しか見て
// いないと、サムネイルが生成されなくなる退行が起きる。両綴りを受けること。
func TestAPNGSpellingsAreBothRecognised(t *testing.T) {
	for _, m := range []string{"image/apng", "image/vnd.mozilla.apng"} {
		assert.True(t, isMimeImage(m), "%s must be treated as an image", m)
		assert.True(t, isAnimatedMime(m), "%s must be treated as animated", m)
	}
	// 判定器が返すのは upstream と同じ `image/apng`。こちらは inline 配信できる
	// 必要がある。`image/vnd.mozilla.apng` は upstream の FILE_TYPE_BROWSERSAFE にも
	// 無い綴りなので allowlist に載せない (= 配信は octet-stream)。内部分岐だけが
	// 旧綴りを防御的に受ける。
	assert.Equal(t, "image/apng", BrowserSafeContentType("image/apng"))
	assert.Equal(t, MIMEOctetStream, BrowserSafeContentType("image/vnd.mozilla.apng"))
}

// 判定が改善された形式が、drive service の image/video 分岐に正しく載ること。
// ここがずれるとサムネイル生成やセンシティブ判定が走らない。
func TestDetectedTypesRouteToCorrectPipeline(t *testing.T) {
	video := []string{"video/quicktime", "video/mp4", "video/x-m4v", "video/3gpp", "video/3gpp2", "video/mpeg"}
	for _, m := range video {
		assert.True(t, isMimeVideo(m), "%s must route to the video pipeline", m)
	}
	// 音声は video/ でも image でもない (サムネイル生成に回さない)。
	for _, m := range []string{"audio/x-m4a", "audio/flac", "audio/wav", "audio/aac", "audio/mpeg"} {
		assert.False(t, isMimeVideo(m), "%s must not route to the video pipeline", m)
		assert.False(t, isMimeImage(m), "%s must not route to the image pipeline", m)
	}
	// tiff は drive 側の decoder が対応しているので image 扱い。
	assert.True(t, isMimeImage("image/tiff"))
}

// 挙動変化の明示: 従来は `text/plain; charset=utf-8` のように charset 付きで
// drive_file.type に記録されていた。MIME 型の列に charset が混ざるのは不適切で、
// upstream (file-type の `type.mime`) も charset を持たない。判定入口で落とす。
//
// 落としても影響は無いことを確認する:
//   - `uploadableFileTypes` の `text/*` には引き続き一致する
//   - browser-safe allowlist には元々無いので、配信は従来どおり octet-stream
func TestDetectMIME_StripsCharsetParameter(t *testing.T) {
	text := []byte(strings.Repeat("plain text content\n", 16))
	got := DetectMIME(text)
	assert.Equal(t, "text/plain", got)
	assert.NotContains(t, got, "charset")
	assert.True(t, mimeAllowedByPolicy(got, []string{"text/*"}), "text/* policy に一致し続けること")
	assert.Equal(t, MIMEOctetStream, BrowserSafeContentType(got))
}
