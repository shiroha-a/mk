package role

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiroha-a/mk/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestResolvePolicyProviderCachedSupersededFlightCannotRepublishAfterReplacement(t *testing.T) {
	runtime := newPolicyProviderRuntime(defaultEffectivePolicyProviderCacheEntries)
	userID := "u1"
	key := policyProviderCacheKey{userID: userID}
	stale := &policyProviderFlight{done: make(chan struct{}), userEpoch: 0}
	replacement := &policyProviderFlight{done: make(chan struct{}), userEpoch: 1}
	runtime.userEpoch[userID] = 1
	runtime.userFlights[userID] = 2
	runtime.flights[key] = replacement

	finishPolicyProviderFlight(runtime, key, userID, replacement, []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: true}}, true)
	assert.Equal(t, uint64(1), runtime.userFlights[userID])
	assert.Equal(t, uint64(1), runtime.userEpoch[userID], "the replacement must retain the epoch while the superseded owner is alive")

	finishPolicyProviderFlight(runtime, key, userID, stale, []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: false}}, true)

	runtime.cacheMu.Lock()
	cached, ok := runtime.cacheGet(key)
	_, hasFlightCounter := runtime.userFlights[userID]
	runtime.cacheMu.Unlock()
	require.True(t, ok)
	require.Len(t, cached, 1)
	assert.Equal(t, true, cached[0].Value, "the superseded generation must not overwrite the replacement")
	assert.False(t, hasFlightCounter, "the last completed flight must reclaim its user refcount")
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

// warnBuffer is a mutex-guarded sink for slog output.
type warnBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *warnBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *warnBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// runtime は生成時の logger を握る (#2867)。
//
// **グローバルを差し替えたまま何かを待つテストにしないこと。** provider を
// 実際に走らせて外から観測する形にすると、`slog.Default()` が自分のバッファを
// 指している間に同じプロセスの他のテストが書き込み、**検証したいのと同じ
// 「グローバルの取り合い」で自分が落ちる** (実際に 2 度踏んだ)。ここでは
// 構築直後に default を戻し、warn を直接起こして出力先だけを見る。
func TestPolicyProviderRuntime_UsesLoggerCapturedAtConstruction(t *testing.T) {
	var captured warnBuffer
	previous := slog.Default()
	// **panic / t.Fatal でも必ず戻す** (#2795)。差し替わったまま残すと、
	// 同じバイナリの後続テストがグローバルを取り合う。
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&captured, &slog.HandlerOptions{Level: slog.LevelWarn})))
	runtime := newPolicyProviderRuntime(defaultEffectivePolicyProviderCacheEntries)
	// **構築の直後に戻す。** 以降の warn がグローバルではなく runtime の
	// logger へ出ることを見たいので、ここで default を別物にしておく。
	slog.SetDefault(previous)

	disablePolicyProvider(runtime)
	recordPolicyProviderFallback(runtime)

	out := captured.String()
	assert.Contains(t, out, "effective policy provider disabled after timeout",
		"timeout の warn が構築時の logger に出ていない")
	assert.Contains(t, out, "effective policy provider fallback",
		"fallback の warn が構築時の logger に出ていない")
}

// コンストラクタを通らない runtime でも落ちない (内部テストが直接組み立てる)。
func TestPolicyProviderRuntime_NilLoggerFallsBackToDefault(t *testing.T) {
	assert.NotPanics(t, func() {
		disablePolicyProvider(&policyProviderRuntime{})
		recordPolicyProviderFallback(&policyProviderRuntime{})
	})
}

// **warn は CAS と同じクリティカルセクションで出す** (#2867)。
//
// disable は requester と provider の goroutine の両方から呼ばれ、CAS に
// 勝ったほうだけが warn を出す。warn を Unlock の後に置くと、勝ったほうが
// 実際に書くまでの間に負けたほうが先へ進めてしまい、呼び出しから戻った時点で
// **まだ何も記録されていない**状態が作れる (テストはそこで 0 件を観測して落ちる)。
// stall を注入して実際にそうなることを確認してある。
//
// **構造で固定する。** タイミングで見ようとすると、勝者が warn を書き終えるまでの
// 窓が狭すぎて lock の外に戻す変異を捕まえられなかった (実測で空振り)。
func TestDisablePolicyProviderWarnsInsideCriticalSection(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "plugin_policy.go", nil, 0)
	require.NoError(t, err)

	var body *ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "disablePolicyProvider" {
			body = fn.Body
			return false
		}
		return true
	})
	require.NotNil(t, body, "disablePolicyProvider が見つからない")

	// 関数内での Unlock と Warn の位置を取る。
	var unlockPos, warnPos token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Unlock":
			if !unlockPos.IsValid() {
				unlockPos = call.Pos()
			}
		case "Warn":
			if !warnPos.IsValid() {
				warnPos = call.Pos()
			}
		}
		return true
	})
	require.True(t, unlockPos.IsValid(), "Unlock の呼び出しが無い")
	require.True(t, warnPos.IsValid(), "Warn の呼び出しが無い")

	assert.Less(t, int(warnPos), int(unlockPos),
		"warn が Unlock より後にある。CAS に負けた側が、記録される前に戻れてしまう (#2867)")
}
