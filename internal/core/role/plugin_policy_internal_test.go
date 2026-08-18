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
	runtime := newPolicyProviderRuntime()
	<-runtime.token
	result := make(chan policyProviderResult, 1)

	finishPolicyProviderInvocation(ctx, runtime, result, policyProviderResult{ok: true})

	assert.True(t, runtime.disabled.Load())
	assert.Len(t, runtime.token, 1)
	assert.False(t, (<-result).completedAt.IsZero())
}
