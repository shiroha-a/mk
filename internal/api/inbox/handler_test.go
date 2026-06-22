package inbox

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/federation"
	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubFetcher returns canned actor JSON.
type stubFetcher struct {
	body []byte
}

func (s *stubFetcher) FetchObject(_ string) ([]byte, error) {
	return s.body, nil
}

func actorBody(pubKeyPEM string) string {
	return `{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id": "https://remote.example/users/alice",
		"type": "Person",
		"preferredUsername": "alice",
		"name": "Alice",
		"inbox": "https://remote.example/users/alice/inbox",
		"publicKey": {
			"id": "https://remote.example/users/alice#main-key",
			"owner": "https://remote.example/users/alice",
			"publicKeyPem": "` + escapeJSON(pubKeyPEM) + `"
		}
	}`
}

func escapeJSON(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r == '\n' {
			out = append(out, '\\', 'n')
		} else {
			out = append(out, byte(r))
		}
	}
	return string(out)
}

func newHandler(t *testing.T, pubKeyPEM string) (*Handler, *testutil.MockUserRepository, *testutil.MockFollowingRepository) {
	t.Helper()
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	idGen, _ := id.NewGenerator("aidx")
	urls := activitypub.NewURLBuilder("https://example.com")
	resolver := federation.NewResolver(repo, noteRepo, urls, &stubFetcher{body: []byte(actorBody(pubKeyPEM))}, idGen)
	followingSvc := corefollowing.NewService(repo, followingRepo, testutil.NewMockFollowRequestRepository(), idGen)
	processor := federation.NewProcessor(resolver, followingSvc, nil, nil, repo, noteRepo)
	return NewHandler(resolver, processor), repo, followingRepo
}

func newPost(t *testing.T, body []byte) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "https://example.com/inbox", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func TestInbox_HappyPathFollow(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)

	h, repo, followingRepo := newHandler(t, pub)

	// 受信側 bob を登録
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}`)

	c, rec := newPost(t, body)
	// Echoはリクエストボディをコピーするのでサイン用にreqに直接アクセス
	req := c.Request()
	digest := activitypub.SHA256Digest(body)
	require.NoError(t, activitypub.SignRequest(req, key, digest, []string{"(request-target)", "date", "host", "digest"}))
	// Hostヘッダはサーバ側で復元する必要がある
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Len(t, followingRepo.Followings, 1)
}

func TestInbox_UnsupportedActivity(t *testing.T) {
	priv, pub, _ := activitypub.GenerateRSAKeypair()
	key, _ := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	h, _, _ := newHandler(t, pub)

	body := []byte(`{"type":"Like","actor":"https://remote.example/users/alice"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	require.NoError(t, activitypub.SignRequest(req, key, activitypub.SHA256Digest(body), []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	// Unsupportedでも 202 Accepted
	assert.Equal(t, http.StatusAccepted, rec.Code)
}

func TestInbox_BadJSON(t *testing.T) {
	priv, pub, _ := activitypub.GenerateRSAKeypair()
	key, _ := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	h, _, _ := newHandler(t, pub)

	body := []byte(`{not json`)
	c, rec := newPost(t, body)
	req := c.Request()
	require.NoError(t, activitypub.SignRequest(req, key, activitypub.SHA256Digest(body), []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInbox_MissingSignature(t *testing.T) {
	_, pub, _ := activitypub.GenerateRSAKeypair()
	h, _, _ := newHandler(t, pub)

	body := []byte(`{"type":"Follow","actor":"x"}`)
	c, rec := newPost(t, body)
	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestInbox_InvalidSignature(t *testing.T) {
	_, pub, _ := activitypub.GenerateRSAKeypair()
	priv2, _, _ := activitypub.GenerateRSAKeypair()
	wrongKey, _ := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv2)

	h, _, _ := newHandler(t, pub)

	body := []byte(`{"type":"Follow","actor":"x"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	require.NoError(t, activitypub.SignRequest(req, wrongKey, activitypub.SHA256Digest(body), []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestInbox_HostHeaderEmpty(t *testing.T) {
	priv, pub, _ := activitypub.GenerateRSAKeypair()
	key, _ := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	h, _, _ := newHandler(t, pub)

	body := []byte(`{"type":"Like","actor":"https://remote.example/users/alice"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	require.NoError(t, activitypub.SignRequest(req, key, activitypub.SHA256Digest(body), []string{"(request-target)", "date", "host", "digest"}))
	// Host を消す → handler 側で req.Host から復元される
	req.Header.Del("Host")
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusAccepted, rec.Code)
}

// readErrReader is an io.Reader that returns an error.
type readErrReader struct{}

func (readErrReader) Read(_ []byte) (int, error) { return 0, assertErrSentinel }

var assertErrSentinel = newSentinelErr("read fail")

type sentinelErr struct{ msg string }

func (e *sentinelErr) Error() string { return e.msg }
func newSentinelErr(s string) error  { return &sentinelErr{msg: s} }

// errorFetcher always errors so the resolver fails.
type errorFetcher struct{}

func (errorFetcher) FetchObject(_ string) ([]byte, error) {
	return nil, assertErrSentinel
}

func TestInbox_ResolverError(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	idGen, _ := id.NewGenerator("aidx")
	urls := activitypub.NewURLBuilder("https://example.com")
	resolver := federation.NewResolver(repo, noteRepo, urls, errorFetcher{}, idGen)
	followingSvc := corefollowing.NewService(repo, followingRepo, testutil.NewMockFollowRequestRepository(), idGen)
	processor := federation.NewProcessor(resolver, followingSvc, nil, nil, repo, noteRepo)
	h := NewHandler(resolver, processor)

	priv, _, _ := activitypub.GenerateRSAKeypair()
	key, _ := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)

	body := []byte(`{"type":"Follow","actor":"x"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	require.NoError(t, activitypub.SignRequest(req, key, activitypub.SHA256Digest(body), []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestInbox_PublicKeyMissing(t *testing.T) {
	// Pre-populate user with a URI so resolver hits cached path. The fetcher
	// errors out → refreshPublicKey silently fails → keys map stays empty.
	// PublicKeyForActor then returns an error inside verifySignature.
	repo := testutil.NewMockUserRepository()
	uri := "https://remote.example/users/alice"
	repo.Users["alice"] = &model.User{ID: "alice", Username: "alice", URI: &uri}

	noteRepo := testutil.NewMockNoteRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	idGen, _ := id.NewGenerator("aidx")
	urls := activitypub.NewURLBuilder("https://example.com")
	resolver := federation.NewResolver(repo, noteRepo, urls, errorFetcher{}, idGen)
	followingSvc := corefollowing.NewService(repo, followingRepo, testutil.NewMockFollowRequestRepository(), idGen)
	processor := federation.NewProcessor(resolver, followingSvc, nil, nil, repo, noteRepo)
	h := NewHandler(resolver, processor)

	priv, _, _ := activitypub.GenerateRSAKeypair()
	key, _ := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)

	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	require.NoError(t, activitypub.SignRequest(req, key, activitypub.SHA256Digest(body), []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// stubBlocker is a HostBlockChecker that returns true for matching host names.
// disallowed simulates federation mode "specified" / "none". 既定 (zero-value)
// では allow-all。
type stubBlocker struct {
	blocked    map[string]bool
	disallowed map[string]bool
}

func (s *stubBlocker) IsBlocked(host string) bool { return s.blocked[host] }
func (s *stubBlocker) IsAllowed(host string) bool { return !s.disallowed[host] }

func TestInbox_BlockedHost(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)

	h, repo, _ := newHandler(t, pub)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}
	h.SetHostBlockChecker(&stubBlocker{blocked: map[string]bool{"remote.example": true}})

	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	digest := activitypub.SHA256Digest(body)
	require.NoError(t, activitypub.SignRequest(req, key, digest, []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// federation: specified で allowlist 外 host からの inbound は 403 で reject
// される (#536)。blocked と独立した経路。
// actorBody は固定で host=remote.example を返すので disallowed もそれに合わせる。
func TestInbox_DisallowedHost(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)

	h, repo, _ := newHandler(t, pub)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}
	// blocked は空のまま、disallowed (= specified モードで allowlist 外) を
	// シミュレート。これだけで 403 reject されること = allowlist enforcement
	// が deliver path と独立に inbox path にも入ったこと。
	h.SetHostBlockChecker(&stubBlocker{disallowed: map[string]bool{"remote.example": true}})

	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	digest := activitypub.SHA256Digest(body)
	require.NoError(t, activitypub.SignRequest(req, key, digest, []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusForbidden, rec.Code,
		"federation policy excluded host must be rejected with 403")
}

// stubInstanceTracker captures MarkRequestReceived calls.
type stubInstanceTracker struct {
	hosts []string
}

func (s *stubInstanceTracker) MarkRequestReceived(host string) error {
	s.hosts = append(s.hosts, host)
	return nil
}

func TestInbox_TouchesInstance(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)

	h, repo, _ := newHandler(t, pub)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}
	tracker := &stubInstanceTracker{}
	h.SetInstanceTracker(tracker)

	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	digest := activitypub.SHA256Digest(body)
	require.NoError(t, activitypub.SignRequest(req, key, digest, []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusAccepted, rec.Code)
	require.Len(t, tracker.hosts, 1)
	assert.Equal(t, "remote.example", tracker.hosts[0])
}

func TestInbox_TouchesInstanceTrackerNoOpWithoutTracker(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)

	h, repo, _ := newHandler(t, pub)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	digest := activitypub.SHA256Digest(body)
	require.NoError(t, activitypub.SignRequest(req, key, digest, []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusAccepted, rec.Code)
}

// stubChartHook captures inbox chart events.
type stubChartHook struct {
	hosts []string
}

func (s *stubChartHook) OnInboxReceived(host string) {
	s.hosts = append(s.hosts, host)
}

func TestInbox_FiresChartHook(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)

	h, repo, _ := newHandler(t, pub)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}
	hook := &stubChartHook{}
	h.SetChartHook(hook)

	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	digest := activitypub.SHA256Digest(body)
	require.NoError(t, activitypub.SignRequest(req, key, digest, []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusAccepted, rec.Code)
	require.Len(t, hook.hosts, 1)
	assert.Equal(t, "remote.example", hook.hosts[0])
}

func TestInbox_BlockedHostDoesNotApplyWithoutChecker(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)

	h, repo, _ := newHandler(t, pub)
	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	digest := activitypub.SHA256Digest(body)
	require.NoError(t, activitypub.SignRequest(req, key, digest, []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.NotEqual(t, http.StatusForbidden, rec.Code)
}

func TestInbox_BodyReadError(t *testing.T) {
	_, pub, _ := activitypub.GenerateRSAKeypair()
	h, _, _ := newHandler(t, pub)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "https://example.com/inbox", readErrReader{})
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// recordingEnqueuer captures EnqueueInbox calls so async-mode tests can
// assert that the handler dispatches the activity to the queue instead of
// running Process synchronously (#534).
type recordingEnqueuer struct {
	calls []queue.InboxPayload
	err   error
}

func (r *recordingEnqueuer) EnqueueInbox(_ context.Context, p queue.InboxPayload) error {
	if r.err != nil {
		return r.err
	}
	r.calls = append(r.calls, p)
	return nil
}

// SetEnqueuer 後は Inbox は Process / verifySignature を呼ばずに
// EnqueueInbox に委譲する (#565)。followingRepo に書き込みが起きないこと、
// enqueuer が body と signature 関連 header を受け取ること、HTTP は 202
// Accepted を返すことを確認する。worker 側の verify に必要な Method /
// Path / Headers (Signature / Date / Host / Digest) が payload に含まれて
// いることも検証。
func TestInbox_AsyncMode_DispatchesToQueue(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)

	h, repo, followingRepo := newHandler(t, pub)
	enq := &recordingEnqueuer{}
	h.SetEnqueuer(enq)

	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	digest := activitypub.SHA256Digest(body)
	require.NoError(t, activitypub.SignRequest(req, key, digest, []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusAccepted, rec.Code)
	require.Len(t, enq.calls, 1, "activity should be enqueued, not processed synchronously")
	got := enq.calls[0]
	assert.JSONEq(t, string(body), string(got.Body))
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/inbox", got.Path)
	assert.NotEmpty(t, got.Headers["Signature"], "Signature header must be captured")
	assert.NotEmpty(t, got.Headers["Date"], "Date header must be captured")
	assert.NotEmpty(t, got.Headers["Digest"], "Digest header must be captured")
	assert.Empty(t, followingRepo.Followings, "Process should NOT have been invoked synchronously")
}

// signature ヘッダ無しは即 401。RSA verify は走らないので fast path 成立。
func TestInbox_AsyncMode_RejectsMissingSignatureHeader(t *testing.T) {
	_, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	h, _, _ := newHandler(t, pub)
	enq := &recordingEnqueuer{}
	h.SetEnqueuer(enq)

	body := []byte(`{"type":"Follow"}`)
	c, rec := newPost(t, body)
	c.Request().Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, enq.calls, "no enqueue when signature header is missing")
}

// queue 障害時は 500 を返して上流に retry を促す (202 で握り潰すと
// activity が silent drop される)。
func TestInbox_AsyncMode_EnqueueFailureReturns500(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)

	h, repo, _ := newHandler(t, pub)
	h.SetEnqueuer(&recordingEnqueuer{err: errors.New("redis down")})

	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	digest := activitypub.SHA256Digest(body)
	require.NoError(t, activitypub.SignRequest(req, key, digest, []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- #1949 inbox admission (digest body integrity + host binding) ---

func TestInbox_TamperedBodyRejected(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)
	h, _, _ := newHandler(t, pub)

	original := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}`)
	tampered := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/eve"}`)

	c, rec := newPost(t, tampered)
	req := c.Request()
	// Digest は original の hash で署名するが、reader には tampered を流す。
	require.NoError(t, activitypub.SignRequest(req, key, activitypub.SHA256Digest(original), []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestInbox_DigestNotSignedRejected(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)
	h, _, _ := newHandler(t, pub)

	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	// Digest header は正しく付くが、署名対象ヘッダに digest を含めない。
	require.NoError(t, activitypub.SignRequest(req, key, activitypub.SHA256Digest(body), []string{"(request-target)", "date", "host"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestInbox_HostMismatchRejected(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)
	h, _, _ := newHandler(t, pub)
	h.SetExpectedHost("example.com")

	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	require.NoError(t, activitypub.SignRequest(req, key, activitypub.SHA256Digest(body), []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "evil.example"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestInbox_HostSignedAndMatchAccepted(t *testing.T) {
	priv, pub, err := activitypub.GenerateRSAKeypair()
	require.NoError(t, err)
	key, err := activitypub.NewPrivateKey("https://remote.example/users/alice#main-key", priv)
	require.NoError(t, err)
	h, repo, followingRepo := newHandler(t, pub)
	h.SetExpectedHost("example.com")

	bobURI := "https://example.com/users/bob"
	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob", URI: &bobURI}

	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}`)
	c, rec := newPost(t, body)
	req := c.Request()
	require.NoError(t, activitypub.SignRequest(req, key, activitypub.SHA256Digest(body), []string{"(request-target)", "date", "host", "digest"}))
	req.Host = "example.com"

	require.NoError(t, h.Inbox(c))
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Len(t, followingRepo.Followings, 1)
}

// #2069 (upstream #17558): bodyHasActor は actor を持つ object のみ true。
func TestBodyHasActor(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"string actor", `{"type":"Create","actor":"https://r/u/a"}`, true},
		{"embedded object actor", `{"type":"Create","actor":{"id":"https://r/u/a"}}`, true},
		{"no actor key", `{"type":"Create","object":{}}`, false},
		{"null actor", `{"type":"Create","actor":null}`, false},
		{"not an object (array)", `[{"actor":"x"}]`, false},
		{"not json", `garbage`, false},
		{"empty actor string is present", `{"actor":""}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, bodyHasActor([]byte(tc.body)))
		})
	}
}
