// Package announcement provides a thin creation service that persists an
// announcement and emits the realtime `announcementCreated` main-stream
// event, mirroring upstream AnnouncementService.create (#1606).
//
// Repository の Create を直接呼ぶ経路 (例: #1563 の moderator-activity check)
// は stream event が出ず、対象 user が realtime に announcement へ気付けない。
// 永続化と event 発行を常に対で行いたい caller はこの Creator を使う。
// announcements handler (AdminCreate) は modlog 等が絡む既存経路のため
// 本パッケージへは未統合 (同 shape の publish を handler 内で行っている)。
package announcement

import (
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
)

// Repo is the persistence dependency (repository.AnnouncementRepository).
type Repo interface {
	Create(a *model.Announcement) error
}

// MainStreamPublisher emits events to a user's `main` WebSocket channel
// (internal/stream). Accepted as an interface to avoid a hard dependency on
// internal/stream (循環依存回避、api/announcements の同名 interface と同型)。
type MainStreamPublisher interface {
	PublishMainEvent(userID, eventType string, body any)
}

// Creator persists announcements and publishes the `announcementCreated`
// event for per-user announcements, like upstream AnnouncementService.create.
type Creator struct {
	repo      Repo
	idGen     id.Generator
	publisher MainStreamPublisher
}

// NewCreator constructs a Creator. publisher may be nil to disable the
// stream emit (graceful for tests / partial wiring).
func NewCreator(repo Repo, idGen id.Generator, publisher MainStreamPublisher) *Creator {
	return &Creator{repo: repo, idGen: idGen, publisher: publisher}
}

// Create persists the announcement and, for a per-user announcement
// (UserID != nil), publishes `announcementCreated` to the target user's main
// channel with the upstream event body shape ({"announcement": packed}).
//
// global announcement (UserID == nil) は本家では broadcast stream へ
// publish されるが、mk-go は broadcast インフラ未実装のため skip する
// (api/announcements AdminCreate と同じ判断)。
func (c *Creator) Create(a *model.Announcement) error {
	if err := c.repo.Create(a); err != nil {
		return err
	}
	if c.publisher != nil && a.UserID != nil {
		body := map[string]any{"announcement": entity.PackAnnouncement(a, c.idGen, false)}
		c.publisher.PublishMainEvent(*a.UserID, "announcementCreated", body)
	}
	return nil
}
