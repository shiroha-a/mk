package model

import (
	"time"
)

// Webhook represents the `webhook` table.
type Webhook struct {
	ID           string      `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UserID       string      `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	Name         string      `gorm:"column:name;type:varchar(128);not null" json:"name"`
	On           StringArray `gorm:"column:on;type:varchar(128)[];default:'{}'" json:"on"`
	URL          string      `gorm:"column:url;type:varchar(1024);not null" json:"url"`
	Secret       string      `gorm:"column:secret;type:varchar(1024);not null;default:''" json:"secret"`
	Active       bool        `gorm:"column:active;default:true" json:"active"`
	LatestSentAt *time.Time  `gorm:"column:latestSentAt;type:timestamp with time zone" json:"latestSentAt"`
	LatestStatus *int        `gorm:"column:latestStatus;type:integer" json:"latestStatus"`

	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (Webhook) TableName() string { return "webhook" }
