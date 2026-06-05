// Package inbox provides the ActivityPub inbox endpoint.
package inbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
)

// HostBlockChecker reports whether a host is on the blocked list and whether
// the federation mode allows ingesting from it. Used by the inbox handler to
// reject activities from blocked / disallowed instances (#536).
type HostBlockChecker interface {
	IsBlocked(host string) bool
	// IsAllowed returns true when federation with host is permitted under
	// the current admin federation mode (none / specified / all) and the
	// host is not in blockedHosts.
	IsAllowed(host string) bool
}

// InstanceTracker is invoked after a successfully verified inbound request so
// that the instance row's latestRequestReceivedAt can be updated. パッケージ
// 間の循環依存を避けるため interface で受け取る。
type InstanceTracker interface {
	MarkRequestReceived(host string) error
}

// ChartHook is invoked after a successfully verified inbound request
// so the chart subsystem can record ApRequest.Inbox / FederationChart.
// Inbox / InstanceChart.RequestReceived. パッケージ間の循環依存を
// 避けるため interface で受け取る (実装は core/chart/charthook)。
type ChartHook interface {
	OnInboxReceived(host string)
}

// InboxEnqueuer is the narrow subset of queue.Enqueuer the inbox handler
// uses to dispatch verified activities to the worker pool (#534). Falling
// back to nil disables async dispatch — the handler then runs Process in
// the request goroutine (legacy synchronous behaviour, used in tests that
// don't wire a queue client).
type InboxEnqueuer interface {
	EnqueueInbox(ctx context.Context, payload queue.InboxPayload) error
}

// Handler accepts incoming activities and dispatches them to the federation
// processor after verifying their HTTP signature.
type Handler struct {
	resolver        *federation.Resolver
	processor       *federation.Processor
	hostBlocker     HostBlockChecker
	instanceTracker InstanceTracker
	chartHook       ChartHook
	enqueuer        InboxEnqueuer
	// keyCache は同期 fallback 経路 (SetEnqueuer 未配線時) の HTTP Signature
	// verify で公開鍵パースをメモ化する (#1426)。worker 側 InboxProcessor と
	// 同じ最適化を、enqueue せず inline verify する構成にも適用する。
	keyCache *activitypub.PublicKeyCache
}

// NewHandler constructs a Handler.
func NewHandler(resolver *federation.Resolver, processor *federation.Processor) *Handler {
	return &Handler{resolver: resolver, processor: processor, keyCache: activitypub.NewPublicKeyCache(0)}
}

// SetEnqueuer wires the queue.Enqueuer used to dispatch verified activities
// to the worker pool. When unset, the handler falls back to running
// processor.Process synchronously inside the request goroutine — which is
// the pre-#534 behaviour and what unit tests without a queue rely on.
func (h *Handler) SetEnqueuer(e InboxEnqueuer) {
	h.enqueuer = e
}

// SetHostBlockChecker attaches a HostBlockChecker. 設定されると、シグネチャ
// 検証成功後の actor が属するホストが blocked リストに含まれる場合は 403 を
// 返して以降の処理をスキップする。
func (h *Handler) SetHostBlockChecker(c HostBlockChecker) {
	h.hostBlocker = c
}

// SetInstanceTracker attaches an InstanceTracker. 設定されると、署名検証に
// 成功するたびに対応 instance row の latestRequestReceivedAt が更新される。
func (h *Handler) SetInstanceTracker(t InstanceTracker) {
	h.instanceTracker = t
}

// SetChartHook attaches a ChartHook invoked after each successfully
// verified inbound request.
func (h *Handler) SetChartHook(c ChartHook) {
	h.chartHook = c
}

// signatureRelevantHeaders is the small set of HTTP headers worker-side
// verification needs to reconstruct the signed request. Capturing the
// full Header map would needlessly bloat the queue payload.
var signatureRelevantHeaders = []string{
	"Signature",
	"Date",
	"Host",
	"Digest",
	"Content-Type",
	"Content-Length",
	"Accept",
}

// captureSignatureHeaders extracts the headers needed to re-verify the
// HTTP signature in the inbox worker. Only the small allowlist above is
// included so the queue payload stays compact.
//
// Host は net/http で req.Host に格納される (Header map には入らない) た
// め、明示的に補完してから capture する。これが無いと worker 側 verify が
// `host: ""` で署名再構築して RSA verify が失敗する。
func captureSignatureHeaders(req *http.Request) map[string]string {
	out := make(map[string]string, len(signatureRelevantHeaders))
	for _, h := range signatureRelevantHeaders {
		if v := req.Header.Get(h); v != "" {
			out[h] = v
		}
	}
	if out["Host"] == "" && req.Host != "" {
		out["Host"] = req.Host
	}
	return out
}

// Inbox handles POST /inbox and POST /users/:id/inbox.
//
// User ごとの inbox であっても処理は同じ (現状のシンプル実装では shared
// inbox と同様に activity を dispatch するだけ)。
//
// #565 で signature 検証 / host block / instance touch / chart hook を
// すべて inbox worker (queue/processors) 側に移し、handler は body+
// signature 関連 header を payload に詰めて 202 即返しする「fast write」
// 設計に変更した。これにより HTTP handler の同期 RSA-2048 verify (~1-2ms)
// が消え、faker.send rps が ~2x 改善する (#564 bench)。Misskey TS と同じ
// アーキテクチャ (read-and-enqueue + verify-in-worker)。
//
// SetEnqueuer 未配線時は legacy synchronous mode に fallback し、handler
// 内で従来通り verify + block + track + chart + process を実行する。これは
// 単体テスト / 旧来配線を維持するためのもので、production 配線では
// SetEnqueuer 経由の async path を踏む。
func (h *Handler) Inbox(c echo.Context) error {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return c.NoContent(http.StatusBadRequest)
	}

	// Echo の Request からはホストヘッダが空のことがあるため、明示的に補完する。
	if c.Request().Header.Get("Host") == "" {
		c.Request().Header.Set("Host", c.Request().Host)
	}

	if h.enqueuer != nil {
		// Fast write path. signature 検証は worker 側で再現するので
		// handler では実施しない。最低限のチェックだけ handler で行う:
		// Signature ヘッダの presence (= 明らかな malformed を 401 で
		// 即返す)。これは O(1) の文字列存在 check で RSA は走らない。
		if c.Request().Header.Get("Signature") == "" {
			return c.NoContent(http.StatusUnauthorized)
		}
		payload := queue.InboxPayload{
			Body:    body,
			Method:  c.Request().Method,
			Path:    c.Request().URL.Path,
			Headers: captureSignatureHeaders(c.Request()),
		}
		if err := h.enqueuer.EnqueueInbox(c.Request().Context(), payload); err != nil {
			// queue 障害時は 500 を返して上流に retry させる (best-effort
			// を装って 202 で握りつぶすと activity が黙って消える)。
			slog.Error("inbox enqueue failed", "err", err)
			return c.NoContent(http.StatusInternalServerError)
		}
		return c.NoContent(http.StatusAccepted)
	}

	// Legacy synchronous fallback (enqueuer 未配線時)。テストや旧来配線を
	// 維持するために残してある。production では router.go が必ず SetEnqueuer を
	// 呼ぶため到達しない。なお本経路は worker 側 InboxProcessor が持つ
	// actor-authorization gate (署名者 != activity.actor のなりすまし検出,
	// #parity review AUTH-1/AUTH-4) を通らない。production が async 一択である
	// 前提のため許容しているが、将来この fallback を残すなら同 gate の適用
	// (または fallback 自体の撤去) が必要。
	actor, err := h.verifySignature(c.Request())
	if err != nil {
		slog.Warn("inbox signature verification failed", "err", err)
		return c.NoContent(http.StatusUnauthorized)
	}

	if h.isHostBlocked(actor) {
		return c.NoContent(http.StatusForbidden)
	}

	h.touchInstance(actor)
	h.commitChart(actor)

	return h.processSynchronously(c, body)
}

// processSynchronously runs federation.Processor.Process inline and maps
// the outcome to an HTTP status code. Kept as a fallback for tests and
// configurations that don't wire SetEnqueuer.
func (h *Handler) processSynchronously(c echo.Context, body []byte) error {
	if err := h.processor.Process(body); err != nil {
		if errors.Is(err, federation.ErrUnsupportedActivity) {
			// 未対応typeでも 202 Accepted を返す。
			return c.NoContent(http.StatusAccepted)
		}
		slog.Warn("inbox process failed", "err", err)
		return c.NoContent(http.StatusBadRequest)
	}
	return c.NoContent(http.StatusAccepted)
}

// verifySignature parses the Signature header, resolves the actor, and
// validates the request signature against the actor's stored public key.
// 戻り値の actor は後続の host block チェックに再利用される。
func (h *Handler) verifySignature(req *http.Request) (*model.User, error) {
	parsed, err := activitypub.ParseSignatureHeader(req.Header.Get("Signature"))
	if err != nil {
		return nil, err
	}
	actorURI := activitypub.ResolveKeyURL(parsed.KeyID)
	actor, err := h.resolver.ResolveActor(actorURI)
	if err != nil {
		return nil, err
	}
	// keyId fragment ベースで Ed25519 / RSA を dispatch する (#1067 / #1070)。
	pem, err := h.resolver.PublicKeyForKeyID(actor.ID, parsed.KeyID)
	if err != nil {
		return nil, err
	}
	// keyCache 経由で verify し、同一 (keyId, PEM) の x509 パースをメモ化する
	// (#1426)。挙動は VerifyRequest と等価。
	if err := h.keyCache.VerifyRequestCached(req, parsed.KeyID, pem); err != nil {
		return nil, err
	}
	return actor, nil
}

// isHostBlocked reports whether the actor's host is rejected by the local
// federation policy — either on the blocklist, or excluded by the
// federation mode (none / specified). hostBlocker が未設定 / actor が
// ローカル / Host nil なら false (#536)。
func (h *Handler) isHostBlocked(actor *model.User) bool {
	if h.hostBlocker == nil || actor == nil || actor.Host == nil {
		return false
	}
	host := *actor.Host
	if h.hostBlocker.IsBlocked(host) {
		return true
	}
	return !h.hostBlocker.IsAllowed(host)
}

// touchInstance is a best-effort hook into the InstanceTracker. tracker が
// 未設定 / actor がローカル / Host nil の場合は no-op。エラーは握りつぶす。
func (h *Handler) touchInstance(actor *model.User) {
	if h.instanceTracker == nil || actor == nil || actor.Host == nil {
		return
	}
	_ = h.instanceTracker.MarkRequestReceived(*actor.Host)
}

// commitChart fires the chart hook for one inbound request. Chart hook
// が未設定 / actor がローカル / Host nil の場合は no-op。
func (h *Handler) commitChart(actor *model.User) {
	if h.chartHook == nil || actor == nil || actor.Host == nil {
		return
	}
	h.chartHook.OnInboxReceived(*actor.Host)
}
