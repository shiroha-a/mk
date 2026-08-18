package model

import (
	"time"
)

// Poll represents the `poll` table.
type Poll struct {
	NoteID         string         `gorm:"column:noteId;type:varchar(32);primaryKey" json:"noteId"`
	ExpiresAt      *time.Time     `gorm:"column:expiresAt;type:timestamp with time zone" json:"expiresAt"`
	Multiple       bool           `gorm:"column:multiple;not null" json:"multiple"`
	Choices        StringArray    `gorm:"column:choices;type:varchar(256)[];default:'{}'" json:"choices"`
	Votes          Int64Array     `gorm:"column:votes;type:integer[]" json:"votes"`
	NoteVisibility NoteVisibility `gorm:"column:noteVisibility;type:note_visibility_enum" json:"noteVisibility"`
	// Denormalized fields
	UserID    string  `gorm:"column:userId;type:varchar(32)" json:"userId"`
	UserHost  *string `gorm:"column:userHost;type:varchar(128)" json:"userHost"`
	ChannelID *string `gorm:"column:channelId;type:varchar(32)" json:"channelId"`
	// NotifiedAt は expiresAt 経過後の pollEnded 通知が著者 + 投票者に発火
	// 済みかを記録する (#690)。NULL = まだ送ってない、NOT NULL = 送信済み。
	// periodic ticker (core/poll/expiry_worker) が WHERE expiresAt < NOW()
	// AND notifiedAt IS NULL で scan して二重通知を防ぐ。
	NotifiedAt *time.Time `gorm:"column:notifiedAt;type:timestamp with time zone" json:"notifiedAt,omitempty"`

	// Relations
	Note *Note `gorm:"foreignKey:NoteID" json:"note,omitempty"`
}

func (Poll) TableName() string { return "poll" }
