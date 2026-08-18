package model

import (
	"time"
)

// Channel represents the `channel` table. ユーザーが作成するトピックスペース
// (Misskey 互換)。Channel は ActivityPub 連携を持たないローカル機能。
type Channel struct {
	ID                    string      `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	LastNotedAt           *time.Time  `gorm:"column:lastNotedAt;type:timestamp with time zone" json:"lastNotedAt"`
	UserID                *string     `gorm:"column:userId;type:varchar(32)" json:"userId"`
	Name                  string      `gorm:"column:name;type:varchar(128);not null" json:"name"`
	Description           *string     `gorm:"column:description;type:varchar(2048)" json:"description"`
	BannerID              *string     `gorm:"column:bannerId;type:varchar(32)" json:"bannerId"`
	PinnedNoteIDs         StringArray `gorm:"column:pinnedNoteIds;type:varchar(128)[];default:'{}'" json:"pinnedNoteIds"`
	Color                 string      `gorm:"column:color;type:varchar(16);default:'#86b300'" json:"color"`
	IsArchived            bool        `gorm:"column:isArchived;default:false" json:"isArchived"`
	NotesCount            int         `gorm:"column:notesCount;default:0" json:"notesCount"`
	UsersCount            int         `gorm:"column:usersCount;default:0" json:"usersCount"`
	IsSensitive           bool        `gorm:"column:isSensitive;default:false" json:"isSensitive"`
	AllowRenoteToExternal bool        `gorm:"column:allowRenoteToExternal;default:true" json:"allowRenoteToExternal"`
}

func (Channel) TableName() string { return "channel" }

// ChannelFollowing represents the `channel_following` table.
type ChannelFollowing struct {
	ID         string `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	FolloweeID string `gorm:"column:followeeId;type:varchar(32);not null" json:"followeeId"`
	FollowerID string `gorm:"column:followerId;type:varchar(32);not null" json:"followerId"`
}

func (ChannelFollowing) TableName() string { return "channel_following" }

// ChannelListFilter narrows down the channels returned by repository queries.
// 0 値は無効化を意味する (フィルタしない)。Repository と service の双方から
// 参照されるため model パッケージに置く (循環依存の回避)。
type ChannelListFilter struct {
	OwnerID string // 所有者で絞る
	Query   string // name (SearchDescription 時は description も) 部分一致
	// SearchDescription: true で Query を name OR description にマッチさせる
	// (upstream channels/search type=nameAndDescription, 既定)。false は name のみ。
	SearchDescription bool
	IsArchived        *bool // archived 状態で絞る
	// LastNotedAtNotNull: true で lastNotedAt IS NOT NULL を AND する
	// (upstream channels/featured: 一度も投稿の無い channel を除外, #1540)。
	LastNotedAtNotNull bool
	SortBy             string // "+lastNotedAt" / "-lastNotedAt" / "+id" / "-id" / "+name" / "-notesCount" 等
	Limit              int
	Offset             int
	// Cursor pagination (frontend Paginator). 指定時は SortBy を id 順に
	// 上書きして cursor 一貫性を保つ。Offset は無視する。
	SinceID string
	UntilID string
}
