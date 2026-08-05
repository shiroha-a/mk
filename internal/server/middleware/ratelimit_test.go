package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- mock store ---

type mockCall struct {
	Key      string
	Duration time.Duration
	Max      int
}

type mockResult struct {
	Info LimitInfo
	Err  error
}

type mockLimitStore struct {
	calls   []mockCall
	results []mockResult
	idx     int
}

func (m *mockLimitStore) Check(_ context.Context, key string, duration time.Duration, max int) (LimitInfo, error) {
	m.calls = append(m.calls, mockCall{Key: key, Duration: duration, Max: max})
	i := m.idx
	m.idx++
	if i < len(m.results) {
		return m.results[i].Info, m.results[i].Err
	}
	return LimitInfo{Remaining: 999}, nil
}

// --- helpers ---

func setupEcho(rl *RateLimiter) (*echo.Echo, echo.HandlerFunc) {
	e := echo.New()
	handler := func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	}
	return e, handler
}

func doRequest(e *echo.Echo, mw echo.MiddlewareFunc, handler echo.HandlerFunc, path string, user *model.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.RemoteAddr = "192.168.1.100:12345"
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath(path)
	if user != nil {
		c.Set(string(UserContextKey), user)
	}
	wrapped := mw(handler)
	_ = wrapped(c)
	return rec
}

// --- RateLimiter Middleware tests ---

func TestMiddleware_NoLimitDefined(t *testing.T) {
	store := &mockLimitStore{}
	rl := NewRateLimiter(store, true, map[string]*EndpointLimit{})
	e, h := setupEcho(rl)

	rec := doRequest(e, rl.Middleware(), h, "/api/notes/show", nil)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, store.calls)
}

// Disable() を呼んだら、リミット定義があっても store を一切引かずに素通しする。
// 本家 RateLimiterService が NODE_ENV !== 'production' でやっているのと同じ。
func TestMiddleware_Disabled(t *testing.T) {
	store := &mockLimitStore{
		results: []mockResult{
			{Info: LimitInfo{Remaining: 0, ResetMs: time.Now().Add(time.Hour).UnixMilli()}},
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 1},
	}
	rl := NewRateLimiter(store, true, limits)
	rl.Disable()
	e, h := setupEcho(rl)

	// 上限 1 に対して 3 回叩いても 429 にならない。
	for i := 0; i < 3; i++ {
		rec := doRequest(e, rl.Middleware(), h, "/api/notes/create", &model.User{ID: "user1"})
		assert.Equal(t, http.StatusOK, rec.Code)
	}
	assert.Empty(t, store.calls, "無効時は store を引かないこと")
}

func TestMiddleware_WithinLimit(t *testing.T) {
	store := &mockLimitStore{
		results: []mockResult{
			{Info: LimitInfo{Remaining: 5, ResetMs: time.Now().Add(time.Hour).UnixMilli()}},
			{Info: LimitInfo{Remaining: 8, ResetMs: time.Now().Add(time.Hour).UnixMilli()}},
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 300},
	}
	rl := NewRateLimiter(store, true, limits)
	e, h := setupEcho(rl)

	rec := doRequest(e, rl.Middleware(), h, "/api/notes/create", &model.User{ID: "user1"})

	assert.Equal(t, http.StatusOK, rec.Code)
	// 認証済 → userID + IP bucket の 2 actor (#606 item 2)。X-RateLimit-Remaining
	// は最も逼迫した bucket (ここでは userID 側 5)。
	assert.Equal(t, "5", rec.Header().Get("X-RateLimit-Remaining"))
	require.Len(t, store.calls, 2)
	assert.Equal(t, "user1:notes/create", store.calls[0].Key)
	assert.Contains(t, store.calls[1].Key, "ip-")
	assert.Equal(t, time.Hour, store.calls[0].Duration)
	assert.Equal(t, 300, store.calls[0].Max)
}

func TestMiddleware_ExceedsLimit(t *testing.T) {
	resetMs := time.Now().Add(30 * time.Second).UnixMilli()
	store := &mockLimitStore{
		results: []mockResult{
			{Info: LimitInfo{Remaining: 0, ResetMs: resetMs}},
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 300},
	}
	rl := NewRateLimiter(store, true, limits)
	e, h := setupEcho(rl)

	rec := doRequest(e, rl.Middleware(), h, "/api/notes/create", &model.User{ID: "user1"})

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "RATE_LIMIT_EXCEEDED", errObj["code"])
	assert.Equal(t, "d5826d14-3982-4d2e-8011-b9e9f02499ef", errObj["id"])
}

func TestMiddleware_AuthenticatedActor(t *testing.T) {
	store := &mockLimitStore{
		results: []mockResult{
			{Info: LimitInfo{Remaining: 10}}, // userID bucket
			{Info: LimitInfo{Remaining: 20}}, // IP bucket
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 300},
	}
	rl := NewRateLimiter(store, true, limits)
	e, h := setupEcho(rl)

	doRequest(e, rl.Middleware(), h, "/api/notes/create", &model.User{ID: "myuserid"})

	// userID + IP bucket の 2 つに対して check が走る (#606 item 2)
	require.Len(t, store.calls, 2)
	assert.Equal(t, "myuserid:notes/create", store.calls[0].Key)
	assert.Contains(t, store.calls[1].Key, "ip-")
}

// 認証済 user の userID bucket が満杯でも IP bucket が余裕でも 429
// (どちらかのしきい値超過で reject、OR-bucket セマンティクス)。
func TestMiddleware_AuthenticatedActor_UserBucketExceeds(t *testing.T) {
	resetMs := time.Now().Add(30 * time.Second).UnixMilli()
	store := &mockLimitStore{
		results: []mockResult{
			{Info: LimitInfo{Remaining: 0, ResetMs: resetMs}}, // userID bucket exceeded
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 300},
	}
	rl := NewRateLimiter(store, true, limits)
	e, h := setupEcho(rl)

	rec := doRequest(e, rl.Middleware(), h, "/api/notes/create", &model.User{ID: "u1"})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Len(t, store.calls, 1, "userID bucket で reject なら IP bucket は check しない")
}

// 認証済 user の userID bucket は OK でも IP bucket が満杯なら 429
// (cross-account brute-force 防御 #606 item 2)。
func TestMiddleware_AuthenticatedActor_IPBucketExceeds(t *testing.T) {
	resetMs := time.Now().Add(30 * time.Second).UnixMilli()
	store := &mockLimitStore{
		results: []mockResult{
			{Info: LimitInfo{Remaining: 50}},                  // userID bucket OK
			{Info: LimitInfo{Remaining: 0, ResetMs: resetMs}}, // IP bucket exceeded
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 300},
	}
	rl := NewRateLimiter(store, true, limits)
	e, h := setupEcho(rl)

	rec := doRequest(e, rl.Middleware(), h, "/api/notes/create", &model.User{ID: "u1"})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Len(t, store.calls, 2)
}

// EnableIPRateLimit=false なら認証済でも IP bucket は走らない
// (operator 配線の意図を尊重)。
func TestMiddleware_AuthenticatedActor_IPDisabled(t *testing.T) {
	store := &mockLimitStore{
		results: []mockResult{
			{Info: LimitInfo{Remaining: 10}},
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 300},
	}
	rl := NewRateLimiter(store, false, limits)
	e, h := setupEcho(rl)

	doRequest(e, rl.Middleware(), h, "/api/notes/create", &model.User{ID: "u1"})
	require.Len(t, store.calls, 1, "IP bucket は EnableIPRateLimit=false で skip")
	assert.Equal(t, "u1:notes/create", store.calls[0].Key)
}

// PolicyProvider 経由で rateLimitFactor=2.0 が反映されると Max が upstream 同様
// max/factor で scale される (#606 item 4 / #2106 N28: factor=2.0 で 1/2 に厳格化)。
func TestMiddleware_PolicyProviderScalesMax(t *testing.T) {
	store := &mockLimitStore{
		results: []mockResult{
			{Info: LimitInfo{Remaining: 100}},
			{Info: LimitInfo{Remaining: 100}},
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 300},
	}
	rl := NewRateLimiter(store, true, limits)
	rl.SetPolicyProvider(stubPolicy{factor: 2.0})
	e, h := setupEcho(rl)

	doRequest(e, rl.Middleware(), h, "/api/notes/create", &model.User{ID: "vip"})

	require.Len(t, store.calls, 2)
	// userID bucket は factor=2.0 で 150 に scale (300/2.0)、IP bucket は factor=1.0
	assert.Equal(t, 150, store.calls[0].Max, "userID bucket: 300 / 2.0 (factor は除数)")
	assert.Equal(t, 300, store.calls[1].Max, "IP bucket は factor 適用外")
}

// factor が 0 / 負 / 不正型なら 1.0 (= base) に fallback
func TestMiddleware_PolicyProviderInvalidFactor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy stubPolicy
	}{
		{"zero", stubPolicy{factor: 0}},
		{"negative", stubPolicy{factor: -1}},
		{"missing", stubPolicy{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &mockLimitStore{
				results: []mockResult{{Info: LimitInfo{Remaining: 10}}, {Info: LimitInfo{Remaining: 10}}},
			}
			limits := map[string]*EndpointLimit{
				"notes/create": {Duration: time.Hour, Max: 300},
			}
			rl := NewRateLimiter(store, true, limits)
			rl.SetPolicyProvider(tc.policy)
			e, h := setupEcho(rl)
			doRequest(e, rl.Middleware(), h, "/api/notes/create", &model.User{ID: "u"})
			require.GreaterOrEqual(t, len(store.calls), 1)
			assert.Equal(t, 300, store.calls[0].Max, "不正 factor は 1.0 fallback")
		})
	}
}

// stubPolicy は PolicyProvider のテスト double。factor=0 なら policies map
// に rateLimitFactor を含めない (= 「未設定」を再現)。
type stubPolicy struct{ factor float64 }

func (s stubPolicy) GetUserPolicies(_ string) map[string]any {
	if s.factor == 0 {
		return map[string]any{}
	}
	return map[string]any{"rateLimitFactor": s.factor}
}

// scaledMax は upstream RateLimiterService の max/factor (除数) semantics (#2106 N28)。
// factor が大きいほど実効 max が小さく (厳格)、小さいほど大きく (緩和) なる。
func TestScaledMax(t *testing.T) {
	assert.Equal(t, 300, scaledMax(300, 1.0))
	assert.Equal(t, 150, scaledMax(300, 2.0), "factor=2.0 (200%) で 1/2 に厳格化")
	assert.Equal(t, 600, scaledMax(300, 0.5), "factor=0.5 (50%) で 2x に緩和")
	assert.Equal(t, 1, scaledMax(10, 1000.0), "極大 factor でも 1 にクランプ")
	assert.Equal(t, 300, scaledMax(300, 0), "factor=0 は 1.0 扱い (= base)")
	assert.Equal(t, 300, scaledMax(300, -1), "負数も 1.0 扱い (防御)")
}

// scaledMinInterval は upstream の minInterval*factor semantics (#2106 N28)。
func TestScaledMinInterval(t *testing.T) {
	base := 1000 * time.Millisecond
	assert.Equal(t, base, scaledMinInterval(base, 1.0))
	assert.Equal(t, 2000*time.Millisecond, scaledMinInterval(base, 2.0), "factor=2.0 で window 2x (厳格)")
	assert.Equal(t, 500*time.Millisecond, scaledMinInterval(base, 0.5), "factor=0.5 で window 半分 (緩和)")
	assert.Equal(t, base, scaledMinInterval(base, 0), "factor=0 は base")
}

func TestMiddleware_UnauthenticatedIPActor(t *testing.T) {
	store := &mockLimitStore{
		results: []mockResult{
			{Info: LimitInfo{Remaining: 10}},
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 300},
	}
	rl := NewRateLimiter(store, true, limits)
	e, h := setupEcho(rl)

	rec := doRequest(e, rl.Middleware(), h, "/api/notes/create", nil)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, store.calls, 1)
	// キーがip-プレフィックスのIPハッシュを含む
	assert.Contains(t, store.calls[0].Key, "ip-")
	assert.Contains(t, store.calls[0].Key, ":notes/create")
}

func TestMiddleware_UnauthenticatedIPDisabled(t *testing.T) {
	store := &mockLimitStore{}
	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 300},
	}
	rl := NewRateLimiter(store, false, limits)
	e, h := setupEcho(rl)

	rec := doRequest(e, rl.Middleware(), h, "/api/notes/create", nil)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, store.calls)
}

func TestMiddleware_MinIntervalCheckFirst(t *testing.T) {
	store := &mockLimitStore{
		results: []mockResult{
			{Info: LimitInfo{Remaining: 1}},  // userID: minInterval OK
			{Info: LimitInfo{Remaining: 50}}, // userID: duration/max OK
			{Info: LimitInfo{Remaining: 1}},  // IP: minInterval OK
			{Info: LimitInfo{Remaining: 50}}, // IP: duration/max OK
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/delete": {Duration: time.Hour, Max: 300, MinInterval: time.Second},
	}
	rl := NewRateLimiter(store, true, limits)
	e, h := setupEcho(rl)

	rec := doRequest(e, rl.Middleware(), h, "/api/notes/delete", &model.User{ID: "u1"})

	assert.Equal(t, http.StatusOK, rec.Code)
	// 認証済 → userID + IP の 2 actor、各 actor で minInterval + duration の
	// 2 チェック → 計 4 calls
	require.Len(t, store.calls, 4)
	// userID actor: minInterval が先、その後 duration/max
	assert.Equal(t, "u1:notes/delete:min", store.calls[0].Key)
	assert.Equal(t, time.Second, store.calls[0].Duration)
	assert.Equal(t, 1, store.calls[0].Max)
	assert.Equal(t, "u1:notes/delete", store.calls[1].Key)
	assert.Equal(t, time.Hour, store.calls[1].Duration)
	assert.Equal(t, 300, store.calls[1].Max)
	// IP actor: 同じ順序
	assert.Contains(t, store.calls[2].Key, "ip-")
	assert.Contains(t, store.calls[2].Key, ":notes/delete:min")
	assert.Contains(t, store.calls[3].Key, ":notes/delete")
}

func TestMiddleware_MinIntervalBlock(t *testing.T) {
	resetMs := time.Now().Add(500 * time.Millisecond).UnixMilli()
	store := &mockLimitStore{
		results: []mockResult{
			{Info: LimitInfo{Remaining: 0, ResetMs: resetMs}}, // minInterval: blocked
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/delete": {Duration: time.Hour, Max: 300, MinInterval: time.Second},
	}
	rl := NewRateLimiter(store, true, limits)
	e, h := setupEcho(rl)

	rec := doRequest(e, rl.Middleware(), h, "/api/notes/delete", &model.User{ID: "u1"})

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	// minIntervalでブロックされたらduration/maxチェックは呼ばれない
	require.Len(t, store.calls, 1)
}

func TestMiddleware_RedisError_FailOpen(t *testing.T) {
	store := &mockLimitStore{
		results: []mockResult{
			{Err: assert.AnError},
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 300},
	}
	rl := NewRateLimiter(store, true, limits)
	e, h := setupEcho(rl)

	rec := doRequest(e, rl.Middleware(), h, "/api/notes/create", &model.User{ID: "u1"})

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddleware_RedisError_MinInterval_FailOpen(t *testing.T) {
	store := &mockLimitStore{
		results: []mockResult{
			{Err: assert.AnError},
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/delete": {Duration: time.Hour, Max: 300, MinInterval: time.Second},
	}
	rl := NewRateLimiter(store, true, limits)
	e, h := setupEcho(rl)

	rec := doRequest(e, rl.Middleware(), h, "/api/notes/delete", &model.User{ID: "u1"})

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddleware_RetryAfterHeader(t *testing.T) {
	// 30秒後にリセット
	resetMs := time.Now().Add(30 * time.Second).UnixMilli()
	store := &mockLimitStore{
		results: []mockResult{
			{Info: LimitInfo{Remaining: 0, ResetMs: resetMs}},
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 300},
	}
	rl := NewRateLimiter(store, true, limits)
	e, h := setupEcho(rl)

	rec := doRequest(e, rl.Middleware(), h, "/api/notes/create", &model.User{ID: "u1"})

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	retryAfter := rec.Header().Get("Retry-After")
	assert.NotEmpty(t, retryAfter)
	// 30±2秒の範囲であること
	var secs int
	_, _ = fmt.Sscanf(retryAfter, "%d", &secs)
	assert.InDelta(t, 30, secs, 2)
}

func TestMiddleware_NonAPIPath(t *testing.T) {
	store := &mockLimitStore{}
	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 300},
	}
	rl := NewRateLimiter(store, true, limits)
	e, h := setupEcho(rl)

	// /api/のないパスはエンドポイント名が一致しないのでスルー
	rec := doRequest(e, rl.Middleware(), h, "/healthz", nil)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, store.calls)
}

// --- ipHash tests ---

func TestIpHash_IPv4(t *testing.T) {
	h := ipHash("192.168.1.1")
	assert.True(t, len(h) > 3)
	assert.True(t, h[:3] == "ip-")
}

func TestIpHash_IPv4_DifferentAddresses(t *testing.T) {
	h1 := ipHash("192.168.1.1")
	h2 := ipHash("10.0.0.1")
	assert.NotEqual(t, h1, h2)
}

func TestIpHash_IPv6(t *testing.T) {
	h := ipHash("2001:db8::1")
	assert.True(t, len(h) > 3)
	assert.True(t, h[:3] == "ip-")
}

func TestIpHash_IPv6_SameSubnet(t *testing.T) {
	// 同一/64サブネットのIPは同じハッシュ
	h1 := ipHash("2001:db8:1234:5678::1")
	h2 := ipHash("2001:db8:1234:5678::ffff")
	assert.Equal(t, h1, h2)
}

func TestIpHash_IPv6_DifferentSubnet(t *testing.T) {
	h1 := ipHash("2001:db8:1234:5678::1")
	h2 := ipHash("2001:db8:1234:9999::1")
	assert.NotEqual(t, h1, h2)
}

func TestIpHash_Invalid(t *testing.T) {
	h := ipHash("not-an-ip")
	assert.True(t, len(h) > 3)
	assert.True(t, h[:3] == "ip-")
}

func TestIpHash_Deterministic(t *testing.T) {
	h1 := ipHash("192.168.1.1")
	h2 := ipHash("192.168.1.1")
	assert.Equal(t, h1, h2)
}

// --- parseIntFromString ---

func TestParseIntFromString(t *testing.T) {
	assert.Equal(t, int64(12345), parseIntFromString("12345"))
	assert.Equal(t, int64(0), parseIntFromString(""))
	assert.Equal(t, int64(0), parseIntFromString("abc"))
}

// --- DefaultEndpointLimits ---

func TestDefaultEndpointLimits_NotEmpty(t *testing.T) {
	assert.Greater(t, len(DefaultEndpointLimits), 50)
}

func TestDefaultEndpointLimits_KnownEndpoints(t *testing.T) {
	cases := []struct {
		endpoint string
		max      int
	}{
		{"notes/create", 300},
		{"following/create", 100},
		{"blocking/create", 20},
		{"drive/files/create", 120},
		{"channels/create", 10},
		{"ap/show", 30},
		// Auth / password reset (#600 item 3): signup spam / brute-force 対策。
		{"signup", 5},
		{"signup-pending", 30},
		{"signin", 10},
		{"signin-flow", 10},
		{"signin-with-passkey", 200},
		{"request-reset-password", 3},
		{"reset-password", 30},
	}
	for _, tc := range cases {
		t.Run(tc.endpoint, func(t *testing.T) {
			limit, ok := DefaultEndpointLimits[tc.endpoint]
			require.True(t, ok, "endpoint %s not found in limits", tc.endpoint)
			assert.Equal(t, tc.max, limit.Max)
		})
	}
}

func TestDefaultEndpointLimits_MinIntervalEndpoints(t *testing.T) {
	cases := []struct {
		endpoint    string
		minInterval time.Duration
	}{
		{"notes/delete", time.Second},
		{"notes/unrenote", time.Second},
		{"notes/reactions/delete", 3 * time.Second},
		{"bubble-game/register", 30 * time.Second},
		{"signin", time.Second},
		{"signin-flow", time.Second},
		{"signin-with-passkey", 250 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.endpoint, func(t *testing.T) {
			limit, ok := DefaultEndpointLimits[tc.endpoint]
			require.True(t, ok)
			assert.Equal(t, tc.minInterval, limit.MinInterval)
		})
	}
}

func TestMiddleware_RetryAfterNegativeClampsToZero(t *testing.T) {
	// リセット時刻が過去の場合、Retry-Afterは0にクランプ
	resetMs := time.Now().Add(-10 * time.Second).UnixMilli()
	store := &mockLimitStore{
		results: []mockResult{
			{Info: LimitInfo{Remaining: 0, ResetMs: resetMs}},
		},
	}
	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 300},
	}
	rl := NewRateLimiter(store, true, limits)
	e, h := setupEcho(rl)

	rec := doRequest(e, rl.Middleware(), h, "/api/notes/create", &model.User{ID: "u1"})

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "0", rec.Header().Get("Retry-After"))
}

// TestMiddleware_SigninReturnsTooManyAuthFailures verifies the signin endpoint's
// 429 carries the upstream TOO_MANY_AUTHENTICATION_FAILURES code/id rather than
// the generic RATE_LIMIT_EXCEEDED (#1829)。signin は minInterval(1s) と
// duration/max(10/1h) の 2 gate を持つので、両方の reject 経路で custom error が
// 返ることを確認する (1 件目で minInterval gate、2 件目で minInterval pass →
// duration gate)。
func TestMiddleware_SigninReturnsTooManyAuthFailures(t *testing.T) {
	cases := []struct {
		name    string
		results []mockResult
	}{
		{
			name:    "minInterval gate",
			results: []mockResult{{Info: LimitInfo{Remaining: 0, ResetMs: time.Now().Add(time.Hour).UnixMilli()}}},
		},
		{
			name: "duration gate",
			results: []mockResult{
				{Info: LimitInfo{Remaining: 1, ResetMs: time.Now().Add(time.Second).UnixMilli()}}, // minInterval pass
				{Info: LimitInfo{Remaining: 0, ResetMs: time.Now().Add(time.Hour).UnixMilli()}},   // duration reject
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &mockLimitStore{results: tc.results}
			// 実際の wired def を使い、RejectResponse 配線まで含めて検証する。
			limits := map[string]*EndpointLimit{"signin": DefaultEndpointLimits["signin"]}
			rl := NewRateLimiter(store, true, limits)
			e, h := setupEcho(rl)

			rec := doRequest(e, rl.Middleware(), h, "/api/signin", &model.User{ID: "u1"})

			assert.Equal(t, http.StatusTooManyRequests, rec.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			errObj, _ := body["error"].(map[string]any)
			require.NotNil(t, errObj)
			assert.Equal(t, "TOO_MANY_AUTHENTICATION_FAILURES", errObj["code"])
			assert.Equal(t, "22d05606-fbcf-421a-a2db-b32610dcfd1b", errObj["id"])
		})
	}
}

// TestMiddleware_GenericEndpointReturnsRateLimitExceeded confirms endpoints
// without a RejectResponse override still return the generic error.
func TestMiddleware_GenericEndpointReturnsRateLimitExceeded(t *testing.T) {
	store := &mockLimitStore{
		results: []mockResult{
			{Info: LimitInfo{Remaining: 0, ResetMs: time.Now().Add(time.Hour).UnixMilli()}},
		},
	}
	limits := map[string]*EndpointLimit{"notes/create": {Duration: time.Hour, Max: 300}}
	rl := NewRateLimiter(store, true, limits)
	e, h := setupEcho(rl)

	rec := doRequest(e, rl.Middleware(), h, "/api/notes/create", &model.User{ID: "u1"})

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj, _ := body["error"].(map[string]any)
	require.NotNil(t, errObj)
	assert.Equal(t, "RATE_LIMIT_EXCEEDED", errObj["code"])
}

// --- Redis integration test ---

func TestRedisRateLimitStore_Integration(t *testing.T) {
	ctx := context.Background()
	tr, err := testutil.SetupRedis(ctx)
	if err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	defer tr.Teardown(ctx)
	tr.FlushAll(ctx)

	store := NewRedisRateLimitStore(tr.Client)

	// 5リクエスト/10秒のリミット
	const maxReqs = 5
	dur := 10 * time.Second

	// ZCARDはZADD前に実行されるので、remaining=max-count(before add)。
	// maxReqs回は通る（remaining: max, max-1, ..., 1）
	for i := 0; i < maxReqs; i++ {
		info, err := store.Check(ctx, "test-user:test-ep", dur, maxReqs)
		require.NoError(t, err)
		assert.Equal(t, maxReqs-i, info.Remaining, "iteration %d", i)
	}

	// maxReqs+1回目はremaining=0（ブロック対象）
	info, err := store.Check(ctx, "test-user:test-ep", dur, maxReqs)
	require.NoError(t, err)
	assert.Equal(t, 0, info.Remaining)
	assert.Greater(t, info.ResetMs, time.Now().UnixMilli())
}

func TestRedisRateLimitStore_SeparateKeys(t *testing.T) {
	ctx := context.Background()
	tr, err := testutil.SetupRedis(ctx)
	if err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	defer tr.Teardown(ctx)
	tr.FlushAll(ctx)

	store := NewRedisRateLimitStore(tr.Client)
	dur := 10 * time.Second

	// 異なるキーは独立してカウント
	// ZCARDはZADD前なので1回目のremaining=max(=1)
	info1, err := store.Check(ctx, "user1:ep", dur, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, info1.Remaining) // max=1で1回目、ZCARD=0 → remaining=1

	info2, err := store.Check(ctx, "user2:ep", dur, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, info2.Remaining) // 別キーなのでこちらも1回目

	// 2回目はremaining=0
	info3, err := store.Check(ctx, "user1:ep", dur, 1)
	require.NoError(t, err)
	assert.Equal(t, 0, info3.Remaining)
}

// TestRateLimiter_NilLimitsBypassesAllEndpoints verifies that passing a nil
// limits map (used when disableEndpointRateLimits=true) lets every endpoint
// through, even ones that would normally be capped (e.g. notes/create).
func TestRateLimiter_NilLimitsBypassesAllEndpoints(t *testing.T) {
	store := &mockLimitStore{}
	rl := NewRateLimiter(store, true, nil)

	e := echo.New()
	handler := func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	}
	mw := rl.Middleware()

	for i := range 100 {
		req := httptest.NewRequest(http.MethodPost, "/api/notes/create", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/api/notes/create")
		c.Set(string(UserContextKey), &model.User{ID: "testuser"})
		_ = mw(handler)(c)
		require.Equal(t, http.StatusOK, rec.Code, "iter %d should always pass when limits is nil", i)
	}

	assert.Empty(t, store.calls, "store.Check must not be invoked when limits is nil")
}

// TestRateLimiter_EmptyLimitsBypassesAllEndpoints is structurally similar to
// the nil-map case but exercises the explicit empty-map path so refactors
// that swap nil for map[...]{} don't regress.
func TestRateLimiter_EmptyLimitsBypassesAllEndpoints(t *testing.T) {
	store := &mockLimitStore{}
	rl := NewRateLimiter(store, true, map[string]*EndpointLimit{})

	e := echo.New()
	handler := func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	}
	mw := rl.Middleware()

	req := httptest.NewRequest(http.MethodPost, "/api/notes/create", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/notes/create")
	c.Set(string(UserContextKey), &model.User{ID: "u"})
	_ = mw(handler)(c)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, store.calls)
}

func TestNewRedisRateLimiter_Integration(t *testing.T) {
	ctx := context.Background()
	tr, err := testutil.SetupRedis(ctx)
	if err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	defer tr.Teardown(ctx)
	tr.FlushAll(ctx)

	limits := map[string]*EndpointLimit{
		"notes/create": {Duration: time.Hour, Max: 2},
	}
	rl := NewRedisRateLimiter(tr.Client, true, limits)

	e := echo.New()
	handler := func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	}
	mw := rl.Middleware()

	// 2回は通る
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/notes/create", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/api/notes/create")
		c.Set(string(UserContextKey), &model.User{ID: "testuser"})
		_ = mw(handler)(c)
		assert.Equal(t, http.StatusOK, rec.Code, "request %d", i)
	}

	// 3回目は429
	req := httptest.NewRequest(http.MethodPost, "/api/notes/create", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/notes/create")
	c.Set(string(UserContextKey), &model.User{ID: "testuser"})
	_ = mw(handler)(c)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
}
