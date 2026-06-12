package notesfilter

import (
	"strings"

	"github.com/shiroha-a/mk/internal/model"
)

// ApplyBlockedHosts drops notes whose author, reply author, or renote author
// belongs to an admin-blocked instance, mirroring upstream
// QueryService.generateBlockedHostQueryForNote (#1562)。
//
// upstream は meta.blockedHosts を [x, %.x] に展開して NOT ILIKE ALL で突合
// する = exact 一致または subdomain 一致で除外。local user (host IS NULL) は
// 常に通す。blockedHosts が空なら no-op。
func ApplyBlockedHosts(notes []*model.Note, blockedHosts []string) []*model.Note {
	if len(notes) == 0 || len(blockedHosts) == 0 {
		return notes
	}
	out := make([]*model.Note, 0, len(notes))
	for _, n := range notes {
		if n == nil {
			continue
		}
		if hostBlocked(n.UserHost, blockedHosts) ||
			hostBlocked(n.ReplyUserHost, blockedHosts) ||
			hostBlocked(n.RenoteUserHost, blockedHosts) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// hostBlocked reports whether host matches any blocked entry exactly or as a
// subdomain (`%.entry` 相当)。ILIKE 突合なので case-insensitive。
func hostBlocked(host *string, blockedHosts []string) bool {
	if host == nil || *host == "" {
		return false
	}
	h := strings.ToLower(*host)
	for _, b := range blockedHosts {
		b = strings.ToLower(b)
		if b == "" {
			continue
		}
		if h == b || strings.HasSuffix(h, "."+b) {
			return true
		}
	}
	return false
}
