package role

import (
	"context"
	"testing"
	"time"

	"github.com/shiroha-a/mk/plugin"
	"github.com/stretchr/testify/assert"
)

func TestAcquirePolicyProviderTokenRejectsExpiredContext(t *testing.T) {
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runtime := newPolicyProviderRuntime(defaultEffectivePolicyProviderCacheEntries)

		assert.False(t, acquirePolicyProviderToken(ctx, runtime))
		assert.Len(t, runtime.token, 1)
	}
}

func TestReceivePolicyProviderResultRejectsExpiredContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan policyProviderResult, 1)
	result <- policyProviderResult{ok: true}

	_, ok := receivePolicyProviderResult(ctx, result)
	assert.False(t, ok)
}

func TestPolicyProviderFlightIsCurrent(t *testing.T) {
	runtime := newPolicyProviderRuntime(defaultEffectivePolicyProviderCacheEntries)
	runtime.userEpoch["u1"] = 2
	runtime.globalEpoch = 3

	assert.True(t, policyProviderFlightIsCurrent(runtime, "u1", &policyProviderFlight{userEpoch: 2, globalEpoch: 3}))
	assert.False(t, policyProviderFlightIsCurrent(runtime, "u1", &policyProviderFlight{userEpoch: 1, globalEpoch: 3}), "user invalidation rejects the old flight")
	assert.False(t, policyProviderFlightIsCurrent(runtime, "u1", &policyProviderFlight{userEpoch: 2, globalEpoch: 2}), "role invalidation rejects the old flight")
}

func TestWaitPolicyProviderFlightRejectsStaleResult(t *testing.T) {
	flight := &policyProviderFlight{
		done:          make(chan struct{}),
		contributions: []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: false}},
		ok:            true,
	}
	close(flight.done)

	contributions, ok, joined := waitPolicyProviderFlight(flight, false)

	assert.False(t, joined)
	assert.False(t, ok)
	assert.Nil(t, contributions)
}

func TestWaitPolicyProviderFlightReturnsCurrentResult(t *testing.T) {
	flight := &policyProviderFlight{
		done:          make(chan struct{}),
		contributions: []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: true}},
		ok:            true,
	}
	close(flight.done)

	contributions, ok, joined := waitPolicyProviderFlight(flight, true)

	assert.True(t, joined)
	assert.True(t, ok)
	assert.Equal(t, true, contributions[0].Value)
}

func TestPolicyProviderCacheLRUEvictsLeastRecentlyUsed(t *testing.T) {
	runtime := newPolicyProviderRuntime(defaultEffectivePolicyProviderCacheEntries)
	runtime.cacheEntries = 2
	k1 := policyProviderCacheKey{userID: "u1"}
	k2 := policyProviderCacheKey{userID: "u2"}
	k3 := policyProviderCacheKey{userID: "u3"}
	value := []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: true}}

	runtime.cachePut(k1, value)
	runtime.cachePut(k2, value)
	_, ok := runtime.cacheGet(k1)
	assert.True(t, ok)
	runtime.cachePut(k3, value)

	_, ok = runtime.cacheGet(k2)
	assert.False(t, ok)
	assert.Len(t, runtime.cache, 2)
	assert.Equal(t, 2, runtime.cacheLRU.Len())
}

func TestPolicyProviderCacheLRUReplacementKeepsOneElement(t *testing.T) {
	runtime := newPolicyProviderRuntime(2)
	key := policyProviderCacheKey{userID: "u1"}
	runtime.cachePut(key, []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: false}})
	runtime.cachePut(key, []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: true}})

	contributions, ok := runtime.cacheGet(key)
	assert.True(t, ok)
	assert.Equal(t, true, contributions[0].Value)
	assert.Len(t, runtime.cache, 1)
	assert.Equal(t, 1, runtime.cacheLRU.Len())
}

func TestSetEffectivePolicyProviderCacheEntriesShrinksExistingRuntime(t *testing.T) {
	runtime := newPolicyProviderRuntime(3)
	value := []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: true}}
	runtime.cachePut(policyProviderCacheKey{userID: "u1"}, value)
	runtime.cachePut(policyProviderCacheKey{userID: "u2"}, value)
	runtime.cachePut(policyProviderCacheKey{userID: "u3"}, value)
	svc := &Service{
		policyProviders:                     []policyProvider{{runtime: runtime}},
		effectivePolicyProviderCacheEntries: 3,
	}

	svc.SetEffectivePolicyProviderCacheEntries(2)

	assert.Len(t, runtime.cache, 2)
	assert.Equal(t, 2, runtime.cacheLRU.Len())
	_, ok := runtime.cacheGet(policyProviderCacheKey{userID: "u1"})
	assert.False(t, ok)
}

func TestPolicyProviderCacheClearResetsMapAndList(t *testing.T) {
	runtime := newPolicyProviderRuntime(defaultEffectivePolicyProviderCacheEntries)
	runtime.cachePut(policyProviderCacheKey{userID: "u1"}, []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: true}})

	runtime.cacheClear()

	assert.Empty(t, runtime.cache)
	assert.Zero(t, runtime.cacheLRU.Len())
}

func TestDisablePolicyProviderClearsMapAndList(t *testing.T) {
	runtime := newPolicyProviderRuntime(defaultEffectivePolicyProviderCacheEntries)
	runtime.cachePut(policyProviderCacheKey{userID: "u1"}, []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: true}})

	disablePolicyProvider(runtime)

	assert.True(t, runtime.disabled.Load())
	assert.Empty(t, runtime.cache)
	assert.Zero(t, runtime.cacheLRU.Len())
}

func TestPolicyProviderCacheDeleteUserRemovesEveryRoleVariant(t *testing.T) {
	runtime := newPolicyProviderRuntime(defaultEffectivePolicyProviderCacheEntries)
	value := []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: true}}
	runtime.cachePut(policyProviderCacheKey{userID: "u1", roleIDs: "2:r1"}, value)
	runtime.cachePut(policyProviderCacheKey{userID: "u1", roleIDs: "2:r2"}, value)
	runtime.cachePut(policyProviderCacheKey{userID: "u2", roleIDs: "2:r1"}, value)

	runtime.cacheDeleteUser("u1")

	assert.Len(t, runtime.cache, 1)
	assert.Equal(t, 1, runtime.cacheLRU.Len())
	_, ok := runtime.cacheGet(policyProviderCacheKey{userID: "u2", roleIDs: "2:r1"})
	assert.True(t, ok)
}

func TestFinishPolicyProviderInvocationDisablesBeforeTokenReturn(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	runtime := &policyProviderRuntime{token: make(chan struct{})}
	result := make(chan policyProviderResult, 1)
	done := make(chan struct{})
	go func() {
		finishPolicyProviderInvocation(ctx, runtime, result, policyProviderResult{ok: true})
		close(done)
	}()

	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !runtime.disabled.Load() {
		select {
		case <-ticker.C:
		case <-deadline:
			// 順序が逆でもgoroutineを残さずtestを終了する。
			<-runtime.token
			<-done
			t.Fatal("provider was not disabled before token return")
		}
	}
	<-runtime.token
	<-done
	assert.False(t, (<-result).completedAt.IsZero())
}
