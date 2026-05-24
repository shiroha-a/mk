package entity

import (
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
)

// PackClip converts a model.Clip into the Misskey-compatible clip shape.
//
// misskey_dart の Clip.fromJson は createdAt (String) / userId (String) /
// user (UserLite) / isPublic (bool) / favoritedCount (num) を非null必須とする
// ため、これらを必ず出す (#1237)。owner は clip の所有ユーザーで、user フィールド
// を埋めるために caller が渡す (nil なら user は省略するが、misskey_dart 互換の
// ため呼び出し側は必ず owner を解決して渡すこと)。createdAt は clip ID (aidx)
// から復元する。
//
// favoritedCount は mk-go が clip favorite を追跡しないため 0 固定 (upstream は
// clip_favorite の count)。
func PackClip(cl *model.Clip, idGen id.Generator, owner *model.User) map[string]any {
	if cl == nil {
		return nil
	}
	out := map[string]any{
		"id":             cl.ID,
		"userId":         cl.UserID,
		"name":           cl.Name,
		"description":    cl.Description,
		"isPublic":       cl.IsPublic,
		"notesCount":     cl.NotesCount,
		"lastClippedAt":  cl.LastClippedAt,
		"favoritedCount": 0,
	}
	if idGen != nil {
		if t, err := idGen.ParseTime(cl.ID); err == nil {
			out["createdAt"] = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}
	if owner != nil {
		out["user"] = PackUserLite(owner)
	}
	return out
}
