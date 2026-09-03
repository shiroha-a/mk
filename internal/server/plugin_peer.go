package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
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

// apiGroupPrefix is the echo group every API route (plugins included) hangs off.
//
// **1 つの定数から作る。** ここと peerAPIPrefix が別々のリテラルだと、片方を
// 変えたときに BodyLimitByPath の表だけが外れて、上限が黙って /api の既定に
// 戻る (配線の gate は文字列一致なので緑のまま)。
const apiGroupPrefix = "/api"

// peerAPIPrefix is where plugin routes actually live.
//
// **`pluginRoutePrefix` だけでは足りない。** ルートは `/api` グループの下に
// 張られるので、送信先の URL を組むときは `/api` から始める。ここを間違えると
// SPA catchall に落ちて 405 が返り、「相手が受け取らない」という形で出る。
const peerAPIPrefix = apiGroupPrefix + pluginRoutePrefix

// 本文の上限はプラグインごとに決まる (plugin_peer_limit.go)。相手は同じ
// プラグインを持っているだけで善良とは限らないので、**受信側にも置く**
// (送信側の自制に頼らない)。

// peerTimeout bounds one delivery attempt.
const peerTimeout = 15 * time.Second

// peerHandlerTimeout bounds one inbound handler run.
//
// **送信側の 1 試行 (peerTimeout) より短くする。** 長いと、相手が諦めて再送に
// 移った後もこちらは走り続け、同じ交換の処理が重なる。
//
// プラグインのハンドラは**リクエストの中で同期に動く** (AP inbox のように
// キューへ逃がしていない)。ctx を見ないハンドラは打ち切れないので、これは
// 「DB 呼び出し等 ctx を見る処理が止まる」ところまでしか効かない。
const peerHandlerTimeout = 10 * time.Second

// peerRetryDelays are the waits between delivery attempts.
//
// **恒久的な失敗では使わない。** 4xx (429 を除く) は送り直しても同じ答えに
// なるので 1 回で止める (deliver 参照)。
//
// **プロセス内で完結させる (初版)。** キューに載せれば再起動をまたげるが、
// queue.Client への公開と専用プロセッサが要る。プラグイン側は「応答が
// 届かないこともある」前提で期限を持つ設計なので、まずはここまでにする。
var peerRetryDelays = []time.Duration{2 * time.Second, 10 * time.Second, 60 * time.Second}

// isPluginPeerPath reports whether p addresses a plugin's reserved peer endpoint.
//
// **catchall で 404 を返す対象を絞るためのもの (#2822)。** プラグインの通常の
// ルートまで 404 にすると、設定で無効化したプラグインのフロントエンドが
// 200 + {} ではなく例外を受け取るようになる。peer の受け口は mk-go 同士でしか
// 使わず `_` 予約なので、ここだけ切り替えれば UI の挙動は変わらない。
func isPluginPeerPath(p string) bool {
	// **長さを先に見る。** `/api/plugin/_peer` のように接頭辞と接尾辞が重なる
	// 値があり、確かめずに切ると slice の範囲外で panic する (実測)。
	if len(p) < len(peerAPIPrefix)+len(peerPath) {
		return false
	}
	if !strings.HasPrefix(p, peerAPIPrefix) || !strings.HasSuffix(p, peerPath) {
		return false
	}
	name := p[len(peerAPIPrefix) : len(p)-len(peerPath)]
	// プラグイン名は 1 セグメント。空も弾く。
	return name != "" && !strings.Contains(name, "/")
}

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
	// Forget drops the cached answer for host.
	//
	// 受け口が無いと分かった (404) ときに呼ぶ。肯定キャッシュは 6 時間なので、
	// 捨てないと相手がプラグインを外してからその間ずっと送り続ける。
	Forget(host string)
}

// peerStatusError is a non-2xx response from the peer.
type peerStatusError struct{ status int }

func (e *peerStatusError) Error() string { return fmt.Sprintf("status %d", e.status) }

// permanent reports whether retrying the same request cannot help.
//
// **429 と 5xx は時間で解ける。** それ以外の 4xx (受け口が無い / 署名が通らない /
// ブロックされている / 大きすぎる) は、同じものを送り直しても同じ答えになる。
func (e *peerStatusError) permanent() bool {
	return e.status >= 400 && e.status < 500 && e.status != http.StatusTooManyRequests
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
	// limiter は受け口のレート制限。**全プラグインで 1 つを共有する**
	// (プラグインごとに持つと、相手は受け口の数だけ枠を得る)。
	limiter *peerRateLimiter
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
	// retryDelays は再送の待ち時間。nil なら peerRetryDelays。**テストが
	// package 変数を差し替えないようにするためのもの** — 差し替えると、
	// 別のテストが起こした goroutine と競合する。
	retryDelays []time.Duration
	// maxBody はこのプラグインの本文上限 (エンベロープ込み)。受け口側の
	// 実効的な上限は global の BodyLimitByPath が同じ値で掛けており、ここは
	// 送信側と応答の読み取りに効く。
	maxBody int64

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

// Send posts a payload to the same plugin on host.
//
// **検査はここで済ませ、goroutine に逃がすのは通る見込みのものだけ。** 送ってから
// 失敗を知る形にすると、ブロックしている相手にも一度は接続してしまう。
func (p *pluginPeer) Send(ctx context.Context, host string, payload any) (string, error) {
	if !p.peered {
		return "", errNotPeered
	}
	host = normalizePeerHost(host)
	if host == "" {
		return "", fmt.Errorf("plugin peer: 宛先が空か、ホスト名として不正です")
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

	sendID := p.deps.idGen.Generate(time.Now())
	envelope, err := json.Marshal(peerEnvelope{ID: sendID, Payload: json.RawMessage(body)})
	if err != nil {
		return "", err
	}
	// **エンベロープで測る。** 受信側は本文全体に上限を掛けるので、送信側が
	// payload だけで測ると相関 ID の分 (aidx なら 36 バイト) だけ境界がずれ、
	// 上限ちょうどの payload がここを通って相手で 413 になる。
	if int64(len(envelope)) > p.maxBody {
		return "", fmt.Errorf("plugin peer: 本文が大きすぎます (%d bytes, 上限 %d)", len(envelope), p.maxBody)
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

	delays := p.retryDelays
	if delays == nil {
		delays = peerRetryDelays
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		reply, err := p.post(url, envelope)
		if err == nil {
			p.dispatchReply(host, sendID, reply)
			return
		}
		lastErr = err

		var se *peerStatusError
		if errors.As(err, &se) {
			if se.status == http.StatusNotFound && p.deps.remote != nil {
				// **肯定キャッシュを捨てる。** 受け口が無いと分かったのに
				// 覚えたままだと、6 時間のあいだ送り続ける。次の Send が
				// nodeinfo を引き直して止まる。
				p.deps.remote.Forget(host)
			}
			if se.permanent() {
				// 送り直しても同じ答えになる。**無関係なインスタンスに
				// こちらの都合でリクエストを重ねない** (この層の設計原則)。
				break
			}
		}

		if attempt >= len(delays) {
			break
		}
		time.Sleep(delays[attempt])
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
		return nil, &peerStatusError{status: res.StatusCode}
	}
	// **切り詰めない。** LimitReader で黙って先頭だけ返すと、OnReply に途中で
	// 切れた JSON が渡る。1 バイト多く読んで超過を検出し、エラーにする。
	reply, err := io.ReadAll(io.LimitReader(res.Body, p.maxBody+1))
	if err != nil {
		return nil, err
	}
	if int64(len(reply)) > p.maxBody {
		return nil, fmt.Errorf("応答が大きすぎます (上限 %d bytes)", p.maxBody)
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

		// **署名検証の前に IP で見る。** 検証は actor 解決 (未知の keyId なら
		// 外向きの取得) と公開鍵検証を伴うので、ここを通してしまうと相手は
		// 署名を持たないまま高い処理を無制限に起こせる。
		//
		// **key は middleware.IPHash を通す。** 生のアドレスだと IPv6 では
		// /64 の中でアドレスを回すだけで無限に枠を取れる (利用者に /64 が
		// 丸ごと割り当たるのは普通)。本体の rate limiter と同じ丸め方にする。
		//
		// ここより手前で global な auth.Authenticate が本文を読み終えている
		// ので、**本文の読み取りは止まらない** (それは BodyLimitByPath の
		// 仕事)。ここが止めるのは署名検証から先。
		if !p.deps.limiter.allow("ip:" + middleware.IPHash(c.RealIP())) {
			return peerTooManyRequests(c)
		}

		// 実効的な上限は global の BodyLimitByPath が同じ値で掛けている
		// (署名検証より前に body が読まれるため、handler で判定しても消費は
		// 止まらない)。ここは middleware を通らない経路への保険。
		body, err := io.ReadAll(io.LimitReader(req.Body, p.maxBody+1))
		if err != nil {
			var he *echo.HTTPError
			if errors.As(err, &he) {
				// BodyLimitByPath が wrap した limitedReader の 413 を潰さない。
				return he
			}
			return peerError(c, http.StatusBadRequest, "リクエストを読めません")
		}
		if int64(len(body)) > p.maxBody {
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
		// **確定したホストでも見る。** IP は共有されうる (前段のプロキシ、
		// 同居インスタンス) ので、認証を通った相手ごとの枠を別に持つ。
		if !p.deps.limiter.allow("host:" + from) {
			return peerTooManyRequests(c)
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

		ctx, cancel := context.WithTimeout(req.Context(), peerHandlerTimeout)
		defer cancel()
		res, err := fn(ctx, from, env.Payload)
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

// peerTooManyRequests answers a throttled caller.
//
// **Retry-After を付ける。** 送信側は失敗を 2 秒 / 10 秒 / 60 秒で再送するので、
// 目安が無いと同じ勢いで戻ってくる。
func peerTooManyRequests(c echo.Context) error {
	c.Response().Header().Set("Retry-After", "60")
	return peerError(c, http.StatusTooManyRequests, "リクエストが多すぎます")
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
		activitypub.InboxDateHeader(req.Header), req.Header.Get("Digest"), body); err != nil {
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
	// 受信側でも形を見る。ここを通した値はブロック判定の key になり、そのまま
	// プラグインの handler へ渡る。
	from := normalizePeerHost(*actor.Host)
	if from == "" {
		return "", fmt.Errorf("送信元のホスト名が不正です")
	}
	return from, nil
}

// peerHostPattern is the shape a peer host must have: LDH labels, optional port.
//
// IDN は Misskey と同じく punycode で保存される (mk-go も #2706 で保存側を揃えた)
// ので ASCII に閉じてよい。
var peerHostPattern = regexp.MustCompile(
	`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`)

// normalizePeerHost trims a host to its comparable form, returning "" when the
// result is not a plain host.
//
// **形まで見るのがこの関数の仕事。** scheme を剥がして小文字にするだけだと
// `example.test@10.0.0.1` のような値が peerURL / nodeinfo の URL にそのまま
// 連結され、userinfo として解釈されて別のホストへ飛ぶ。この層は「宛先の検査は
// ここで担保する」と宣言しているので、プラグインに任せてはいけない。
//
// SSRF 自体は outbound が safehttp なので塞がっている。ここで防ぐのは、意図
// しない**公開**ホストへ署名付きのリクエストを送ってしまうこと。
func normalizePeerHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimSuffix(host, "/")
	host = strings.ToLower(host)
	if !peerHostPattern.MatchString(host) {
		return ""
	}
	return host
}

// apiCatchall answers requests to /api paths that no route claimed.
//
// **無名関数のままにしない。** router を組み立てるには DB / Redis が要るので、
// ここに置いて直接呼べる形にしておかないと挙動をテストで固定できない
// (実際 #2822 まで catchall のテストは 1 つも無かった)。
func apiCatchall(c echo.Context) error {
	path := c.Request().URL.Path
	slog.Warn("unimplemented API endpoint", "method", c.Request().Method, "path", path)
	// GET は upstream ApiServerService の `fastify.get('/*')` と同じく 404
	// UNKNOWN_API_ENDPOINT。200 を返すと SPA catchall と区別が付かず、
	// 「/api 配下なのに HTML が返る」状態をクライアントが検出できない。
	//
	// **プラグイン同士の通信の受け口も、GET 以外で 404 を返す (#2822)。**
	// 200 + {} だと送信側から「受け口が無い」と「プラグインが空の応答を
	// 返した」が区別できず、OnReply が偽の応答で呼ばれる。この経路は mk-go
	// 同士でしか使わないので、公式フロント向けの pass-through とは事情が違う。
	//
	// それ以外の GET 以外は意図的に 200 + 空オブジェクトのまま。未登録
	// エンドポイントへの 404 は Misskey 公式フロントの一部ページで例外を
	// 投げてしまうため、実装が出揃うまで pass-through にしている。実装漏れは
	// warn ログで検知する。
	if c.Request().Method == http.MethodGet || isPluginPeerPath(path) {
		return c.JSON(http.StatusNotFound, apierr.Error(
			"UNKNOWN_API_ENDPOINT",
			"Unknown API endpoint.",
			"2ca3b769-540a-4f08-9dd5-b5a825b6d0f1",
		))
	}
	return c.JSON(http.StatusOK, map[string]any{})
}
