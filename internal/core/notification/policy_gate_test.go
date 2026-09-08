package notification

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubPolicyResolver records lookups and returns a fixed opt-out list.
type stubPolicyResolver struct {
	optOut  map[string][]string
	queried []string
}

func (s *stubPolicyResolver) OptOutNotificationTypes(userID string) []string {
	s.queried = append(s.queried, userID)
	return s.optOut[userID]
}

// TestCreate_RolePolicyOptOutSuppresses asserts an opted-out type is neither
// persisted nor published.
//
// **抑制は永続化の前でなければ意味が無い (#2898)。** 後段で捨てるだけだと
// Redis stream には残り、既読位置や通知一覧に現れる。
func TestCreate_RolePolicyOptOutSuppresses(t *testing.T) {
	svc := newTestSvc(t)
	svc.SetPolicyResolver(&stubPolicyResolver{optOut: map[string][]string{
		"u1": {"abuseReport", "note"},
	}})
	ctx := context.Background()

	n, err := svc.Create(ctx, CreateInput{NotifieeID: "u1", NotifierID: "u2", Type: "abuseReport"})
	require.NoError(t, err, "抑制はエラーではない")
	require.Nil(t, n, "抑制された通知は返さない")

	list, err := svc.List(ctx, "u1", "", "", 10, nil, nil)
	require.NoError(t, err)
	require.Empty(t, list, "抑制した通知が永続化されている")
}

// TestCreate_RolePolicyAllowsOtherTypes asserts the gate only cuts the listed
// types.
func TestCreate_RolePolicyAllowsOtherTypes(t *testing.T) {
	svc := newTestSvc(t)
	svc.SetPolicyResolver(&stubPolicyResolver{optOut: map[string][]string{
		"u1": {"abuseReport"},
	}})
	ctx := context.Background()

	n, err := svc.Create(ctx, CreateInput{NotifieeID: "u1", NotifierID: "u2", Type: TypeFollow})
	require.NoError(t, err)
	require.NotNil(t, n, "opt-out していない型まで抑制している")

	list, err := svc.List(ctx, "u1", "", "", 10, nil, nil)
	require.NoError(t, err)
	require.Len(t, list, 1)
}

// TestCreate_RolePolicyIsPerUser asserts one user's opt-out does not affect
// another's.
func TestCreate_RolePolicyIsPerUser(t *testing.T) {
	svc := newTestSvc(t)
	resolver := &stubPolicyResolver{optOut: map[string][]string{"u1": {"abuseReport"}}}
	svc.SetPolicyResolver(resolver)
	ctx := context.Background()

	n, err := svc.Create(ctx, CreateInput{NotifieeID: "u9", NotifierID: "u2", Type: "abuseReport"})
	require.NoError(t, err)
	require.NotNil(t, n, "別ユーザーの opt-out が適用されている")
	require.Contains(t, resolver.queried, "u9", "notifiee ではない ID で policy を引いている")
}

// TestCreate_WithoutResolverFailsOpen asserts an unwired resolver lets
// everything through.
func TestCreate_WithoutResolverFailsOpen(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()

	n, err := svc.Create(ctx, CreateInput{NotifieeID: "u1", NotifierID: "u2", Type: "abuseReport"})
	require.NoError(t, err)
	require.NotNil(t, n, "resolver 未配線では抑制しない (fail-open)")
}
