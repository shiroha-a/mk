package mediaproxy

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsVideoMIME covers the helper that gates whether the proxy attempts
// video thumbnail extraction.
func TestIsVideoMIME(t *testing.T) {
	cases := map[string]bool{
		"video/mp4":       true,
		"video/webm":      true,
		"VIDEO/MP4":       true, // 大文字混じりも video/* と認識
		"image/png":       false,
		"application/pdf": false,
		"":                false,
		"video":           false, // missing slash
		"audio/mp4":       false,
	}
	for ct, want := range cases {
		assert.Equal(t, want, isVideoMIME(ct), "%q", ct)
	}
}

// 設定無しの video request は dummy PNG fallback (frontend は placeholder
// 表示で degrade)。
func TestProcessAndReturn_VideoFallback_NoGenerator(t *testing.T) {
	s := testService(nil)
	res, err := s.processAndReturn(context.Background(), []byte("fake video bytes"), "video/mp4", ModePreview, FormatWebP, "https://remote.example/clip.mp4")
	assert.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "image/png", res.ContentType)
}

// POST mode (default): mk-go は download 済 bytes を multipart で投げ、返
// ってきた静止画を image pipeline (resize → WebP) に通す。
func TestProcessAndReturn_VideoGeneratorRoundtrip_POST(t *testing.T) {
	gen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/thumbnail", r.URL.Path)
		// multipart body の field "file" に bytes が乗っていることを確認
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		require.NoError(t, err)
		mr := multipart.NewReader(r.Body, params["boundary"])
		part, err := mr.NextPart()
		require.NoError(t, err)
		assert.Equal(t, "file", part.FormName())
		body, _ := io.ReadAll(part)
		assert.Equal(t, "fake video bytes", string(body))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(makePNG())
	}))
	defer gen.Close()

	s := testService(nil)
	s.SetVideoThumbnailGenerator(gen.URL)
	s.videoThumbClient = gen.Client()

	res, err := s.processAndReturn(context.Background(), []byte("fake video bytes"), "video/mp4", ModePreview, FormatWebP, "https://remote.example/clip.mp4")
	require.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "image/webp", res.ContentType, "thumbnail must be re-encoded to webp")
}

// GET mode: Misskey TS 互換。クライアントは sourceURL を query で送り、
// generator 側が自分で fetch する。bytes は送らない。
func TestProcessAndReturn_VideoGeneratorRoundtrip_GET(t *testing.T) {
	gen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/thumbnail.webp", r.URL.Path)
		assert.Equal(t, "1", r.URL.Query().Get("thumbnail"))
		assert.Equal(t, "https://remote.example/clip.mp4", r.URL.Query().Get("url"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(makePNG())
	}))
	defer gen.Close()

	s := testService(nil)
	s.SetVideoThumbnailGeneratorWithMode(gen.URL, "get")
	s.videoThumbClient = gen.Client()

	res, err := s.processAndReturn(context.Background(), []byte("fake video bytes"), "video/mp4", ModePreview, FormatWebP, "https://remote.example/clip.mp4")
	require.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "image/webp", res.ContentType)
}

// Generator が 5xx を返した場合、proxy は dummy PNG にフォールバックして
// frontend を壊さない。
func TestProcessAndReturn_VideoGeneratorFailure(t *testing.T) {
	gen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer gen.Close()

	s := testService(nil)
	s.SetVideoThumbnailGenerator(gen.URL)
	s.videoThumbClient = gen.Client()

	res, err := s.processAndReturn(context.Background(), []byte("fake video bytes"), "video/mp4", ModePreview, FormatWebP, "https://remote.example/clip.mp4")
	require.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "image/png", res.ContentType)
}

// POST mode は bytes を送るので local /files/ source も同じ path で扱える
// (skip しない)。
func TestProcessAndReturn_VideoLocalSource_POSTForwardsBytes(t *testing.T) {
	hit := false
	gen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(makePNG())
	}))
	defer gen.Close()

	s := testService(nil)
	s.SetVideoThumbnailGenerator(gen.URL)
	s.videoThumbClient = gen.Client()

	res, err := s.processAndReturn(context.Background(), []byte("local video bytes"), "video/mp4", ModePreview, FormatWebP, "https://example.com/files/abc123")
	require.NoError(t, err)
	defer res.Body.Close()
	assert.True(t, hit, "POST mode forwards bytes regardless of source URL origin")
	assert.Equal(t, "image/webp", res.ContentType)
}

// videoThumbnailRequestURL builds the expected target for POST / GET wires
// over plain HTTP and UDS.
func TestVideoThumbnailRequestURL(t *testing.T) {
	t.Run("http_post", func(t *testing.T) {
		got, err := videoThumbnailRequestURL("https://thumb.example.com", "post", "")
		require.NoError(t, err)
		u, err := url.Parse(got)
		require.NoError(t, err)
		assert.Equal(t, "thumb.example.com", u.Host)
		assert.Equal(t, "/thumbnail", u.Path)
		assert.Empty(t, u.RawQuery)
	})
	t.Run("http_get", func(t *testing.T) {
		got, err := videoThumbnailRequestURL("https://thumb.example.com", "get", "https://video.example.com/clip.mp4")
		require.NoError(t, err)
		u, err := url.Parse(got)
		require.NoError(t, err)
		assert.Equal(t, "thumb.example.com", u.Host)
		assert.Equal(t, "/thumbnail.webp", u.Path)
		assert.Equal(t, "https://video.example.com/clip.mp4", u.Query().Get("url"))
	})
	t.Run("unix_post", func(t *testing.T) {
		got, err := videoThumbnailRequestURL("unix:///run/video-thumb.sock", "post", "")
		require.NoError(t, err)
		assert.Equal(t, "http://localhost/thumbnail", got)
	})
	t.Run("unix_get", func(t *testing.T) {
		got, err := videoThumbnailRequestURL("unix:///run/video-thumb.sock", "get", "https://video.example.com/clip.mp4")
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(got, "http://localhost/thumbnail.webp"))
		u, err := url.Parse(got)
		require.NoError(t, err)
		assert.Equal(t, "https://video.example.com/clip.mp4", u.Query().Get("url"))
	})
	t.Run("unix with stray query is ignored", func(t *testing.T) {
		got, err := videoThumbnailRequestURL("unix:///run/video-thumb.sock?token=xyz", "post", "")
		require.NoError(t, err)
		// Stray query on the unix:// URL must NOT leak into the request
		// URL (regression for the TrimRight(s, "") dead-code).
		assert.Equal(t, "http://localhost/thumbnail", got)
	})
}

// fetchVideoThumbnail surfaces ErrVideoThumbnailUnavailable when no client
// is configured.
func TestFetchVideoThumbnail_NoClient(t *testing.T) {
	s := testService(nil)
	_, _, err := s.fetchVideoThumbnail(context.Background(), []byte("x"), "video/mp4", "https://x/clip.mp4")
	assert.ErrorIs(t, err, ErrVideoThumbnailUnavailable)
}

// videoThumbnailClient: empty URL → nil client.
func TestNewVideoThumbnailClient_EmptyURL(t *testing.T) {
	assert.Nil(t, newVideoThumbnailClient(""))
}

// videoThumbnailClient: malformed unix:// URL → nil (no panic).
func TestNewVideoThumbnailClient_BadUnixURL(t *testing.T) {
	assert.Nil(t, newVideoThumbnailClient("unix://"))
}

// SetVideoThumbnailGeneratorWithMode: 不正な mode は "post" に倒れる。
func TestSetVideoThumbnailGeneratorWithMode_FallsBackToPOST(t *testing.T) {
	s := testService(nil)
	s.SetVideoThumbnailGeneratorWithMode("https://thumb.example", "ftp")
	assert.Equal(t, "post", s.videoThumbMode)
}

// extensionForMIME maps known video MIME types; unknown falls back to .bin
// (filename hint only — generator uses ffmpeg autodetect).
func TestExtensionForMIME(t *testing.T) {
	cases := map[string]string{
		"video/mp4":        ".mp4",
		"VIDEO/MP4":        ".mp4",
		"video/webm":       ".webm",
		"video/quicktime":  ".mov",
		"video/3gpp":       ".3gp",
		"video/3gpp2":      ".3g2",
		"video/mpeg":       ".mpg",
		"video/ogg":        ".ogv",
		"video/x-matroska": ".mkv",
		"video/mp2t":       ".ts",
		"":                 ".bin",
		"image/png":        ".bin",
	}
	for in, want := range cases {
		assert.Equal(t, want, extensionForMIME(in), "%q", in)
	}
}

// GET mode で local /files/ source は generator にループバックさせず dummy
// PNG fallback (review PR #646 #1)。
func TestProcessAndReturn_VideoLocalSource_GETSkipsGenerator(t *testing.T) {
	gen := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("GET mode must NOT call generator for local /files/ sources")
	}))
	defer gen.Close()

	s := testService(nil)
	s.SetVideoThumbnailGeneratorWithMode(gen.URL, "get")
	s.videoThumbClient = gen.Client()

	res, err := s.processAndReturn(context.Background(), []byte("local"), "video/mp4", ModePreview, FormatWebP, "https://example.com/files/abc123")
	require.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "image/png", res.ContentType, "must fall back to dummy PNG")
}

// POST mode + UDS の e2e roundtrip (review PR #646 #4)。tempfile unix
// socket に net.Listen → http.Server をかぶせて、mk-go の UDS dialer
// 経由で multipart POST が届くことを確認。
func TestProcessAndReturn_VideoGeneratorRoundtrip_UDS_POST(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "video-thumb.sock")
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	defer ln.Close()

	hit := make(chan struct{}, 1)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/thumbnail", r.URL.Path)
			_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			require.NoError(t, err)
			mr := multipart.NewReader(r.Body, params["boundary"])
			part, err := mr.NextPart()
			require.NoError(t, err)
			body, _ := io.ReadAll(part)
			assert.Equal(t, "uds video bytes", string(body))
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(makePNG())
			hit <- struct{}{}
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	s := testService(nil)
	s.SetVideoThumbnailGenerator("unix://" + sockPath)
	require.NotNil(t, s.videoThumbClient, "UDS client must be wired")

	res, err := s.processAndReturn(context.Background(), []byte("uds video bytes"), "video/mp4", ModePreview, FormatWebP, "https://remote.example/clip.mp4")
	require.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "image/webp", res.ContentType)
	select {
	case <-hit:
	case <-time.After(2 * time.Second):
		t.Fatal("UDS POST did not reach the generator within 2s")
	}
}

// Generator が image/* 以外の Content-Type を返した場合は dummy PNG に
// 倒し、raw HTML / JSON が proxy 経由で frontend に届くのを防ぐ
// (review PR #646 #3)。
func TestProcessAndReturn_VideoGeneratorNonImageRejected(t *testing.T) {
	gen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not an image</html>"))
	}))
	defer gen.Close()

	s := testService(nil)
	s.SetVideoThumbnailGenerator(gen.URL)
	s.videoThumbClient = gen.Client()

	res, err := s.processAndReturn(context.Background(), []byte("video bytes"), "video/mp4", ModePreview, FormatWebP, "https://remote.example/clip.mp4")
	require.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "image/png", res.ContentType, "non-image response must be rejected → dummy PNG")
}

// TestIsResizeMode covers the predicate used to gate video thumbnail logic.
func TestIsResizeMode(t *testing.T) {
	for _, m := range []ProxyMode{ModeEmoji, ModeAvatar, ModeStatic, ModePreview, ModeBadge} {
		assert.True(t, isResizeMode(m))
	}
	assert.False(t, isResizeMode(ModeDefault))
}

// #2106 N19: resize 系 mode は非変換 MIME (text/html 等) を 404 にする (生 bytes を
// 宣言 content-type で配信しない)。upstream FileServerProxyHandler の "Unexpected mime" 404。
func TestProcessAndReturn_ResizeMode_NonConvertibleMIME_NotFound(t *testing.T) {
	s := testService(nil)
	html := []byte("<html><body><script>alert(1)</script></body></html>")
	for _, mode := range []ProxyMode{ModeEmoji, ModeAvatar, ModeStatic, ModePreview, ModeBadge} {
		_, err := s.processAndReturn(context.Background(), html, "text/html", mode, FormatWebP, "https://remote.example/x.html")
		assert.ErrorIs(t, err, ErrNotFound, "mode=%v: 非変換 MIME は 404", mode)
	}
}
