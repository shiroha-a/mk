package entity

import (
	"encoding/json"
	"sync"
)

// AvatarDecorationLookup resolves avatar decoration ids to their public URL
// (the static asset that the frontend renders on top of an avatar). Returns
// ok=false when the id is not in the catalog (e.g. admin deleted it after
// users装着済み) so the entry can be filtered out — mirrors upstream
// UserEntityService.pack which does .filter(...some(...)) before mapping.
type AvatarDecorationLookup interface {
	LookupURL(id string) (url string, ok bool)
}

// avatarDecorationLookup is process-wide so PackUserLite can enrich without
// changing its signature (called from 100+ sites). Router wires this once at
// startup via SetAvatarDecorationLookup. nil disables enrichment, which keeps
// existing tests that don't bother wiring the lookup green (the response will
// simply omit url, identical to pre-#521 behaviour).
var (
	avatarDecorationLookupMu sync.RWMutex
	avatarDecorationLookup   AvatarDecorationLookup
)

// SetAvatarDecorationLookup wires the catalog used by PackUserLite to embed
// `url` on each avatarDecorations entry. Pass nil to clear (used in tests).
func SetAvatarDecorationLookup(l AvatarDecorationLookup) {
	avatarDecorationLookupMu.Lock()
	avatarDecorationLookup = l
	avatarDecorationLookupMu.Unlock()
}

// AvatarDecorationItem is the per-entry shape sent to clients. Mirrors
// upstream Misskey's UserEntityService output: {id, angle, flipH, offsetX,
// offsetY, url}. `url` is resolved from the avatar_decoration catalog.
//
// angle / flipH / offsetX / offsetY は upstream UserEntityService が
// `ud.angle || undefined` 等で falsy 値を省くため (#1781)、omitempty で
// 0 / false のとき出力しない (json-schema 上 optional)。id / url は常に出す。
type AvatarDecorationItem struct {
	ID      string  `json:"id"`
	Angle   float64 `json:"angle,omitempty"`
	FlipH   bool    `json:"flipH,omitempty"`
	OffsetX float64 `json:"offsetX,omitempty"`
	OffsetY float64 `json:"offsetY,omitempty"`
	URL     string  `json:"url"`
}

// resolveAvatarDecorations parses the raw `user.avatarDecorations` jsonb bytes
// into enriched items. Entries whose id no longer exists in the catalog are
// silently dropped (TS upstream does the same — a deleted decoration must not
// keep rendering on user profiles). Returns an empty slice (not nil) so the
// JSON output is `[]` rather than `null`, which is what the frontend expects.
func resolveAvatarDecorations(raw []byte) []AvatarDecorationItem {
	out := []AvatarDecorationItem{}
	if len(raw) == 0 {
		return out
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) == 0 {
		return out
	}
	avatarDecorationLookupMu.RLock()
	lookup := avatarDecorationLookup
	avatarDecorationLookupMu.RUnlock()
	for _, r := range rows {
		idVal, _ := r["id"].(string)
		if idVal == "" {
			continue
		}
		item := AvatarDecorationItem{ID: idVal}
		if v, ok := r["angle"].(float64); ok {
			item.Angle = v
		}
		if v, ok := r["flipH"].(bool); ok {
			item.FlipH = v
		}
		if v, ok := r["offsetX"].(float64); ok {
			item.OffsetX = v
		}
		if v, ok := r["offsetY"].(float64); ok {
			item.OffsetY = v
		}
		if lookup != nil {
			url, ok := lookup.LookupURL(idVal)
			if !ok {
				// catalog から消えた decoration はクライアント側で render
				// できないので silent drop。upstream TS も同じ filter を行う。
				continue
			}
			item.URL = url
		}
		out = append(out, item)
	}
	return out
}
