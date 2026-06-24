package processors

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/repository"
)

// WebhookStatusRecorder is the subset of repository methods the processor
// uses to persist the latest delivery outcome. Abstracted so that user and
// system webhook processors share the same Handle implementation.
type WebhookStatusRecorder interface {
	UpdateLatestStatus(id string, sentAt time.Time, status int) error
}

// WebhookHTTPClient abstracts the HTTP round-trip so the processor can be
// unit tested with a fake transport.
type WebhookHTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// DefaultWebhookTimeout is the per-request HTTP timeout for webhook delivery.
// Matches upstream Misskey's 10 second timeout. Exported so the wire layer
// can use the same value when constructing the SSRF-safe outbound client.
const DefaultWebhookTimeout = 10 * time.Second

// WebhookProcessor handles a single webhook delivery task. It is instantiated
// twice (once for user webhooks, once for system webhooks) with different
// status recorders and URL/secret lookup strategies.
type WebhookProcessor struct {
	userRepo   repository.WebhookRepository
	systemRepo repository.SystemWebhookRepository
	client     WebhookHTTPClient
	host       string // instance hostname, used as X-Misskey-Host
	userAgent  string
}

// NewWebhookProcessor constructs a WebhookProcessor. A zero client falls back
// to http.DefaultClient with the configured timeout.
func NewWebhookProcessor(
	userRepo repository.WebhookRepository,
	systemRepo repository.SystemWebhookRepository,
	client WebhookHTTPClient,
	host string,
) *WebhookProcessor {
	if client == nil {
		client = &http.Client{Timeout: DefaultWebhookTimeout}
	}
	return &WebhookProcessor{
		userRepo:   userRepo,
		systemRepo: systemRepo,
		client:     client,
		host:       host,
		userAgent:  "Misskey-Hooks",
	}
}

// HandleUser dispatches a user webhook task.
func (p *WebhookProcessor) HandleUser(ctx context.Context, t driver.Task) error {
	return p.handle(ctx, t, true)
}

// HandleSystem dispatches a system webhook task.
func (p *WebhookProcessor) HandleSystem(ctx context.Context, t driver.Task) error {
	return p.handle(ctx, t, false)
}

// handle is the shared delivery implementation. user==true uses user webhooks,
// user==false uses system webhooks.
func (p *WebhookProcessor) handle(ctx context.Context, t driver.Task, user bool) error {
	payload, err := queue.DecodeWebhookPayload(t.Payload())
	if err != nil {
		return fmt.Errorf("decode webhook payload: %w: %w", err, driver.SkipRetry)
	}
	if payload.WebhookID == "" {
		return fmt.Errorf("webhook payload missing webhookId: %w", driver.SkipRetry)
	}

	// i/webhooks/test の override (#1546): 非空なら保存済 webhook を引かず指定の
	// url/secret へ送る。test 送信なので saved webhook の status は汚さない (下記)。
	isOverride := payload.OverrideURL != ""
	url, secret := payload.OverrideURL, payload.OverrideSecret
	if !isOverride {
		var err error
		url, secret, err = p.resolveTarget(payload.WebhookID, user)
		if err != nil {
			return fmt.Errorf("resolve webhook %s: %w: %w", payload.WebhookID, err, driver.SkipRetry)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload.Body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w: %w", err, driver.SkipRetry)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", p.userAgent)
	req.Header.Set("X-Misskey-Host", p.host)
	req.Header.Set("X-Misskey-Hook-Id", payload.WebhookID)
	if secret != "" {
		req.Header.Set("X-Misskey-Hook-Secret", secret)
	}

	resp, err := p.client.Do(req)
	sentAt := time.Now()
	if err != nil {
		// ネットワーク障害はリトライ対象 (asynq が再試行する)。
		// ステータスコード 0 を記録しておく (override test 送信時は記録しない)。
		if !isOverride {
			p.recordStatus(payload.WebhookID, user, sentAt, 0)
		}
		slog.Warn("webhook deliver: http error",
			"hookId", payload.WebhookID, "url", url, "err", err)
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if !isOverride {
		p.recordStatus(payload.WebhookID, user, sentAt, resp.StatusCode)
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests:
		// #2106 N30: 429 は 4xx だが upstream status-error.ts では retryable。
		// rate-limited な webhook 先には backoff 付きで再試行する (SkipRetry を付けない)。
		return errors.New("webhook rate limited: " + resp.Status)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// 4xx は受信側の不正リクエスト扱いでリトライしない。
		return fmt.Errorf("webhook client error (%d): %w", resp.StatusCode, driver.SkipRetry)
	default:
		// 5xx 等は一時的障害としてリトライ。
		return errors.New("webhook server error: " + resp.Status)
	}
}

// resolveTarget looks up a webhook row's URL and secret. For user webhooks it
// queries the WebhookRepository; for system webhooks it uses the
// SystemWebhookRepository.
func (p *WebhookProcessor) resolveTarget(id string, user bool) (url, secret string, err error) {
	if user {
		if p.userRepo == nil {
			return "", "", errors.New("user webhook repo not configured")
		}
		h, err := p.userRepo.FindByID(id)
		if err != nil {
			return "", "", err
		}
		return h.URL, h.Secret, nil
	}
	if p.systemRepo == nil {
		return "", "", errors.New("system webhook repo not configured")
	}
	h, err := p.systemRepo.FindByID(id)
	if err != nil {
		return "", "", err
	}
	return h.URL, h.Secret, nil
}

// recordStatus updates latestSentAt/latestStatus for the webhook. Errors from
// the repo are logged but never propagated—status recording is best-effort.
func (p *WebhookProcessor) recordStatus(id string, user bool, sentAt time.Time, status int) {
	var recorder WebhookStatusRecorder
	if user {
		recorder = p.userRepo
	} else {
		recorder = p.systemRepo
	}
	if recorder == nil {
		return
	}
	if err := recorder.UpdateLatestStatus(id, sentAt, status); err != nil {
		slog.Warn("webhook deliver: record status failed",
			"hookId", id, "err", err)
	}
}
