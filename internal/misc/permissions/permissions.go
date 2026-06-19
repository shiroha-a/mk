// Package permissions defines the canonical set of OAuth / app permission
// "kinds" (scopes), mirroring upstream misskey-js `permissions`
// (packages/misskey-js/src/consts.ts). Used to filter requested OAuth scopes to
// known kinds and to advertise scopes_supported in the RFC8414 discovery
// document (#1904). read:messaging / write:messaging are deprecated upstream but
// retained verbatim for parity.
package permissions

// All is the ordered list of known permission kinds, matching upstream
// misskey-js `permissions` exactly.
var All = []string{
	"read:account",
	"write:account",
	"read:blocks",
	"write:blocks",
	"read:drive",
	"write:drive",
	"read:favorites",
	"write:favorites",
	"read:following",
	"write:following",
	"read:messaging",
	"write:messaging",
	"read:mutes",
	"write:mutes",
	"write:notes",
	"read:notifications",
	"write:notifications",
	"read:reactions",
	"write:reactions",
	"write:votes",
	"read:pages",
	"write:pages",
	"write:page-likes",
	"read:page-likes",
	"read:user-groups",
	"write:user-groups",
	"read:channels",
	"write:channels",
	"read:gallery",
	"write:gallery",
	"read:gallery-likes",
	"write:gallery-likes",
	"read:flash",
	"write:flash",
	"read:flash-likes",
	"write:flash-likes",
	"read:admin:abuse-user-reports",
	"write:admin:delete-account",
	"write:admin:delete-all-files-of-a-user",
	"read:admin:index-stats",
	"read:admin:table-stats",
	"read:admin:user-ips",
	"read:admin:meta",
	"write:admin:reset-password",
	"write:admin:resolve-abuse-user-report",
	"write:admin:send-email",
	"read:admin:server-info",
	"read:admin:show-moderation-log",
	"read:admin:show-user",
	"write:admin:suspend-user",
	"write:admin:unset-user-avatar",
	"write:admin:unset-user-banner",
	"write:admin:unsuspend-user",
	"write:admin:meta",
	"write:admin:user-note",
	"write:admin:roles",
	"read:admin:roles",
	"write:admin:relays",
	"read:admin:relays",
	"write:admin:invite-codes",
	"read:admin:invite-codes",
	"write:admin:announcements",
	"read:admin:announcements",
	"write:admin:avatar-decorations",
	"read:admin:avatar-decorations",
	"write:admin:federation",
	"write:admin:account",
	"read:admin:account",
	"write:admin:emoji",
	"read:admin:emoji",
	"write:admin:queue",
	"read:admin:queue",
	"write:admin:promo",
	"write:admin:drive",
	"read:admin:drive",
	"write:admin:ad",
	"read:admin:ad",
	"write:invite-codes",
	"read:invite-codes",
	"write:clip-favorite",
	"read:clip-favorite",
	"read:federation",
	"write:report-abuse",
	"write:chat",
	"read:chat",
}

// set is a lookup set built from All for O(1) IsKnown checks.
var set = func() map[string]struct{} {
	m := make(map[string]struct{}, len(All))
	for _, p := range All {
		m[p] = struct{}{}
	}
	return m
}()

// IsKnown reports whether s is a recognized permission kind.
func IsKnown(s string) bool {
	_, ok := set[s]
	return ok
}
