package federation

import (
	"context"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	// Standard library decoders for jpeg / png / gif.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	// WebP support via golang.org/x/image (already in go.mod via other
	// transitive deps).
	_ "golang.org/x/image/webp"
)

// imageFetchTimeout は AP Document attachment の dimension probe に
// 使うタイムアウト。inbox processor 経由で同期実行されるため、ジョブ
// 全体を遅延させすぎないよう短めに設定する。失敗時は properties が
// 空のまま残るが、表示破綻 (#461 stretch) するよりは ingestion 完走を
// 優先する best-effort 方針。
const imageFetchTimeout = 3 * time.Second

// imageFetchMaxBytes は dimension decode に必要十分な先頭バイト数。
// JPEG / PNG / GIF / WebP いずれも 64KiB あればヘッダから dimensions を
// 復元できるので、サーバが Range を許可しない (= フル GET になる) 場合
// でも LimitReader で読み込み上限を切って早期 close する。
const imageFetchMaxBytes int64 = 64 * 1024

// attachmentProbeBudget bounds the total time one inbound document may spend
// probing image dimensions.
//
// probe は添付ごとに**直列**で走るので、`imageFetchTimeout` (3s) だけでは
// `maxRemoteAttachments` (16) × 3s = 48s まで伸びる。note 単位でも予算を切って、
// 無応答のメディアサーバーを並べた 1 通で inbox worker を押さえ込めないようにする。
// 予算切れの添付は `properties` が空のまま残るだけ (probe 失敗時と同じ degrade)。
const attachmentProbeBudget = 10 * time.Second

// fetchImageDimensions issues a best-effort HTTP GET against rawURL,
// decodes only the image header bytes, and returns width / height in
// pixels. Errors (non-200, decode failure, timeout, non-image MIME) are
// surfaced so callers can decide whether to log; in this package the
// upsertAttachments call site simply skips the property update.
//
// httpClient is taken as a parameter so tests can inject a mock client.
// In production, callers MUST pass an SSRF-safe *http.Client (see
// probeImageDimensions / router.go's safehttp.NewSSRFSafeTransport
// wiring). nil は test convenience として http.DefaultClient に
// フォールバックするのみで、production 経路は probeImageDimensions が
// nil を弾くので到達しない。
func fetchImageDimensions(ctx context.Context, httpClient *http.Client, rawURL string) (width, height int, err error) {
	if httpClient == nil {
		// nil fallback は test convenience のみ。production の SSRF
		// 経路は probeImageDimensions の nil-guard で塞がれている。
		httpClient = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, imageFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("federation: build image request: %w", err)
	}
	// User-Agent を付けないと Cloudflare 等の WAF に弾かれることが
	// あるので明示する。Accept は AP fetcher と違い image/* を要求。
	req.Header.Set("User-Agent", "mk-go (+image-dimension-probe)")
	req.Header.Set("Accept", "image/*")

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("federation: image fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, 0, fmt.Errorf("federation: image fetch: unexpected status %d", resp.StatusCode)
	}
	// MIME 確認 (Content-Type が image/* でない場合は decode 試行を
	// 諦める。HTML エラーページが 200 で返ってくるケースを想定)。
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "image/") {
		return 0, 0, fmt.Errorf("federation: image fetch: non-image content-type %q", ct)
	}

	limited := io.LimitReader(resp.Body, imageFetchMaxBytes)
	cfg, _, err := image.DecodeConfig(limited)
	if err != nil {
		return 0, 0, fmt.Errorf("federation: decode image header: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, fmt.Errorf("federation: decoded zero dimensions")
	}
	return cfg.Width, cfg.Height, nil
}

// probeImageDimensions wraps fetchImageDimensions with logging at WARN
// for failed probes (operator visibility) and DEBUG for success.
// Returns zero values on any failure, never panics.
//
// httpClient は SSRF-safe transport を持つ *http.Client (典型的には
// resolver.imageProbeClient = router.go で safehttp.NewSSRFSafeTransport
// を適用したもの) を渡すこと。 untrusted な URL を fetch する経路なので
// http.DefaultClient で呼ぶと SSRF 脆弱性になる (#464 review)。nil 渡しは
// 呼び出し側で gate されている前提だが、安全側に倒して probe 自体を
// 止める。
//
// **ctx は呼び出し側が握る。** 以前はここで `context.Background()` を作って
// いたため、1 添付ごとの `imageFetchTimeout` 以外に打ち切る手段が無く、添付を
// 並べるだけで直列の外向き GET を積み上げられた。呼び出し側 (upsertAttachments)
// は note 単位の予算を張った ctx を渡す。
func probeImageDimensions(ctx context.Context, httpClient *http.Client, rawURL string) (width, height int, ok bool) {
	if httpClient == nil {
		// SSRF 対策: 未配線時は probe しない (defaultClient フォールバック禁止)
		return 0, 0, false
	}
	w, h, err := fetchImageDimensions(ctx, httpClient, rawURL)
	if err != nil {
		slog.Warn("federation: image dimension probe failed",
			"url", rawURL, "err", err)
		return 0, 0, false
	}
	return w, h, true
}
