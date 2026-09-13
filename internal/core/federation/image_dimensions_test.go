package federation

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderTestPNG returns a minimal valid PNG of the given dimensions so
// the dimension probe can decode real image bytes (rather than relying
// on a hand-rolled fixture).
func renderTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestFetchImageDimensions_Success(t *testing.T) {
	body := renderTestPNG(t, 1280, 720)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	w, h, err := fetchImageDimensions(context.Background(), server.Client(), server.URL+"/cat.png")
	require.NoError(t, err)
	assert.Equal(t, 1280, w)
	assert.Equal(t, 720, h)
}

func TestFetchImageDimensions_NonImageContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>error page</html>"))
	}))
	defer server.Close()

	_, _, err := fetchImageDimensions(context.Background(), server.Client(), server.URL+"/x.png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-image content-type")
}

func TestFetchImageDimensions_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	_, _, err := fetchImageDimensions(context.Background(), server.Client(), server.URL+"/x.png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestFetchImageDimensions_DecodeFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("not actually a png"))
	}))
	defer server.Close()

	_, _, err := fetchImageDimensions(context.Background(), server.Client(), server.URL+"/x.png")
	require.Error(t, err)
}

func TestFetchImageDimensions_BadURL(t *testing.T) {
	_, _, err := fetchImageDimensions(context.Background(), http.DefaultClient, "://invalid")
	require.Error(t, err)
}

// SSRF 対策 (PR #464 review): probeImageDimensions に nil client を渡すと
// http.DefaultClient へフォールバックせず即座に skip する。fetchImageDimensions
// 単体は test 用に DefaultClient フォールバックを残しているが、production
// 経路の probeImageDimensions では required にする。
func TestProbeImageDimensions_NilClientReturnsFalse(t *testing.T) {
	w, h, ok := probeImageDimensions(context.Background(), nil, "https://attacker.example/x.png")
	assert.False(t, ok, "nil client should not perform any HTTP request")
	assert.Equal(t, 0, w)
	assert.Equal(t, 0, h)
}

func TestProbeImageDimensions_Success(t *testing.T) {
	body := renderTestPNG(t, 800, 600)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	w, h, ok := probeImageDimensions(context.Background(), server.Client(), server.URL+"/cat.png")
	require.True(t, ok)
	assert.Equal(t, 800, w)
	assert.Equal(t, 600, h)
}

func TestProbeImageDimensions_FailureReturnsFalse(t *testing.T) {
	// HTTP error 経路 → fetchImageDimensions が err を返し probe は
	// WARN ログを残して (0, 0, false) を返す。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	w, h, ok := probeImageDimensions(context.Background(), server.Client(), server.URL+"/x.png")
	assert.False(t, ok)
	assert.Equal(t, 0, w)
	assert.Equal(t, 0, h)
}

func TestFetchImageDimensions_NilClientUsesDefault(t *testing.T) {
	body := renderTestPNG(t, 10, 20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	defer server.Close()
	// nil client は http.DefaultClient にフォールバックする
	w, h, err := fetchImageDimensions(context.Background(), nil, server.URL+"/x.png")
	require.NoError(t, err)
	assert.Equal(t, 10, w)
	assert.Equal(t, 20, h)
}
