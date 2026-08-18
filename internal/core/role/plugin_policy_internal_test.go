package role

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAcquirePolicyProviderTokenRejectsExpiredContext(t *testing.T) {
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runtime := newPolicyProviderRuntime()

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
