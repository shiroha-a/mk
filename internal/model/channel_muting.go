package model

import "time"

// ChannelMuting represents the `channel_muting` table.
type ChannelMuting struct {
	ID        string `gorm:"column:id;type:varchar(32);primaryKey"`
	UserID    string `gorm:"column:userId;type:varchar(32);not null"`
	ChannelID string `gorm:"column:channelId;type:varchar(32);not null"`
	// ExpiresAt is when the mute lapses (nil = indefinite). Timed channel
	// mutes mirror upstream channel_muting.expiresAt (#1603).
	ExpiresAt *time.Time `gorm:"column:expiresAt"`
}

func (ChannelMuting) TableName() string { return "channel_muting" }
