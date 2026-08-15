package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/plugin"
)

/*
 * プラグイン同士の通信 (#2537)。
 *
 * **ActivityPub には出さない。** AP に載せるとこちらの不具合の症状が他人の
 * サーバー側に出るうえ、一度公開した形は後から塞げない (#2476)。ここは
 * mk-go 専用の HTTP 経路に閉じるので、壊れても他実装には届かない。
 *
 * 宛先の検査・署名・ブロック・大きさ・リトライは全部この層で担保する。
 * プラグインに任せると、プラグインの数だけ抜けが増える。
 */

// peerPath is the reserved endpoint every peered plugin exposes.
const peerPath = "/_peer"

// peerAPIPrefix is where plugin routes actually live.
//
// **`pluginRoutePrefix` だけでは足りない。** ルートは `/api` グループの下に
// 張られるので、送信先の URL を組むときは `/api` から始める。ここを間違えると
// SPA catchall に落ちて 405 が返り、「相手が受け取らない」という形で出る。
const peerAPIPrefix = "/api" + pluginRoutePrefix

// peerMaxBody bounds one request and one reply.
//
// 相手は同じプラグインを持っているだけで善良とは限らない。**受信側にも
// 上限を置く** (送信側の自制に頼らない)。
const peerMaxBody = 1 << 20

// peerTimeout bounds one delivery attempt.
const peerTimeout = 15 * time.Second

// peerRetryDelays are the waits between delivery attempts.
//
// **プロセス内で完結させる (初版)。** キューに載せれば再起動をまたげるが、
// queue.Client への公開と専用プロセッサが要る。プラグイン側は「応答が
// 届かないこともある」前提で期限を持つ設計なので、まずはここまでにする。
var peerRetryDelays = []time.Duration{2 * time.Second, 10 * time.Second, 60 * time.Second}

// peerActorResolver resolves the sender of an incoming request.
type peerActorResolver interface {
	ResolveActor(uri string) (*model.User, error)
	PublicKeyForKeyID(actorID, keyID string) (string, error)
}

// peerHostBlocker mirrors the federation policy checks used by the inbox.
type peerHostBlocker interface {
	IsBlocked(host string) bool
	IsAllowed(host string) bool
}

// peerSigner provides the instance actor key used to sign outgoing requests.
type peerSigner interface {
	Signer() (*activitypub.PrivateKey, error)
}

// peerPluginLister reports which plugins a remote host declares in nodeinfo.
type peerPluginLister interface {
	Plugins(ctx context.Context, host string) ([]string, error)
}

// pluginPeerDeps is what the peer channel needs from the server.
type pluginPeerDeps struct {
	selfHost string
	client   *http.Client
	resolver peerActorResolver
	keyCache *activitypub.PublicKeyCache
	blocker  peerHostBlocker
	signer   peerSigner
	remote   peerPluginLister
	idGen    id.Generator
	// urlFor builds the peer endpoint URL. テストから http の httptest
	// サーバーへ向けられるように関数にしてある。nil なら既定 (https)。
	urlFor func(host, plugin string) string
}

// peerURL builds the endpoint URL for a host.
func (d *pluginPeerDeps) peerURL(host, plugin string) string {
	if d.urlFor != nil {
		return d.urlFor(host, plugin)
	}
	return "https://" + host + peerAPIPrefix + plugin + peerPath
}

// pluginPeer implements plugin.Peer for one plugin.
type pluginPeer struct {
	name   string
	peered bool
	deps   *pluginPeerDeps
	logger *slog.Logger

	mu      sync.RWMutex
	handler plugin.PeerHandler
	onReply plugin.PeerReplyHandler
}

// errNotPeered is returned when a plugin uses Peer without declaring it.
var errNotPeered = fmt.Errorf("plugin: Definition.Peered を立てていないため peer 経路は使えません")

func (p *pluginPeer) Handle(fn plugin.PeerHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handler = fn
}

func (p *pluginPeer) OnReply(fn plugin.PeerReplyHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onReply = fn
}

func (p *pluginPeer) handlerFn() plugin.PeerHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.handler
}

func (p *pluginPeer) replyFn() plugin.PeerReplyHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.onReply
}

// Has reports whether host declares this plugin in its nodeinfo.
func (p *pluginPeer) Has(ctx context.Context, host string) (bool, error) {
	if !p.peered {
		return false, errNotPeered
	}
	host = normalizePeerHost(host)
	if host == "" || host == p.deps.selfHost {
		return false, nil
	}
	if p.blocked(host) {
		return false, nil
	}
	if p.deps.remote == nil {
		return false, fmt.Errorf("plugin peer: nodeinfo を引く経路が未配線です")
	}
	names, err := p.deps.remote.Plugins(ctx, host)
	if err != nil {
		return false, err
	}
	for _, n := range names {
		if n == p.name {
			return true, nil
		}
	}
	return false, nil
}

// Send queues a payload for the same plugin on host.
//
// **検査はここで済ませ、キューに積むのは通る見込みのものだけ。** 送ってから
// 失敗を知る形にすると、ブロックしている相手にも一度は接続してしまう。
func (p *pluginPeer) Send(ctx context.Context, host string, payload any) (string, error) {
	if !p.peered {
		return "", errNotPeered
	}
	host = normalizePeerHost(host)
	if host == "" {
		return "", fmt.Errorf("plugin peer: 宛先が空です")
	}
	if host == p.deps.selfHost {
		return "", fmt.Errorf("plugin peer: 自分自身には送れません")
	}
	if p.blocked(host) {
		return "", fmt.Errorf("plugin peer: %s はブロックされています", host)
	}
	ok, err := p.Has(ctx, host)
	if err != nil {
		return "", fmt.Errorf("plugin peer: %s の nodeinfo を引けません: %w", host, err)
	}
	if !ok {
		return "", fmt.Errorf("plugin peer: %s は %s を持っていません", host, p.name)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("plugin peer: payload を JSON 化できません: %w", err)
	}
	if len(body) > peerMaxBody {
		return "", fmt.Errorf("plugin peer: payload が大きすぎます (%d bytes)", len(body))
	}

	sendID := p.deps.idGen.Generate(time.Now())
	envelope, err := json.Marshal(peerEnvelope{ID: sendID, Payload: json.RawMessage(body)})
	if err != nil {
		return "", err
	}

	// **呼び出し元の ctx を引き継がない。** HTTP リクエストの ctx はハンドラが
	// 返ると切れるので、そのまま渡すと送信前に必ず中断される。
	p.goSafe(func() { p.deliver(host, sendID, envelope) })
	return sendID, nil
}

// goSafe runs fn in a goroutine, recovering panics.
//
// **プロセスごと落とさない。** Go は他の goroutine の panic を回収できないので、
// ここで必ず受ける (pluginContext.Go と同じ理由)。
func (p *pluginPeer) goSafe(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				p.logger.Error("peer の配送で panic しました", "panic", r)
			}
		}()
		fn()
	}()
}

func (p *pluginPeer) blocked(host string) bool {
	if p.deps.blocker == nil {
		return false
	}
	return p.deps.blocker.IsBlocked(host) || !p.deps.blocker.IsAllowed(host)
}

// peerEnvelope wraps the plugin's payload with the correlation id.
//
// 相関 ID を**本体が振る**ので、プラグインは payload に自前の ID を埋めなくて
// よい。中身 (Payload) は解釈しない。
type peerEnvelope struct {
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
}

// deliver posts the envelope, retrying a few times, then hands the reply to
// the plugin.
func (p *pluginPeer) deliver(host, sendID string, envelope []byte) {
	url := p.deps.peerURL(host, p.name)

	var lastErr error
	for attempt := 0; ; attempt++ {
		reply, err := p.post(url, envelope)
		if err == nil {
			p.dispatchReply(host, sendID, reply)
			return
		}
		lastErr = err
		if attempt >= len(peerRetryDelays) {
			break
		}
		time.Sleep(peerRetryDelays[attempt])
	}
	// **握りつぶさずログに出す。** 届かなかったことはプラグインには通知
	// されないので (OnReply が呼ばれないだけ)、運営者が追える経路を残す。
	p.logger.Warn("peer への送信に失敗しました", "host", host, "id", sendID, "err", lastErr)
}

func (p *pluginPeer) post(url string, body []byte) (json.RawMessage, error) {
	key, err := p.deps.signer.Signer()
	if err != nil {
		return nil, fmt.Errorf("署名鍵を取れません: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), peerTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// 署名対象は AP の deliver と同じ 4 つ。受信側は inbox と同じ受け入れ判定
	// ((request-target) / date / host が署名対象であること) を通すので、
	// ここを減らすと相手に弾かれる。
	digest := activitypub.SHA256Digest(body)
	if err := activitypub.SignRequest(req, key, digest,
		[]string{"(request-target)", "date", "host", "digest"}); err != nil {
		return nil, fmt.Errorf("署名できません: %w", err)
	}

	res, err := p.deps.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close() //nolint:errcheck // 読み捨て

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d", res.StatusCode)
	}
	reply, err := io.ReadAll(io.LimitReader(res.Body, peerMaxBody))
	if err != nil {
		return nil, err
	}
	if len(reply) == 0 {
		return nil, nil
	}
	return reply, nil
}

func (p *pluginPeer) dispatchReply(host, sendID string, reply json.RawMessage) {
	fn := p.replyFn()
	if fn == nil || len(reply) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), peerTimeout)
	defer cancel()
	if err := fn(ctx, host, sendID, reply); err != nil {
		p.logger.Warn("peer の応答処理でエラーが返りました", "host", host, "id", sendID, "err", err)
	}
}

// echoHandler serves an incoming peer request.
//
// **wrapPluginHandler には載せない。** 署名検証に生の *http.Request が要る
// うえ、これは本体の責務であってプラグインのハンドラではない。
func (p *pluginPeer) echoHandler() echo.HandlerFunc {
	return func(c echo.Context) error {
		req := c.Request()

		body, err := io.ReadAll(io.LimitReader(req.Body, peerMaxBody+1))
		if err != nil {
			return peerError(c, http.StatusBadRequest, "リクエストを読めません")
		}
		if len(body) > peerMaxBody {
			return peerError(c, http.StatusRequestEntityTooLarge, "リクエストが大きすぎます")
		}

		from, err := p.verify(req, body)
		if err != nil {
			p.logger.Debug("peer の署名検証に失敗しました", "err", err)
			return peerError(c, http.StatusUnauthorized, "署名を検証できません")
		}
		if p.blocked(from) {
			return peerError(c, http.StatusForbidden, "ブロックされています")
		}

		var env peerEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			return peerError(c, http.StatusBadRequest, "リクエストを読めません")
		}

		fn := p.handlerFn()
		if fn == nil {
			// 宣言はしているがハンドラを登録していない。相手が悪いわけでは
			// ないので「こちらが受けられない」と伝える。
			return peerError(c, http.StatusNotImplemented, "受信ハンドラが登録されていません")
		}

		res, err := fn(req.Context(), from, env.Payload)
		if err != nil {
			// **プラグインのエラー文面は返さない。** 内部事情 (DB のエラー等)
			// が相手のサーバーに漏れる。ログにだけ残す。
			p.logger.Error("peer ハンドラがエラーを返しました", "from", from, "err", err)
			return peerError(c, http.StatusInternalServerError, "処理できません")
		}
		if res == nil {
			return c.NoContent(http.StatusNoContent)
		}
		return c.JSON(http.StatusOK, res)
	}
}

func peerError(c echo.Context, status int, msg string) error {
	return c.JSON(status, map[string]any{"error": map[string]any{"message": msg}})
}

// verify checks the HTTP Signature and returns the sending host.
//
// **名乗りは使わない。** Host ヘッダや payload の中身ではなく、署名した鍵の
// 持ち主から確定する。受け入れ判定は inbox と同じものを通す ((request-target)
// / date / host が署名対象で、Digest が本文と一致すること)。
func (p *pluginPeer) verify(req *http.Request, body []byte) (string, error) {
	parsed, err := activitypub.ParseSignatureHeader(req.Header.Get("Signature"))
	if err != nil {
		return "", err
	}
	if err := activitypub.VerifyInboxAdmission(parsed, req.Host, p.deps.selfHost,
		req.Header.Get("Digest"), body); err != nil {
		return "", err
	}
	actorURI := activitypub.ResolveKeyURL(parsed.KeyID)
	actor, err := p.deps.resolver.ResolveActor(actorURI)
	if err != nil {
		return "", err
	}
	pem, err := p.deps.resolver.PublicKeyForKeyID(actor.ID, parsed.KeyID)
	if err != nil {
		return "", err
	}
	if _, err := p.deps.keyCache.VerifyRequestCached(req, parsed.KeyID, pem); err != nil {
		return "", err
	}
	if actor.Host == nil || *actor.Host == "" {
		return "", fmt.Errorf("送信元がローカルユーザーです")
	}
	return normalizePeerHost(*actor.Host), nil
}

// normalizePeerHost trims a host to its comparable form.
func normalizePeerHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimSuffix(host, "/")
	return strings.ToLower(host)
}
