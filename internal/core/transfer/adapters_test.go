package transfer_test

import (
	"testing"

	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	"github.com/shiroha-a/mk/internal/core/transfer"
	"github.com/stretchr/testify/assert"
)

func TestNewFollowingServiceAdapter_ConstructsWithRealType(t *testing.T) {
	// Adapter wraps *corefollowing.Service. We can't easily construct a real
	// one without pulling in DB dependencies, so nil service + nil adapter
	// exercises the construction and nil-safety paths.
	var svc *corefollowing.Service
	a := transfer.NewFollowingServiceAdapter(svc)
	assert.NotNil(t, a)
	// Follow on a nil inner returns (nil, nil) via the guard.
	out, err := a.Follow("f", "g", transfer.FollowOptions{})
	assert.NoError(t, err)
	assert.Nil(t, out)
}
