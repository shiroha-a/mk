package model

import (
	"time"

	"gorm.io/datatypes"
)

// AntennaSource represents the antenna note source filter (Misskey 互換)。
type AntennaSource string

const (
	// AntennaSourceHome captures notes from people the owner follows.
	AntennaSourceHome AntennaSource = "home"
	// AntennaSourceAll captures every public note.
	AntennaSourceAll AntennaSource = "all"
	// AntennaSourceUsers captures notes from a whitelist of usernames.
	AntennaSourceUsers AntennaSource = "users"
	// AntennaSourceList is a UserList-backed source. Phase 4.3 ではサポート
	// しないが enum 値だけ残す (将来 UserList 実装時に有効化)。
	AntennaSourceList AntennaSource = "list"
	// AntennaSourceUsersBlacklist captures notes from anyone except the listed
	// usernames.
	AntennaSourceUsersBlacklist AntennaSource = "users_blacklist"
)

// Antenna represents the `antenna` table.
type Antenna struct {
	ID                             string         `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	LastUsedAt                     time.Time      `gorm:"column:lastUsedAt;type:timestamp with time zone;not null" json:"lastUsedAt"`
	UserID                         string         `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	Name                           string         `gorm:"column:name;type:varchar(128);not null" json:"name"`
	Src                            AntennaSource  `gorm:"column:src;type:antenna_src_enum;not null" json:"src"`
	UserListID                     *string        `gorm:"column:userListId;type:varchar(32)" json:"userListId"`
	Users                          StringArray    `gorm:"column:users;type:varchar(1024)[];default:'{}'" json:"users"`
	Keywords                       datatypes.JSON `gorm:"column:keywords;type:jsonb;default:'[]'" json:"keywords"`
	ExcludeKeywords                datatypes.JSON `gorm:"column:excludeKeywords;type:jsonb;default:'[]'" json:"excludeKeywords"`
	CaseSensitive                  bool           `gorm:"column:caseSensitive;default:false" json:"caseSensitive"`
	ExcludeBots                    bool           `gorm:"column:excludeBots;default:false" json:"excludeBots"`
	WithReplies                    bool           `gorm:"column:withReplies;default:false" json:"withReplies"`
	WithFile                       bool           `gorm:"column:withFile;default:false" json:"withFile"`
	Expression                     *string        `gorm:"column:expression;type:varchar(2048)" json:"expression"`
	IsActive                       bool           `gorm:"column:isActive;default:true" json:"isActive"`
	LocalOnly                      bool           `gorm:"column:localOnly;default:false" json:"localOnly"`
	ExcludeNotesInSensitiveChannel bool           `gorm:"column:excludeNotesInSensitiveChannel;default:false" json:"excludeNotesInSensitiveChannel"`
}

func (Antenna) TableName() string { return "antenna" }
