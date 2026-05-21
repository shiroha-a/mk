package processors

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
)

// FederationProcessor is the narrow surface of federation.Processor that
// InboxProcessor consumes (#534). 切り出すことで unit test が
// federation.NewProcessor(...) の重い依存 chain (resolver / following /
// reaction / userRepo / noteRepo) を組まなくても済む。
type FederationProcessor interface {
	Process(body []byte) error
}

// SignatureVerifier resolves an actor and confirms an HTTP request was
// signed with that actor's published public key. Implemented by
// federation.Resolver in production. nil の場合 verify を skip するので、
// payload に headers が無い (= legacy 形式) ケースと同様に扱う。
//
// PublicKeyForKeyID は signature header の keyId fragment (`#main-key` /
// `#ed25519-key` 等) に対応する PEM を返す。user_publickey_extra (FEP-521a
// Multikey) を先に探し、miss なら user_publickey の primary key (RSA) を
// fallback で返す → drop-in 互換維持 (#1067 / #1070)。
type SignatureVerifier interface {
	ResolveActor(actorURI string) (*model.User, error)
	PublicKeyForActor(actorID string) (string, error)
	PublicKeyForKeyID(actorID, keyID string) (string, error)
}

// HostBlockChecker reports whether a host is on the blocked list and whether
// the federation mode allows ingesting from it (#536). nil なら check を skip。
type HostBlockChecker interface {
	IsBlocked(host string) bool
	IsAllowed(host string) bool
}

// InstanceTracker is invoked after a successfully verified inbound
// activity so that the instance row's latestRequestReceivedAt can be
// updated. nil なら no-op。
type InstanceTracker interface {
	MarkRequestReceived(host string) error
}

// InboxChartHook is invoked after a successfully verified inbound
// activity so the chart subsystem can record federation / instance /
// request charts. nil なら no-op。
//
// processors.ChartHook は deliver 側 (OnDelivered) で同名占有されている
// ので、inbox 用は別名に分けて両 driver が共存できるようにしている。
type InboxChartHook interface {
	OnInboxReceived(host string)
}

// InboxProcessor handles ap:inbox tasks. From #565 onwards, signature
// verification + host block + instance tracker + chart hook all run in
// this worker (previously they were synchronous in the HTTP handler).
// The handler now just builds the payload and 202-returns, so faker.send
// rps is bound by HTTP I/O instead of RSA-2048 verify (~1-2ms).
//
// 各 activity handler は冪等であることを前提とする (Misskey TS と同じ
// 戦略)。順序保証や per-actor lock は持たない。
// LDSignatureVerifier is the per-activity LD-Signature gate (#1164 Phase D)。
// signature field がある activity に対して RsaSignature2017 + 2026.5.4
// hardening (forbidden directives / cache cap / freeze) を実行する。nil なら
// LD-Sig 経路を skip (= HTTP Signature だけで認証完了とする後方互換)。
// 実装は core/federation.LDSignatureVerifier。
type LDSignatureVerifier interface {
	VerifyIfPresent(rawBody []byte) error
}

type InboxProcessor struct {
	processor       FederationProcessor
	verifier        SignatureVerifier
	ldVerifier      LDSignatureVerifier
	hostBlocker     HostBlockChecker
	instanceTracker InstanceTracker
	chartHook       InboxChartHook
}

// NewInboxProcessor constructs an InboxProcessor wrapping the supplied
// federation.Processor. Set* メソッドで verify / block / track / chart の
// 各 dep を別途配線する。未配線の dep は no-op として扱われる。
func NewInboxProcessor(p FederationProcessor) *InboxProcessor {
	return &InboxProcessor{processor: p}
}

// SetLDSignatureVerifier wires an optional LD-Signature verifier that runs
// after HTTP Signature verify but before activity dispatch. signature 無し
// activity は skip され、verify fail なら activity を drop (return nil で
// queue を ack するが Process は呼ばない)。
func (p *InboxProcessor) SetLDSignatureVerifier(v LDSignatureVerifier) {
	p.ldVerifier = v
}

// SetSignatureVerifier wires a verifier used to re-verify the inbound
// HTTP signature in the worker. Without it, payloads carrying Headers
// are dropped (handler should not have produced them).
func (p *InboxProcessor) SetSignatureVerifier(v SignatureVerifier) {
	p.verifier = v
}

// SetHostBlockChecker wires a checker used to reject activities from
// federation-blocked hosts after signature verification.
func (p *InboxProcessor) SetHostBlockChecker(c HostBlockChecker) {
	p.hostBlocker = c
}

// SetInstanceTracker wires the InstanceTracker used to update
// latestRequestReceivedAt for a verified actor's host.
func (p *InboxProcessor) SetInstanceTracker(t InstanceTracker) {
	p.instanceTracker = t
}

// SetChartHook wires the InboxChartHook used to record federation /
// inbox / request charts for a verified actor's host.
func (p *InboxProcessor) SetChartHook(h InboxChartHook) {
	p.chartHook = h
}

// Handle dispatches a single inbox task. driver runtime invokes this for
// every dequeued task.
//
// payload decode 失敗は再 retry しても無意味なので driver.SkipRetry で
// 確定 fail にする。worker 側の verify 失敗 / host block も retry せず
// silently drop する (sender に retry 要求しても解決しないため)。
// federation.ErrUnsupportedActivity は handler 不在 (= 受け付けたが何も
// しない) 扱いで成功扱い。それ以外の処理エラーは driver の retry policy
// (inboxJobMaxAttempts) に任せる。
func (p *InboxProcessor) Handle(_ context.Context, t driver.Task) error {
	payload, err := queue.DecodeInboxPayload(t.Payload())
	if err != nil {
		return fmt.Errorf("decode inbox payload: %w: %w", err, driver.SkipRetry)
	}

	// payload に Headers が含まれている = #565 の fast-write handler から
	// 来た payload。worker 側で signature を verify する。Headers が無い
	// (= legacy / direct enqueue) 経路では verify をスキップする (handler
	// 側で既に検証済みという旧 contract)。
	host := payload.Host
	if len(payload.Headers) > 0 && p.verifier != nil {
		actor, err := p.verifyPayload(payload)
		if err != nil {
			slog.Warn("inbox: signature verification failed in worker",
				"host", payload.Host, "err", err)
			return nil
		}
		if p.isBlocked(actor) {
			// upstream Misskey #17336 (= 2026.5.0 fix) は NoteCreateService 深部で
			// 出る "Instance is blocked" IdentifiableError を error handler で捕まえて
			// retry を防ぐ修正だが、mk-go は verify-in-worker 設計 (#565) でここに
			// signature verify 直後に block check を入れているため、blocked host の
			// activity は body parse / NoteCreate の段にすら届かず、retry にも乗らない。
			// よって upstream の追加 case 分岐は mk-go では不要 (= triage #1003 close)。
			return nil
		}
		if actor != nil && actor.Host != nil {
			host = *actor.Host
		}
		p.touchInstance(actor)
		p.commitChart(actor)
	}
	_ = host // reserved for future per-host stats; currently unused

	// LD-Signature verify (#1164 Phase D)。HTTP Signature 検証通過後の追加
	// gate で、activity body に signature field があれば RsaSignature2017 +
	// 2026.5.4 hardening を実行する。fail なら activity を drop (= queue ack
	// するが Process は呼ばない、upstream UnrecoverableError 互換挙動)。
	// signature 無し / verifier 未配線では skip (= HTTP Signature のみ
	// で従来通り処理)。
	if p.ldVerifier != nil {
		if err := p.ldVerifier.VerifyIfPresent(payload.Body); err != nil {
			slog.Warn("inbox: LD-Signature verification failed, dropping activity",
				"host", payload.Host, "err", err)
			return nil
		}
	}

	if err := p.processor.Process(payload.Body); err != nil {
		if errors.Is(err, federation.ErrUnsupportedActivity) {
			slog.Debug("inbox: unsupported activity, dropped", "host", payload.Host)
			return nil
		}
		return fmt.Errorf("process inbox activity: %w", err)
	}
	return nil
}

// verifyPayload reconstructs the signed HTTP request from the queued
// payload and validates it against the actor's stored public key.
//
// 復元する request は Method / Path / Headers / Body のみで、TLS / remote
// addr 等は含まれない。HTTP Signature の signing string は Method (大文字
// 小文字混在不可なので handler 側で normalize 済前提)、Path、`(request-
// target)` を含む header 集合だけが必要なので、これで足りる。
func (p *InboxProcessor) verifyPayload(payload queue.InboxPayload) (*model.User, error) {
	parsed, err := activitypub.ParseSignatureHeader(payload.Headers["Signature"])
	if err != nil {
		return nil, err
	}
	actorURI := activitypub.ResolveKeyURL(parsed.KeyID)
	actor, err := p.verifier.ResolveActor(actorURI)
	if err != nil {
		return nil, err
	}
	// keyId fragment ベースで Ed25519 / RSA を dispatch する (#1067 / #1070)。
	pem, err := p.verifier.PublicKeyForKeyID(actor.ID, parsed.KeyID)
	if err != nil {
		return nil, err
	}
	req, err := buildSignedRequest(payload)
	if err != nil {
		return nil, err
	}
	if err := activitypub.VerifyRequest(req, pem); err != nil {
		return nil, err
	}
	return actor, nil
}

// buildSignedRequest reconstructs an *http.Request that activitypub.
// VerifyRequest can read for HTTP Signature verification. Path は signing
// string の `(request-target)` 構築にだけ使われるため、scheme/host は
// dummy で良い。Body は Digest header の再計算 (verify 側) に使われる。
func buildSignedRequest(payload queue.InboxPayload) (*http.Request, error) {
	method := payload.Method
	if method == "" {
		method = http.MethodPost
	}
	path := payload.Path
	if path == "" {
		path = "/inbox"
	}
	rawURL := "http://placeholder" + path
	if _, err := url.Parse(rawURL); err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}
	req, err := http.NewRequest(method, rawURL, bytes.NewReader(payload.Body))
	if err != nil {
		return nil, err
	}
	for k, v := range payload.Headers {
		req.Header.Set(k, v)
	}
	if host := payload.Headers["Host"]; host != "" {
		req.Host = host
	}
	return req, nil
}

func (p *InboxProcessor) isBlocked(actor *model.User) bool {
	if p.hostBlocker == nil || actor == nil || actor.Host == nil {
		return false
	}
	host := *actor.Host
	if p.hostBlocker.IsBlocked(host) {
		return true
	}
	return !p.hostBlocker.IsAllowed(host)
}

func (p *InboxProcessor) touchInstance(actor *model.User) {
	if p.instanceTracker == nil || actor == nil || actor.Host == nil {
		return
	}
	_ = p.instanceTracker.MarkRequestReceived(*actor.Host)
}

func (p *InboxProcessor) commitChart(actor *model.User) {
	if p.chartHook == nil || actor == nil || actor.Host == nil {
		return
	}
	p.chartHook.OnInboxReceived(*actor.Host)
}
