package model

import "github.com/lib/pq"

// App represents the `app` table for MiAuth applications.
//
// createdAt 列は持たない。upstream が 1697420555911-deleteCreatedAt で DROP
// 済みで TS 製 DB には存在せず、mk-go も読み取っていなかった (#2243)。
type App struct {
	ID          string         `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UserID      *string        `gorm:"column:userId;type:varchar(32)" json:"userId"`
	Secret      string         `gorm:"column:secret;type:varchar(64);not null" json:"secret"`
	Name        string         `gorm:"column:name;type:varchar(128);not null" json:"name"`
	Description string         `gorm:"column:description;type:varchar(512);not null" json:"description"`
	Permission  pq.StringArray `gorm:"column:permission;type:varchar(64)[];not null" json:"permission"`
	CallbackURL *string        `gorm:"column:callbackUrl;type:varchar(512)" json:"callbackUrl"`
}

func (App) TableName() string { return "app" }
