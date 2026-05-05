package drive

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSensitivityThreshold(t *testing.T) {
	assert.Equal(t, 0.95, SensitivityThreshold("veryLow"))
	assert.Equal(t, 0.8, SensitivityThreshold("low"))
	assert.Equal(t, 0.5, SensitivityThreshold("medium"))
	assert.Equal(t, 0.3, SensitivityThreshold("high"))
	assert.Equal(t, 0.1, SensitivityThreshold("veryHigh"))
	assert.Equal(t, 0.5, SensitivityThreshold("unknown"))
}

func TestIsSilencedHost(t *testing.T) {
	hosts := []string{"bad.example.com", "spam.org"}

	assert.True(t, IsSilencedHost("bad.example.com", hosts))
	assert.True(t, IsSilencedHost("sub.bad.example.com", hosts))
	assert.True(t, IsSilencedHost("spam.org", hosts))
	assert.False(t, IsSilencedHost("good.example.com", hosts))
	assert.False(t, IsSilencedHost("", hosts))
	assert.False(t, IsSilencedHost("anything.com", nil))
}

func TestIsSilencedHost_EmptyEntries(t *testing.T) {
	assert.False(t, IsSilencedHost("anything.com", []string{"", "  "}))
}

func TestShouldDetect(t *testing.T) {
	assert.True(t, ShouldDetect(SensitiveConfig{Detection: "all"}, true))
	assert.True(t, ShouldDetect(SensitiveConfig{Detection: "all"}, false))
	assert.True(t, ShouldDetect(SensitiveConfig{Detection: "local"}, true))
	assert.False(t, ShouldDetect(SensitiveConfig{Detection: "local"}, false))
	assert.False(t, ShouldDetect(SensitiveConfig{Detection: "remote"}, true))
	assert.True(t, ShouldDetect(SensitiveConfig{Detection: "remote"}, false))
	assert.False(t, ShouldDetect(SensitiveConfig{Detection: "none"}, true))
	assert.False(t, ShouldDetect(SensitiveConfig{Detection: ""}, true))
}

func TestIsVideoMIME(t *testing.T) {
	assert.True(t, IsVideoMIME("video/mp4"))
	assert.True(t, IsVideoMIME("video/webm"))
	assert.False(t, IsVideoMIME("image/png"))
	assert.False(t, IsVideoMIME("audio/mp3"))
}

func TestHTTPDetector_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "image/png", r.Header.Get("Content-Type"))
		json.NewEncoder(w).Encode(map[string]any{"score": 0.85})
	}))
	defer srv.Close()

	d := NewHTTPDetector(srv.URL, nil)
	score, err := d.Detect(context.Background(), []byte("fake-image"), "image/png")
	require.NoError(t, err)
	assert.InDelta(t, 0.85, score, 0.001)
}

func TestHTTPDetector_NetworkError(t *testing.T) {
	d := NewHTTPDetector("http://127.0.0.1:1", nil)
	_, err := d.Detect(context.Background(), []byte("data"), "image/png")
	assert.Error(t, err)
}

func TestHTTPDetector_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	d := NewHTTPDetector(srv.URL, nil)
	_, err := d.Detect(context.Background(), []byte("data"), "image/png")
	assert.Error(t, err)
}

// --- detectSensitive (Service method) tests ---

// stubDetector returns the configured score / err.
type stubDetector struct {
	score float64
	err   error
}

func (s stubDetector) Detect(_ context.Context, _ []byte, _ string) (float64, error) {
	if s.err != nil {
		return 0, s.err
	}
	return s.score, nil
}

// detector=nil → false (no auto-detection)。
func TestDetectSensitive_NoDetector(t *testing.T) {
	s := &Service{}
	s.SetSensitiveDetection(nil, SensitiveConfig{
		Detection: "all", Sensitivity: "medium", SetFlagAutomatically: true,
	})
	assert.False(t, s.detectSensitive(&model.User{}, []byte("data"), "image/png"))
}

// SilencedHost match は detector を呼ばずに true を返す。
func TestDetectSensitive_SilencedHostShortCircuit(t *testing.T) {
	host := "bad.example.com"
	s := &Service{}
	s.SetSensitiveDetection(stubDetector{score: 0.0}, SensitiveConfig{
		Detection: "all", Sensitivity: "medium",
		SetFlagAutomatically: false, // silenced は flag 設定とは無関係
		SilencedHosts:        []string{"bad.example.com"},
	})
	assert.True(t, s.detectSensitive(&model.User{Host: &host}, []byte("data"), "image/png"))
}

// SetFlagAutomatically=false なら detector は呼ばれず false。
func TestDetectSensitive_FlagDisabled(t *testing.T) {
	s := &Service{}
	s.SetSensitiveDetection(stubDetector{score: 0.99}, SensitiveConfig{
		Detection: "all", Sensitivity: "medium", SetFlagAutomatically: false,
	})
	assert.False(t, s.detectSensitive(&model.User{}, []byte("data"), "image/png"))
}

// Detection mode が remote で local user → detector skip → false。
func TestDetectSensitive_DetectionModeMismatch(t *testing.T) {
	s := &Service{}
	s.SetSensitiveDetection(stubDetector{score: 0.99}, SensitiveConfig{
		Detection: "remote", Sensitivity: "medium", SetFlagAutomatically: true,
	})
	assert.False(t, s.detectSensitive(&model.User{}, []byte("data"), "image/png"))
}

// 動画 MIME + EnableForVideos=false → detector skip → false。
func TestDetectSensitive_VideoSkipWhenDisabled(t *testing.T) {
	s := &Service{}
	s.SetSensitiveDetection(stubDetector{score: 0.99}, SensitiveConfig{
		Detection: "all", Sensitivity: "medium", SetFlagAutomatically: true,
		EnableForVideos: false,
	})
	assert.False(t, s.detectSensitive(&model.User{}, []byte("data"), "video/mp4"))
}

// 動画 MIME + EnableForVideos=true + score>=threshold → true。
func TestDetectSensitive_VideoEnabledAndAboveThreshold(t *testing.T) {
	s := &Service{}
	s.SetSensitiveDetection(stubDetector{score: 0.7}, SensitiveConfig{
		Detection: "all", Sensitivity: "medium", SetFlagAutomatically: true,
		EnableForVideos: true,
	})
	assert.True(t, s.detectSensitive(&model.User{}, []byte("data"), "video/mp4"))
}

// score >= threshold → true。
func TestDetectSensitive_AboveThreshold(t *testing.T) {
	s := &Service{}
	s.SetSensitiveDetection(stubDetector{score: 0.7}, SensitiveConfig{
		Detection: "all", Sensitivity: "medium", SetFlagAutomatically: true,
	})
	assert.True(t, s.detectSensitive(&model.User{}, []byte("data"), "image/png"))
}

// score < threshold → false。
func TestDetectSensitive_BelowThreshold(t *testing.T) {
	s := &Service{}
	s.SetSensitiveDetection(stubDetector{score: 0.4}, SensitiveConfig{
		Detection: "all", Sensitivity: "medium", SetFlagAutomatically: true,
	})
	assert.False(t, s.detectSensitive(&model.User{}, []byte("data"), "image/png"))
}

// detector がエラーを返したら false (best-effort)。
func TestDetectSensitive_DetectorError(t *testing.T) {
	s := &Service{}
	s.SetSensitiveDetection(stubDetector{err: errors.New("net fail")}, SensitiveConfig{
		Detection: "all", Sensitivity: "medium", SetFlagAutomatically: true,
	})
	assert.False(t, s.detectSensitive(&model.User{}, []byte("data"), "image/png"))
}
