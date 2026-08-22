package role

import (
	"context"
	"sync/atomic"
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

func TestResolvePolicyProviderCachedSupersedesStaleFlightWithoutWaiting(t *testing.T) {
	runtime := newPolicyProviderRuntime(defaultEffectivePolicyProviderCacheEntries)
	key := policyProviderCacheKey{userID: "u1"}
	stale := &policyProviderFlight{done: make(chan struct{}), globalEpoch: 1}
	runtime.globalEpoch = 2
	runtime.flights[key] = stale
	started := make(chan struct{})
	provider := policyProvider{
		reg: plugin.EffectivePolicyRegistration{
			Keys: []string{"canSearchNotes"},
			Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
				close(started)
				return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: true}}, nil
			},
		},
		runtime: runtime,
	}
	done := make(chan struct{})
	go func() {
		resolvePolicyProviderCached(provider, plugin.EffectivePolicyRequest{UserID: "u1"})
		close(done)
	}()

	resolverStartedBeforeStaleCompletion := false
	select {
	case <-started:
		resolverStartedBeforeStaleCompletion = true
	case <-time.After(100 * time.Millisecond):
	}
	runtime.cacheMu.Lock()
	delete(runtime.flights, key)
	close(stale.done)
	runtime.cacheMu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("provider resolution did not complete after releasing the stale flight")
	}
	assert.True(t, resolverStartedBeforeStaleCompletion, "a stale generation must not consume the current request's timeout budget")
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

func TestAcquireEnabledPolicyProviderTokenReturnsTokenWhenDisabled(t *testing.T) {
	runtime := newPolicyProviderRuntime(defaultEffectivePolicyProviderCacheEntries)
	runtime.disabled.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	assert.False(t, acquireEnabledPolicyProviderToken(ctx, runtime))
	assert.Len(t, runtime.token, 1, "the acquired token must be returned when disable wins the wait race")
}

func TestResolvePolicyProviderCachedSupersededTokenWaiterSkipsResolver(t *testing.T) {
	runtime := newPolicyProviderRuntime(defaultEffectivePolicyProviderCacheEntries)
	<-runtime.token
	var calls atomic.Int32
	provider := policyProvider{
		reg: plugin.EffectivePolicyRegistration{
			Keys: []string{"canSearchNotes"},
			Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
				calls.Add(1)
				return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: true}}, nil
			},
		},
		runtime: runtime,
	}
	key := policyProviderCacheKey{userID: "u1"}
	done := make(chan struct{})
	go func() {
		resolvePolicyProviderCached(provider, plugin.EffectivePolicyRequest{UserID: "u1"})
		close(done)
	}()

	var original *policyProviderFlight
	deadline := time.After(time.Second)
	for original == nil {
		runtime.cacheMu.Lock()
		original = runtime.flights[key]
		runtime.cacheMu.Unlock()
		select {
		case <-deadline:
			t.Fatal("original flight was not registered")
		default:
		}
	}

	runtime.cacheMu.Lock()
	runtime.globalEpoch++
	replacement := &policyProviderFlight{done: make(chan struct{}), globalEpoch: runtime.globalEpoch}
	runtime.flights[key] = replacement
	runtime.cacheMu.Unlock()
	runtime.token <- struct{}{}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("superseded owner did not finish")
	}
	assert.Zero(t, calls.Load(), "an owner superseded while waiting for the token must not run its resolver")

	runtime.cacheMu.Lock()
	delete(runtime.flights, key)
	close(replacement.done)
	runtime.cacheMu.Unlock()
}

func TestReceivePolicyProviderResultRejectsCompletionAtOrAfterDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	result := make(chan policyProviderResult, 1)
	result <- policyProviderResult{ok: true, completedAt: deadline}

	_, ok := receivePolicyProviderResult(ctx, result)
	assert.False(t, ok)
}

func TestEncodePolicyProviderRoleIDsPreventsConcatenationCollision(t *testing.T) {
	assert.NotEqual(t, encodePolicyProviderRoleIDs([]string{"a", "bc"}), encodePolicyProviderRoleIDs([]string{"ab", "c"}))
}

func TestResolvePolicyProviderCachedReclaimsUserEpochAfterLastFlight(t *testing.T) {
	runtime := newPolicyProviderRuntime(defaultEffectivePolicyProviderCacheEntries)
	runtime.userEpoch["u1"] = 7
	provider := policyProvider{
		reg: plugin.EffectivePolicyRegistration{
			Keys: []string{"canSearchNotes"},
			Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
				return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: true}}, nil
			},
		},
		runtime: runtime,
	}

	_, ok := resolvePolicyProviderCached(provider, plugin.EffectivePolicyRequest{UserID: "u1"})
	assert.True(t, ok)
	assert.NotContains(t, runtime.userEpoch, "u1")
}

func TestClonePolicyContributionsScrubsIgnoredUseDefaultValue(t *testing.T) {
	secret := &struct{ Value string }{Value: "provider-owned"}
	cloned := clonePolicyContributions([]plugin.EffectivePolicyContribution{{Key: "canSearchNotes", UseDefault: true, Value: secret}})

	assert.Nil(t, cloned[0].Value)
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
