package middleware

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
)

// RolePolicyChecker reports whether a user has a given role policy enabled.
// Implemented by core/role.Service. Declared here so middleware does not
// depend on the role package (avoids the cycle role → server → middleware →
// role and matches the RoleChecker pattern used by RequireAdmin /
// RequireModerator).
type RolePolicyChecker interface {
	HasRolePolicy(userID, policyKey string) bool
}

// RequireRolePolicy returns an Echo middleware that gates the wrapped
// handler by the given role policy key. The middleware:
//
//   - returns 401 CREDENTIAL_REQUIRED when no authenticated user is in
//     context (= same shape as RequireAuth);
//   - returns 403 ROLE_PERMISSION_DENIED with upstream-compatible UUID
//     (apierr.UUIDRolePermissionDenied) when checker.HasRolePolicy reports
//     false (= upstream `ApiCallService` requiredRolePolicy violation shape);
//   - skips the policy gate entirely when checker is nil so wire-time test
//     fixtures that do not provide a policy backend behave as before.
//
// Callers must place this middleware after Authenticate() so GetUser can
// resolve the bearer token. The standard order is `RequireAuth(),
// RequireRolePolicy(checker, key)` since the 401 path here is a fallback
// for misconfigured routes — RequireAuth gives the same response shape
// when called first.
func RequireRolePolicy(checker RolePolicyChecker, policyKey string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			user := GetUser(c)
			if user == nil {
				return c.JSON(http.StatusUnauthorized, map[string]any{
					"error": map[string]any{
						"message": "Authentication is required.",
						"code":    "CREDENTIAL_REQUIRED",
						"id":      "1384574d-a912-4b81-8601-c7b1c4085df1",
						"kind":    apierr.KindClient,
					},
				})
			}
			// checker 未配線 (= 旧挙動 / test 経路) では gate を skip する。
			// 本番経路では router.setupRoutes で core/role.Service を必ず注入する
			// ので nil 経路は通らない。
			if checker == nil {
				return next(c)
			}
			if !checker.HasRolePolicy(user.ID, policyKey) {
				return c.JSON(http.StatusForbidden, apierr.RolePermissionDenied())
			}
			return next(c)
		}
	}
}
