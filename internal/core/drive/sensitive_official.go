package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// Prediction is one class probability returned by the official
// sensitive-detector service (upstream AiService.Prediction).
type Prediction struct {
	ClassName   string  `json:"className"`
	Probability float64 `json:"probability"`
}

// OfficialDetectorSettings carries the meta-driven connection settings for
// the official misskey-dev/sensitive-detector protocol (2026.7.0 migration
// sensitiveMediaDetectionExternalService).
type OfficialDetectorSettings struct {
	// APIURL is meta.sensitiveMediaDetectionApiUrl. Empty = detector disabled.
	APIURL string
	// APIKey is meta.sensitiveMediaDetectionApiKey (Bearer token, optional).
	APIKey string
	// TimeoutMs is meta.sensitiveMediaDetectionTimeout (default 60000).
	TimeoutMs int
	// MaxImagesPerRequest is meta.sensitiveMediaDetectionMaxImagesPerRequest
	// (default 4, min 1 clamp).
	MaxImagesPerRequest int
}

// officialDetectImagesPath は公式 sensitive-detector の推論 endpoint。
const officialDetectImagesPath = "v1/detect-images"

// defaultOfficialTimeout matches the meta column default (60000 ms).
const defaultOfficialTimeout = 60 * time.Second

// OfficialDetector calls the official sensitive-detector sidecar with the
// upstream 2026.7.0 AiService protocol: multipart batches of normalized
// 299x299 PNGs in, raw class predictions out. Every failure is fail-open
// (nil predictions = 非センシティブ扱い) per misskey-dev/misskey#16804.
type OfficialDetector struct {
	client *http.Client
}

// NewOfficialDetector wraps an outbound HTTP client. Production must pass a
// SSRF-safe client (upstream #17828 は HttpRequestService 経由に統一した。
// mk-go は safehttp transport の outbound client がそれに相当する)。
func NewOfficialDetector(client *http.Client) *OfficialDetector {
	if client == nil {
		client = &http.Client{}
	}
	return &OfficialDetector{client: client}
}

// DetectMany scores each image and returns per-image predictions. A nil
// element means "判定不能" (通信失敗・型不一致・per-image エラー) で、呼び出し
// 側は非センシティブとして扱う。images が空なら空 slice を返す。
func (d *OfficialDetector) DetectMany(ctx context.Context, images [][]byte, settings OfficialDetectorSettings) [][]Prediction {
	results := make([][]Prediction, len(images))
	if len(images) == 0 {
		return results
	}

	base := strings.TrimSpace(settings.APIURL)
	if base == "" {
		return results
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	endpoint, err := url.Parse(base)
	if err != nil {
		slog.Warn("drive: invalid sensitiveMediaDetectionApiUrl", "err", err)
		return results
	}
	ref, err := url.Parse(officialDetectImagesPath)
	if err != nil {
		return results
	}
	target := endpoint.ResolveReference(ref).String()

	timeout := time.Duration(settings.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultOfficialTimeout
	}
	chunkSize := settings.MaxImagesPerRequest
	if chunkSize < 1 {
		chunkSize = 1
	}

	for start := 0; start < len(images); start += chunkSize {
		end := min(start+chunkSize, len(images))
		chunk := images[start:end]
		for i, preds := range d.detectChunk(ctx, target, settings.APIKey, timeout, chunk) {
			results[start+i] = preds
		}
	}
	return results
}

// officialBatchItem / officialResponse は公式契約のレスポンス shape。
// upstream AiService は型ガードで厳密検証し、不一致は全件 null (fail-open)。
// Go では json.Unmarshal の型不一致エラーがそれに相当する。
type officialBatchItem struct {
	Success     bool         `json:"success"`
	Predictions []Prediction `json:"predictions"`
	Error       *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type officialResponse struct {
	Success bool `json:"success"`
	Result  *struct {
		Results []officialBatchItem `json:"results"`
	} `json:"result"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func (d *OfficialDetector) detectChunk(ctx context.Context, target, apiKey string, timeout time.Duration, chunk [][]byte) [][]Prediction {
	out := make([][]Prediction, len(chunk))

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for i, img := range chunk {
		// upstream は field 名 `image{i}` / filename `{i}.png` / part
		// Content-Type image/png で送る (フィールド名は任意仕様だが揃える)。
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image%d"; filename="%d.png"`, i, i))
		h.Set("Content-Type", "image/png")
		part, err := w.CreatePart(h)
		if err != nil {
			return out
		}
		if _, err := part.Write(img); err != nil {
			return out
		}
	}
	if err := w.Close(); err != nil {
		return out
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, &buf)
	if err != nil {
		slog.Warn("drive: sensitive-detector request creation failed", "err", err)
		return out
	}
	// boundary 付き Content-Type は multipart.Writer から取る (手動指定すると
	// boundary 不一致で読めなくなる。upstream も FormData 任せにしている)。
	req.Header.Set("Content-Type", w.FormDataContentType())
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		slog.Warn("drive: sensitive-detector request failed", "err", err)
		return out
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("drive: sensitive-detector responded non-OK", "status", resp.StatusCode)
		return out
	}

	var body officialResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		slog.Warn("drive: sensitive-detector response parse failed", "err", err)
		return out
	}
	if !body.Success || body.Result == nil {
		code := ""
		if body.Error != nil {
			code = body.Error.Code
		}
		slog.Warn("drive: sensitive-detector responded with failure", "code", code)
		return out
	}

	items := body.Result.Results
	for i := range chunk {
		if i < len(items) && items[i].Success && items[i].Predictions != nil {
			out[i] = items[i].Predictions
		}
	}
	return out
}

// OfficialSensitivityThreshold maps meta.sensitiveMediaDetectionSensitivity
// to the upstream DriveService threshold (感度が高いほどしきい値は低い)。
// legacy detector 用の SensitivityThreshold とは値が異なる (veryLow 0.9 /
// low 0.7、upstream DriveService.ts の三項演算子と同値)。
func OfficialSensitivityThreshold(sensitivity string) float64 {
	switch strings.ToLower(sensitivity) {
	case "veryhigh":
		return 0.1
	case "high":
		return 0.3
	case "low":
		return 0.7
	case "verylow":
		return 0.9
	default:
		return 0.5
	}
}

// officialPornThreshold は upstream sensitiveThresholdForPorn 固定値。
const officialPornThreshold = 0.75

// JudgePredictions applies the upstream FileInfoService judgePrediction:
// Sexy / Hentai / Porn のいずれかが threshold を超えたら sensitive。
// porn は Porn > 0.75 (現状 flag には未使用、upstream 準拠で算出のみ)。
func JudgePredictions(preds []Prediction, sensitiveThreshold float64) (sensitive bool, porn bool) {
	prob := func(class string) float64 {
		for _, p := range preds {
			if p.ClassName == class {
				return p.Probability
			}
		}
		return 0
	}
	if prob("Sexy") > sensitiveThreshold || prob("Hentai") > sensitiveThreshold || prob("Porn") > sensitiveThreshold {
		sensitive = true
	}
	if prob("Porn") > officialPornThreshold {
		porn = true
	}
	return sensitive, porn
}

// AggregateFrameJudgements applies the upstream video aggregation: 判定済み
// フレームのうち sensitive 割合が ceil(n*threshold) 以上なら sensitive。
// 判定成功 0 件は false (2026.7.0 の「全動画センシティブ化」バグ修正の
// results.length > 0 ガードに相当)。
func AggregateFrameJudgements(frameSensitive []bool, threshold float64) bool {
	n := len(frameSensitive)
	if n == 0 {
		return false
	}
	count := 0
	for _, s := range frameSensitive {
		if s {
			count++
		}
	}
	return count >= int(math.Ceil(float64(n)*threshold))
}

// DetectionFrameExtractor is an optional capability of VideoProcessor
// implementations that can extract normalized detection frames from a video
// (FFmpegVideoProcessor)。interface 拡張ではなく type-assertion で使う
// (既存 mock 実装への波及を避ける)。
type DetectionFrameExtractor interface {
	ExtractDetectionFrames(body []byte) ([][]byte, error)
}
