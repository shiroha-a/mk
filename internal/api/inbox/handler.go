// Package inbox provides the ActivityPub inbox endpoint.
package inbox

import (
	"context"
	"encoding/json"
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
	// FederationDisabled reports whether the instance federation mode is
	// "none". upstream ActivityPubServerService.inbox は federation==='none' の
	// とき body を読む前に 403 を即返す (#1959)。per-actor の IsAllowed は actor が
	// local 扱い (host nil) のとき素通る余地があるため、最前段の mode gate を別途置く。
	FederationDisabled() bool
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
	// expectedHost は config 由来の自ホスト (host:port)。inbox admission の
	// host 署名必須 + 一致チェックに使う。未設定時は host チェックを skip する
	// (#1949)。
	expectedHost string
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

// SetExpectedHost wires the configured host (host:port) used by inbox admission
// to require the request Host header to be signed and to match this server.
// Mirrors upstream `request.headers.host !== this.config.host` (#1949).
func (h *Handler) SetExpectedHost(host string) {
	h.expectedHost = host
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
	// upstream ActivityPubServerService.inbox と同じく、federation mode が "none" なら
	// body を読む前に 403 を即返す (#1959)。per-actor の IsAllowed gate は actor が
	// local 扱い (host nil) や async enqueue 後にしか効かない経路があるため、最前段で弾く。
	if h.hostBlocker != nil && h.hostBlocker.FederationDisabled() {
		return c.NoContent(http.StatusForbidden)
	}

	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		// BodyLimit (#1958) が wrap した limitedReader は 64KB 超過時に 413 HTTPError を
		// 返す。ContentLength での即時 reject は handler に到達しないが、chunked 超過は
		// ここで read error になるため echo.HTTPError を伝播して upstream fastify と同じ
		// 413 を返す (それ以外の read error は従来通り 400)。
		var he *echo.HTTPError
		if errors.As(err, &he) {
			return he
		}
		return c.NoContent(http.StatusBadRequest)
	}

	// Echo の Request からはホストヘッダが空のことがあるため、明示的に補完する。
	if c.Request().Header.Get("Host") == "" {
		c.Request().Header.Set("Host", c.Request().Host)
	}

	// upstream ActivityPubServerService.inbox と同じく、queue/処理に回す前に
	// host 署名+一致・digest 署名・digest 値 (sha256(body)) を検証する。RSA は
	// 走らない O(body) の安価なチェックで、fast write / legacy 両 path 共通。
	// Signature ヘッダ欠落/malformed もここで 401 になる (#1949 body integrity)。
	if err := h.admitInbox(c.Request(), body); err != nil {
		slog.Warn("inbox admission rejected", "err", err)
		return c.NoContent(http.StatusUnauthorized)
	}

	// upstream ActivityPubServerService.inbox (#17558): actor を持たない activity は
	// authenticate 不能なので enqueue せず 400 で弾く。mk-go は actor 欠落で worker 側が
	// drop する (crash はしない) が、無駄な enqueue/retry を避けるため早期 reject する。
	if !bodyHasActor(body) {
		slog.Warn("inbox rejected: activity has no actor")
		return c.NoContent(http.StatusBadRequest)
	}

	if h.enqueuer != nil {
		// Fast write path. signature の crypto 検証は worker 側で再現する。
		// admitInbox を上で通しているので handler 側の presence check は不要。
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

// admitInbox enforces the upstream inbox admission checks before the activity
// is queued or processed: the Host header must be signed and match the
// configured host, the Digest header must be signed, and its SHA-256 value must
// equal the body hash. Returns an error to reject with 401 (#1949). The Signature
// header must parse; a missing/malformed Signature also fails here.
func (h *Handler) admitInbox(req *http.Request, body []byte) error {
	parsed, err := activitypub.ParseSignatureHeader(req.Header.Get("Signature"))
	if err != nil {
		return err
	}
	return activitypub.VerifyInboxAdmission(parsed, req.Host, h.expectedHost, req.Header.Get("Digest"), body)
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

// bodyHasActor reports whether the inbox activity body is a JSON object with a
// non-null `actor` field. upstream #17558: actor 無し activity は authenticate
// 不能なので enqueue 前に弾く。actor は string ID / embedded object どちらも許容
// (presence + 非 null のみ判定)。
func bodyHasActor(body []byte) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return false // object でない (= 不正) → reject
	}
	actor, ok := probe["actor"]
	return ok && len(actor) > 0 && string(actor) != "null"
}
