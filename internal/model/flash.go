package model

import (
	"time"
)

// Flash represents the `flash` table. AI Script ベースのインタラクティブ
// コンテンツ (Misskey Play) で、サーバ側ではスクリプト本体・パーミッション
// 一覧などを保存するだけ。実行はクライアントの責任。
type Flash struct {
	ID          string      `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UpdatedAt   time.Time   `gorm:"column:updatedAt;type:timestamp with time zone;not null" json:"updatedAt"`
	Title       string      `gorm:"column:title;type:varchar(256);not null" json:"title"`
	Summary     string      `gorm:"column:summary;type:varchar(1024);default:''" json:"summary"`
	UserID      string      `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	Script      string      `gorm:"column:script;type:varchar(65536);not null" json:"script"`
	Permissions StringArray `gorm:"column:permissions;type:varchar(64)[];default:'{}'" json:"permissions"`
	LikedCount  int         `gorm:"column:likedCount;default:0" json:"likedCount"`
	Visibility  string      `gorm:"column:visibility;type:varchar(32);default:'public'" json:"visibility"`
}

func (Flash) TableName() string { return "flash" }

// FlashLike represents the `flash_like` table.
type FlashLike struct {
	ID      string `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UserID  string `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	FlashID string `gorm:"column:flashId;type:varchar(32);not null" json:"flashId"`
}

func (FlashLike) TableName() string { return "flash_like" }
