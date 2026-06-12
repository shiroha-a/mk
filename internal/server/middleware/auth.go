package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// errOrphanedAccessToken は access_token 行は残っているが Preload した
// User が nil (= ユーザー削除済み) のケース。Authenticate はこれを「認証
// 失敗扱い」(anonymous request 継続) として扱い、後続 dereference panic
// を避ける (Devin #514 FLAG-1)。
var errOrphanedAccessToken = errors.New("auth: access_token references a deleted user")

type contextKey string

const (
	UserContextKey  contextKey = "misskeyUser"
	TokenContextKey contextKey = "misskeyToken"
	// AuthScopeContextKey carries the *AuthScope (OAuth scope view) for the
	// authenticated request. RequireScope reads it to enforce endpoint
	// meta.kind against an app token's permission array (#1552).
	AuthScopeContextKey contextKey = "misskeyAuthScope"
)

// AuthScope is the OAuth scope view of an authenticated request. IsApp is
// true when the request used an app access_token (Scopes = its permission
// array); false for a native login token, which has full access.
type AuthScope struct {
	IsApp  bool
	Scopes []string
}

// lastActiveUpdateInterval は同一ユーザーの lastActiveDate 書き込みを抑制
// する間隔。本家 TS は WebSocket 接続中 5 分おきに更新する (#421)。HTTP
// 経路でも 5 分おきの memory-throttle で十分 online 判定できる。
const lastActiveUpdateInterval = 5 * time.Minute

// AuthMiddleware provides token-based authentication.
type AuthMiddleware struct {
	userRepo        repository.UserRepository
	accessTokenRepo repository.AccessTokenRepository

	// tokenCache は token → resolved user の短命キャッシュ (#512 / #413 #3)。
	// 認証要リクエストの DB hit を 30 秒 TTL で消す。logout/token revoke の
	// 反映遅延は許容範囲内 (TTL 以内)。
	tokenCache *tokenCache

	// lastActiveSeen はユーザー ID → 直近で lastActiveDate を更新した時刻。
	// 認証済みリクエストごとに DB に書き込むと負荷が高くなるので、
	// lastActiveUpdateInterval で gating する (#421)。
	lastActiveMu   sync.Mutex
	lastActiveSeen map[string]time.Time
}

// NewAuthMiddleware creates a new AuthMiddleware.
func NewAuthMiddleware(userRepo repository.UserRepository, accessTokenRepo repository.AccessTokenRepository) *AuthMiddleware {
	return &AuthMiddleware{
		userRepo:        userRepo,
		accessTokenRepo: accessTokenRepo,
		tokenCache:      newTokenCache(),
		lastActiveSeen:  make(map[string]time.Time),
	}
}

// InvalidateToken removes a token entry from the auth cache so the next
// /api/i request with that token re-resolves through the DB (= will fail
// after `users.token` was rotated by i/regenerate-token、#884 security
// fix)。本 method は i.TokenInvalidator interface の実装。
func (a *AuthMiddleware) InvalidateToken(token string) {
	if token == "" {
		return
	}
	a.tokenCache.invalidate(token)
}

// InvalidateTokensForUser drops every cached entry that resolves to the
// given userID, across all of the user's tokens (native login + access
// tokens). Admin actions (suspend / unsuspend / delete-account) call this
// so the target user is locked out of (or restored back into) the cache
// without waiting up to 30s for natural TTL expiry (#965). #884 の
// InvalidateToken は本 request の token 単独削除なので、admin が他 user
// の全 session を即時失効したいケースには別 method が要る。
//
// noop when userID is empty.
func (a *AuthMiddleware) InvalidateTokensForUser(userID string) {
	if userID == "" {
		return
	}
	a.tokenCache.invalidateByUserID(userID)
}

// Authenticate is an Echo middleware that extracts and validates the user token.
// It does NOT reject unauthenticated requests - it just sets the user if valid.
func (a *AuthMiddleware) Authenticate() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			token := extractToken(c)
			if token == "" {
				return next(c)
			}

			user, scopes, isApp, err := a.resolveUser(token)
			if err != nil {
				return next(c)
			}

			// 論理削除 / 凍結された user を anonymous request 扱いに落とす
			// (#962 P2)。upstream Misskey は signin handler で isSuspended
			// だけ check しているが (signin/handler.go:112)、有効 token を
			// 既に持っている session は post-auth pipeline では gate されず、
			// tokenCache (30s TTL) 経由でも bypass できてしまう。本 gate を
			// 入れることで cache miss → DB fetch 経路では確実に弾ける。
			//
			// 限界: cache hit (= 凍結前にキャッシュされた entry) は stale
			// な isSuspended=false を返すので gate は fire しない。
			// self-mutation 経由 (i/delete-account #962 P0) は handler 側
			// で InvalidateToken を呼ぶので 30s → μs window。admin 経由
			// (admin/suspend-user 等、他 user の token を強制 invalidate
			// する経路) は本 PR scope 外、別 issue で追跡する想定。
			if user.IsSuspended || user.IsDeleted {
				return next(c)
			}

			c.Set(string(UserContextKey), user)
			// handler 側でこの request の auth token を引きたいケースがある
			// (例: i/update が成功後に tokenCache を invalidate する目的、
			// #960)。raw token をそのまま context に積む。app/auth 経由の
			// access_token / native login token どちらも同じキーで取得可能。
			c.Set(string(TokenContextKey), token)
			// OAuth scope view を積む。RequireScope が endpoint の meta.kind を
			// app token の permission で強制する (#1552)。native token は
			// IsApp=false で full access 扱い。
			c.Set(string(AuthScopeContextKey), &AuthScope{IsApp: isApp, Scopes: scopes})
			a.touchLastActive(user.ID)
			return next(c)
		}
	}
}

// touchLastActive bumps the user's `lastActiveDate` column to now() if the
// last update was more than `lastActiveUpdateInterval` ago. The DB write
// runs in a goroutine to keep the request hot path lean (#421)。
//
// 同じ map で eviction も行う。古いエントリ (= last update から
// `lastActiveUpdateInterval * 2` 以上経過) は次回 lookup の機会に削除する
// ことで、長期稼働しても map サイズが「直近アクティブな user 数」程度に
// 収束する (#421 Devin review: unbounded growth 対策)。
//
// 通過頻度が高くないので、専用 ticker goroutine ではなく lazy 削除で十分。
func (a *AuthMiddleware) touchLastActive(userID string) {
	if userID == "" || a.userRepo == nil {
		return
	}
	now := time.Now()
	a.lastActiveMu.Lock()
	last, ok := a.lastActiveSeen[userID]
	if ok && now.Sub(last) < lastActiveUpdateInterval {
		a.lastActiveMu.Unlock()
		return
	}
	a.lastActiveSeen[userID] = now
	// 古い entry の lazy eviction。同じ lock 取得中に O(n) 走査するが、
	// touch は既に DB 書き込み rate-limited されている (5 分間隔) ので
	// hot path への影響は限定的。
	staleBefore := now.Add(-2 * lastActiveUpdateInterval)
	for uid, t := range a.lastActiveSeen {
		if t.Before(staleBefore) {
			delete(a.lastActiveSeen, uid)
		}
	}
	a.lastActiveMu.Unlock()

	go func(uid string, t time.Time) {
		if err := a.userRepo.UpdateUser(uid, map[string]any{"lastActiveDate": t}); err != nil {
			slog.Debug("auth: lastActiveDate update failed", "userId", uid, "err", err)
		}
	}(userID, now)
}

// RequireAuth is a middleware that requires authentication.
func RequireAuth() echo.MiddlewareFunc {
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
			return next(c)
		}
	}
}

// RequireSecure requires the request to be authenticated via the user's native
// session token (users.token), not a third-party app / MiAuth access token.
// It mirrors Misskey's `secure: true` meta (isSecure = user != null && token ==
// null): account-security endpoints (password change / 2FA / data export &
// import / authorized-apps) must not be drivable by an OAuth app even when the
// app holds a valid access token.
//
// The native token is the only credential that equals users.token; app / MiAuth
// access tokens resolve the same user but via a separate access_tokens row, so
// their raw token never matches user.Token.
func RequireSecure() echo.MiddlewareFunc {
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
			if user.Token == nil || *user.Token != GetToken(c) {
				return c.JSON(http.StatusForbidden, map[string]any{
					"error": map[string]any{
						"message": "Access denied.",
						"code":    "ACCESS_DENIED",
						"id":      "56f35758-7dd5-468b-8439-5d6fb8ec9b8e",
						"kind":    apierr.KindClient,
					},
				})
			}
			return next(c)
		}
	}
}

// GetUser returns the authenticated user from the context.
func GetUser(c echo.Context) *model.User {
	u, ok := c.Get(string(UserContextKey)).(*model.User)
	if !ok {
		return nil
	}
	return u
}

// GetToken returns the raw auth token used for the current request, or
// empty string if the request was not authenticated. Handlers that perform
// self-mutation (e.g. i/update) use this to invalidate the auth middleware's
// tokenCache entry so subsequent requests read fresh user state from DB
// instead of returning stale cached values within the 30s TTL window
// (#960).
func GetToken(c echo.Context) string {
	t, ok := c.Get(string(TokenContextKey)).(string)
	if !ok {
		return ""
	}
	return t
}

// GetAuthScope returns the OAuth scope view for the current request, or nil
// when the request was unauthenticated (no token). A non-nil result with
// IsApp=false denotes a native login token (full access).
func GetAuthScope(c echo.Context) *AuthScope {
	s, ok := c.Get(string(AuthScopeContextKey)).(*AuthScope)
	if !ok {
		return nil
	}
	return s
}

// RequireScope enforces the upstream endpoint meta.kind (OAuth scope) for app
// access tokens, mirroring ApiCallService.ts: a request authenticated with an
// app token must carry `kind` in its permission array, otherwise 403
// PERMISSION_DENIED. Native login tokens (and the anonymous case, which the
// companion RequireModerator/RequireAuth rejects first) are not scope-checked
// — they have full access. Place RequireScope after the role gate so role and
// scope are both enforced (#1552).
func RequireScope(kind string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			sc := GetAuthScope(c)
			// native login token / anonymous → no scope restriction.
			if sc == nil || !sc.IsApp {
				return next(c)
			}
			for _, p := range sc.Scopes {
				if p == kind {
					return next(c)
				}
			}
			return c.JSON(http.StatusForbidden, apierr.PermissionDenied())
		}
	}
}

// RequireNotMoved rejects requests from accounts that have completed an
// account migration (movedToUri set), mirroring upstream prohibitMoved
// endpoints (ApiCallService.ts:368-377、#1562)。RequireAuth の後段に配線する
// こと (GetUser が nil の場合は素通しし、認可は RequireAuth 側に任せる)。
func RequireNotMoved() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			user := GetUser(c)
			if user != nil && user.MovedToURI != nil && *user.MovedToURI != "" {
				return c.JSON(http.StatusForbidden, apierr.ErrorWithKind(
					"YOUR_ACCOUNT_MOVED",
					"You have moved your account.",
					"56f20ec9-fd06-4fa5-841b-edd6d7d4fa31",
					apierr.KindPermission,
				))
			}
			return next(c)
		}
	}
}

// RoleChecker abstracts role checking to avoid circular dependency with core/role.
type RoleChecker interface {
	IsAdministrator(userID string) bool
	IsModerator(userID string) bool
}

// RequireAdmin is a middleware that requires the user to have an administrator role.
func RequireAdmin(checker RoleChecker) echo.MiddlewareFunc {
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
			if !checker.IsAdministrator(user.ID) {
				return c.JSON(http.StatusForbidden, map[string]any{
					"error": map[string]any{
						"message": "You are not an administrator.",
						"code":    "ROLE_PERMISSION_DENIED",
						"id":      "c3d38592-54c0-429d-bfe8-f1571e00eb14",
						"kind":    apierr.KindPermission,
					},
				})
			}
			return next(c)
		}
	}
}

// RequireModerator is a middleware that requires the user to have a moderator or admin role.
func RequireModerator(checker RoleChecker) echo.MiddlewareFunc {
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
			if !checker.IsModerator(user.ID) {
				return c.JSON(http.StatusForbidden, map[string]any{
					"error": map[string]any{
						"message": "You are not a moderator.",
						"code":    "ROLE_PERMISSION_DENIED",
						"id":      "c3d38592-54c0-429d-bfe8-f1571e00eb14",
						"kind":    apierr.KindPermission,
					},
				})
			}
			return next(c)
		}
	}
}

func extractToken(c echo.Context) string {
	// Bearer token from Authorization header
	auth := c.Request().Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}

	// Query parameter
	if t := c.QueryParam("i"); t != "" {
		return t
	}

	// multipart/form-data の "i" フィールド (ファイルアップロード時)
	req := c.Request()
	ct := req.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if t := c.FormValue("i"); t != "" {
			return t
		}
		return ""
	}

	// JSON body の "i" フィールド (Misskey-style)
	// フロントエンドは全 API で POST {"i":"token", ...} を送信する。
	// ボディを読んだ後にリセットし、後続ハンドラが再読み取り可能にする。
	if req.Body != nil && req.ContentLength != 0 {
		body, err := io.ReadAll(req.Body)
		if err == nil && len(body) > 0 {
			// ボディをリセット (下流のJSONBodyParse/c.Bindには原文を渡す)
			req.Body = io.NopCloser(bytes.NewReader(body))
			var parsed struct {
				I string `json:"i"`
			}
			// 本家 (secure-json-parse) はUTF-8 BOMを除去してからparseする
			// ため、BOM付きbodyのtoken抽出もここで除去して揃える (#1609)。
			// encoding/json はBOMを受理しないので除去しないと匿名扱いになる。
			if json.Unmarshal(bytes.TrimPrefix(body, utf8BOM), &parsed) == nil && parsed.I != "" {
				return parsed.I
			}
		}
	}

	return ""
}

// resolveUser resolves a raw token to its user and OAuth scope view.
// isApp is true when the token is an app access_token (its permission array
// is returned as scopes); false for a native login token (full access,
// scopes nil). RequireScope (#1552) uses these to enforce endpoint meta.kind.
func (a *AuthMiddleware) resolveUser(token string) (user *model.User, scopes []string, isApp bool, err error) {
	// hot path: TTL 内なら DB を引かずに cached user/scope を返す (#512)。
	if u, sc, app, ok := a.tokenCache.get(token); ok {
		return u, sc, app, nil
	}

	// まずnative tokenで検索 (= user 自身の login token, full access)。
	user, err = a.userRepo.FindByToken(token)
	if err == nil {
		a.tokenCache.put(token, user, nil, false)
		return user, nil, false, nil
	}

	// hash 列 (miauth: sha256(token)) と token 列 (raw, app/auth) を 1 query
	// で OR 検索する。upstream Misskey TS の AuthenticateService と同 pattern
	// (#910)。両 index がある前提で BitmapOr scan に乗る。
	hash := sha256Hash(token)
	accessToken, err := a.accessTokenRepo.FindByHashOrToken(hash, token)
	if err != nil {
		// not-found 系は cache に積まない: 失効後 30 秒間 stale を返さない
		// ようにするのと、未知 token 連打への DDoS を rate limiter 側に任せる
		// 設計のため (#512 scope)。
		return nil, nil, false, err
	}

	// orphaned access_token (User 行が削除済み) のとき GORM の Preload は
	// User を nil にする。そのまま (nil, nil) を返すと Authenticate 側で
	// user.ID dereference panic になるので、専用 error を返して anonymous
	// request 扱いに落とす (Devin #514 FLAG-1)。cache にも積まない。
	if accessToken.User == nil {
		return nil, nil, false, errOrphanedAccessToken
	}
	// app token は permission 配列を scope として持つ。空 ([]) でも isApp=true
	// として扱い、RequireScope が kind 不足を弾けるようにする。
	scopes = []string(accessToken.Permission)
	a.tokenCache.put(token, accessToken.User, scopes, true)
	return accessToken.User, scopes, true, nil
}

func sha256Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
