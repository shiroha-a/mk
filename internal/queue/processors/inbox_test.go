package processors_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/queue/processors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubFedProcessor は processors.FederationProcessor を満たす最小スタブ。
// federation.NewProcessor の重い依存 chain (resolver / userRepo / noteRepo)
// を組まずに InboxProcessor の dispatch 経路を unit-test するため切る。
type stubFedProcessor struct {
	calls    [][]byte
	returnFn func(body []byte) error
}

func (s *stubFedProcessor) Process(body []byte) error {
	s.calls = append(s.calls, body)
	if s.returnFn != nil {
		return s.returnFn(body)
	}
	return nil
}

func mustEncode(t *testing.T, p queue.InboxPayload) []byte {
	t.Helper()
	b, err := json.Marshal(p)
	require.NoError(t, err)
	return b
}

// #534: InboxProcessor は payload decode 失敗を SkipRetry で確定 fail に
// する (壊れた payload を retry しても無限ループするため)。
func TestInboxProcessor_DecodeFailureIsSkipRetry(t *testing.T) {
	p := processors.NewInboxProcessor(&stubFedProcessor{})

	err := p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     []byte(`{not json`),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, driver.SkipRetry),
		"malformed payload should bubble up driver.SkipRetry to suppress retries")
}

// 正常 path: federation.Processor.Process が nil を返せば Handle も nil。
func TestInboxProcessor_HappyPathDelegatesToFederation(t *testing.T) {
	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)

	body := []byte(`{"type":"Follow","actor":"https://e/u/a"}`)
	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, queue.InboxPayload{Body: body, Host: "e"}),
	}))
	require.Len(t, stub.calls, 1)
	assert.JSONEq(t, string(body), string(stub.calls[0]))
}

// ErrUnsupportedActivity は HTTP 同期処理時と同じく成功扱い。worker が
// 永続 retry に持ち込まないようにするため。
func TestInboxProcessor_UnsupportedActivityIsSwallowed(t *testing.T) {
	stub := &stubFedProcessor{returnFn: func(_ []byte) error {
		return federation.ErrUnsupportedActivity
	}}
	p := processors.NewInboxProcessor(stub)

	err := p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, queue.InboxPayload{Body: []byte(`{}`)}),
	})
	assert.NoError(t, err, "unsupported type should not fail the worker")
}

// stubVerifier returns the supplied actor + pubkey deterministically so
// worker-side signature verification can be exercised without the full
// federation.Resolver dep chain.
type stubVerifier struct {
	actor  *model.User
	pubKey string
}

func (s *stubVerifier) ResolveActor(_ string) (*model.User, error) {
	if s.actor == nil {
		return nil, errors.New("not found")
	}
	return s.actor, nil
}

func (s *stubVerifier) PublicKeyForActor(_ string) (string, error) {
	if s.pubKey == "" {
		return "", errors.New("no key")
	}
	return s.pubKey, nil
}

// PublicKeyForKeyID は dual lookup を mock では行わず、常に primary key を
// 返す。Ed25519 / keyId fragment 別 dispatch のテストは federation.Resolver
// 側の integration test で cover する。
func (s *stubVerifier) PublicKeyForKeyID(actorID, _ string) (string, error) {
	return s.PublicKeyForActor(actorID)
}

type stubBlocker struct{ blocked, allowed func(string) bool }

func (s *stubBlocker) IsBlocked(h string) bool {
	if s.blocked == nil {
		return false
	}
	return s.blocked(h)
}

func (s *stubBlocker) IsAllowed(h string) bool {
	if s.allowed == nil {
		return true
	}
	return s.allowed(h)
}

type stubInstanceTracker struct{ hosts []string }

func (s *stubInstanceTracker) MarkRequestReceived(host string) error {
	s.hosts = append(s.hosts, host)
	return nil
}

type stubInboxChartHook struct{ hosts []string }

func (s *stubInboxChartHook) OnInboxReceived(host string) {
	s.hosts = append(s.hosts, host)
}

// signed builds a payload whose Headers/Method/Path can be re-verified
// against the supplied private key. Mirrors what the new handler emits.
func signedInboxPayload(t *testing.T, key *activitypub.PrivateKey, body []byte) queue.InboxPayload {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "https://example.com/inbox", nil)
	req.Host = "example.com"
	digest := activitypub.SHA256Digest(body)
	require.NoError(t, activitypub.SignRequest(req, key, digest, []string{"(request-target)", "date", "host", "digest"}))
	headers := map[string]string{}
	for _, h := range []string{"Signature", "Date", "Host", "Digest"} {
		if v := req.Header.Get(h); v != "" {
			headers[h] = v
		}
	}
	if headers["Host"] == "" && req.Host != "" {
		headers["Host"] = req.Host
	}
	return queue.InboxPayload{
		Body:    body,
		Method:  req.Method,
		Path:    req.URL.Path,
		Headers: headers,
	}
}

// #565: payload に Headers が含まれている場合、worker は signature を
// 再 verify し、成功時に instance tracker / chart hook を呼ぶ。
func TestInboxProcessor_VerifiesSignatureAndCommitsHooks(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)

	host := "remote.example"
	aliceURI := "https://remote.example/users/alice"
	verifier := &stubVerifier{
		actor:  &model.User{ID: "alice", Host: &host, URI: &aliceURI},
		pubKey: pub,
	}
	tracker := &stubInstanceTracker{}
	chart := &stubInboxChartHook{}

	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(verifier)
	p.SetInstanceTracker(tracker)
	p.SetChartHook(chart)

	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice"}`)
	payload := signedInboxPayload(t, key, body)

	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, payload),
	}))
	require.Len(t, stub.calls, 1, "Process should run after verify")
	assert.Equal(t, []string{host}, tracker.hosts)
	assert.Equal(t, []string{host}, chart.hosts)
}

// host が blockedHosts に入っていれば verify 後に Process まで到達せず
// silently drop される (sender に retry 要求しない)。
func TestInboxProcessor_BlockedHostDropped(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)

	host := "remote.example"
	verifier := &stubVerifier{
		actor:  &model.User{ID: "alice", Host: &host},
		pubKey: pub,
	}
	blocker := &stubBlocker{blocked: func(h string) bool { return h == host }}

	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(verifier)
	p.SetHostBlockChecker(blocker)

	body := []byte(`{"type":"Follow"}`)
	payload := signedInboxPayload(t, key, body)

	err = p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, payload),
	})
	assert.NoError(t, err, "blocked host should not bubble up an error")
	assert.Empty(t, stub.calls, "Process must NOT run for blocked host")
}

// signature 検証失敗 (例: pubkey 不一致) は drop で扱う。retry しても
// 成功しない静的 error なので driver retry を抑止する。
func TestInboxProcessor_VerifyFailureDropped(t *testing.T) {
	priv, _, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)

	// 別 keypair の pub を持たせる → verify が失敗する
	_, otherPub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)

	host := "remote.example"
	verifier := &stubVerifier{
		actor:  &model.User{ID: "alice", Host: &host},
		pubKey: otherPub,
	}
	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(verifier)

	payload := signedInboxPayload(t, key, []byte(`{"type":"Follow"}`))
	err = p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, payload),
	})
	assert.NoError(t, err, "verify failures are dropped silently, not retried")
	assert.Empty(t, stub.calls)
}

// hostBlocker が "allowed=false" を返した場合も blocked 経路と同じく
// silently drop されること (IsBlocked=false でも IsAllowed=false なら
// federation policy 上 reject)。
func TestInboxProcessor_DisallowedHostDropped(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)
	host := "remote.example"
	verifier := &stubVerifier{actor: &model.User{ID: "alice", Host: &host}, pubKey: pub}
	blocker := &stubBlocker{
		blocked: func(string) bool { return false },
		allowed: func(string) bool { return false },
	}
	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(verifier)
	p.SetHostBlockChecker(blocker)

	payload := signedInboxPayload(t, key, []byte(`{}`))
	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, payload),
	}))
	assert.Empty(t, stub.calls)
}

// host が nil な actor (= local) は blocked / tracker / chart 全て
// no-op で素通りし Process まで到達する。
func TestInboxProcessor_LocalActorSkipsHostDependentHooks(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)
	verifier := &stubVerifier{actor: &model.User{ID: "alice", Host: nil}, pubKey: pub}
	tracker := &stubInstanceTracker{}
	chart := &stubInboxChartHook{}
	blocker := &stubBlocker{}

	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(verifier)
	p.SetHostBlockChecker(blocker)
	p.SetInstanceTracker(tracker)
	p.SetChartHook(chart)

	payload := signedInboxPayload(t, key, []byte(`{}`))
	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, payload),
	}))
	require.Len(t, stub.calls, 1)
	assert.Empty(t, tracker.hosts, "local actor must not trigger instance tracker")
	assert.Empty(t, chart.hosts, "local actor must not trigger chart hook")
}

// signature header が壊れていれば parse 段階で失敗 → drop。
func TestInboxProcessor_InvalidSignatureHeaderDropped(t *testing.T) {
	verifier := &stubVerifier{actor: &model.User{ID: "alice"}, pubKey: "x"}
	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(verifier)

	payload := queue.InboxPayload{
		Body:    []byte(`{}`),
		Method:  "POST",
		Path:    "/inbox",
		Headers: map[string]string{"Signature": "not-a-valid-sig", "Host": "x", "Date": "today"},
	}
	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, payload),
	}))
	assert.Empty(t, stub.calls)
}

// resolver が actor を見つけられない (= ResolveActor error) と verify は
// drop される (404 sender に伝えても解決しないため retry しない)。
func TestInboxProcessor_ResolveActorErrorDropped(t *testing.T) {
	priv, _, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)

	verifier := &stubVerifier{actor: nil, pubKey: ""}
	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(verifier)

	payload := signedInboxPayload(t, key, []byte(`{}`))
	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, payload),
	}))
	assert.Empty(t, stub.calls)
}

// Headers が空の payload (= legacy 形式 / 同期 handler 経路) では verify
// は skip され、Process が直接走る。後方互換のため。
func TestInboxProcessor_LegacyPayloadSkipsVerify(t *testing.T) {
	verifier := &stubVerifier{} // never called
	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(verifier)

	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body: mustEncode(t, queue.InboxPayload{
			Body: []byte(`{"type":"Follow"}`),
			Host: "legacy.example",
		}),
	}))
	require.Len(t, stub.calls, 1, "legacy payload still reaches Process")
}

// --- LD-Signature gate (#1164 Phase D) ---

// stubLDVerifier captures VerifyIfPresent calls and lets tests force the
// outcome.
type stubLDVerifier struct {
	err       error
	callCount int
	// VerifyAndCreator outcome (forwarded-activity path).
	creator      string
	present      bool
	creatorCount int
}

func (s *stubLDVerifier) VerifyIfPresent(_ []byte) error {
	s.callCount++
	return s.err
}

func (s *stubLDVerifier) VerifyAndCreator(_ []byte) (string, bool, error) {
	s.creatorCount++
	return s.creator, s.present, s.err
}

// multiActorVerifier resolves different actors keyed by the (fragment-less)
// actor URI passed to ResolveActor, so actor-authorization tests can model a
// signer and an LD-Signature creator that map to distinct users.
type multiActorVerifier struct {
	pubKey string
	byURI  map[string]*model.User
}

func (m *multiActorVerifier) ResolveActor(actorURI string) (*model.User, error) {
	if u, ok := m.byURI[actorURI]; ok {
		return u, nil
	}
	return nil, errors.New("not found")
}

func (m *multiActorVerifier) PublicKeyForActor(_ string) (string, error) {
	return m.pubKey, nil
}

func (m *multiActorVerifier) PublicKeyForKeyID(_, _ string) (string, error) {
	return m.pubKey, nil
}

func uptr(s string) *string { return &s }

// 署名者 (HTTP signature) と activity body の actor が異なり、LD-Signature も
// 無い場合は actor spoofing として drop される。
func TestInboxProcessor_ActorSpoofDropped(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	// 署名者 = evil/bob だが body actor は victim/alice を詐称する。
	key, err := activitypub.NewPrivateKey("https://evil.example/users/bob#main-key", priv)
	require.NoError(t, err)

	bobHost := "evil.example"
	verifier := &multiActorVerifier{
		pubKey: pub,
		byURI: map[string]*model.User{
			"https://evil.example/users/bob": {ID: "bob", Host: &bobHost, URI: uptr("https://evil.example/users/bob")},
		},
	}
	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(verifier)
	p.SetLDSignatureVerifier(&stubLDVerifier{present: false})

	body := []byte(`{"type":"Delete","actor":"https://victim.example/users/alice","object":"https://victim.example/notes/1"}`)
	payload := signedInboxPayload(t, key, body)
	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, payload),
	}))
	assert.Empty(t, stub.calls, "spoofed actor (signer != actor, no LD-sig) must be dropped")
}

// 署名者 != actor でも、LD-Signature が body actor を正しく認証していれば
// 転送活動として受理する (Mastodon-style forwarding)。
func TestInboxProcessor_ForwardedWithValidLDSig(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	// 署名者 = relay。
	key, err := activitypub.NewPrivateKey("https://relay.example/actor#main-key", priv)
	require.NoError(t, err)

	relayHost := "relay.example"
	originHost := "origin.example"
	verifier := &multiActorVerifier{
		pubKey: pub,
		byURI: map[string]*model.User{
			"https://relay.example/actor":        {ID: "relay", Host: &relayHost, URI: uptr("https://relay.example/actor")},
			"https://origin.example/users/alice": {ID: "alice", Host: &originHost, URI: uptr("https://origin.example/users/alice")},
		},
	}
	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(verifier)
	// LD-Signature が origin/alice の鍵で署名済 (creator が alice を指す)。
	p.SetLDSignatureVerifier(&stubLDVerifier{present: true, creator: "https://origin.example/users/alice#main-key"})

	body := []byte(`{"type":"Create","actor":"https://origin.example/users/alice","signature":{"type":"RsaSignature2017"}}`)
	payload := signedInboxPayload(t, key, body)
	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, payload),
	}))
	require.Len(t, stub.calls, 1, "forwarded activity authenticated by LD-Signature must be processed")
}

// activity.actor が embedded object ({"id": ...}) 形式でも署名者 URI と
// 照合できる (upstream #17340 互換)。
func TestInboxProcessor_ActorObjectFormMatches(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)

	host := "remote.example"
	verifier := &multiActorVerifier{
		pubKey: pub,
		byURI: map[string]*model.User{
			"https://remote.example/users/alice": {ID: "alice", Host: &host, URI: uptr("https://remote.example/users/alice")},
		},
	}
	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(verifier)

	body := []byte(`{"type":"Follow","actor":{"id":"https://remote.example/users/alice","type":"Person"}}`)
	payload := signedInboxPayload(t, key, body)
	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, payload),
	}))
	require.Len(t, stub.calls, 1, "object-form actor matching the signer must be processed")
}

// 署名者 != actor かつ LD verifier 未配線なら drop。
func TestInboxProcessor_ActorMismatchNoLDVerifierDropped(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://evil.example/users/bob#main-key", priv)
	require.NoError(t, err)

	bobHost := "evil.example"
	verifier := &multiActorVerifier{
		pubKey: pub,
		byURI: map[string]*model.User{
			"https://evil.example/users/bob": {ID: "bob", Host: &bobHost, URI: uptr("https://evil.example/users/bob")},
		},
	}
	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(verifier) // ldVerifier は配線しない

	body := []byte(`{"type":"Delete","actor":"https://victim.example/users/alice"}`)
	payload := signedInboxPayload(t, key, body)
	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, payload),
	}))
	assert.Empty(t, stub.calls, "mismatch with no LD verifier must be dropped")
}

// 署名者 != actor、LD-Signature は present だが creator を resolve できない
// 場合は drop。
func TestInboxProcessor_ForwardedLDCreatorUnresolvableDropped(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://relay.example/actor#main-key", priv)
	require.NoError(t, err)

	relayHost := "relay.example"
	verifier := &multiActorVerifier{
		pubKey: pub,
		byURI: map[string]*model.User{
			"https://relay.example/actor": {ID: "relay", Host: &relayHost, URI: uptr("https://relay.example/actor")},
			// LD creator (unknown.example) は byURI に無い → ResolveActor が error。
		},
	}
	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(verifier)
	p.SetLDSignatureVerifier(&stubLDVerifier{present: true, creator: "https://unknown.example/users/x#main-key"})

	body := []byte(`{"type":"Create","actor":"https://origin.example/users/alice","signature":{"type":"RsaSignature2017"}}`)
	payload := signedInboxPayload(t, key, body)
	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, payload),
	}))
	assert.Empty(t, stub.calls, "unresolvable LD creator must be dropped")
}

// 署名者 != actor で LD-Signature はあるが、その creator が body actor とは
// 別人を指す場合は drop する (自分の鍵で署名して他人を詐称する攻撃)。
func TestInboxProcessor_ForwardedLDSigWrongActorDropped(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://relay.example/actor#main-key", priv)
	require.NoError(t, err)

	relayHost := "relay.example"
	evilHost := "evil.example"
	verifier := &multiActorVerifier{
		pubKey: pub,
		byURI: map[string]*model.User{
			"https://relay.example/actor":    {ID: "relay", Host: &relayHost, URI: uptr("https://relay.example/actor")},
			"https://evil.example/users/mal": {ID: "mal", Host: &evilHost, URI: uptr("https://evil.example/users/mal")},
		},
	}
	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(verifier)
	// LD creator = mal だが body actor は alice を詐称。
	p.SetLDSignatureVerifier(&stubLDVerifier{present: true, creator: "https://evil.example/users/mal#main-key"})

	body := []byte(`{"type":"Create","actor":"https://origin.example/users/alice","signature":{"type":"RsaSignature2017"}}`)
	payload := signedInboxPayload(t, key, body)
	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, payload),
	}))
	assert.Empty(t, stub.calls, "LD-Signature creator != activity.actor must be dropped")
}

// SetLDSignatureVerifier 未配線 (= ldVerifier nil) なら gate は skip され
// federation.Processor.Process が呼ばれる。
func TestInboxProcessor_LDVerify_NoVerifierBypassesGate(t *testing.T) {
	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, queue.InboxPayload{Body: []byte(`{"type":"Note"}`)}),
	}))
	require.Len(t, stub.calls, 1)
}

// LD-Sig verify pass (= VerifyIfPresent nil error) なら Process が呼ばれる。
func TestInboxProcessor_LDVerify_PassDelegates(t *testing.T) {
	stub := &stubFedProcessor{}
	verifier := &stubLDVerifier{err: nil}
	p := processors.NewInboxProcessor(stub)
	p.SetLDSignatureVerifier(verifier)

	require.NoError(t, p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, queue.InboxPayload{Body: []byte(`{"type":"Note"}`)}),
	}))
	assert.Equal(t, 1, verifier.callCount, "VerifyIfPresent is called exactly once")
	require.Len(t, stub.calls, 1, "verify pass → Process is called")
}

// LD-Sig verify fail (= VerifyIfPresent returns error) なら Process は呼ばれず
// activity が drop される (= queue ack するが Process bypass)。
func TestInboxProcessor_LDVerify_FailDropsActivity(t *testing.T) {
	stub := &stubFedProcessor{}
	verifier := &stubLDVerifier{err: errors.New("bad signature")}
	p := processors.NewInboxProcessor(stub)
	p.SetLDSignatureVerifier(verifier)

	err := p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, queue.InboxPayload{Body: []byte(`{"type":"Note"}`)}),
	})
	require.NoError(t, err, "verify fail でも error は返さず ack (= 同 activity を retry に乗せない)")
	assert.Equal(t, 1, verifier.callCount)
	require.Len(t, stub.calls, 0, "verify fail → Process is NOT called (activity dropped)")
}

// 任意 error は driver の retry policy (inboxJobMaxAttempts) に任せるため
// そのまま返す (SkipRetry を付けない)。
func TestInboxProcessor_GenericErrorPropagatesForRetry(t *testing.T) {
	boom := errors.New("transient db error")
	stub := &stubFedProcessor{returnFn: func(_ []byte) error { return boom }}
	p := processors.NewInboxProcessor(stub)

	err := p.Handle(context.Background(), driver.RawTask{
		TypeName: queue.TaskTypeInbox,
		Body:     mustEncode(t, queue.InboxPayload{Body: []byte(`{}`)}),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, boom), "underlying error must be wrapped, not swallowed")
	assert.False(t, errors.Is(err, driver.SkipRetry),
		"transient errors should NOT carry SkipRetry — driver retry handles them")
}
