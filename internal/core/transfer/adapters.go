package transfer

import (
	corefollowing "github.com/shiroha-a/mk/internal/core/following"
)

// FollowingServiceAdapter wraps core/following.Service so that its Follow
// method matches the Importer's FollowingService interface (which returns
// `any` to avoid depending on the concrete FollowResult type).
type FollowingServiceAdapter struct {
	svc *corefollowing.Service
}

// NewFollowingServiceAdapter constructs a FollowingServiceAdapter.
func NewFollowingServiceAdapter(svc *corefollowing.Service) *FollowingServiceAdapter {
	return &FollowingServiceAdapter{svc: svc}
}

// Follow delegates to core/following.Service.Follow with default options.
// CSV import の `withReplies=bool` field 解析は未実装 (import_csv.go:11 の
// godoc は intent 表記であって実装はまだ first field しか読まない)。実装する
// 際は本 adapter シグネチャを拡張して FollowOptions を threading する。
func (a *FollowingServiceAdapter) Follow(followerID, followeeID string) (any, error) {
	if a == nil || a.svc == nil {
		return nil, nil
	}
	return a.svc.Follow(followerID, followeeID, corefollowing.FollowOptions{})
}
