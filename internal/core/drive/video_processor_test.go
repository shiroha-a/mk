package drive

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// mock CommandRunner
// ---------------------------------------------------------------------------

type mockCommandRunner struct {
	// outputFile に PNG データを書き込んで「FFmpeg が成功した」ように見せる
	writePNG bool
	err      error
}

func (m *mockCommandRunner) Run(name string, args ...string) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.writePNG {
		// args の最後が出力ファイルパス
		outPath := args[len(args)-1]
		// テスト用の小さな PNG を書き込む
		pngData := makeTestPNG(640, 480)
		if err := os.WriteFile(outPath, pngData, 0o644); err != nil {
			return nil, err
		}
	}
	return []byte("ok"), nil
}

// mockImageProcessor はテスト用の ImageProcessor。
type mockImageProcessor struct {
	thumbnail *ProcessedImage
	err       error
}

func (m *mockImageProcessor) GenerateThumbnail(_ []byte, _ string) (*ProcessedImage, error) {
	return m.thumbnail, m.err
}

func (m *mockImageProcessor) GenerateWebpublic(_ []byte, _ string) (*ProcessedImage, error) {
	return nil, nil
}

func (m *mockImageProcessor) GetDimensions(_ []byte, _ string) (int, int, error) {
	return 0, 0, nil
}

func (m *mockImageProcessor) CalculateBlurhash(_ []byte, _ string) (string, error) {
	return "", nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestVideoProcessor_Success(t *testing.T) {
	imgProc := &mockImageProcessor{
		thumbnail: &ProcessedImage{Data: []byte("webp-data"), MimeType: "image/webp"},
	}
	runner := &mockCommandRunner{writePNG: true}
	vp := NewFFmpegVideoProcessor(imgProc, runner)

	result, err := vp.GenerateThumbnail([]byte("video-data"), "video/mp4")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "image/webp", result.MimeType)
	assert.Equal(t, []byte("webp-data"), result.Data)
}

func TestVideoProcessor_FFmpegError(t *testing.T) {
	runner := &mockCommandRunner{err: errors.New("ffmpeg not found")}
	vp := NewFFmpegVideoProcessor(nil, runner)

	result, err := vp.GenerateThumbnail([]byte("video-data"), "video/mp4")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestVideoProcessor_NotVideo(t *testing.T) {
	vp := NewFFmpegVideoProcessor(nil, &mockCommandRunner{})

	result, err := vp.GenerateThumbnail([]byte("data"), "image/jpeg")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestVideoProcessor_NilImageProcessor(t *testing.T) {
	runner := &mockCommandRunner{writePNG: true}
	vp := NewFFmpegVideoProcessor(nil, runner)

	result, err := vp.GenerateThumbnail([]byte("video-data"), "video/mp4")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestVideoProcessor_EmptyOutputFile(t *testing.T) {
	// FFmpeg は成功するが出力ファイルが空
	runner := &mockCommandRunner{writePNG: false}
	vp := NewFFmpegVideoProcessor(nil, runner)

	result, err := vp.GenerateThumbnail([]byte("video-data"), "video/mp4")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestVideoProcessor_DefaultRunner(t *testing.T) {
	// runner=nil の場合は ExecCommandRunner が使われることを確認
	vp := NewFFmpegVideoProcessor(nil, nil)
	assert.NotNil(t, vp.runner)
	_, ok := vp.runner.(*ExecCommandRunner)
	assert.True(t, ok)
}

func TestExecCommandRunner_Run(t *testing.T) {
	r := &ExecCommandRunner{}
	out, err := r.Run("echo", "hello")
	require.NoError(t, err)
	assert.Contains(t, string(out), "hello")
}

func TestExecCommandRunner_RunError(t *testing.T) {
	r := &ExecCommandRunner{}
	_, err := r.Run("nonexistent-command-xyz")
	assert.Error(t, err)
}

func TestVideoProcessor_MkdirTempError(t *testing.T) {
	restore := SetOsMkdirTempForTest(func(string, string) (string, error) {
		return "", errors.New("mkdirtemp error")
	})
	defer restore()

	vp := NewFFmpegVideoProcessor(nil, &mockCommandRunner{})
	result, err := vp.GenerateThumbnail([]byte("video"), "video/mp4")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestVideoProcessor_WriteFileError(t *testing.T) {
	restore := SetOsWriteFileForTest(func(string, []byte, os.FileMode) error {
		return errors.New("writefile error")
	})
	defer restore()

	vp := NewFFmpegVideoProcessor(nil, &mockCommandRunner{})
	result, err := vp.GenerateThumbnail([]byte("video"), "video/mp4")
	require.NoError(t, err)
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// ExtractDetectionFrames (公式 sensitive-detector 用フレーム抽出)
// ---------------------------------------------------------------------------

// frameWritingRunner は ffmpeg の %d.png 連番出力を模倣する。
type frameWritingRunner struct {
	frames int
	err    error
	// 記録: 呼び出し時の args (filter 指定の検証用)。
	gotArgs []string
}

func (m *frameWritingRunner) Run(name string, args ...string) ([]byte, error) {
	m.gotArgs = args
	if m.err != nil {
		return nil, m.err
	}
	// args の最後が %d.png パターンの出力パス。
	pattern := args[len(args)-1]
	for i := 1; i <= m.frames; i++ {
		path := strings.ReplaceAll(pattern, "%d", strconv.Itoa(i))
		// フレーム番号を内容にして、どのフレームが選ばれたか検証できるようにする。
		if err := os.WriteFile(path, []byte("frame-"+strconv.Itoa(i)), 0o644); err != nil {
			return nil, err
		}
	}
	return []byte("ok"), nil
}

func TestExtractDetectionFrames_SamplesUpstreamIndices(t *testing.T) {
	runner := &frameWritingRunner{frames: 6}
	p := NewFFmpegVideoProcessor(nil, runner)
	frames, err := p.ExtractDetectionFrames([]byte("video"))
	require.NoError(t, err)
	// upstream の index 間引きループは index 0, 1 を選んで停止する
	// (targetIndex が 1 のまま進まなくなる)。挙動一致を固定する。
	require.Len(t, frames, 2)
	assert.Equal(t, "frame-1", string(frames[0]))
	assert.Equal(t, "frame-2", string(frames[1]))
	// I-frame select + blackframe + 299x299 scale の filter が渡っている。
	joined := strings.Join(runner.gotArgs, " ")
	assert.Contains(t, joined, "PICT_TYPE_I")
	assert.Contains(t, joined, "blackframe")
	assert.Contains(t, joined, "scale=299:299")
	assert.Contains(t, joined, "-skip_frame nokey")
}

func TestExtractDetectionFrames_FFmpegError(t *testing.T) {
	p := NewFFmpegVideoProcessor(nil, &frameWritingRunner{err: errors.New("no ffmpeg")})
	frames, err := p.ExtractDetectionFrames([]byte("video"))
	assert.Error(t, err)
	assert.Nil(t, frames)
}

func TestExtractDetectionFrames_NoFrames(t *testing.T) {
	p := NewFFmpegVideoProcessor(nil, &frameWritingRunner{frames: 0})
	frames, err := p.ExtractDetectionFrames([]byte("video"))
	require.NoError(t, err)
	assert.Empty(t, frames)
}
