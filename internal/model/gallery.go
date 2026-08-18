package model

import (
	"time"
)

// GalleryPost represents the `gallery_post` table.
type GalleryPost struct {
	ID          string      `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UpdatedAt   time.Time   `gorm:"column:updatedAt;not null" json:"updatedAt"`
	Title       string      `gorm:"column:title;type:varchar(256);not null" json:"title"`
	Description *string     `gorm:"column:description;type:varchar(2048)" json:"description"`
	UserID      string      `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	FileIDs     StringArray `gorm:"column:fileIds;type:varchar(32)[];default:'{}'" json:"fileIds"`
	IsSensitive bool        `gorm:"column:isSensitive;default:false" json:"isSensitive"`
	LikedCount  int         `gorm:"column:likedCount;default:0" json:"likedCount"`
	Tags        StringArray `gorm:"column:tags;type:varchar(128)[];default:'{}'" json:"tags"`

	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (GalleryPost) TableName() string { return "gallery_post" }

// GalleryLike represents the `gallery_like` table.
type GalleryLike struct {
	ID     string `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UserID string `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	PostID string `gorm:"column:postId;type:varchar(32);not null" json:"postId"`
}

func (GalleryLike) TableName() string { return "gallery_like" }
