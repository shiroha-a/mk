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
	verifier := &stubVerifier{
		actor:  &model.User{ID: "alice", Host: &host},
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
