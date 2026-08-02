package model

import "time"

// Clip represents the `clip` table. ユーザーが note を集めるブックマーク /
// コレクション (Misskey 互換)。Local 限定機能で ActivityPub 連携は持たない。
//
// notesCount 列は持たない。upstream の Clip entity に存在せず (TS 製 DB では
// 生えない)、件数は clip_note を数えて算出する (#2243)。
type Clip struct {
	ID            string     `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	LastClippedAt *time.Time `gorm:"column:lastClippedAt;type:timestamp with time zone" json:"lastClippedAt"`
	UserID        string     `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	Name          string     `gorm:"column:name;type:varchar(128);not null" json:"name"`
	IsPublic      bool       `gorm:"column:isPublic;default:false" json:"isPublic"`
	Description   *string    `gorm:"column:description;type:varchar(2048)" json:"description"`
}

func (Clip) TableName() string { return "clip" }

// ClipNote represents the `clip_note` table. clip に追加された note の中間表。
type ClipNote struct {
	ID     string `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	NoteID string `gorm:"column:noteId;type:varchar(32);not null" json:"noteId"`
	ClipID string `gorm:"column:clipId;type:varchar(32);not null" json:"clipId"`
}

func (ClipNote) TableName() string { return "clip_note" }
