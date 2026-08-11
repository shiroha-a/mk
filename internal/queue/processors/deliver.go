// Package processors contains task handlers used by the queue worker.
// Handlers are driver-neutral: they accept a driver.Task and return
// driver.SkipRetry to suppress retries.
package processors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/redis/go-redis/v9"
	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/deliveryhealth"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
)

// deliverKeyCacheSize bounds the parsed-signing-key LRU. 署名者は実質ローカル
// ユーザー数なので 1024 で大半の instance を賄える。超過分は LRU が evict する。
const deliverKeyCacheSize = 1024

// signingKey の cache key 先頭に付ける鍵種別の判別子。RSA / Ed25519 の entry を
// 区別する。将来 P-256 等を増やす際の typo 耐性のため定数化 (#1425 review)。
const (
	keyKindRSA     = "rsa"
	keyKindEd25519 = "ed"
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
	// MarkGoneSuspended suspends an instance whose shared inbox returned 410
	// (goneSuspended、#1811)。
	MarkGoneSuspended(host string) error
}

// ChartHook is invoked after each HTTP attempt so the chart subsystem
// can record ApRequest / Federation / Instance metrics. パッケージ間の
// 循環依存を避けるため interface で受け取る。実装は core/chart/charthook。
type ChartHook interface {
	OnDelivered(host string, succeeded bool)
}

// DeliveryTelemetry receives the detailed outcome of each attempt (#2461).
//
// ResponseHook が `isNotResponding` の真偽値に潰してしまう情報 (status /
// レイテンシ / 失敗の種別) をそのまま渡す先。**分類は本 file の応答 switch を
// そのまま反映する**ので、実装側で「成功とみなす範囲」を再判定しないこと。
//
// 循環依存を避けるため interface で受け取る。実装は core/deliveryhealth。
type DeliveryTelemetry interface {
	RecordDelivery(host string, o deliveryhealth.Outcome)
}

// SuspendedChecker reports whether delivery to a host should be skipped
// based on meta.deliverSuspendedSoftware.
type SuspendedChecker interface {
	IsSuspended(host string) bool
}

// DeliveryGate reports whether delivery to a host must be skipped because the
// remote instance is administratively blocked or suspended. dispatch 時に
// 呼ばれるため、suspend / block する前にキューへ積まれたジョブや retry-backoff
// 中のジョブも止められる (= enqueue 時フィルタだけでは取りこぼす経路の
// safety net、#1404)。実装は core/instance.Service。
type DeliveryGate interface {
	ShouldSkipDelivery(host string) bool
}

// Ed25519AcceptanceRecorder records that a remote host accepted an
// Ed25519-signed delivery (#2393). 実装は core/instance.CapabilityBuffer。
type Ed25519AcceptanceRecorder interface {
	ObserveEd25519Accepted(host string)
}

// DeliverProcessor handles ap:deliver tasks by posting the activity body to
// the recipient inbox with an HTTP signature.
type DeliverProcessor struct {
	signer           HTTPSigner
	responseHook     ResponseHook
	chartHook        ChartHook
	telemetry        DeliveryTelemetry
	suspendedChecker SuspendedChecker
	deliveryGate     DeliveryGate
	capabilities     Ed25519AcceptanceRecorder
	// redis is used for the per-host Ed25519 degrade flag (#1067 / #1071).
	// 未配線時は capability gate なしの楽観動作 (= payload に Ed25519 鍵が
	// あれば必ず Ed25519 sign を試す) になるが、production では必須。
	redis *redis.Client
	// keyCache は (kind, keyID, PEM) -> パース済み *PrivateKey の LRU。
	// ap:deliver は inbox ごとに 1 job なので、同一署名者へ fan-out する際に
	// 毎 job で PEM->x509 を再パースしていた。worker 間共有の単一 processor
	// にここを持たせて job 横断でメモ化する (#1425)。nil の場合は都度パース。
	keyCache *lru.Cache[string, *activitypub.PrivateKey]
}

// NewDeliverProcessor constructs a DeliverProcessor.
func NewDeliverProcessor(signer HTTPSigner) *DeliverProcessor {
	p := &DeliverProcessor{signer: signer}
	// lru.New は size>0 で error を返さない実装だが、防御的に握って nil
	// fallback (= 都度パース) に倒す。
	if c, err := lru.New[string, *activitypub.PrivateKey](deliverKeyCacheSize); err == nil {
		p.keyCache = c
	}
	return p
}

// signingKey returns a parsed PrivateKey for (keyID, pem), memoizing the parse
// across deliver jobs. kind は "rsa" / "ed" で、RSA と Ed25519 の cache entry を
// 区別する。PEM が変わる (= 鍵 rotation) と cache key も変わるため stale な鍵を
// 返さない。keyCache 未配線時は parse をそのまま呼ぶ。
//
// cache key は PEM 本体ではなく sha256(keyID+PEM) の hex を使う。map key を
// ~1.5KB から 64B 程度に縮めて memory footprint を抑え、cache key 経由で PEM
// 本体がログ等に混入するリスクも下げる。null-byte separator で keyID/PEM 境界
// の曖昧さを避ける。
func (p *DeliverProcessor) signingKey(kind, keyID, pem string, parse func(string, string) (*activitypub.PrivateKey, error)) (*activitypub.PrivateKey, error) {
	if p.keyCache == nil {
		return parse(keyID, pem)
	}
	sum := sha256.Sum256([]byte(keyID + "\x00" + pem))
	ck := kind + "\x00" + hex.EncodeToString(sum[:])
	if k, ok := p.keyCache.Get(ck); ok {
		return k, nil
	}
	k, err := parse(keyID, pem)
	if err != nil {
		return nil, err
	}
	p.keyCache.Add(ck, k)
	return k, nil
}

// SetRedis attaches a Redis client used to persist per-host Ed25519 degrade
// flags. Ed25519 sign + POST が 4xx で失敗した host は `ed25519:degrade:<host>`
// に EX=300 で記録され、以後 5 分間は同 host への配送が即 RSA fallback される
// (= "assertionMethod を expose しているが Ed25519 verify が壊れている" 実装
// に対する safety net #1067 / #1071)。
func (p *DeliverProcessor) SetRedis(c *redis.Client) {
	p.redis = c
}

// SetSignatureCapabilityRecorder wires the recorder that tracks which remote
// hosts successfully accept Ed25519-signed deliveries (#2393).
func (p *DeliverProcessor) SetSignatureCapabilityRecorder(r Ed25519AcceptanceRecorder) {
	p.capabilities = r
}

// recordEd25519Accepted is a best-effort hook fired when an Ed25519-signed
// delivery returned 2xx. recorder 未配線 / host 不明なら no-op。
func (p *DeliverProcessor) recordEd25519Accepted(host string) {
	if p.capabilities == nil || host == "" {
		return
	}
	p.capabilities.ObserveEd25519Accepted(host)
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

// SetDeliveryGate attaches a gate consulted at dispatch time to skip delivery
// to blocked / suspended instances. 未配線なら従来どおりゲート無しで配送する
// (#1404)。
func (p *DeliverProcessor) SetDeliveryGate(g DeliveryGate) {
	p.deliveryGate = g
}

// SetResponseHook attaches a ResponseHook used to update instance health flags.
func (p *DeliverProcessor) SetResponseHook(h ResponseHook) {
	p.responseHook = h
}

// SetChartHook attaches a ChartHook invoked after each delivery attempt.
func (p *DeliverProcessor) SetChartHook(h ChartHook) {
	p.chartHook = h
}

// SetDeliveryTelemetry wires the per-host outcome recorder (#2461)。
// 未配線なら記録しないだけで、配送そのものには影響しない。
func (p *DeliverProcessor) SetDeliveryTelemetry(t DeliveryTelemetry) {
	p.telemetry = t
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

// recordGoneSuspended is a best-effort wrapper that suspends the instance whose
// shared inbox returned 410 Gone (#1811)。
func (p *DeliverProcessor) recordGoneSuspended(inbox string) {
	host := hostFromInbox(inbox)
	if p.responseHook != nil && host != "" {
		_ = p.responseHook.MarkGoneSuspended(host)
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

// recordTelemetry forwards one attempt's detail to the health recorder.
//
// **判定はしない。** class は呼び出し元 (応答 switch) が決める。ここで
// status から再分類すると「成功とみなす範囲」が二重管理になる。
func (p *DeliverProcessor) recordTelemetry(host string, class deliveryhealth.OutcomeClass, status int, started time.Time, errMsg string) {
	if p.telemetry == nil || host == "" {
		return
	}
	p.telemetry.RecordDelivery(host, deliveryhealth.Outcome{
		Class:   class,
		Status:  status,
		Latency: time.Since(started),
		Err:     errMsg,
	})
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
	// suspend / block されたインスタンスへは配送しない。enqueue 時にもフィルタ
	// しているが、ブロック前に積まれたジョブや retry-backoff 中のジョブは enqueue
	// 時チェックを通り抜けるため、dispatch 時にも弾く (#1404)。
	if p.deliveryGate != nil && host != "" && p.deliveryGate.ShouldSkipDelivery(host) {
		slog.Info("ap deliver: skip (instance suspended or blocked)", "inbox", payload.Inbox)
		return nil
	}
	if p.suspendedChecker != nil && host != "" && p.suspendedChecker.IsSuspended(host) {
		slog.Info("ap deliver: skip (software suspended)", "inbox", payload.Inbox)
		return nil
	}

	// Ed25519 sign 経路: payload に Ed25519 鍵情報があり (= DeliverService が
	// recipient capable と判定) かつ host が degrade flag に立っていない
	// (= 過去 5min 以内に Ed25519 4xx 失敗がない) ときに試す (#1067 / #1071)。
	useEd25519 := payload.Ed25519PrivPEM != "" && !p.isEd25519Degraded(host)

	// 配送レイテンシの起点 (#2461)。sendOnce の中で Ed25519->RSA の再送が起きた
	// 場合も含めて「この job が相手にかけた時間」を測る。
	started := time.Now()

	resp, signedWithEd25519, err := p.sendOnce(payload, useEd25519)
	if err != nil {
		// network error / parse error は再投函。詳細は sendOnce 内で log 済。
		p.recordError(payload.Inbox)
		p.recordTelemetry(host, deliveryhealth.ClassTransport, 0, started, err.Error())
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
	// #2106 N30: 429 (rate limited) は Ed25519 verify の問題ではなく単なる送信過多なので、
	// Ed25519→RSA degrade 経路には乗せず通常の retryable 扱い (下の switch) に落とす。
	if useEd25519 && resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusGone && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusTooManyRequests {
		slog.Warn("ap deliver: ed25519 4xx, recording failure + retry with RSA",
			"host", host, "status", resp.StatusCode)
		p.recordEd25519Failure(host)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		resp = nil // defer の double Close を避ける
		retryResp, retrySignedWithEd25519, retryErr := p.sendOnce(payload, false)
		if retryErr != nil {
			p.recordError(payload.Inbox)
			p.recordTelemetry(host, deliveryhealth.ClassTransport, 0, started, retryErr.Error())
			return retryErr
		}
		resp = retryResp // defer は新 resp を Close する
		// RSA で送り直したので、この後の 2xx を Ed25519 の成功として数えない。
		signedWithEd25519 = retrySignedWithEd25519
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		p.recordSuccess(payload.Inbox)
		p.recordTelemetry(host, deliveryhealth.ClassSuccess, resp.StatusCode, started, "")
		if signedWithEd25519 {
			// Ed25519 で送って同期的に拒否されなかったことを記録する (#2393)。
			// 「相手が検証できた」までは言えない (verify-in-worker な実装は検証前に
			// 202 を返す)。同期拒否は上の 4xx degrade 経路が拾う。
			p.recordEd25519Accepted(host)
		}
		return nil
	case resp.StatusCode == http.StatusGone,
		resp.StatusCode == http.StatusNotFound:
		// 410 / 404: 受信側がもう存在しない。リトライしても無駄なのでスキップ。
		// 「応答した」事自体は事実なので isNotResponding は解除する。
		slog.Info("ap deliver: target gone",
			"inbox", payload.Inbox, "status", resp.StatusCode)
		p.recordSuccess(payload.Inbox)
		p.recordTelemetry(host, deliveryhealth.ClassGone, resp.StatusCode, started, "")
		// shared inbox が 410 Gone を返したらインスタンス全体が消滅したとみなし
		// goneSuspended に切り替えて以後の配送を止める (upstream
		// DeliverProcessorService の isSharedInbox && 410、#1811)。404 は個別 actor
		// 不在の可能性があるので suspend しない。
		if payload.IsSharedInbox && resp.StatusCode == http.StatusGone {
			p.recordGoneSuspended(payload.Inbox)
		}
		return fmt.Errorf("target gone (%d): %w", resp.StatusCode, driver.SkipRetry)
	case resp.StatusCode == http.StatusTooManyRequests:
		// #2106 N30: 429 は 4xx だが upstream status-error.ts では retryable
		// (isRetryable = !isClientError || statusCode === 429)。rate-limited な配送先には
		// backoff 付きで再試行する。「応答した」事実があるので isNotResponding は解除する
		// (instance は健在で rate-limit しているだけ)。SkipRetry を付けず queue に retry させる。
		slog.Warn("ap deliver: rate limited (429), will retry",
			"inbox", payload.Inbox)
		p.recordSuccess(payload.Inbox)
		p.recordTelemetry(host, deliveryhealth.ClassRateLimited, resp.StatusCode, started, "")
		return fmt.Errorf("rate limited (429): %s", resp.Status)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// その他の4xxは受信側の不正リクエスト扱い。HTTP として応答が返って
		// きているので isNotResponding 状態は解除する。
		slog.Warn("ap deliver: client error",
			"inbox", payload.Inbox, "status", resp.StatusCode)
		p.recordSuccess(payload.Inbox)
		p.recordTelemetry(host, deliveryhealth.ClassClientError, resp.StatusCode, started, resp.Status)
		return fmt.Errorf("client error (%d): %w", resp.StatusCode, driver.SkipRetry)
	default:
		// 5xx は受信側の一時的な障害。リトライさせる + 不調状態としてマーク。
		slog.Warn("ap deliver: server error",
			"inbox", payload.Inbox, "status", resp.StatusCode)
		p.recordError(payload.Inbox)
		p.recordTelemetry(host, deliveryhealth.ClassServerError, resp.StatusCode, started, resp.Status)
		return errors.New("server error: " + resp.Status)
	}
}

// sendOnce performs one signed POST attempt. useEd25519=true なら
// payload.Ed25519KeyID + Ed25519PrivPEM で sign、それ以外は RSA。鍵 parse 失敗
// 時は (useEd25519=true ならば) RSA に fallback する。RSA 鍵自体が壊れている
// 場合は driver.SkipRetry で投函を中止。
//
// 2 つ目の戻り値は「実際に Ed25519 で署名したか」。呼び出し元の useEd25519 とは
// 一致しないことがある (鍵 parse 失敗で RSA に落ちた場合)。相手が Ed25519 を
// 観測として記録してよいのは実際に署名した方式だけなので、引数ではなくこの
// 戻り値を見ること (#2393)。
func (p *DeliverProcessor) sendOnce(payload queue.DeliverPayload, useEd25519 bool) (*http.Response, bool, error) {
	if useEd25519 {
		key, err := p.signingKey(keyKindEd25519, payload.Ed25519KeyID, payload.Ed25519PrivPEM, activitypub.NewEd25519PrivateKey)
		if err == nil {
			resp, perr := p.signer.PostSigned(payload.Inbox, payload.Body, key)
			if perr != nil {
				slog.Warn("ap deliver: ed25519 post failed",
					"inbox", payload.Inbox, "err", perr)
				return nil, true, perr
			}
			return resp, true, nil
		}
		slog.Warn("ap deliver: ed25519 key parse failed, falling back to RSA",
			"inbox", payload.Inbox, "err", err)
		// fallthrough: RSA で sign
	}
	key, err := p.signingKey(keyKindRSA, payload.KeyID, payload.KeyPEM, activitypub.NewPrivateKey)
	if err != nil {
		return nil, false, fmt.Errorf("parse private key: %w: %w", err, driver.SkipRetry)
	}
	resp, perr := p.signer.PostSigned(payload.Inbox, payload.Body, key)
	if perr != nil {
		slog.Warn("ap deliver: post failed",
			"inbox", payload.Inbox, "err", perr)
		return nil, false, perr
	}
	return resp, false, nil
}
