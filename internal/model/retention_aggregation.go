package model

import (
	"time"

	"gorm.io/datatypes"
)

// RetentionAggregation represents the `retention_aggregation` table.
type RetentionAggregation struct {
	ID         string         `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	CreatedAt  time.Time      `gorm:"column:createdAt;type:timestamp with time zone;not null;default:now()" json:"createdAt"`
	UpdatedAt  time.Time      `gorm:"column:updatedAt;type:timestamp with time zone;not null;default:now()" json:"updatedAt"`
	UserIDs    StringArray    `gorm:"column:userIds;type:varchar(32)[];default:'{}'" json:"userIds"`
	Data       datatypes.JSON `gorm:"column:data;type:jsonb;default:'{}'" json:"data"`
	DateKey    string         `gorm:"column:dateKey;type:varchar(64);not null;uniqueIndex" json:"dateKey"`
	UsersCount int            `gorm:"column:usersCount;type:integer;not null;default:0" json:"usersCount"`
}

func (RetentionAggregation) TableName() string { return "retention_aggregation" }
