package role

import (
	"context"
	"testing"

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
