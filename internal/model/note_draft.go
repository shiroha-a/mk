package model

import (
	"time"
)

// NoteDraft represents the `note_draft` table.
type NoteDraft struct {
	ID                  string      `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UserID              string      `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	Text                *string     `gorm:"column:text;type:text" json:"text"`
	CW                  *string     `gorm:"column:cw;type:varchar(512)" json:"cw"`
	Visibility          string      `gorm:"column:visibility;type:varchar(32);default:'public'" json:"visibility"`
	LocalOnly           bool        `gorm:"column:localOnly;default:false" json:"localOnly"`
	ReactionAcceptance  *string     `gorm:"column:reactionAcceptance;type:varchar(64)" json:"reactionAcceptance"`
	FileIDs             StringArray `gorm:"column:fileIds;type:varchar(32)[];default:'{}'" json:"fileIds"`
	VisibleUserIDs      StringArray `gorm:"column:visibleUserIds;type:varchar(32)[];default:'{}'" json:"visibleUserIds"`
	ReplyID             *string     `gorm:"column:replyId;type:varchar(32)" json:"replyId"`
	RenoteID            *string     `gorm:"column:renoteId;type:varchar(32)" json:"renoteId"`
	ChannelID           *string     `gorm:"column:channelId;type:varchar(32)" json:"channelId"`
	Hashtag             *string     `gorm:"column:hashtag;type:varchar(128)" json:"hashtag"`
	HasPoll             bool        `gorm:"column:hasPoll;default:false" json:"hasPoll"`
	PollChoices         StringArray `gorm:"column:pollChoices;type:varchar(256)[];default:'{}'" json:"pollChoices"`
	PollMultiple        bool        `gorm:"column:pollMultiple;default:false" json:"pollMultiple"`
	PollExpiresAt       *time.Time  `gorm:"column:pollExpiresAt;type:timestamp with time zone" json:"pollExpiresAt"`
	ScheduledAt         *time.Time  `gorm:"column:scheduledAt;type:timestamp with time zone" json:"scheduledAt"`
	IsActuallyScheduled bool        `gorm:"column:isActuallyScheduled;default:false" json:"isActuallyScheduled"`
	PollExpiredAfter    *int64      `gorm:"column:pollExpiredAfter;type:bigint" json:"pollExpiredAfter"`

	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (NoteDraft) TableName() string { return "note_draft" }
