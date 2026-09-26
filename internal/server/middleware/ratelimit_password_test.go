package middleware

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultEndpointLimits_PasswordCheckingEndpoints guards the endpoints that
// verify the current password.
//
// **native token を盗んだ攻撃者がパスワードの総当たりに使える**ので、
// i/change-password と同じ上限を置く。IP をローテートされても止まるよう、
// 認証済みで叩いたときに **user bucket を消費する**ことまで見る。
// 一覧の網羅は entitycompat の TestPasswordCheckingRoutesHaveRateLimits が
// handler の AST から導出して見る。
func TestDefaultEndpointLimits_PasswordCheckingEndpoints(t *testing.T) {
	paths := []string{
		"/api/i/change-password",
		"/api/i/delete-account",
		"/api/i/regenerate-token",
		"/api/i/2fa/register",
		"/api/i/2fa/unregister",
		"/api/i/2fa/register-key",
		"/api/i/2fa/key-done",
		"/api/i/2fa/remove-key",
	}
	user := &model.User{ID: "01hpasswordbruteforce00000"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			key := strings.TrimPrefix(path, "/api/")
			limit, ok := DefaultEndpointLimits[key]
			require.True(t, ok, "%s に上限が無い", path)
			assert.Equal(t, time.Hour, limit.Duration)
			assert.Equal(t, 10, limit.Max)
			assert.Equal(t, time.Second, limit.MinInterval)
			assert.False(t, limit.UserBucketOnly)

			store := &mockLimitStore{}
			rl := NewRateLimiter(store, true, DefaultEndpointLimits)
			e, h := setupEcho(rl)
			doRequest(e, rl.Middleware(), h, path, user)
			var userBucket bool
			for _, call := range store.calls {
				if call.Key == user.ID+":"+key {
					userBucket = true
				}
			}
			assert.True(t, userBucket,
				"%s が user bucket を消費していない。IP をローテートすれば総当たりが止まらない", path)

			// user bucket が尽きたら 429 で止まる (IP bucket はまだ余っている)。
			blocked := &mockLimitStore{results: []mockResult{
				{Info: LimitInfo{Remaining: 1}},
				{Info: LimitInfo{Remaining: 0, ResetMs: time.Now().Add(time.Minute).UnixMilli()}},
			}}
			rl2 := NewRateLimiter(blocked, true, DefaultEndpointLimits)
			e2, h2 := setupEcho(rl2)
			rec := doRequest(e2, rl2.Middleware(), h2, path, user)
			assert.Equal(t, http.StatusTooManyRequests, rec.Code)
		})
	}
}
