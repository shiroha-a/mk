package mediaproxy

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	jpeg2000 "github.com/mrjoshuak/go-jpeg2000"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- mock AllowlistChecker ---

type mockAllowlist struct {
	allowed map[string]bool
}

func (m *mockAllowlist) IsAllowedURL(_ context.Context, url string) (bool, error) {
	return m.allowed[url], nil
}

// --- mock Storage ---

type mockStorage struct {
	files map[string][]byte
}

func (m *mockStorage) Put(_ string, _ io.Reader) (string, error) { return "", nil }
func (m *mockStorage) Delete(_ string) error                     { return nil }
func (m *mockStorage) Get(key string) (io.ReadCloser, error) {
	data, ok := m.files[key]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return io.NopCloser(io.Reader(io.NopCloser(
		&byteReadCloser{data: data, pos: 0},
	))), nil
}

type byteReadCloser struct {
	data []byte
	pos  int
}

func (b *byteReadCloser) Read(p []byte) (int, error) {
	if b.pos >= len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.pos:])
	b.pos += n
	return n, nil
}
func (b *byteReadCloser) Close() error { return nil }

// --- test helpers ---

// testAllowedCIDRs はテストでhttptest (127.0.0.1) への接続を許可するCIDRリスト
var testAllowedCIDRs = []string{"127.0.0.0/8", "::1/128"}

func testService(allowedURLs map[string]bool) *Service {
	return NewService(
		"https://example.com",
		"Misskey/2026.5.1 (https://example.com)",
		&mockStorage{files: map[string][]byte{}},
		&mockAllowlist{allowed: allowedURLs},
		[]byte("test-secret"),
		testAllowedCIDRs,
	)
}

func makePNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	w := &byteWriter{}
	_ = png.Encode(w, img)
	return w.data
}

type byteWriter struct {
	data []byte
}

func (w *byteWriter) Write(p []byte) (int, error) {
	w.data = append(w.data, p...)
	return len(p), nil
}

// --- tests ---

func TestAuthorize_ValidHMAC(t *testing.T) {
	s := testService(nil)
	url := "https://remote.example/avatar.png"
	sig := s.SignURL(url)

	err := s.Authorize(context.Background(), url, sig)
	assert.NoError(t, err)
}

func TestAuthorize_InvalidHMAC_AllowlistedURL(t *testing.T) {
	s := testService(map[string]bool{
		"https://remote.example/avatar.png": true,
	})

	err := s.Authorize(context.Background(), "https://remote.example/avatar.png", "wrong-sig")
	assert.NoError(t, err)
}

func TestAuthorize_NoSig_AllowlistedURL(t *testing.T) {
	s := testService(map[string]bool{
		"https://remote.example/avatar.png": true,
	})

	err := s.Authorize(context.Background(), "https://remote.example/avatar.png", "")
	assert.NoError(t, err)
}

func TestAuthorize_NoSig_NotAllowlisted(t *testing.T) {
	s := testService(map[string]bool{})

	err := s.Authorize(context.Background(), "https://evil.example/malware.exe", "")
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestAuthorize_InvalidHMAC_NotAllowlisted(t *testing.T) {
	s := testService(map[string]bool{})

	err := s.Authorize(context.Background(), "https://evil.example/malware.exe", "bad-sig")
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestFetch_RemoteImage(t *testing.T) {
	imgData := makePNG()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(imgData)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/img.png": true})

	result, err := s.Fetch(context.Background(), ts.URL+"/img.png", ModeDefault, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()

	assert.Equal(t, "image/png", result.ContentType)

	body, _ := io.ReadAll(result.Body)
	assert.NotEmpty(t, body)
}

func TestFetch_RemoteImage_Emoji(t *testing.T) {
	imgData := makePNG()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(imgData)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/emoji.png": true})

	result, err := s.Fetch(context.Background(), ts.URL+"/emoji.png", ModeEmoji, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()

	assert.Equal(t, "image/webp", result.ContentType)
}

func TestFetch_RemoteImage_Avatar(t *testing.T) {
	imgData := makePNG()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(imgData)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/avatar.png": true})

	result, err := s.Fetch(context.Background(), ts.URL+"/avatar.png", ModeAvatar, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()

	assert.Equal(t, "image/webp", result.ContentType)
}

func TestFetch_RemoteImage_Static(t *testing.T) {
	imgData := makePNG()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(imgData)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/img.png": true})

	result, err := s.Fetch(context.Background(), ts.URL+"/img.png", ModeStatic, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()

	assert.Equal(t, "image/webp", result.ContentType)
}

func TestFetch_RemoteImage_Preview(t *testing.T) {
	imgData := makePNG()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(imgData)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/img.png": true})

	result, err := s.Fetch(context.Background(), ts.URL+"/img.png", ModePreview, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()

	assert.Equal(t, "image/webp", result.ContentType)
}

func TestFetch_RemoteImage_Badge(t *testing.T) {
	imgData := makePNG()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(imgData)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/img.png": true})

	result, err := s.Fetch(context.Background(), ts.URL+"/img.png", ModeBadge, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()

	assert.Equal(t, "image/png", result.ContentType)
}

// アニメ GIF 等の multi-frame 形式は emoji / avatar / preview mode で
// pass-through されることを確認する (#941)。Go の image.Decode は 1 frame
// しか返さないため resize 経路に乗せると静止化してしまうため。
func TestFetch_RemoteImage_AnimatedGIF_PassThroughOnEmoji(t *testing.T) {
	gifData := []byte("GIF89a") // valid な animation でなくとも MIME type で判定される
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/gif")
		w.Write(gifData)
	}))
	defer ts.Close()
	s := testService(map[string]bool{ts.URL + "/anim.gif": true})

	for _, mode := range []ProxyMode{ModeEmoji, ModeAvatar, ModePreview} {
		result, err := s.Fetch(context.Background(), ts.URL+"/anim.gif", mode, FormatWebP)
		require.NoError(t, err, "mode=%v", mode)
		assert.Equal(t, "image/gif", result.ContentType, "mode=%v should preserve animated MIME", mode)
		result.Body.Close()
	}
}

func TestFetch_RemoteImage_AnimatedAPNG_PassThroughOnEmoji(t *testing.T) {
	pngData := makePNG()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/apng")
		w.Write(pngData)
	}))
	defer ts.Close()
	s := testService(map[string]bool{ts.URL + "/anim.apng": true})

	result, err := s.Fetch(context.Background(), ts.URL+"/anim.apng", ModeEmoji, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()
	assert.Equal(t, "image/apng", result.ContentType)
}

func TestIsAnimatedFormat(t *testing.T) {
	assert.True(t, isAnimatedFormat("image/gif"))
	assert.True(t, isAnimatedFormat("image/apng"))
	assert.True(t, isAnimatedFormat("image/vnd.mozilla.apng"))
	assert.False(t, isAnimatedFormat("image/png"))
	assert.False(t, isAnimatedFormat("image/jpeg"))
	assert.False(t, isAnimatedFormat("image/webp"))
	assert.False(t, isAnimatedFormat(""))
}

func TestFetch_Remote404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/missing.png": true})

	_, err := s.Fetch(context.Background(), ts.URL+"/missing.png", ModeDefault, FormatWebP)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestFetch_LocalFile(t *testing.T) {
	imgData := makePNG()

	s := NewService(
		"https://example.com",
		"Misskey/2026.5.1 (https://example.com)",
		&mockStorage{files: map[string][]byte{"abc123": imgData}},
		&mockAllowlist{allowed: map[string]bool{}},
		[]byte("test-secret"),
		nil,
	)

	result, err := s.Fetch(context.Background(), "https://example.com/files/abc123", ModeDefault, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()

	assert.Equal(t, "image/png", result.ContentType)
}

// favicon.ico を返すリモートサーバーは IANA 公式 MIME `image/vnd.microsoft.icon`
// を使う場合がある (#418)。古い慣例の `image/x-icon` だけで safe-list を組むと
// 多くのリモートホストの favicon が unsafe MIME 拒否で 500 になり、UI 上で
// instance ticker のアイコンが消える。両 alias とも許容することを確認する。
func TestFetch_FaviconWithIANAMIMETypeAccepted(t *testing.T) {
	imgData := []byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x10, 0x10} // bogus ico header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/vnd.microsoft.icon")
		w.Write(imgData)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/favicon.ico": true})

	result, err := s.Fetch(context.Background(), ts.URL+"/favicon.ico", ModeDefault, FormatWebP)
	require.NoError(t, err, "image/vnd.microsoft.icon must pass through")
	defer result.Body.Close()
	assert.Equal(t, "image/vnd.microsoft.icon", result.ContentType)
}

func TestFetch_ContentTypeWithParameters(t *testing.T) {
	// Content-Type に charset 等のパラメータが付いていても media type だけで
	// allowlist 照合する (#418 Devin review)。
	imgData := makePNG()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png; charset=binary")
		w.Write(imgData)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/img.png": true})

	result, err := s.Fetch(context.Background(), ts.URL+"/img.png", ModeDefault, FormatWebP)
	require.NoError(t, err, "media type should match after stripping parameters")
	defer result.Body.Close()
	assert.Equal(t, "image/png", result.ContentType)
}

func TestFetch_FaviconWithLegacyMIMETypeAccepted(t *testing.T) {
	// 古い慣例 image/x-icon も引き続き許可する (regression guard)。
	imgData := []byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x10, 0x10}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/x-icon")
		w.Write(imgData)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/favicon.ico": true})

	result, err := s.Fetch(context.Background(), ts.URL+"/favicon.ico", ModeDefault, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()
	assert.Equal(t, "image/x-icon", result.ContentType)
}

func TestFetch_UnsafeMIME_Rejected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte("alert('xss')"))
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/evil.js": true})

	_, err := s.Fetch(context.Background(), ts.URL+"/evil.js", ModeDefault, FormatWebP)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rejected MIME type")
}

func TestDummyPNG(t *testing.T) {
	result := DummyPNG()
	defer result.Body.Close()
	assert.Equal(t, "image/png", result.ContentType)
	data, _ := io.ReadAll(result.Body)
	assert.NotEmpty(t, data)
}

func TestIsConvertibleImage(t *testing.T) {
	tests := []struct {
		mime string
		want bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"image/gif", true},
		{"image/webp", true},
		{"image/bmp", true},
		{"image/tiff", true},
		// IANA 公式名 (vnd.microsoft.icon) と古い慣例 (x-icon) を両方許可 (#418)
		{"image/x-icon", true},
		{"image/vnd.microsoft.icon", true},
		// #672 Phase 1: pure Go decoder で input transcode 対応
		{"image/x-portable-bitmap", true},
		{"image/x-portable-graymap", true},
		{"image/x-portable-pixmap", true},
		{"image/x-portable-anymap", true},
		{"image/x-tga", true},
		{"image/x-targa", true},
		// #734: pure Go JPEG 2000 decoder (mrjoshuak/go-jpeg2000)。
		// JP2/J2K は完全対応、JPX (Part 2) は library 未対応のため pass-through
		// のみで convertible では false。
		{"image/jp2", true},
		{"image/jpeg2000", true},
		{"image/jpx", false},
		// #672 Phase 1 partial: JXR / MNG は decode library が無く pass-through 用
		// にのみ browsersafe 許可。convertible では無いので false。
		{"image/jxr", false},
		{"video/x-mng", false},
		{"image/svg+xml", false},
		{"video/mp4", false},
		{"text/plain", false},
	}
	for _, tt := range tests {
		t.Run(tt.mime, func(t *testing.T) {
			assert.Equal(t, tt.want, isConvertibleImage(tt.mime))
		})
	}
}

func TestBrowsersafeMIMEs(t *testing.T) {
	assert.True(t, browsersafeMIMEs["image/png"])
	assert.True(t, browsersafeMIMEs["video/mp4"])
	assert.True(t, browsersafeMIMEs["audio/mpeg"])
	// favicon.ico の MIME alias 両方を許可 (#418)
	assert.True(t, browsersafeMIMEs["image/x-icon"])
	assert.True(t, browsersafeMIMEs["image/vnd.microsoft.icon"])
	// #672 Phase 1 で追加した formats は browsersafe (decode 経路あり or
	// pass-through) で受け入れる
	assert.True(t, browsersafeMIMEs["image/x-portable-pixmap"])
	assert.True(t, browsersafeMIMEs["image/x-tga"])
	assert.True(t, browsersafeMIMEs["image/jp2"])
	assert.True(t, browsersafeMIMEs["image/jpeg2000"])
	assert.True(t, browsersafeMIMEs["image/jxr"])
	assert.True(t, browsersafeMIMEs["video/x-mng"])
	assert.False(t, browsersafeMIMEs["application/javascript"])
	assert.False(t, browsersafeMIMEs["text/html"])
}

func TestFetch_SVG_ReturnsDummyPNG(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="10" height="10"/></svg>`))
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/icon.svg": true})

	result, err := s.Fetch(context.Background(), ts.URL+"/icon.svg", ModeDefault, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()

	// SVGはXSSリスクがあるためダミーPNGにフォールバック
	assert.Equal(t, "image/png", result.ContentType)
}

func TestFetch_LocalFile_NotFound(t *testing.T) {
	s := NewService(
		"https://example.com",
		"Misskey/2026.5.1 (https://example.com)",
		&mockStorage{files: map[string][]byte{}},
		&mockAllowlist{allowed: map[string]bool{}},
		[]byte("test-secret"),
		nil,
	)

	_, err := s.Fetch(context.Background(), "https://example.com/files/nonexistent", ModeDefault, FormatWebP)
	assert.Error(t, err)
}

func TestFetch_LocalFile_EmptyAccessKey(t *testing.T) {
	s := NewService(
		"https://example.com",
		"Misskey/2026.5.1 (https://example.com)",
		&mockStorage{files: map[string][]byte{}},
		&mockAllowlist{allowed: map[string]bool{}},
		[]byte("test-secret"),
		nil,
	)

	_, err := s.Fetch(context.Background(), "https://example.com/files/", ModeDefault, FormatWebP)
	assert.ErrorIs(t, err, ErrBadRequest)
}

func TestFetch_LocalFile_WithPathSegments(t *testing.T) {
	imgData := makePNG()
	s := NewService(
		"https://example.com",
		"Misskey/2026.5.1 (https://example.com)",
		&mockStorage{files: map[string][]byte{"abc123": imgData}},
		&mockAllowlist{allowed: map[string]bool{}},
		[]byte("test-secret"),
		nil,
	)

	// /files/abc123/extra のようなパスでもabc123だけ使う
	result, err := s.Fetch(context.Background(), "https://example.com/files/abc123/extra", ModeDefault, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()
	assert.Equal(t, "image/png", result.ContentType)
}

func TestFetch_LocalFile_Emoji(t *testing.T) {
	imgData := makePNG()
	s := NewService(
		"https://example.com",
		"Misskey/2026.5.1 (https://example.com)",
		&mockStorage{files: map[string][]byte{"emoji1": imgData}},
		&mockAllowlist{allowed: map[string]bool{}},
		[]byte("test-secret"),
		nil,
	)

	result, err := s.Fetch(context.Background(), "https://example.com/files/emoji1", ModeEmoji, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()
	assert.Equal(t, "image/webp", result.ContentType)
}

func TestFetch_RemoteServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/error.png": true})

	_, err := s.Fetch(context.Background(), ts.URL+"/error.png", ModeDefault, FormatWebP)
	assert.Error(t, err)
}

func TestFetch_RemoteGone(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/deleted.png": true})

	_, err := s.Fetch(context.Background(), ts.URL+"/deleted.png", ModeDefault, FormatWebP)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestFetch_RemoteNoContentType(t *testing.T) {
	imgData := makePNG()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Content-Typeヘッダなしで返す
		w.Write(imgData)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/img.png": true})

	result, err := s.Fetch(context.Background(), ts.URL+"/img.png", ModeDefault, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()
	// auto-detected from content
	assert.Equal(t, "image/png", result.ContentType)
}

func TestProcessResize_NonConvertibleImage(t *testing.T) {
	s := testService(nil)
	data := []byte("not an image")
	result, err := s.processResize(data, "application/octet-stream", 100, 100, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()
	// 変換できない場合はそのまま返す
	assert.Equal(t, "application/octet-stream", result.ContentType)
}

func TestProcessBadge_NonConvertibleImage(t *testing.T) {
	s := testService(nil)
	data := []byte("not an image")
	result, err := s.processBadge(data, "text/plain")
	require.NoError(t, err)
	defer result.Body.Close()
	assert.Equal(t, "text/plain", result.ContentType)
}

func TestResizeToHeight_SmallImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 50, 50))
	result := resizeToHeight(img, 128)
	// 元画像が128以下なので拡大されない
	assert.Equal(t, 50, result.Bounds().Dy())
}

func TestResizeFit_SmallImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 50, 50))
	result := resizeFit(img, 498, 422)
	// 元画像が範囲内なのでそのまま
	assert.Equal(t, 50, result.Bounds().Dx())
	assert.Equal(t, 50, result.Bounds().Dy())
}

func TestDecodeImage_InvalidData(t *testing.T) {
	_, err := decodeImage([]byte("not an image"), "image/jpeg")
	assert.Error(t, err)
}

// TestDecodeImage_PPM は #672 Phase 1 で追加した Netpbm 系 (spakin/netpbm)
// の input decode 経路が standard image.Decode 経由で動作することを確認する。
// 単純な 2x2 PPM (P3) を decode して bounds が期待通りなら OK。
func TestDecodeImage_PPM(t *testing.T) {
	// P3 = ASCII PPM, 2x2, max=255。R G B が 4 ピクセル分。
	ppm := []byte("P3\n2 2\n255\n255 0 0 0 255 0 0 0 255 255 255 0\n")
	img, err := decodeImage(ppm, "image/x-portable-pixmap")
	require.NoError(t, err)
	assert.Equal(t, 2, img.Bounds().Dx())
	assert.Equal(t, 2, img.Bounds().Dy())
}

// TestFetch_PassThrough_JXR は #672 Phase 1 partial: JPEG XR / MNG family
// が browsersafeMIMEs に追加されたことで 415 で reject されず pass-through
// で raw bytes が配信されることを assert する。pure Go decoder 無しの
// formats が browser ネイティブ対応 (Edge legacy / viewer plugin) に委譲
// される経路の regression guard。
func TestFetch_PassThrough_JXR(t *testing.T) {
	// 適当なバイト列 (実 JXR ファイル相当の最小マジック)。decoder は通らない
	// ので中身が valid である必要は無い。
	jxrBytes := []byte("II\xbc\x01" + "fakejxrpayload")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jxr")
		w.Write(jxrBytes)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/img.jxr": true})
	result, err := s.Fetch(context.Background(), ts.URL+"/img.jxr", ModeDefault, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()

	assert.Equal(t, "image/jxr", result.ContentType, "JXR は pass-through で content-type 維持")
	body, _ := io.ReadAll(result.Body)
	assert.Equal(t, jxrBytes, body, "raw bytes がそのまま返る (transcode しない)")
}

// TestFetch_PassThrough_MNG は MNG が同様に pass-through されることを確認。
func TestFetch_PassThrough_MNG(t *testing.T) {
	mngBytes := []byte("\x8aMNG\r\n\x1a\n" + "fakemngpayload")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/x-mng")
		w.Write(mngBytes)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/anim.mng": true})
	result, err := s.Fetch(context.Background(), ts.URL+"/anim.mng", ModeDefault, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()

	assert.Equal(t, "video/x-mng", result.ContentType)
	body, _ := io.ReadAll(result.Body)
	assert.Equal(t, mngBytes, body)
}

// TestDecodeImage_JP2 は #734 で追加した JPEG 2000 pure Go decoder
// (mrjoshuak/go-jpeg2000) の input 経路を確認する。Encode → Decode の
// round-trip で bounds が保たれれば OK。標準 image.Decode 経由で動作する。
func TestDecodeImage_JP2(t *testing.T) {
	// 2x2 RGB image を JP2 で encode してから decode で読み直す
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	src.Set(0, 0, color.RGBA{R: 255, A: 255})
	src.Set(1, 0, color.RGBA{G: 255, A: 255})
	src.Set(0, 1, color.RGBA{B: 255, A: 255})
	src.Set(1, 1, color.RGBA{R: 255, G: 255, A: 255})

	var buf bytes.Buffer
	require.NoError(t, jpeg2000.Encode(&buf, src, nil), "encode JP2")
	require.NotEmpty(t, buf.Bytes())

	img, err := decodeImage(buf.Bytes(), "image/jp2")
	require.NoError(t, err)
	assert.Equal(t, 2, img.Bounds().Dx())
	assert.Equal(t, 2, img.Bounds().Dy())
}

// TestDecodeImage_TGA は #672 Phase 1 で追加した TGA decoder (blezek/tga)
// が contentType 経由で manual dispatch されることを確認する。
// blezek/tga は magic bytes 無し format のため image.RegisterFormat には載
// せず、decodeImage 内で MIME type 判定で dispatch する設計。
func TestDecodeImage_TGA(t *testing.T) {
	// 最小 uncompressed TGA v2: header 18 + pixel 4 + footer 26 = 48 bytes。
	// footer は v2 signature "TRUEVISION-XFILE" を含み、blezek/tga が
	// SeekEnd-26 で読みに行く (footer 無いと "negative position" エラー)。
	header := []byte{
		0,    // ID length
		0,    // color map type (none)
		2,    // image type: uncompressed true-color
		0, 0, // color map first index
		0, 0, // color map length
		0,    // color map entry size
		0, 0, // x origin
		0, 0, // y origin
		1, 0, // width = 1
		1, 0, // height = 1
		32,   // bpp
		0x28, // image descriptor: top-left origin (bit5=1) + 8 alpha bits
	}
	pixel := []byte{0x00, 0xff, 0x00, 0xff} // BGRA pixel (green opaque)
	footer := make([]byte, 26)
	// 4 bytes extension offset = 0, 4 bytes developer offset = 0
	copy(footer[8:], "TRUEVISION-XFILE.\x00")
	tgaBytes := append(append(header, pixel...), footer...)

	img, err := decodeImage(tgaBytes, "image/x-tga")
	require.NoError(t, err)
	assert.Equal(t, 1, img.Bounds().Dx())
	assert.Equal(t, 1, img.Bounds().Dy())
}

func TestEncodeWebP(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	data, err := encodeWebP(img)
	require.NoError(t, err)
	assert.NotEmpty(t, data)
}

func TestSignURL_MatchesStandalone(t *testing.T) {
	s := testService(nil)
	url := "https://example.com/test.png"
	sig := s.SignURL(url)
	assert.Equal(t, SignURL([]byte("test-secret"), url), sig)
}

func TestResizeToHeight_LargeImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 500, 500))
	result := resizeToHeight(img, 128)
	assert.Equal(t, 128, result.Bounds().Dy())
}

func TestResizeFit_LargeImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1000, 1000))
	result := resizeFit(img, 200, 200)
	assert.LessOrEqual(t, result.Bounds().Dx(), 200)
	assert.LessOrEqual(t, result.Bounds().Dy(), 200)
}

func TestFetch_Remote_InvalidURL(t *testing.T) {
	s := testService(map[string]bool{"not://valid": true})

	_, err := s.Fetch(context.Background(), "not://valid", ModeDefault, FormatWebP)
	assert.Error(t, err)
}

func TestProcessResize_LargeImage_WidthAndHeight(t *testing.T) {
	s := testService(nil)
	imgData := makePNG() // 100x100

	result, err := s.processResize(imgData, "image/png", 50, 50, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()
	assert.Equal(t, "image/webp", result.ContentType)
}

func TestProcessResize_HeightOnly(t *testing.T) {
	s := testService(nil)
	imgData := makePNG() // 100x100

	result, err := s.processResize(imgData, "image/png", 0, 50, FormatWebP)
	require.NoError(t, err)
	defer result.Body.Close()
	assert.Equal(t, "image/webp", result.ContentType)
}

func TestProcessBadge_ValidImage(t *testing.T) {
	s := testService(nil)
	imgData := makePNG() // 100x100

	result, err := s.processBadge(imgData, "image/png")
	require.NoError(t, err)
	defer result.Body.Close()
	assert.Equal(t, "image/png", result.ContentType)
}

func TestAuthorize_AllowlistError(t *testing.T) {
	// errorAllowlistはIsAllowedURLでエラーを返す
	s := NewService(
		"https://example.com",
		"Misskey/2026.5.1 (https://example.com)",
		&mockStorage{files: map[string][]byte{}},
		&errorAllowlist{},
		[]byte("test-secret"),
		nil,
	)

	err := s.Authorize(context.Background(), "https://example.com/img.png", "")
	assert.ErrorIs(t, err, ErrUnauthorized)
}

type errorAllowlist struct{}

func (e *errorAllowlist) IsAllowedURL(_ context.Context, _ string) (bool, error) {
	return false, fmt.Errorf("db connection failed")
}

func TestFetch_LocalFile_TooLarge(t *testing.T) {
	// maxDownloadを超えるローカルファイル
	bigData := make([]byte, maxDownload+100)
	s := NewService(
		"https://example.com",
		"Misskey/2026.5.1 (https://example.com)",
		&mockStorage{files: map[string][]byte{"big": bigData}},
		&mockAllowlist{allowed: map[string]bool{}},
		[]byte("test-secret"),
		nil,
	)

	_, err := s.Fetch(context.Background(), "https://example.com/files/big", ModeDefault, FormatWebP)
	assert.ErrorIs(t, err, ErrTooLarge)
}

func TestFetch_Remote_ContentLengthExceedsMax(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Content-Lengthが maxDownload を超える値を設定
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "999999999")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/huge.png": true})

	_, err := s.Fetch(context.Background(), ts.URL+"/huge.png", ModeDefault, FormatWebP)
	assert.ErrorIs(t, err, ErrTooLarge)
}

func TestFetch_Remote_BodyExceedsMaxNoContentLength(t *testing.T) {
	// Content-LengthなしでmaxDownloadを超えるボディを返す
	// テストではmaxDownloadの実値(32MB)を使うと遅いので、
	// 小さいサービスを作って検証する
	bigBody := make([]byte, maxDownload+100)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(bigBody)
	}))
	defer ts.Close()

	s := testService(map[string]bool{ts.URL + "/huge.png": true})

	_, err := s.Fetch(context.Background(), ts.URL+"/huge.png", ModeDefault, FormatWebP)
	assert.ErrorIs(t, err, ErrTooLarge)
}
