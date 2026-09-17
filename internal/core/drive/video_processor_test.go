package drive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

func (m *mockCommandRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
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

	result, err := vp.GenerateThumbnail(context.Background(), []byte("video-data"), "video/mp4")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "image/webp", result.MimeType)
	assert.Equal(t, []byte("webp-data"), result.Data)
}

func TestVideoProcessor_FFmpegError(t *testing.T) {
	runner := &mockCommandRunner{err: errors.New("ffmpeg not found")}
	vp := NewFFmpegVideoProcessor(nil, runner)

	result, err := vp.GenerateThumbnail(context.Background(), []byte("video-data"), "video/mp4")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestVideoProcessor_NotVideo(t *testing.T) {
	vp := NewFFmpegVideoProcessor(nil, &mockCommandRunner{})

	result, err := vp.GenerateThumbnail(context.Background(), []byte("data"), "image/jpeg")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestVideoProcessor_NilImageProcessor(t *testing.T) {
	runner := &mockCommandRunner{writePNG: true}
	vp := NewFFmpegVideoProcessor(nil, runner)

	result, err := vp.GenerateThumbnail(context.Background(), []byte("video-data"), "video/mp4")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestVideoProcessor_EmptyOutputFile(t *testing.T) {
	// FFmpeg は成功するが出力ファイルが空
	runner := &mockCommandRunner{writePNG: false}
	vp := NewFFmpegVideoProcessor(nil, runner)

	result, err := vp.GenerateThumbnail(context.Background(), []byte("video-data"), "video/mp4")
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
	out, err := r.Run(context.Background(), "echo", "hello")
	require.NoError(t, err)
	assert.Contains(t, string(out), "hello")
}

func TestExecCommandRunner_RunError(t *testing.T) {
	r := &ExecCommandRunner{}
	_, err := r.Run(context.Background(), "nonexistent-command-xyz")
	assert.Error(t, err)
}

func TestVideoProcessor_MkdirTempError(t *testing.T) {
	restore := SetOsMkdirTempForTest(func(string, string) (string, error) {
		return "", errors.New("mkdirtemp error")
	})
	defer restore()

	vp := NewFFmpegVideoProcessor(nil, &mockCommandRunner{})
	result, err := vp.GenerateThumbnail(context.Background(), []byte("video"), "video/mp4")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestVideoProcessor_WriteFileError(t *testing.T) {
	restore := SetOsWriteFileForTest(func(string, []byte, os.FileMode) error {
		return errors.New("writefile error")
	})
	defer restore()

	vp := NewFFmpegVideoProcessor(nil, &mockCommandRunner{})
	result, err := vp.GenerateThumbnail(context.Background(), []byte("video"), "video/mp4")
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

func (m *frameWritingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
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
	frames, err := p.ExtractDetectionFrames(context.Background(), []byte("video"))
	require.NoError(t, err)
	// upstream の index 間引きループは index 0, 1 を選んで停止する
	// (targetIndex が 1 のまま進まなくなる)。挙動一致を固定する。
	require.Len(t, frames, 2)
	assert.Equal(t, "frame-1", string(frames[0]))
	assert.Equal(t, "frame-2", string(frames[1]))
	// I-frame select + blackframe + 299x299 scale の filter が渡っている。
	joined := strings.Join(runner.gotArgs, " ")
	// ffmpeg 側を早期終了させ、長尺動画で temp を溢れさせない。
	assert.Contains(t, joined, "-frames:v 2")
	assert.Contains(t, joined, "PICT_TYPE_I")
	assert.Contains(t, joined, "blackframe")
	assert.Contains(t, joined, "scale=299:299")
	assert.Contains(t, joined, "-skip_frame nokey")
}

func TestExtractDetectionFrames_FFmpegError(t *testing.T) {
	p := NewFFmpegVideoProcessor(nil, &frameWritingRunner{err: errors.New("no ffmpeg")})
	frames, err := p.ExtractDetectionFrames(context.Background(), []byte("video"))
	assert.Error(t, err)
	assert.Nil(t, frames)
}

func TestExtractDetectionFrames_NoFrames(t *testing.T) {
	p := NewFFmpegVideoProcessor(nil, &frameWritingRunner{frames: 0})
	frames, err := p.ExtractDetectionFrames(context.Background(), []byte("video"))
	require.NoError(t, err)
	assert.Empty(t, frames)
}

// **ctx が切れたら外部プロセスも終わること (#3037)。**
//
// 以前は `exec.Command` を直に呼んでいたので、細工した動画で ffmpeg が
// 何分でも回り、利用者が接続を切っても止まらなかった。
func TestExecCommandRunner_ContextCancelStopsTheProcess(t *testing.T) {
	r := &ExecCommandRunner{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, "sleep", "30")
		done <- err
	}()

	cancel()
	select {
	case err := <-done:
		require.Error(t, err, "cancel しても正常終了として返っている")
	case <-time.After(10 * time.Second):
		t.Fatal("cancel しても外部プロセスが終わらない")
	}
}

func TestExecCommandRunner_DeadlineStopsTheProcess(t *testing.T) {
	r := &ExecCommandRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := r.Run(ctx, "sleep", "30")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 10*time.Second, "deadline を過ぎても回り続けている")
}

// **出力は上限まで。** ffmpeg は stderr に大量に書き、入力が壊れているほど
// 増える。全部ためると**入力の内容で確保量が決まる**。
func TestExecCommandRunner_OutputIsBounded(t *testing.T) {
	r := &ExecCommandRunner{}
	// 1MiB を吐かせる (上限は 64KiB)。
	out, err := r.Run(context.Background(), "sh", "-c",
		"yes 0123456789abcdef | head -c 1048576")
	require.NoError(t, err, "打ち切りで子プロセスを落としている")
	assert.LessOrEqual(t, len(out), ffmpegMaxOutputBytes, "出力が上限を超えている")
	assert.NotEmpty(t, out, "診断に使える分まで捨てている")
}

// **GenerateThumbnail は ctx を runner へ渡すこと。** 渡していないと、
// 上の 2 つが緑でも実際の ffmpeg 実行は無期限のまま。
func TestVideoProcessor_PassesContextToRunner(t *testing.T) {
	var gotDeadline bool
	runner := &ctxCapturingRunner{onRun: func(ctx context.Context) {
		_, gotDeadline = ctx.Deadline()
	}}
	vp := NewFFmpegVideoProcessor(nil, runner)

	_, _ = vp.GenerateThumbnail(context.Background(), []byte("video-data"), "video/mp4")
	assert.True(t, gotDeadline, "GenerateThumbnail が timeout の無い ctx で ffmpeg を起動している")

	gotDeadline = false
	_, _ = vp.ExtractDetectionFrames(context.Background(), []byte("video-data"))
	assert.True(t, gotDeadline, "ExtractDetectionFrames が timeout の無い ctx で ffmpeg を起動している")
}

// **呼び出し元の cancel も届くこと。** timeout を自前で付けるだけだと、
// 親 ctx を無視する実装 (`context.WithTimeout(context.Background(), ...)`)
// でも上のテストが通る。
func TestVideoProcessor_PropagatesCallerCancel(t *testing.T) {
	var canceled bool
	runner := &ctxCapturingRunner{onRun: func(ctx context.Context) {
		canceled = ctx.Err() != nil
	}}
	vp := NewFFmpegVideoProcessor(nil, runner)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = vp.GenerateThumbnail(ctx, []byte("video-data"), "video/mp4")
	assert.True(t, canceled, "呼び出し元の cancel が ffmpeg に届いていない")
}

type ctxCapturingRunner struct {
	onRun func(context.Context)
}

func (r *ctxCapturingRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	r.onRun(ctx)
	return nil, errors.New("not run")
}

// **フレームの読み込みも上限まで。** PNG の大きさは入力の解像度で決まる
// ので、8K の動画なら 1 枚で数十 MB になる。
func TestReadFileAtMost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(path, []byte("0123456789"), 0o600))

	got, err := readFileAtMost(path, 10)
	require.NoError(t, err)
	assert.Equal(t, "0123456789", string(got), "上限ちょうどを弾いている")

	_, err = readFileAtMost(path, 9)
	assert.Error(t, err, "上限を超えたファイルを読み込んでいる")

	_, err = readFileAtMost(filepath.Join(dir, "missing"), 10)
	assert.Error(t, err)
}

// **ffmpeg 側で縮めてから受け取ること (#3037 レビュー)。**
//
// scale が無いと出力 PNG は入力動画の解像度そのままで、8K なら 50-80MB。
// `readFileAtMost` の上限に当たると `GenerateThumbnail` は `nil, nil` を返すので、
// **エラーも出ないまま 6K 以上の動画のサムネイルが恒久的に欠ける**。
func TestVideoProcessor_ScalesBeforeReadingTheFrame(t *testing.T) {
	var args []string
	runner := &argCapturingRunner{onRun: func(_ string, a []string) { args = a }}
	vp := NewFFmpegVideoProcessor(nil, runner)

	_, _ = vp.GenerateThumbnail(context.Background(), []byte("video-data"), "video/mp4")

	vf := argValue(args, "-vf")
	require.NotEmpty(t, vf, "scale フィルタを渡していない")
	assert.Contains(t, vf, "force_original_aspect_ratio=decrease",
		"縦横比を保たない縮小をしている")

	// **長辺の値まで固定する。** ここを 16384 に変えても 8K のフレームは
	// そのまま出るので、「-vf がある」だけ見る形ではテストが空虚になる
	// (#3037 レビュー 2 周目で実測)。
	assert.Contains(t, vf, "1280", "長辺の上限が変わっている")

	// **`min()` で包んでいること。** `force_original_aspect_ratio=decrease` は
	// 箱に内接させるので、これが無いと**小さい動画が拡大される**
	// (実測: 320x240 が 1280x960 になり中間 PNG が 14 倍)。
	assert.Contains(t, vf, "min(", "小さい動画が拡大される形になっている")
}

// **`-ss` は秒で渡す。** `5%` は fluent-ffmpeg の記法で、ffmpeg 本体は
// `Invalid duration for option ss: 5%` でコマンドごと失敗する (実測
// ffmpeg 7.1.5)。そのため**全解像度でサムネイルが生成されていなかった**。
func TestVideoProcessor_SeeksWithSecondsNotAPercentage(t *testing.T) {
	var args []string
	runner := &argCapturingRunner{
		onRun: func(name string, a []string) {
			if name == "ffmpeg" {
				args = a
			}
		},
		// ffprobe は 40 秒の動画として応答する。
		probeOutput: "40.0\n",
	}
	vp := NewFFmpegVideoProcessor(nil, runner)

	_, _ = vp.GenerateThumbnail(context.Background(), []byte("video-data"), "video/mp4")

	ss := argValue(args, "-ss")
	require.NotEmpty(t, ss, "-ss を渡していない")
	assert.NotContains(t, ss, "%", "ffmpeg が受け付けない書式を渡している")
	secs, err := strconv.ParseFloat(ss, 64)
	require.NoError(t, err, "-ss が秒として読めない: %q", ss)
	assert.InDelta(t, 2.0, secs, 0.001, "5%% 地点になっていない")

	// **`-ss` は `-i` より前。** 後ろに置くと先頭からデコードするので、
	// 長尺動画では 5%% 地点に着く前に timeout する。
	assert.Less(t, indexOf(args, "-ss"), indexOf(args, "-i"), "-ss が -i の後ろにある")
}

// **probe に失敗してもサムネイルを諦めない。**
func TestVideoProcessor_FallsBackToTheStartWhenProbeFails(t *testing.T) {
	var args []string
	runner := &argCapturingRunner{onRun: func(name string, a []string) {
		if name == "ffmpeg" {
			args = a
		}
	}}
	vp := NewFFmpegVideoProcessor(nil, runner)

	_, _ = vp.GenerateThumbnail(context.Background(), []byte("video-data"), "video/mp4")

	assert.Equal(t, "0", argValue(args, "-ss"), "probe 失敗時に先頭から取っていない")
}

func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func indexOf(args []string, flag string) int {
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	return -1
}

type argCapturingRunner struct {
	onRun       func(name string, args []string)
	probeOutput string
}

func (r *argCapturingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.onRun(name, args)
	if name == "ffprobe" {
		if r.probeOutput == "" {
			return nil, errors.New("ffprobe not available")
		}
		return []byte(r.probeOutput), nil
	}
	return nil, errors.New("not run")
}
