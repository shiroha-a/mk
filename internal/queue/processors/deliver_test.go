package processors_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/queue/processors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubSigner records the call and returns a canned response or error.
type stubSigner struct {
	resp     *http.Response
	err      error
	gotURL   string
	gotBody  []byte
	gotKeyID string
}

func (s *stubSigner) PostSigned(url string, body []byte, key *activitypub.PrivateKey) (*http.Response, error) {
	s.gotURL = url
	s.gotBody = body
	if key != nil {
		s.gotKeyID = key.KeyID
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

// generateTestKey returns a freshly minted PEM-encoded RSA private key for
// the processor to parse via activitypub.NewPrivateKey.
func generateTestKey(t *testing.T) string {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	der := x509.MarshalPKCS1PrivateKey(priv)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	return string(pem.EncodeToMemory(block))
}

func makeTask(t *testing.T, payload queue.DeliverPayload) driver.Task {
	t.Helper()
	return queue.NewDeliverTask(payload)
}

func makePayload(t *testing.T) queue.DeliverPayload {
	t.Helper()
	return queue.DeliverPayload{
		Inbox:  "https://remote.example/users/alice/inbox",
		Body:   []byte(`{"type":"Create"}`),
		KeyID:  "https://example.com/users/u1#main-key",
		KeyPEM: generateTestKey(t),
	}
}

func okResponse(status int) *http.Response {
	return &http.Response{
		Status:     http.StatusText(status),
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

func TestDeliverProcessor_Success(t *testing.T) {
	signer := &stubSigner{resp: okResponse(http.StatusOK)}
	p := processors.NewDeliverProcessor(signer)

	payload := makePayload(t)
	err := p.Handle(context.Background(), makeTask(t, payload))
	require.NoError(t, err)
	assert.Equal(t, payload.Inbox, signer.gotURL)
	assert.Equal(t, payload.Body, signer.gotBody)
	assert.Equal(t, payload.KeyID, signer.gotKeyID)
}

func TestDeliverProcessor_Accepted(t *testing.T) {
	signer := &stubSigner{resp: okResponse(http.StatusAccepted)}
	p := processors.NewDeliverProcessor(signer)
	require.NoError(t, p.Handle(context.Background(), makeTask(t, makePayload(t))))
}

func TestDeliverProcessor_Gone_SkipsRetry(t *testing.T) {
	signer := &stubSigner{resp: okResponse(http.StatusGone)}
	p := processors.NewDeliverProcessor(signer)
	err := p.Handle(context.Background(), makeTask(t, makePayload(t)))
	require.Error(t, err)
	assert.ErrorIs(t, err, driver.SkipRetry)
}

func TestDeliverProcessor_NotFound_SkipsRetry(t *testing.T) {
	signer := &stubSigner{resp: okResponse(http.StatusNotFound)}
	p := processors.NewDeliverProcessor(signer)
	err := p.Handle(context.Background(), makeTask(t, makePayload(t)))
	require.Error(t, err)
	assert.ErrorIs(t, err, driver.SkipRetry)
}

func TestDeliverProcessor_Forbidden_SkipsRetry(t *testing.T) {
	signer := &stubSigner{resp: okResponse(http.StatusForbidden)}
	p := processors.NewDeliverProcessor(signer)
	err := p.Handle(context.Background(), makeTask(t, makePayload(t)))
	require.Error(t, err)
	assert.ErrorIs(t, err, driver.SkipRetry)
}

func TestDeliverProcessor_ServerError_RetriesByReturningError(t *testing.T) {
	signer := &stubSigner{resp: okResponse(http.StatusInternalServerError)}
	p := processors.NewDeliverProcessor(signer)
	err := p.Handle(context.Background(), makeTask(t, makePayload(t)))
	require.Error(t, err)
	assert.NotErrorIs(t, err, driver.SkipRetry)
}

func TestDeliverProcessor_NetworkError_Retries(t *testing.T) {
	netErr := errors.New("connection refused")
	signer := &stubSigner{err: netErr}
	p := processors.NewDeliverProcessor(signer)
	err := p.Handle(context.Background(), makeTask(t, makePayload(t)))
	require.Error(t, err)
	assert.ErrorIs(t, err, netErr)
	assert.NotErrorIs(t, err, driver.SkipRetry)
}

func TestDeliverProcessor_BadPayload_SkipsRetry(t *testing.T) {
	p := processors.NewDeliverProcessor(&stubSigner{})
	task := driver.RawTask{TypeName: queue.TaskTypeDeliver, Body: []byte(`{not json`)}
	err := p.Handle(context.Background(), task)
	require.Error(t, err)
	assert.ErrorIs(t, err, driver.SkipRetry)
}

func TestDeliverProcessor_BadKey_SkipsRetry(t *testing.T) {
	p := processors.NewDeliverProcessor(&stubSigner{})
	payload := queue.DeliverPayload{
		Inbox:  "https://remote.example/inbox",
		Body:   []byte(`{}`),
		KeyID:  "k",
		KeyPEM: "not a pem",
	}
	err := p.Handle(context.Background(), makeTask(t, payload))
	require.Error(t, err)
	assert.ErrorIs(t, err, driver.SkipRetry)
}

// --- Ed25519 sign / capability gate / degrade safeguard (#1067 / #1071) ---

// recordingSigner records every PostSigned call so multi-call test (Ed25519
// 4xx → RSA retry) can assert which keyId was used in each attempt.
type recordingSigner struct {
	responses []*http.Response // pop in order
	errs      []error
	calls     []string // recorded KeyID for each call
}

func (r *recordingSigner) PostSigned(_ string, _ []byte, key *activitypub.PrivateKey) (*http.Response, error) {
	if key != nil {
		r.calls = append(r.calls, key.KeyID)
	}
	if len(r.responses) == 0 && len(r.errs) == 0 {
		return okResponse(http.StatusOK), nil
	}
	if len(r.errs) > 0 && r.errs[0] != nil {
		err := r.errs[0]
		r.errs = r.errs[1:]
		if len(r.responses) > 0 {
			r.responses = r.responses[1:]
		}
		return nil, err
	}
	resp := r.responses[0]
	r.responses = r.responses[1:]
	if len(r.errs) > 0 {
		r.errs = r.errs[1:]
	}
	return resp, nil
}

// generateTestEd25519Key returns a freshly minted PEM-encoded Ed25519 (PKCS8)
// private key for the processor to parse via activitypub.NewEd25519PrivateKey.
func generateTestEd25519Key(t *testing.T) string {
	t.Helper()
	priv, _, err := activitypub.GenerateEd25519Keypair()
	require.NoError(t, err)
	return priv
}

// payload に Ed25519 鍵情報がある場合、processor は Ed25519 で sign する。
func TestDeliverProcessor_Ed25519_PreferredWhenPayloadHasKey(t *testing.T) {
	signer := &recordingSigner{responses: []*http.Response{okResponse(http.StatusOK)}}
	p := processors.NewDeliverProcessor(signer)

	payload := makePayload(t)
	payload.Ed25519KeyID = "https://example.com/users/u1#ed25519-key"
	payload.Ed25519PrivPEM = generateTestEd25519Key(t)

	require.NoError(t, p.Handle(context.Background(), makeTask(t, payload)))
	require.Len(t, signer.calls, 1)
	assert.Equal(t, payload.Ed25519KeyID, signer.calls[0], "Ed25519 key was used for signing")
}

// payload に Ed25519 鍵情報がない場合は従来通り RSA で sign される。
func TestDeliverProcessor_Ed25519_AbsentFallbackToRSA(t *testing.T) {
	signer := &recordingSigner{responses: []*http.Response{okResponse(http.StatusOK)}}
	p := processors.NewDeliverProcessor(signer)

	payload := makePayload(t) // Ed25519KeyID / Ed25519PrivPEM は空
	require.NoError(t, p.Handle(context.Background(), makeTask(t, payload)))
	require.Len(t, signer.calls, 1)
	assert.Equal(t, payload.KeyID, signer.calls[0], "RSA key was used")
}

// Ed25519 PEM が壊れていれば RSA に fallback する (fail-soft)。
func TestDeliverProcessor_Ed25519_MalformedKeyFallsBackToRSA(t *testing.T) {
	signer := &recordingSigner{responses: []*http.Response{okResponse(http.StatusOK)}}
	p := processors.NewDeliverProcessor(signer)

	payload := makePayload(t)
	payload.Ed25519KeyID = "https://example.com/users/u1#ed25519-key"
	payload.Ed25519PrivPEM = "NOT A VALID PEM"

	require.NoError(t, p.Handle(context.Background(), makeTask(t, payload)))
	require.Len(t, signer.calls, 1)
	assert.Equal(t, payload.KeyID, signer.calls[0])
}

// Ed25519 sign で 4xx が返ったら host を degrade flag に立てて RSA で再送する。
// 同一 task 内で 2 回 PostSigned が呼ばれ、それぞれ Ed25519 → RSA の順。
func TestDeliverProcessor_Ed25519_4xxTriggersDegradeAndRSARetry(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	signer := &recordingSigner{
		responses: []*http.Response{
			okResponse(http.StatusBadRequest),
			okResponse(http.StatusOK),
		},
	}
	p := processors.NewDeliverProcessor(signer)
	p.SetRedis(rdb)

	payload := makePayload(t)
	payload.Ed25519KeyID = "https://example.com/users/u1#ed25519-key"
	payload.Ed25519PrivPEM = generateTestEd25519Key(t)

	require.NoError(t, p.Handle(context.Background(), makeTask(t, payload)))
	require.Len(t, signer.calls, 2, "Ed25519 4xx → RSA retry の 2 段送信")
	assert.Equal(t, payload.Ed25519KeyID, signer.calls[0])
	assert.Equal(t, payload.KeyID, signer.calls[1])

	// Redis に degrade flag が立っている
	ok, err := rdb.Exists(context.Background(), "ed25519:degrade:remote.example").Result()
	require.NoError(t, err)
	assert.EqualValues(t, 1, ok)
}

// degrade flag が立っている host への deliver は最初から RSA で送られる。
func TestDeliverProcessor_Ed25519_DegradeFlagSkipsEd25519(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	// 事前に degrade flag を立てる
	require.NoError(t, rdb.Set(context.Background(), "ed25519:degrade:remote.example", "1", 0).Err())

	signer := &recordingSigner{responses: []*http.Response{okResponse(http.StatusOK)}}
	p := processors.NewDeliverProcessor(signer)
	p.SetRedis(rdb)

	payload := makePayload(t)
	payload.Ed25519KeyID = "https://example.com/users/u1#ed25519-key"
	payload.Ed25519PrivPEM = generateTestEd25519Key(t)

	require.NoError(t, p.Handle(context.Background(), makeTask(t, payload)))
	require.Len(t, signer.calls, 1)
	assert.Equal(t, payload.KeyID, signer.calls[0], "degrade flag により最初から RSA で送信")
}

// stubResponseHook captures host events for assertions.
type stubResponseHook struct {
	successes []string
	errors    []string
}

func (s *stubResponseHook) RecordResponseSuccess(host string) error {
	s.successes = append(s.successes, host)
	return nil
}

func (s *stubResponseHook) RecordResponseError(host string) error {
	s.errors = append(s.errors, host)
	return nil
}

func TestDeliverProcessor_ResponseHook_Success(t *testing.T) {
	signer := &stubSigner{resp: okResponse(http.StatusOK)}
	p := processors.NewDeliverProcessor(signer)
	hook := &stubResponseHook{}
	p.SetResponseHook(hook)
	require.NoError(t, p.Handle(context.Background(), makeTask(t, makePayload(t))))
	assert.Equal(t, []string{"remote.example"}, hook.successes)
	assert.Empty(t, hook.errors)
}

func TestDeliverProcessor_ResponseHook_Gone_RecordsSuccess(t *testing.T) {
	signer := &stubSigner{resp: okResponse(http.StatusGone)}
	p := processors.NewDeliverProcessor(signer)
	hook := &stubResponseHook{}
	p.SetResponseHook(hook)
	err := p.Handle(context.Background(), makeTask(t, makePayload(t)))
	require.Error(t, err)
	assert.Equal(t, []string{"remote.example"}, hook.successes)
}

func TestDeliverProcessor_ResponseHook_ClientError_RecordsSuccess(t *testing.T) {
	signer := &stubSigner{resp: okResponse(http.StatusForbidden)}
	p := processors.NewDeliverProcessor(signer)
	hook := &stubResponseHook{}
	p.SetResponseHook(hook)
	err := p.Handle(context.Background(), makeTask(t, makePayload(t)))
	require.Error(t, err)
	assert.Equal(t, []string{"remote.example"}, hook.successes)
}

func TestDeliverProcessor_ResponseHook_ServerError_RecordsError(t *testing.T) {
	signer := &stubSigner{resp: okResponse(http.StatusInternalServerError)}
	p := processors.NewDeliverProcessor(signer)
	hook := &stubResponseHook{}
	p.SetResponseHook(hook)
	err := p.Handle(context.Background(), makeTask(t, makePayload(t)))
	require.Error(t, err)
	assert.Equal(t, []string{"remote.example"}, hook.errors)
}

func TestDeliverProcessor_ResponseHook_NetworkError_RecordsError(t *testing.T) {
	signer := &stubSigner{err: errors.New("net down")}
	p := processors.NewDeliverProcessor(signer)
	hook := &stubResponseHook{}
	p.SetResponseHook(hook)
	err := p.Handle(context.Background(), makeTask(t, makePayload(t)))
	require.Error(t, err)
	assert.Equal(t, []string{"remote.example"}, hook.errors)
}

func TestDeliverProcessor_ResponseHook_UnparseableInboxSuccess(t *testing.T) {
	signer := &stubSigner{resp: okResponse(http.StatusOK)}
	p := processors.NewDeliverProcessor(signer)
	hook := &stubResponseHook{}
	p.SetResponseHook(hook)
	payload := queue.DeliverPayload{
		Inbox:  "://bad-url",
		Body:   []byte(`{}`),
		KeyID:  "https://example.com/users/u1#main-key",
		KeyPEM: generateTestKey(t),
	}
	require.NoError(t, p.Handle(context.Background(), makeTask(t, payload)))
	assert.Empty(t, hook.successes)
}

func TestDeliverProcessor_ResponseHook_UnparseableInboxError(t *testing.T) {
	signer := &stubSigner{err: errors.New("net down")}
	p := processors.NewDeliverProcessor(signer)
	hook := &stubResponseHook{}
	p.SetResponseHook(hook)
	payload := queue.DeliverPayload{
		Inbox:  "://bad-url",
		Body:   []byte(`{}`),
		KeyID:  "https://example.com/users/u1#main-key",
		KeyPEM: generateTestKey(t),
	}
	err := p.Handle(context.Background(), makeTask(t, payload))
	require.Error(t, err)
	assert.Empty(t, hook.errors)
}

// stubChartHook captures OnDelivered events from DeliverProcessor.
type stubChartHook struct {
	delivered []struct {
		host      string
		succeeded bool
	}
}

func (s *stubChartHook) OnDelivered(host string, succeeded bool) {
	s.delivered = append(s.delivered, struct {
		host      string
		succeeded bool
	}{host, succeeded})
}

func TestDeliverProcessor_ChartHook_Success(t *testing.T) {
	signer := &stubSigner{resp: okResponse(http.StatusOK)}
	p := processors.NewDeliverProcessor(signer)
	hook := &stubChartHook{}
	p.SetChartHook(hook)
	require.NoError(t, p.Handle(context.Background(), makeTask(t, makePayload(t))))
	require.Len(t, hook.delivered, 1)
	assert.Equal(t, "remote.example", hook.delivered[0].host)
	assert.True(t, hook.delivered[0].succeeded)
}

func TestDeliverProcessor_ChartHook_NetworkError(t *testing.T) {
	signer := &stubSigner{err: errors.New("net down")}
	p := processors.NewDeliverProcessor(signer)
	hook := &stubChartHook{}
	p.SetChartHook(hook)
	err := p.Handle(context.Background(), makeTask(t, makePayload(t)))
	require.Error(t, err)
	require.Len(t, hook.delivered, 1)
	assert.False(t, hook.delivered[0].succeeded)
}

func TestDeliverProcessor_ChartHook_UnparseableInbox(t *testing.T) {
	// 不正な inbox URL でも chart hook は host="" で発火し、route 別に
	// no-op で扱われる (charthook 側のテストでカバー)。
	signer := &stubSigner{resp: okResponse(http.StatusOK)}
	p := processors.NewDeliverProcessor(signer)
	hook := &stubChartHook{}
	p.SetChartHook(hook)
	payload := queue.DeliverPayload{
		Inbox:  "://bad-url",
		Body:   []byte(`{}`),
		KeyID:  "https://example.com/users/u1#main-key",
		KeyPEM: generateTestKey(t),
	}
	require.NoError(t, p.Handle(context.Background(), makeTask(t, payload)))
	require.Len(t, hook.delivered, 1)
	assert.Equal(t, "", hook.delivered[0].host)
}
