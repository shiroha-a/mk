package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthenticate_NoToken(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()
	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auth.Authenticate()(func(c echo.Context) error {
		user := GetUser(c)
		assert.Nil(t, user)
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthenticate_BearerToken(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()

	user := &model.User{ID: "user1", Username: "testuser"}
	nativeToken := "abcdef1234567890"
	userRepo.Tokens[nativeToken] = user

	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+nativeToken)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auth.Authenticate()(func(c echo.Context) error {
		u := GetUser(c)
		assert.NotNil(t, u)
		assert.Equal(t, "user1", u.ID)
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
}

// #960: handler が現在 request の auth token を取得できる必要がある
// (i/update 成功後に tokenCache を invalidate する目的)。Authenticate
// middleware は raw token を context に詰める。
func TestAuthenticate_PutsTokenInContext(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()

	user := &model.User{ID: "user1", Username: "testuser"}
	nativeToken := "abcdef1234567890"
	userRepo.Tokens[nativeToken] = user

	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+nativeToken)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auth.Authenticate()(func(c echo.Context) error {
		assert.Equal(t, nativeToken, GetToken(c),
			"middleware は raw auth token を context にそのまま積むべき")
		return c.String(http.StatusOK, "ok")
	})
	require.NoError(t, handler(c))
}

func TestGetToken_NotAuthenticated(t *testing.T) {
	// 未認証 request では GetToken は空文字を返す (panic しない)。
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	assert.Equal(t, "", GetToken(c))
}

// #965: admin が target user を suspend / unsuspend / delete したとき、
// target の全 token cache entry を 1 回で削除する exported method。
// AuthMiddleware は admin handler の UserTokenInvalidator interface を
// duck-typed で実装する。
func TestAuthMiddleware_InvalidateTokensForUser(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()
	auth := NewAuthMiddleware(userRepo, tokenRepo)

	// 直接 cache に詰めて exported method の挙動だけを isolate に test する。
	auth.tokenCache.put("a", &model.User{ID: "u1"})
	auth.tokenCache.put("b", &model.User{ID: "u1"})
	auth.tokenCache.put("c", &model.User{ID: "u2"})

	auth.InvalidateTokensForUser("u1")

	if _, ok := auth.tokenCache.get("a"); ok {
		t.Error("u1 token a が残っている")
	}
	if _, ok := auth.tokenCache.get("b"); ok {
		t.Error("u1 token b が残っている")
	}
	if _, ok := auth.tokenCache.get("c"); !ok {
		t.Error("u2 token c が誤って削除された")
	}
}

func TestAuthMiddleware_InvalidateTokensForUser_EmptyUserIDIsNoop(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()
	auth := NewAuthMiddleware(userRepo, tokenRepo)
	auth.tokenCache.put("a", &model.User{ID: "u1"})
	auth.InvalidateTokensForUser("")
	if _, ok := auth.tokenCache.get("a"); !ok {
		t.Error("userID 空で invalidate が走って巻き添えになった")
	}
}

// #962 P2: 論理削除 / 凍結された user は middleware 段階で anonymous
// request 扱いに落とす。cache miss → DB fetch path で gate が fire する
// ことを担保する。
func TestAuthenticate_SuspendedUserTreatedAsAnonymous(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()

	suspended := &model.User{ID: "u1", Username: "suspended", IsSuspended: true}
	token := "suspended-user-token"
	userRepo.Tokens[token] = suspended

	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auth.Authenticate()(func(c echo.Context) error {
		assert.Nil(t, GetUser(c), "凍結 user は context に attach されない")
		assert.Equal(t, "", GetToken(c), "凍結 user は token も context に attach されない")
		return c.String(http.StatusOK, "ok")
	})
	require.NoError(t, handler(c))
}

func TestAuthenticate_DeletedUserTreatedAsAnonymous(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()

	deleted := &model.User{ID: "u2", Username: "deleted", IsDeleted: true}
	token := "deleted-user-token"
	userRepo.Tokens[token] = deleted

	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auth.Authenticate()(func(c echo.Context) error {
		assert.Nil(t, GetUser(c), "削除済 user は context に attach されない")
		return c.String(http.StatusOK, "ok")
	})
	require.NoError(t, handler(c))
}

// 凍結 → RequireAuth gate と組み合わせると 401 になることを担保。
// signin handler 以外の認証要 endpoint は middleware 通過時点で
// CREDENTIAL_REQUIRED で弾かれる (= 30s attack window が closed)。
func TestRequireAuth_SuspendedRejected(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()

	suspended := &model.User{ID: "u3", Username: "frozen", IsSuspended: true}
	token := "another-suspended-token"
	userRepo.Tokens[token] = suspended

	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Authenticate → RequireAuth の chain は production の認証要 endpoint
	// と同じ pipeline。
	chained := auth.Authenticate()(RequireAuth()(func(c echo.Context) error {
		t.Fatal("RequireAuth は通過するべきでない")
		return nil
	}))
	require.NoError(t, chained(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"凍結 user は RequireAuth で 401 が返る")
}

func TestAuthenticate_QueryParam(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()

	user := &model.User{ID: "user2", Username: "queryuser"}
	nativeToken := "querytoken123456"
	userRepo.Tokens[nativeToken] = user

	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/?i="+nativeToken, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auth.Authenticate()(func(c echo.Context) error {
		u := GetUser(c)
		assert.NotNil(t, u)
		assert.Equal(t, "user2", u.ID)
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
}

// countingUserRepo wraps the mock to count FindByToken calls so we can
// assert that the resolved user came from the in-memory cache and not DB
// on the second hit (#512).
type countingUserRepo struct {
	*testutil.MockUserRepository
	findByTokenCalls int
}

func (c *countingUserRepo) FindByToken(token string) (*model.User, error) {
	c.findByTokenCalls++
	return c.MockUserRepository.FindByToken(token)
}

func TestAuthenticate_TokenCacheAvoidsDBOnRepeatedHit(t *testing.T) {
	repo := &countingUserRepo{MockUserRepository: testutil.NewMockUserRepository()}
	tokenRepo := testutil.NewMockAccessTokenRepository()
	user := &model.User{ID: "u_cache_1", Username: "cacheuser"}
	const tok = "cache-tok-aaaa"
	repo.Tokens[tok] = user

	auth := NewAuthMiddleware(repo, tokenRepo)
	e := echo.New()
	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		_ = auth.Authenticate()(func(c echo.Context) error {
			u := GetUser(c)
			assert.NotNil(t, u)
			assert.Equal(t, "u_cache_1", u.ID)
			return c.String(http.StatusOK, "ok")
		})(c)
		return rec
	}

	for i := 0; i < 5; i++ {
		assert.Equal(t, http.StatusOK, send().Code)
	}
	assert.Equal(t, 1, repo.findByTokenCalls,
		"FindByToken should be called only on the cache miss; subsequent hits use the in-memory cache")
}

// orphaned access_token (User 削除済み) は anonymous request 扱いに落ちて
// dereference panic を起こさないこと (Devin #514 FLAG-1)。
func TestAuthenticate_OrphanedAccessTokenFallsBackToAnonymous(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()
	const tok = "orphan-tok"
	tokenRepo.Tokens[sha256Hash(tok)] = &model.AccessToken{
		ID: "at_orphan",
		// User intentionally nil to simulate the orphaned row.
	}

	auth := NewAuthMiddleware(repo, tokenRepo)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// 旧実装ではここで panic していた。Authenticate が anonymous で next に
	// 渡すこと、ハンドラ内 GetUser が nil を返すことを確認する。
	require.NotPanics(t, func() {
		err := auth.Authenticate()(func(c echo.Context) error {
			assert.Nil(t, GetUser(c), "orphaned access_token must not produce a user")
			return c.String(http.StatusOK, "ok")
		})(c)
		assert.NoError(t, err)
	})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthenticate_TokenCacheNotFoundIsNotPoisoned(t *testing.T) {
	repo := &countingUserRepo{MockUserRepository: testutil.NewMockUserRepository()}
	tokenRepo := testutil.NewMockAccessTokenRepository()
	auth := NewAuthMiddleware(repo, tokenRepo)

	send := func() {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer ghost-tok")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		_ = auth.Authenticate()(func(c echo.Context) error {
			assert.Nil(t, GetUser(c))
			return c.String(http.StatusOK, "ok")
		})(c)
	}

	send()
	send()
	// 不在 token は cache に積まないので 2 回とも DB を引くこと。
	assert.Equal(t, 2, repo.findByTokenCalls,
		"not-found tokens must not be cached so token revoke / DB recovery is reflected immediately")
}

func TestAuthenticate_InvalidToken(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()
	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer invalidtoken")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auth.Authenticate()(func(c echo.Context) error {
		user := GetUser(c)
		// 無効なトークンの場合、ユーザーはnilだがリクエストは継続する
		assert.Nil(t, user)
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireAuth_Authenticated(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	user := &model.User{ID: "user1", Username: "test"}
	c.Set(string(UserContextKey), user)

	handler := RequireAuth()(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireAuth_Unauthenticated(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := RequireAuth()(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthenticate_AccessToken(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()

	user := &model.User{ID: "user3", Username: "tokenuser"}
	// native tokenには登録しない→access tokenのhashで検索される
	// SHA256("access_secret") = 予め計算したhash
	accessSecret := "access_secret"
	hash := sha256Hash(accessSecret)
	tokenRepo.Tokens[hash] = &model.AccessToken{
		ID:     "at1",
		Hash:   hash,
		UserID: user.ID,
		User:   user,
	}

	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+accessSecret)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auth.Authenticate()(func(c echo.Context) error {
		u := GetUser(c)
		assert.NotNil(t, u)
		assert.Equal(t, "user3", u.ID)
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
}

// TestAuthenticate_AppIssuedAccessToken は #910 drift fix の regression guard:
// auth/accept は hash = sha256(token + app.secret) で保存するため、
// middleware が hash 列だけで lookup すると 401 になる。token (raw) 列での
// fallback (FindByHashOrToken) で resolve されることを確認する。
func TestAuthenticate_AppIssuedAccessToken(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()

	user := &model.User{ID: "user_app_1", Username: "appuser"}
	rawToken := "raw_app_token_xyz"
	// app/auth flow と同じく hash は raw token そのものではなく
	// raw + secret の sha256 で別値。middleware の sha256(rawToken) は
	// 一致しないので、token 列 fallback でしか resolve できない。
	hashWithSecret := sha256Hash(rawToken + "app_secret_value")
	tokenRepo.Tokens[hashWithSecret] = &model.AccessToken{
		ID:     "at_app_1",
		Token:  rawToken,
		Hash:   hashWithSecret,
		UserID: user.ID,
		User:   user,
	}

	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auth.Authenticate()(func(c echo.Context) error {
		u := GetUser(c)
		assert.NotNil(t, u)
		assert.Equal(t, "user_app_1", u.ID)
		return c.String(http.StatusOK, "ok")
	})

	require.NoError(t, handler(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthenticate_JSONBodyToken(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()

	user := &model.User{ID: "user4", Username: "bodyuser"}
	nativeToken := "bodytoken1234567"
	userRepo.Tokens[nativeToken] = user

	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	body := `{"i":"` + nativeToken + `","text":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/notes/create", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auth.Authenticate()(func(c echo.Context) error {
		u := GetUser(c)
		assert.NotNil(t, u)
		assert.Equal(t, "user4", u.ID)
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
}

func TestAuthenticate_JSONBodyToken_EmptyI(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()
	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"i":""}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auth.Authenticate()(func(c echo.Context) error {
		assert.Nil(t, GetUser(c))
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
}

func TestAuthenticate_JSONBodyToken_NoIField(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()
	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auth.Authenticate()(func(c echo.Context) error {
		assert.Nil(t, GetUser(c))
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
}

func TestAuthenticate_JSONBodyToken_InvalidJSON(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()
	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auth.Authenticate()(func(c echo.Context) error {
		assert.Nil(t, GetUser(c))
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
}

func TestAuthenticate_BearerTakesPrecedenceOverBody(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	tokenRepo := testutil.NewMockAccessTokenRepository()

	bearerUser := &model.User{ID: "bearer1", Username: "beareruser"}
	userRepo.Tokens["bearertoken12345"] = bearerUser

	bodyUser := &model.User{ID: "body1", Username: "bodyuser"}
	userRepo.Tokens["bodytoken1234567"] = bodyUser

	auth := NewAuthMiddleware(userRepo, tokenRepo)

	e := echo.New()
	body := `{"i":"bodytoken1234567"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bearertoken12345")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auth.Authenticate()(func(c echo.Context) error {
		u := GetUser(c)
		assert.NotNil(t, u)
		// Bearer header が body より優先
		assert.Equal(t, "bearer1", u.ID)
		return c.String(http.StatusOK, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
}

// --- RequireAdmin / RequireModerator tests ---

type stubRoleChecker struct {
	admin     bool
	moderator bool
}

func (s *stubRoleChecker) IsAdministrator(_ string) bool { return s.admin }
func (s *stubRoleChecker) IsModerator(_ string) bool     { return s.moderator }

func TestRequireAdmin_Success(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(UserContextKey), &model.User{ID: "u1"})

	handler := RequireAdmin(&stubRoleChecker{admin: true})(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	err := handler(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireAdmin_NotAdmin(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(UserContextKey), &model.User{ID: "u1"})

	handler := RequireAdmin(&stubRoleChecker{admin: false})(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	err := handler(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRequireAdmin_NoUser(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := RequireAdmin(&stubRoleChecker{})(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	err := handler(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireModerator_Success(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(UserContextKey), &model.User{ID: "u1"})

	handler := RequireModerator(&stubRoleChecker{moderator: true})(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	err := handler(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireModerator_NotModerator(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(UserContextKey), &model.User{ID: "u1"})

	handler := RequireModerator(&stubRoleChecker{moderator: false})(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	err := handler(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRequireModerator_NoUser(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := RequireModerator(&stubRoleChecker{})(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	err := handler(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestGetUser_NoUser(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	user := GetUser(c)
	assert.Nil(t, user)
}
