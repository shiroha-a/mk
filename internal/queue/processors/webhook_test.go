package processors_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/queue/processors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test doubles ---

type stubHTTPClient struct {
	mu      sync.Mutex
	reqs    []*http.Request
	body    []byte
	resp    *http.Response
	respErr error
	status  int
}

func (s *stubHTTPClient) Do(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// reqはbodyを消費してしまうので、先に保存しておく
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		s.body = b
	}
	s.reqs = append(s.reqs, req)
	if s.respErr != nil {
		return nil, s.respErr
	}
	status := s.status
	if status == 0 {
		status = http.StatusOK
	}
	if s.resp != nil {
		return s.resp, nil
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(bytes.NewReader(nil)),
	}, nil
}

type stubUserWebhookRepo struct {
	hooks        map[string]*model.Webhook
	findErr      error
	statusCalled map[string]int
}

func (r *stubUserWebhookRepo) Create(_ *model.Webhook) error { return nil }
func (r *stubUserWebhookRepo) FindByID(id string) (*model.Webhook, error) {
	if r.findErr != nil {
		return nil, r.findErr
	}
	if w, ok := r.hooks[id]; ok {
		return w, nil
	}
	return nil, errors.New("not found")
}
func (r *stubUserWebhookRepo) FindByIDAndUserID(_, _ string) (*model.Webhook, error) {
	return nil, nil
}
func (r *stubUserWebhookRepo) ListByUserID(_ string) ([]*model.Webhook, error) { return nil, nil }
func (r *stubUserWebhookRepo) CountByUserID(_ string) (int64, error)           { return 0, nil }
func (r *stubUserWebhookRepo) ListActiveByUserID(_ string) ([]*model.Webhook, error) {
	return nil, nil
}
func (r *stubUserWebhookRepo) Update(_ *model.Webhook) error { return nil }
func (r *stubUserWebhookRepo) UpdateLatestStatus(id string, _ time.Time, status int) error {
	if r.statusCalled == nil {
		r.statusCalled = make(map[string]int)
	}
	r.statusCalled[id] = status
	return nil
}
func (r *stubUserWebhookRepo) Delete(_, _ string) error { return nil }

type stubSystemWebhookRepo struct {
	hooks        map[string]*model.SystemWebhook
	statusCalled map[string]int
}

func (r *stubSystemWebhookRepo) Create(_ *model.SystemWebhook) error { return nil }
func (r *stubSystemWebhookRepo) FindByID(id string) (*model.SystemWebhook, error) {
	if w, ok := r.hooks[id]; ok {
		return w, nil
	}
	return nil, errors.New("not found")
}
func (r *stubSystemWebhookRepo) List() ([]*model.SystemWebhook, error)       { return nil, nil }
func (r *stubSystemWebhookRepo) ListActive() ([]*model.SystemWebhook, error) { return nil, nil }
func (r *stubSystemWebhookRepo) Update(_ *model.SystemWebhook) error         { return nil }
func (r *stubSystemWebhookRepo) UpdateAdminFields(_ string, _ map[string]any) error {
	return nil
}
func (r *stubSystemWebhookRepo) UpdateLatestStatus(id string, _ time.Time, status int) error {
	if r.statusCalled == nil {
		r.statusCalled = make(map[string]int)
	}
	r.statusCalled[id] = status
	return nil
}
func (r *stubSystemWebhookRepo) Delete(_ string) error { return nil }

// --- helpers ---

func newTestWebhookProcessor(
	t *testing.T,
	client processors.WebhookHTTPClient,
	userHooks map[string]*model.Webhook,
	sysHooks map[string]*model.SystemWebhook,
) (*processors.WebhookProcessor, *stubUserWebhookRepo, *stubSystemWebhookRepo) {
	t.Helper()
	ur := &stubUserWebhookRepo{hooks: userHooks}
	sr := &stubSystemWebhookRepo{hooks: sysHooks}
	p := processors.NewWebhookProcessor(ur, sr, client, "example.com")
	return p, ur, sr
}

// --- Tests ---

func TestWebhookProcessor_User_Success(t *testing.T) {
	client := &stubHTTPClient{status: http.StatusOK}
	p, repo, _ := newTestWebhookProcessor(t, client, map[string]*model.Webhook{
		"h1": {ID: "h1", URL: "https://hook.example/u1", Secret: "s"},
	}, nil)

	task := queue.NewUserWebhookTask(queue.WebhookPayload{
		WebhookID: "h1",
		UserID:    "alice",
		EventType: "note",
		Body:      []byte(`{"type":"note"}`),
	})
	err := p.HandleUser(context.Background(), task)
	require.NoError(t, err)
	require.Len(t, client.reqs, 1)
	req := client.reqs[0]
	assert.Equal(t, "https://hook.example/u1", req.URL.String())
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, "Misskey-Hooks", req.Header.Get("User-Agent"))
	assert.Equal(t, "example.com", req.Header.Get("X-Misskey-Host"))
	assert.Equal(t, "h1", req.Header.Get("X-Misskey-Hook-Id"))
	assert.Equal(t, "s", req.Header.Get("X-Misskey-Hook-Secret"))
	assert.Equal(t, http.StatusOK, repo.statusCalled["h1"])
}

// #1546: OverrideURL/Secret 指定時は保存済 webhook でなく override 先へ送り、
// 保存済 webhook の latestStatus も記録しない (test 送信は別 URL のため)。
func TestWebhookProcessor_User_Override(t *testing.T) {
	client := &stubHTTPClient{status: http.StatusOK}
	p, repo, _ := newTestWebhookProcessor(t, client, map[string]*model.Webhook{
		"h1": {ID: "h1", URL: "https://saved.example/u1", Secret: "saved"},
	}, nil)
	task := queue.NewUserWebhookTask(queue.WebhookPayload{
		WebhookID: "h1", UserID: "alice", EventType: "note", Body: []byte(`{}`),
		OverrideURL: "https://override.example/hook", OverrideSecret: "ovr",
	})
	require.NoError(t, p.HandleUser(context.Background(), task))
	require.Len(t, client.reqs, 1)
	assert.Equal(t, "https://override.example/hook", client.reqs[0].URL.String())
	assert.Equal(t, "ovr", client.reqs[0].Header.Get("X-Misskey-Hook-Secret"))
	_, recorded := repo.statusCalled["h1"]
	assert.False(t, recorded, "override test send must not record status on the saved webhook")
}

func TestWebhookProcessor_User_NoSecretOmitsHeader(t *testing.T) {
	client := &stubHTTPClient{}
	p, _, _ := newTestWebhookProcessor(t, client, map[string]*model.Webhook{
		"h1": {ID: "h1", URL: "https://hook.example/u1"},
	}, nil)
	task := queue.NewUserWebhookTask(queue.WebhookPayload{WebhookID: "h1", EventType: "note", Body: []byte(`{}`)})
	require.NoError(t, p.HandleUser(context.Background(), task))
	require.Len(t, client.reqs, 1)
	assert.Empty(t, client.reqs[0].Header.Get("X-Misskey-Hook-Secret"))
}

func TestWebhookProcessor_User_4xxSkipsRetry(t *testing.T) {
	client := &stubHTTPClient{status: http.StatusBadRequest}
	p, repo, _ := newTestWebhookProcessor(t, client, map[string]*model.Webhook{
		"h1": {ID: "h1", URL: "https://hook.example/u1"},
	}, nil)
	task := queue.NewUserWebhookTask(queue.WebhookPayload{WebhookID: "h1", EventType: "note", Body: []byte(`{}`)})
	err := p.HandleUser(context.Background(), task)
	require.Error(t, err)
	assert.ErrorIs(t, err, driver.SkipRetry)
	assert.Equal(t, http.StatusBadRequest, repo.statusCalled["h1"])
}

func TestWebhookProcessor_User_5xxRetries(t *testing.T) {
	client := &stubHTTPClient{status: http.StatusInternalServerError}
	p, repo, _ := newTestWebhookProcessor(t, client, map[string]*model.Webhook{
		"h1": {ID: "h1", URL: "https://hook.example/u1"},
	}, nil)
	task := queue.NewUserWebhookTask(queue.WebhookPayload{WebhookID: "h1", EventType: "note", Body: []byte(`{}`)})
	err := p.HandleUser(context.Background(), task)
	require.Error(t, err)
	assert.NotErrorIs(t, err, driver.SkipRetry)
	assert.Equal(t, http.StatusInternalServerError, repo.statusCalled["h1"])
}

func TestWebhookProcessor_User_NetworkError(t *testing.T) {
	client := &stubHTTPClient{respErr: errors.New("connection refused")}
	p, repo, _ := newTestWebhookProcessor(t, client, map[string]*model.Webhook{
		"h1": {ID: "h1", URL: "https://hook.example/u1"},
	}, nil)
	task := queue.NewUserWebhookTask(queue.WebhookPayload{WebhookID: "h1", EventType: "note", Body: []byte(`{}`)})
	err := p.HandleUser(context.Background(), task)
	require.Error(t, err)
	assert.NotErrorIs(t, err, driver.SkipRetry)
	// ネットワークエラーは status=0 で記録
	assert.Equal(t, 0, repo.statusCalled["h1"])
}

func TestWebhookProcessor_User_NotFoundSkipsRetry(t *testing.T) {
	client := &stubHTTPClient{}
	p, _, _ := newTestWebhookProcessor(t, client, map[string]*model.Webhook{}, nil)
	task := queue.NewUserWebhookTask(queue.WebhookPayload{WebhookID: "h1", EventType: "note", Body: []byte(`{}`)})
	err := p.HandleUser(context.Background(), task)
	require.Error(t, err)
	assert.ErrorIs(t, err, driver.SkipRetry)
}

func TestWebhookProcessor_User_InvalidPayloadSkipsRetry(t *testing.T) {
	p, _, _ := newTestWebhookProcessor(t, &stubHTTPClient{}, nil, nil)
	task := driver.RawTask{TypeName: queue.TaskTypeUserWebhook, Body: []byte("not json")}
	err := p.HandleUser(context.Background(), task)
	require.Error(t, err)
	assert.ErrorIs(t, err, driver.SkipRetry)
}

func TestWebhookProcessor_User_MissingWebhookID(t *testing.T) {
	p, _, _ := newTestWebhookProcessor(t, &stubHTTPClient{}, nil, nil)
	task := queue.NewUserWebhookTask(queue.WebhookPayload{})
	err := p.HandleUser(context.Background(), task)
	require.Error(t, err)
	assert.ErrorIs(t, err, driver.SkipRetry)
}

func TestWebhookProcessor_System_Success(t *testing.T) {
	client := &stubHTTPClient{status: http.StatusAccepted}
	p, _, sysRepo := newTestWebhookProcessor(t, client, nil, map[string]*model.SystemWebhook{
		"sh1": {ID: "sh1", URL: "https://hook.example/s1", Secret: "ss"},
	})
	task := queue.NewSystemWebhookTask(queue.WebhookPayload{WebhookID: "sh1", EventType: "userCreated", Body: []byte(`{}`)})
	err := p.HandleSystem(context.Background(), task)
	require.NoError(t, err)
	require.Len(t, client.reqs, 1)
	assert.Equal(t, "sh1", client.reqs[0].Header.Get("X-Misskey-Hook-Id"))
	assert.Equal(t, http.StatusAccepted, sysRepo.statusCalled["sh1"])
}

func TestWebhookProcessor_UserRepoMissing(t *testing.T) {
	p := processors.NewWebhookProcessor(nil, nil, &stubHTTPClient{}, "example.com")
	task := queue.NewUserWebhookTask(queue.WebhookPayload{WebhookID: "h1", EventType: "note", Body: []byte(`{}`)})
	err := p.HandleUser(context.Background(), task)
	require.Error(t, err)
	assert.ErrorIs(t, err, driver.SkipRetry)
}

func TestWebhookProcessor_SystemRepoMissing(t *testing.T) {
	p := processors.NewWebhookProcessor(nil, nil, &stubHTTPClient{}, "example.com")
	task := queue.NewSystemWebhookTask(queue.WebhookPayload{WebhookID: "sh1", EventType: "userCreated", Body: []byte(`{}`)})
	err := p.HandleSystem(context.Background(), task)
	require.Error(t, err)
	assert.ErrorIs(t, err, driver.SkipRetry)
}

func TestNewWebhookProcessor_DefaultClient(t *testing.T) {
	// nil client falls back to http.Client with timeout
	p := processors.NewWebhookProcessor(nil, nil, nil, "example.com")
	assert.NotNil(t, p)
}

func TestWebhookProcessor_BodyForwardedUnchanged(t *testing.T) {
	client := &stubHTTPClient{}
	p, _, _ := newTestWebhookProcessor(t, client, map[string]*model.Webhook{
		"h1": {ID: "h1", URL: "https://hook.example/u1"},
	}, nil)
	body := []byte(`{"hello":"world"}`)
	task := queue.NewUserWebhookTask(queue.WebhookPayload{WebhookID: "h1", EventType: "note", Body: body})
	require.NoError(t, p.HandleUser(context.Background(), task))
	assert.Equal(t, body, client.body)
}

// #2106 N30: webhook 先が 429 を返したら retryable (SkipRetry を付けない)。
func TestWebhookProcessor_User_429Retries(t *testing.T) {
	client := &stubHTTPClient{status: http.StatusTooManyRequests}
	p, repo, _ := newTestWebhookProcessor(t, client, map[string]*model.Webhook{
		"h1": {ID: "h1", URL: "https://hook.example/u1"},
	}, nil)
	task := queue.NewUserWebhookTask(queue.WebhookPayload{WebhookID: "h1", EventType: "note", Body: []byte(`{}`)})
	err := p.HandleUser(context.Background(), task)
	require.Error(t, err)
	assert.NotErrorIs(t, err, driver.SkipRetry)
	assert.Equal(t, http.StatusTooManyRequests, repo.statusCalled["h1"])
}
