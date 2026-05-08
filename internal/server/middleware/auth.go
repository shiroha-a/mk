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
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// errOrphanedAccessToken は access_token 行は残っているが Preload した
// User が nil (= ユーザー削除済み) のケース。Authenticate はこれを「認証
// 失敗扱い」(anonymous request 継続) として扱い、後続 dereference panic
// を避ける (Devin #514 FLAG-1)。
var errOrphanedAccessToken = errors.New("auth: access_token references a deleted user")

type contextKey string

const UserContextKey contextKey = "misskeyUser"

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

// Authenticate is an Echo middleware that extracts and validates the user token.
// It does NOT reject unauthenticated requests - it just sets the user if valid.
func (a *AuthMiddleware) Authenticate() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			token := extractToken(c)
			if token == "" {
				return next(c)
			}

			user, err := a.resolveUser(token)
			if err != nil {
				return next(c)
			}

			c.Set(string(UserContextKey), user)
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
					},
				})
			}
			if !checker.IsAdministrator(user.ID) {
				return c.JSON(http.StatusForbidden, map[string]any{
					"error": map[string]any{
						"message": "You are not an administrator.",
						"code":    "ROLE_PERMISSION_DENIED",
						"id":      "c3d38592-54c0-429d-bfe8-f1571e00eb14",
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
					},
				})
			}
			if !checker.IsModerator(user.ID) {
				return c.JSON(http.StatusForbidden, map[string]any{
					"error": map[string]any{
						"message": "You are not a moderator.",
						"code":    "ROLE_PERMISSION_DENIED",
						"id":      "c3d38592-54c0-429d-bfe8-f1571e00eb14",
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
			// ボディをリセット
			req.Body = io.NopCloser(bytes.NewReader(body))
			var parsed struct {
				I string `json:"i"`
			}
			if json.Unmarshal(body, &parsed) == nil && parsed.I != "" {
				return parsed.I
			}
		}
	}

	return ""
}

func (a *AuthMiddleware) resolveUser(token string) (*model.User, error) {
	// hot path: TTL 内なら DB を引かずに cached user を返す (#512)。
	if u, ok := a.tokenCache.get(token); ok {
		return u, nil
	}

	// まずnative tokenで検索
	user, err := a.userRepo.FindByToken(token)
	if err == nil {
		a.tokenCache.put(token, user)
		return user, nil
	}

	// access tokenのhashで検索。
	// upstream Misskey TS の AuthenticateService が hash 列 OR token 列の
	// dual lookup を 1 query で行う pattern (#910) を再現する:
	//   - miauth/gen-token は hash = sha256(token) → hash 列で hit
	//   - auth/accept は hash = sha256(token + app.secret) → hash 列で miss、
	//     token (raw) 列で hit する経路で resolve される
	// これにより app-issued token も middleware で正しく認証できる。
	hash := sha256Hash(token)
	accessToken, err := a.accessTokenRepo.FindByHashOrToken(hash, token)
	if err != nil {
		// not-found 系は cache に積まない: 失効後 30 秒間 stale を返さない
		// ようにするのと、未知 token 連打への DDoS を rate limiter 側に任せる
		// 設計のため (#512 scope)。
		return nil, err
	}

	// orphaned access_token (User 行が削除済み) のとき GORM の Preload は
	// User を nil にする。そのまま (nil, nil) を返すと Authenticate 側で
	// user.ID dereference panic になるので、専用 error を返して anonymous
	// request 扱いに落とす (Devin #514 FLAG-1)。cache にも積まない。
	if accessToken.User == nil {
		return nil, errOrphanedAccessToken
	}
	a.tokenCache.put(token, accessToken.User)
	return accessToken.User, nil
}

func sha256Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
