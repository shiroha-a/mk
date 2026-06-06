package entity

import (
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
)

// PackAnnouncement converts a model.Announcement into the map shape used by
// /api/announcements and the `announcementCreated` WebSocket event body.
// isRead is a viewer-dependent flag controlled by the caller; for events
// emitted at creation time it should be false. forYou is set to true when
// the announcement targets a specific user (UserID != nil).
func PackAnnouncement(a *model.Announcement, idGen id.Generator, isRead bool) map[string]any {
	if a == nil {
		return nil
	}
	const tsFormat = "2006-01-02T15:04:05.000Z"
	createdAt := ""
	if idGen != nil {
		if t, err := idGen.ParseTime(a.ID); err == nil {
			createdAt = t.UTC().Format(tsFormat)
		}
	}
	var updatedAt *string
	if a.UpdatedAt != nil {
		s := a.UpdatedAt.UTC().Format(tsFormat)
		updatedAt = &s
	}
	return map[string]any{
		"id":        a.ID,
		"createdAt": createdAt,
		"updatedAt": updatedAt,
		"title":     a.Title,
		"text":      a.Text,
		// announcement の imageUrl は admin 設定だが remote URL を指せる。
		// frontend は <img :src="announcement.imageUrl"> で直接読むため、
		// remote origin は proxy 経由へ書き換えて IP 漏洩を防ぐ (#1529)。
		"imageUrl":               ProxyMediaURLPtr(a.ImageURL),
		"icon":                   a.Icon,
		"display":                a.Display,
		"needConfirmationToRead": a.NeedConfirmationToRead,
		"silence":                a.Silence,
		"forYou":                 a.UserID != nil,
		"isRead":                 isRead,
	}
}
