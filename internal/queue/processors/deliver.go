// Package processors contains task handlers used by the queue worker.
// Handlers are driver-neutral: they accept a driver.Task and return
// driver.SkipRetry to suppress retries.
package processors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
)

// HTTPSigner abstracts the signed POST capability of activitypub.Client so
// that DeliverProcessor can be unit-tested without an actual HTTP client.
type HTTPSigner interface {
	PostSigned(url string, body []byte, key *activitypub.PrivateKey) (*http.Response, error)
}

// ResponseHook is invoked after each HTTP attempt so the instance metadata
// (isNotResponding / notRespondingSince) can be kept up to date. パッケージ間
// の循環依存を避けるため interface で受け取る。実装は core/instance.Service。
type ResponseHook interface {
	RecordResponseSuccess(host string) error
	RecordResponseError(host string) error
}

// ChartHook is invoked after each HTTP attempt so the chart subsystem
// can record ApRequest / Federation / Instance metrics. パッケージ間の
// 循環依存を避けるため interface で受け取る。実装は core/chart/charthook。
type ChartHook interface {
	OnDelivered(host string, succeeded bool)
}

// SuspendedChecker reports whether delivery to a host should be skipped
// based on meta.deliverSuspendedSoftware.
type SuspendedChecker interface {
	IsSuspended(host string) bool
}

// DeliverProcessor handles ap:deliver tasks by posting the activity body to
// the recipient inbox with an HTTP signature.
type DeliverProcessor struct {
	signer           HTTPSigner
	responseHook     ResponseHook
	chartHook        ChartHook
	suspendedChecker SuspendedChecker
	// redis is used for the per-host Ed25519 degrade flag (#1067 / #1071).
	// 未配線時は capability gate なしの楽観動作 (= payload に Ed25519 鍵が
	// あれば必ず Ed25519 sign を試す) になるが、production では必須。
	redis *redis.Client
}

// NewDeliverProcessor constructs a DeliverProcessor.
func NewDeliverProcessor(signer HTTPSigner) *DeliverProcessor {
	return &DeliverProcessor{signer: signer}
}

// SetRedis attaches a Redis client used to persist per-host Ed25519 degrade
// flags. Ed25519 sign + POST が 4xx で失敗した host は `ed25519:degrade:<host>`
// に EX=300 で記録され、以後 5 分間は同 host への配送が即 RSA fallback される
// (= "assertionMethod を expose しているが Ed25519 verify が壊れている" 実装
// に対する safety net #1067 / #1071)。
func (p *DeliverProcessor) SetRedis(c *redis.Client) {
	p.redis = c
}

// ed25519DegradeTTL is how long a host stays degraded after the failure
// counter reaches threshold. 5 分は production observation で「broken impl の
// 修正 deploy」に必要な時間より長い (= 改善が deploy されれば自動回復する)
// ことを想定。
const ed25519DegradeTTL = 5 * time.Minute

// ed25519FailWindow defines the sliding window the failure counter is held in.
// 1 つの transient 4xx で degrade が立つのを防ぐ目的で、60s の window 内に
// threshold 件以上の失敗があったときだけ degrade を立てる。
const ed25519FailWindow = 60 * time.Second

// ed25519FailThreshold is the number of failures within ed25519FailWindow
// required to trip the degrade flag. 1 件の false positive (= 受信側 transient
// error) で host 全体を縮退するのを避けるため 3 を採用。
const ed25519FailThreshold = 3

func ed25519DegradeKey(host string) string {
	return "ed25519:degrade:" + host
}

func ed25519FailKey(host string) string {
	return "ed25519:fail:" + host
}

// isEd25519Degraded reports whether the host has the Ed25519 degrade flag
// set in Redis. Redis 未配線 or empty host or Redis 障害は false (= 安全側
// で Ed25519 試行を継続)。
func (p *DeliverProcessor) isEd25519Degraded(host string) bool {
	if p.redis == nil || host == "" {
		return false
	}
	n, err := p.redis.Exists(context.Background(), ed25519DegradeKey(host)).Result()
	if err != nil {
		// Redis 障害時は flag 判定不能 → 安全側に倒して "未 degrade" 扱い
		// (= 引き続き Ed25519 試行)。flag セット側も同じく fail-soft なので
		// 一時的 Redis 障害でも deliver は止まらない。
		return false
	}
	return n > 0
}

// markEd25519Degraded sets the Ed25519 degrade flag for the host with the
// default TTL. Redis 未配線 or empty host or Redis error は best-effort skip。
func (p *DeliverProcessor) markEd25519Degraded(host string) {
	if p.redis == nil || host == "" {
		return
	}
	if err := p.redis.Set(context.Background(), ed25519DegradeKey(host), "1", ed25519DegradeTTL).Err(); err != nil {
		slog.Warn("ed25519 degrade flag set failed", "host", host, "error", err)
	}
}

// recordEd25519Failure increments the per-host failure counter and trips the
// degrade flag once it reaches ed25519FailThreshold within ed25519FailWindow.
// 1 件の transient 4xx で host 全体縮退するのを避け、連続失敗のときだけ縮退
// させる設計 (#1080 review #4)。Redis 未配線 or Redis error は best-effort
// skip して 楽観的 Ed25519 試行を継続。
func (p *DeliverProcessor) recordEd25519Failure(host string) {
	if p.redis == nil || host == "" {
		return
	}
	ctx := context.Background()
	n, err := p.redis.Incr(ctx, ed25519FailKey(host)).Result()
	if err != nil {
		slog.Warn("ed25519 fail counter incr failed", "host", host, "error", err)
		return
	}
	if n == 1 {
		// 初回 INCR で TTL を設定 (window がスライドする)。Expire 失敗時は
		// key が TTL なしで残り counter が永続蓄積するリスクがあるので診断の
		// ために log を出す (#1080 review #2 follow-up)。
		if err := p.redis.Expire(ctx, ed25519FailKey(host), ed25519FailWindow).Err(); err != nil {
			slog.Warn("ed25519 fail counter TTL set failed", "host", host, "error", err)
		}
	}
	if n >= ed25519FailThreshold {
		slog.Warn("ed25519 fail threshold reached, degrading host",
			"host", host, "count", n)
		p.markEd25519Degraded(host)
	}
}

// SetSuspendedChecker attaches a checker for deliverSuspendedSoftware.
func (p *DeliverProcessor) SetSuspendedChecker(c SuspendedChecker) {
	p.suspendedChecker = c
}

// SetResponseHook attaches a ResponseHook used to update instance health flags.
func (p *DeliverProcessor) SetResponseHook(h ResponseHook) {
	p.responseHook = h
}

// SetChartHook attaches a ChartHook invoked after each delivery attempt.
func (p *DeliverProcessor) SetChartHook(h ChartHook) {
	p.chartHook = h
}

// hostFromInbox returns the host portion of an inbox URL, or "" if the URL is
// not parseable. ResponseHook 通知用に共通化する。
func hostFromInbox(inbox string) string {
	u, err := url.Parse(inbox)
	if err != nil {
		return ""
	}
	return u.Host
}

// recordSuccess is a best-effort wrapper that fires both the response
// hook and the chart hook for a successful inbox POST.
func (p *DeliverProcessor) recordSuccess(inbox string) {
	host := hostFromInbox(inbox)
	if p.responseHook != nil && host != "" {
		_ = p.responseHook.RecordResponseSuccess(host)
	}
	if p.chartHook != nil {
		p.chartHook.OnDelivered(host, true)
	}
}

// recordError is a best-effort wrapper that fires both the response
// hook and the chart hook for a failed inbox POST.
func (p *DeliverProcessor) recordError(inbox string) {
	host := hostFromInbox(inbox)
	if p.responseHook != nil && host != "" {
		_ = p.responseHook.RecordResponseError(host)
	}
	if p.chartHook != nil {
		p.chartHook.OnDelivered(host, false)
	}
}

// Handle dispatches a single deliver task. The driver runtime invokes
// this for every dequeued task.
func (p *DeliverProcessor) Handle(_ context.Context, t driver.Task) error {
	payload, err := queue.DecodeDeliverPayload(t.Payload())
	if err != nil {
		// payload が壊れているジョブは何度リトライしても無意味なのでスキップ。
		return fmt.Errorf("decode deliver payload: %w: %w", err, driver.SkipRetry)
	}

	// deliverSuspendedSoftware: 対象インスタンスの software がリストに該当すればスキップ
	host := hostFromInbox(payload.Inbox)
	if p.suspendedChecker != nil && host != "" && p.suspendedChecker.IsSuspended(host) {
		slog.Info("ap deliver: skip (software suspended)", "inbox", payload.Inbox)
		return nil
	}

	// Ed25519 sign 経路: payload に Ed25519 鍵情報があり (= DeliverService が
	// recipient capable と判定) かつ host が degrade flag に立っていない
	// (= 過去 5min 以内に Ed25519 4xx 失敗がない) ときに試す (#1067 / #1071)。
	useEd25519 := payload.Ed25519PrivPEM != "" && !p.isEd25519Degraded(host)

	resp, err := p.sendOnce(payload, useEd25519)
	if err != nil {
		// network error / parse error は再投函。詳細は sendOnce 内で log 済。
		p.recordError(payload.Inbox)
		return err
	}
	// closure 内の resp は defer 実行時の現在値を見る (= retry 後の resp も
	// 同じ defer 1 つで cleanup される)。retry 経路では古い resp を手動 Close
	// したあと `resp = nil` してから新 resp を代入することで double Close を
	// 回避する設計 (#1080 review #1)。
	defer func() {
		if resp == nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	// Ed25519 sign で 4xx が返った場合は host を degrade して RSA で 1 回 retry
	// する safety net。"assertionMethod を expose しているが Ed25519 verify が
	// 壊れている" broken impl 対策。retry も失敗したら通常 4xx 経路で扱う。
	if useEd25519 && resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusGone && resp.StatusCode != http.StatusNotFound {
		slog.Warn("ap deliver: ed25519 4xx, recording failure + retry with RSA",
			"host", host, "status", resp.StatusCode)
		p.recordEd25519Failure(host)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		resp = nil // defer の double Close を避ける
		retryResp, retryErr := p.sendOnce(payload, false)
		if retryErr != nil {
			p.recordError(payload.Inbox)
			return retryErr
		}
		resp = retryResp // defer は新 resp を Close する
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		p.recordSuccess(payload.Inbox)
		return nil
	case resp.StatusCode == http.StatusGone,
		resp.StatusCode == http.StatusNotFound:
		// 410 / 404: 受信側がもう存在しない。リトライしても無駄なのでスキップ。
		// 「応答した」事自体は事実なので isNotResponding は解除する。
		slog.Info("ap deliver: target gone",
			"inbox", payload.Inbox, "status", resp.StatusCode)
		p.recordSuccess(payload.Inbox)
		return fmt.Errorf("target gone (%d): %w", resp.StatusCode, driver.SkipRetry)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// その他の4xxは受信側の不正リクエスト扱い。HTTP として応答が返って
		// きているので isNotResponding 状態は解除する。
		slog.Warn("ap deliver: client error",
			"inbox", payload.Inbox, "status", resp.StatusCode)
		p.recordSuccess(payload.Inbox)
		return fmt.Errorf("client error (%d): %w", resp.StatusCode, driver.SkipRetry)
	default:
		// 5xx は受信側の一時的な障害。リトライさせる + 不調状態としてマーク。
		slog.Warn("ap deliver: server error",
			"inbox", payload.Inbox, "status", resp.StatusCode)
		p.recordError(payload.Inbox)
		return errors.New("server error: " + resp.Status)
	}
}

// sendOnce performs one signed POST attempt. useEd25519=true なら
// payload.Ed25519KeyID + Ed25519PrivPEM で sign、それ以外は RSA。鍵 parse 失敗
// 時は (useEd25519=true ならば) RSA に fallback する。RSA 鍵自体が壊れている
// 場合は driver.SkipRetry で投函を中止。
func (p *DeliverProcessor) sendOnce(payload queue.DeliverPayload, useEd25519 bool) (*http.Response, error) {
	if useEd25519 {
		key, err := activitypub.NewEd25519PrivateKey(payload.Ed25519KeyID, payload.Ed25519PrivPEM)
		if err == nil {
			resp, perr := p.signer.PostSigned(payload.Inbox, payload.Body, key)
			if perr != nil {
				slog.Warn("ap deliver: ed25519 post failed",
					"inbox", payload.Inbox, "err", perr)
				return nil, perr
			}
			return resp, nil
		}
		slog.Warn("ap deliver: ed25519 key parse failed, falling back to RSA",
			"inbox", payload.Inbox, "err", err)
		// fallthrough: RSA で sign
	}
	key, err := activitypub.NewPrivateKey(payload.KeyID, payload.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w: %w", err, driver.SkipRetry)
	}
	resp, perr := p.signer.PostSigned(payload.Inbox, payload.Body, key)
	if perr != nil {
		slog.Warn("ap deliver: post failed",
			"inbox", payload.Inbox, "err", perr)
		return nil, perr
	}
	return resp, nil
}
