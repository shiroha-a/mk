package drive

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// CommandRunner abstracts os/exec.Command for testing.
type CommandRunner interface {
	// Run executes the command and returns its combined output.
	//
	// **ctx を受けるのが要点 (#3037)。** 以前は `exec.Command` を直に呼んで
	// いたので、**timeout も cancel も無い**まま ffmpeg を起動していた。
	// 細工した動画 (壊れた duration、極端なフレームレート、巨大な解像度) で
	// ffmpeg は何分でも回り、利用者が接続を切っても止まらない。
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ffmpegTimeout bounds one ffmpeg invocation.
//
// **upstream にこの上限は無い** (`FileInfoService` / `VideoProcessingService`
// は `execa` を timeout 無しで呼ぶ) が、そちらに合わせない。値は
// 「正当な動画のサムネイル生成が終わる時間」より十分長く、「1 本で枠を
// 潰し続ける」には短い点を採った。
const ffmpegTimeout = 60 * time.Second

// ffmpegMaxOutputBytes caps how much of the command's combined output we keep.
//
// **ffmpeg は stderr に大量に書く。** 入力が壊れているほど多くなるので、
// `CombinedOutput` のように全部ためると**入力の内容で確保量が決まる**。
// 出力そのものは使っていない (エラー判定は終了コード) ので、診断に足りる
// だけ残して捨てる。
const ffmpegMaxOutputBytes = 64 << 10

// ExecCommandRunner is the default CommandRunner that uses os/exec.
type ExecCommandRunner struct{}

func (r *ExecCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out boundedBuffer
	out.limit = ffmpegMaxOutputBytes
	cmd.Stdout = &out
	cmd.Stderr = &out
	// **ctx が切れたあと居座らせない。** `CommandContext` は SIGKILL を
	// 送るが、子プロセスがパイプを掴んだままだと `Wait` は返らない。
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	return out.buf.Bytes(), err
}

// boundedBuffer keeps at most limit bytes and silently drops the rest.
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	// **書けたことにする。** 途中で `io.ErrShortWrite` を返すと ffmpeg 側が
	// SIGPIPE で落ち、正常な動画でもサムネイルが作れなくなる。
	return len(p), nil
}

// FFmpegVideoProcessor extracts thumbnails from video files using FFmpeg.
type FFmpegVideoProcessor struct {
	runner  CommandRunner
	imgProc ImageProcessor
}

// NewFFmpegVideoProcessor creates a new FFmpegVideoProcessor. imgProc is
// used to convert the extracted PNG frame to WebP. runner が nil の場合は
// ExecCommandRunner を使用する。
func NewFFmpegVideoProcessor(imgProc ImageProcessor, runner CommandRunner) *FFmpegVideoProcessor {
	if runner == nil {
		runner = &ExecCommandRunner{}
	}
	return &FFmpegVideoProcessor{runner: runner, imgProc: imgProc}
}

// osMkdirTemp / osWriteFile はテスト時に差し替え可能。
var osMkdirTemp = os.MkdirTemp
var osWriteFile = os.WriteFile

// GenerateThumbnail extracts a frame at 5% of the video duration and
// converts it to a WebP thumbnail. FFmpeg がない場合や失敗時は nil を返す。
func (p *FFmpegVideoProcessor) GenerateThumbnail(ctx context.Context, body []byte, mimeType string) (*ProcessedImage, error) {
	if !isMimeVideo(mimeType) {
		return nil, nil
	}

	tmpDir, err := osMkdirTemp("", "misskey-video-*")
	if err != nil {
		return nil, nil
	}
	defer os.RemoveAll(tmpDir)

	inputPath := filepath.Join(tmpDir, "input")
	outputPath := filepath.Join(tmpDir, "out.png")

	if err := osWriteFile(inputPath, body, 0o600); err != nil {
		return nil, nil
	}

	// FFmpeg: 5% 地点のスクリーンショットを PNG で出力
	runCtx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
	defer cancel()
	_, err = p.runner.Run(runCtx, "ffmpeg",
		"-i", inputPath,
		"-ss", "5%",
		"-vframes", "1",
		"-f", "image2",
		outputPath,
	)
	if err != nil {
		// FFmpeg が見つからない、またはエラーの場合はサムネイルなし
		return nil, nil
	}

	pngData, err := readFileAtMost(outputPath, ffmpegMaxFrameBytes)
	if err != nil || len(pngData) == 0 {
		return nil, nil
	}

	// PNG を ImageProcessor でサムネイルサイズの WebP に変換
	if p.imgProc != nil {
		return p.imgProc.GenerateThumbnail(pngData, "image/png")
	}

	return nil, nil
}

// ffmpegMaxFrameBytes caps how much of an ffmpeg-produced frame we load.
//
// **PNG の大きさは入力の解像度で決まる。** 8K の動画なら 1 枚で数十 MB に
// なり、`os.ReadFile` はそれを丸ごとヒープへ載せる。サムネイルも
// sensitive 判定も**縮小してから**使うので、ここで打ち切っても判定は
// 変わらない (打ち切られた PNG はデコードに失敗し、呼び出し側の
// fail-open へ落ちる)。
const ffmpegMaxFrameBytes = 32 << 20

// readFileAtMost reads at most maxBytes from path.
//
// **上限を超えたら読まない。** 途中まで返すと、壊れた PNG を「正しく
// 読めた」として扱うことになる。
func readFileAtMost(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("ffmpeg frame exceeds %d bytes", maxBytes)
	}
	return data, nil
}

// detectionFrameLimit は ffmpeg に出力させるフレーム数の上限。upstream の
// index 間引きループ (targetIndex/nextIndex) は実際には index 0 と 1 の
// 2 枚を選んだ時点で targetIndex が進まなくなるため、2 枚あれば判定対象
// 集合は upstream と完全に一致する。
const detectionFrameLimit = 2

// ExtractDetectionFrames extracts normalized 299x299 PNG frames for the
// official sensitive-detector (upstream FileInfoService detectSensitivity の
// FFmpeg パイプライン移植)。I-frame のみを選び、暗部 50% 以上のフレームを
// 除外し、scale=299:299 で正規化した連番 PNG を出力してから、upstream と
// 同じ index 間引きループでフレームを選ぶ。失敗時は nil (fail-open)。
func (p *FFmpegVideoProcessor) ExtractDetectionFrames(ctx context.Context, body []byte) ([][]byte, error) {
	tmpDir, err := osMkdirTemp("", "misskey-sensitive-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	inputPath := filepath.Join(tmpDir, "input")
	if err := osWriteFile(inputPath, body, 0o600); err != nil {
		return nil, err
	}

	// upstream の filter 列 (skip_frame nokey / lowres 3 / I-frame select /
	// blackframe<50% / scale 299x299 / vsync 0) をそのまま使う。
	//
	// upstream は ffmpeg の出力を逐次読みながら 1 枚ずつ unlink するが、mk-go
	// の CommandRunner は同期実行なので全フレームが temp に残ってしまう。
	// 下の間引きループが選ぶのは先頭 detectionFrameLimit 枚だけなので、
	// -frames:v で ffmpeg 側を早期終了させ、選択結果を変えずにディスクと
	// CPU を有界にする (長尺動画で /tmp を溢れさせる DoS の防止)。
	runCtx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
	defer cancel()
	_, err = p.runner.Run(runCtx, "ffmpeg",
		"-skip_frame", "nokey",
		"-lowres", "3",
		"-i", inputPath,
		"-an",
		"-vf", "select=eq(pict_type\\,PICT_TYPE_I),blackframe=amount=0,metadata=select:key=lavfi.blackframe.pblack:value=50:function=less,scale=299:299",
		"-vsync", "0",
		"-frames:v", strconv.Itoa(detectionFrameLimit),
		"-f", "image2",
		filepath.Join(tmpDir, "%d.png"),
	)
	if err != nil {
		return nil, err
	}

	// ffmpeg image2 の %d は 1 始まりの連番。数値順で列挙する (Glob は辞書順
	// なので 10.png が 2.png より先に来てしまう)。
	var paths []string
	for i := 1; ; i++ {
		path := filepath.Join(tmpDir, fmt.Sprintf("%d.png", i))
		if _, err := os.Stat(path); err != nil {
			break
		}
		paths = append(paths, path)
	}

	// upstream FileInfoService の index 間引きループを忠実に移植する。
	// (コメント上は fibonacci だが、実際の挙動は先頭側の少数フレームに
	// 収束する。判定対象フレーム集合を TS と一致させるため同じ式を使う。)
	var frames [][]byte
	frameIndex := 0
	targetIndex := 0
	nextIndex := 1
	for _, path := range paths {
		index := frameIndex
		frameIndex++
		if index != targetIndex {
			continue
		}
		targetIndex = nextIndex
		nextIndex += index
		data, err := readFileAtMost(path, ffmpegMaxFrameBytes)
		if err != nil {
			continue
		}
		frames = append(frames, data)
	}
	return frames, nil
}
