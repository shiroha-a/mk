package wellknown

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/core/user"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newHandler(t *testing.T) (*Handler, *testutil.MockUserRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := user.NewService(userRepo, testutil.NewMockNoteRepository(), testutil.NewMockUserNotePiningRepository(), idGen)
	urls := activitypub.NewURLBuilder("https://example.com")
	return NewHandler(urls, svc, "example.com", "https://example.com"), userRepo
}

func newReq(t *testing.T, target string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func addUser(repo *testutil.MockUserRepository, id, username string) {
	repo.Users[id] = &model.User{ID: id, Username: username, UsernameLower: username}
}

func TestWebfinger_AcctWithHost(t *testing.T) {
	h, repo := newHandler(t)
	addUser(repo, "u1", "alice")

	c, rec := newReq(t, "/.well-known/webfinger?resource=acct:alice@example.com")
	require.NoError(t, h.Webfinger(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "acct:alice@example.com", resp["subject"])
}

func TestWebfinger_AcctWithoutHost(t *testing.T) {
	h, repo := newHandler(t)
	addUser(repo, "u1", "alice")

	c, rec := newReq(t, "/.well-known/webfinger?resource=acct:alice")
	require.NoError(t, h.Webfinger(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestWebfinger_AcctWrongHost(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, "/.well-known/webfinger?resource=acct:alice@other.example")
	require.NoError(t, h.Webfinger(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestWebfinger_AcctMalformed(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, "/.well-known/webfinger?resource=acct:alice@bob@charlie")
	require.NoError(t, h.Webfinger(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestWebfinger_HTTPSResource(t *testing.T) {
	h, repo := newHandler(t)
	addUser(repo, "u1", "alice")
	c, rec := newReq(t, "/.well-known/webfinger?resource=https://example.com/users/alice")
	require.NoError(t, h.Webfinger(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestWebfinger_HTTPResource(t *testing.T) {
	h, repo := newHandler(t)
	addUser(repo, "u1", "alice")
	c, rec := newReq(t, "/.well-known/webfinger?resource=http://example.com/users/alice")
	require.NoError(t, h.Webfinger(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestWebfinger_HTTPSWrongHost(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, "/.well-known/webfinger?resource=https://other.example/users/alice")
	require.NoError(t, h.Webfinger(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestWebfinger_HTTPSWrongPath(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, "/.well-known/webfinger?resource=https://example.com/something")
	require.NoError(t, h.Webfinger(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestWebfinger_HTTPSInvalidURL(t *testing.T) {
	h, _ := newHandler(t)
	// invalid URL with control char
	c, rec := newReq(t, "/.well-known/webfinger?resource=https://example.com/users/alice%00")
	require.NoError(t, h.Webfinger(c))
	// パースは成功してusersの後ろにごみが付くので NotFound
	assert.NotEqual(t, http.StatusOK, rec.Code)
}

func TestWebfinger_UnknownScheme(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, "/.well-known/webfinger?resource=mailto:alice@example.com")
	require.NoError(t, h.Webfinger(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestWebfinger_NoResource(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, "/.well-known/webfinger")
	require.NoError(t, h.Webfinger(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestWebfinger_UserNotFound(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, "/.well-known/webfinger?resource=acct:ghost@example.com")
	require.NoError(t, h.Webfinger(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHostMeta(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, "/.well-known/host-meta")
	require.NoError(t, h.HostMeta(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "webfinger")
}

func TestNodeInfoDiscovery(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, "/.well-known/nodeinfo")
	require.NoError(t, h.NodeInfoDiscovery(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	links := resp["links"].([]any)
	// TS本家と同じく2.1と2.0の両方を返す
	assert.Len(t, links, 2)
	assert.Contains(t, rec.Body.String(), "nodeinfo/2.1")
	assert.Contains(t, rec.Body.String(), "nodeinfo/2.0")
}

func TestWebfinger_ResponseLinks(t *testing.T) {
	h, repo := newHandler(t)
	addUser(repo, "u1", "alice")

	c, rec := newReq(t, "/.well-known/webfinger?resource=acct:alice@example.com")
	require.NoError(t, h.Webfinger(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	links := resp["links"].([]any)
	// self, profile-page, subscribe の3リンク
	require.Len(t, links, 3)

	self := links[0].(map[string]any)
	assert.Equal(t, "self", self["rel"])
	assert.Equal(t, "application/activity+json", self["type"])

	profile := links[1].(map[string]any)
	assert.Equal(t, "http://webfinger.net/rel/profile-page", profile["rel"])
	assert.Equal(t, "https://example.com/@alice", profile["href"])

	subscribe := links[2].(map[string]any)
	assert.Equal(t, "http://ostatus.org/schema/1.0/subscribe", subscribe["rel"])
	assert.Equal(t, "https://example.com/authorize-follow?acct={uri}", subscribe["template"])

	// CORSヘッダー
	assert.Equal(t, "Accept", rec.Header().Get("Vary"))
	assert.Equal(t, "Vary", rec.Header().Get("Access-Control-Expose-Headers"))
}

func TestHostMetaJSON(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, "/.well-known/host-meta.json")
	require.NoError(t, h.HostMetaJSON(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	links := resp["links"].([]any)
	require.Len(t, links, 1)
	link := links[0].(map[string]any)
	assert.Equal(t, "lrdd", link["rel"])
	assert.Equal(t, "application/jrd+json", link["type"])
	assert.Equal(t, "https://example.com/.well-known/webfinger?resource={uri}", link["template"])
}

func TestOAuthAuthorizationServer(t *testing.T) {
	h, _ := newHandler(t)
	c, rec := newReq(t, "/.well-known/oauth-authorization-server")
	require.NoError(t, h.OAuthAuthorizationServer(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "https://example.com", resp["issuer"])
	assert.Equal(t, "https://example.com/oauth/authorize", resp["authorization_endpoint"])
	assert.Equal(t, "https://example.com/oauth/token", resp["token_endpoint"])
	// scopes_supported は granular な permission kinds を広告する
	// (Mastodon 風の "read"/"write"/"follow" ではない、#1904)。
	scopes, ok := resp["scopes_supported"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, scopes)
	assert.Contains(t, scopes, "read:account")
	assert.Contains(t, scopes, "write:notes")
}
