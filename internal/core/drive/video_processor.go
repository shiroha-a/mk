package drive

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// CommandRunner abstracts os/exec.Command for testing.
type CommandRunner interface {
	// Run executes the command and returns its combined output.
	Run(name string, args ...string) ([]byte, error)
}

// ExecCommandRunner is the default CommandRunner that uses os/exec.
type ExecCommandRunner struct{}

func (r *ExecCommandRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
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
func (p *FFmpegVideoProcessor) GenerateThumbnail(body []byte, mimeType string) (*ProcessedImage, error) {
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
	_, err = p.runner.Run("ffmpeg",
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

	pngData, err := os.ReadFile(outputPath)
	if err != nil || len(pngData) == 0 {
		return nil, nil
	}

	// PNG を ImageProcessor でサムネイルサイズの WebP に変換
	if p.imgProc != nil {
		return p.imgProc.GenerateThumbnail(pngData, "image/png")
	}

	return nil, nil
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
func (p *FFmpegVideoProcessor) ExtractDetectionFrames(body []byte) ([][]byte, error) {
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
	_, err = p.runner.Run("ffmpeg",
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
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		frames = append(frames, data)
	}
	return frames, nil
}
