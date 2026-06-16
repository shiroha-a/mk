package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubPolicyChecker is a minimal RolePolicyChecker. The allow map is keyed by
// "userID|policyKey" so a single instance can answer multiple lookups.
type stubPolicyChecker struct {
	allow map[string]bool
}

func newStubPolicyChecker() *stubPolicyChecker {
	return &stubPolicyChecker{allow: map[string]bool{}}
}

func (s *stubPolicyChecker) HasRolePolicy(userID, policyKey string) bool {
	return s.allow[userID+"|"+policyKey]
}

func (s *stubPolicyChecker) set(userID, policyKey string, allow bool) {
	s.allow[userID+"|"+policyKey] = allow
}

// newRolePolicyReq is a small helper that wires an Echo context with the
// given authenticated user attached. nil user simulates an unauthenticated
// request.
func newRolePolicyReq(t *testing.T, user *model.User) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if user != nil {
		c.Set(string(UserContextKey), user)
	}
	return c, rec
}

func TestRequireRolePolicy_Unauthenticated(t *testing.T) {
	checker := newStubPolicyChecker()

	c, rec := newRolePolicyReq(t, nil)
	called := false
	handler := RequireRolePolicy(checker, "canSearchNotes")(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	require.NoError(t, handler(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "user 不在は 401")
	assert.Contains(t, rec.Body.String(), "CREDENTIAL_REQUIRED")
	assert.False(t, called, "next は呼ばれない")
}

func TestRequireRolePolicy_PolicyAllowed(t *testing.T) {
	checker := newStubPolicyChecker()
	checker.set("alice", "canSearchNotes", true)

	c, rec := newRolePolicyReq(t, &model.User{ID: "alice"})
	called := false
	handler := RequireRolePolicy(checker, "canSearchNotes")(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	require.NoError(t, handler(c))
	assert.Equal(t, http.StatusOK, rec.Code, "policy=true で通常成功")
	assert.True(t, called, "next が呼ばれる")
}

func TestRequireRolePolicy_PolicyDenied(t *testing.T) {
	checker := newStubPolicyChecker()
	// alice には何も set しない → canSearchNotes=false

	c, rec := newRolePolicyReq(t, &model.User{ID: "alice"})
	called := false
	handler := RequireRolePolicy(checker, "canSearchNotes")(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	require.NoError(t, handler(c))
	assert.Equal(t, http.StatusForbidden, rec.Code, "policy=false は 403")
	assert.Contains(t, rec.Body.String(), "ROLE_PERMISSION_DENIED")
	// upstream ApiCallService の requiredRolePolicy 違反と同じ UUID
	// (apierr.UUIDRolePermissionDenied)。frontend / クライアントが文字列 id で
	// lookup する経路があるので regression guard としても明示する。
	assert.Contains(t, rec.Body.String(), "7f86f06f-7e15-4057-8561-f4b6d4ac755a")
	assert.False(t, called, "next は呼ばれない")
}

func TestRequireRolePolicy_NilCheckerSkipsGate(t *testing.T) {
	// nil checker は test 経路 (= wire-time に provider 未注入) で発生しうる。
	// gate を skip して通常成功するのが期待挙動 (auth は通っている前提)。
	c, rec := newRolePolicyReq(t, &model.User{ID: "alice"})
	called := false
	handler := RequireRolePolicy(nil, "canSearchNotes")(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	require.NoError(t, handler(c))
	assert.Equal(t, http.StatusOK, rec.Code, "checker 未配線は gate skip")
	assert.True(t, called)
}

func TestRequireRolePolicy_DifferentPolicyKey(t *testing.T) {
	// 同一 user が複数 policy を持つケース。canInvite=true を持つが
	// canSearchNotes は持たない user は canSearchNotes gate で reject される
	// (= policyKey 引数だけ見て判定する regression guard)。
	checker := newStubPolicyChecker()
	checker.set("alice", "canInvite", true)

	c, rec := newRolePolicyReq(t, &model.User{ID: "alice"})
	handler := RequireRolePolicy(checker, "canSearchNotes")(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	require.NoError(t, handler(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// #1784: RequireRolePolicyPublic は匿名 (uid="") も base policy で評価し、
// policy=true なら通す (401 は返さない)。
func TestRequireRolePolicyPublic_AnonymousAllowedByBasePolicy(t *testing.T) {
	checker := newStubPolicyChecker()
	checker.set("", "canSearchUsers", true) // base policy

	c, rec := newRolePolicyReq(t, nil)
	called := false
	handler := RequireRolePolicyPublic(checker, "canSearchUsers")(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	require.NoError(t, handler(c))
	assert.Equal(t, http.StatusOK, rec.Code, "匿名でも base policy=true なら通る")
	assert.True(t, called)
}

// base policy=false なら匿名は 403 ROLE_PERMISSION_DENIED (401 ではない)。
func TestRequireRolePolicyPublic_AnonymousDenied(t *testing.T) {
	checker := newStubPolicyChecker() // base canSearchUsers=false

	c, rec := newRolePolicyReq(t, nil)
	called := false
	handler := RequireRolePolicyPublic(checker, "canSearchUsers")(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	require.NoError(t, handler(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "ROLE_PERMISSION_DENIED")
	assert.False(t, called)
}

// 認証済 user は自身の policy で評価される。
func TestRequireRolePolicyPublic_AuthedUser(t *testing.T) {
	checker := newStubPolicyChecker()
	checker.set("alice", "canSearchUsers", true)

	c, rec := newRolePolicyReq(t, &model.User{ID: "alice"})
	handler := RequireRolePolicyPublic(checker, "canSearchUsers")(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	require.NoError(t, handler(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// checker 未配線時は gate skip。
func TestRequireRolePolicyPublic_NilCheckerSkips(t *testing.T) {
	c, rec := newRolePolicyReq(t, nil)
	called := false
	handler := RequireRolePolicyPublic(nil, "canSearchUsers")(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})
	require.NoError(t, handler(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, called)
}
