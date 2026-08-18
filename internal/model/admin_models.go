package model

import (
	"time"
)

// Ad represents the `ad` table.
type Ad struct {
	ID          string    `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	ExpiresAt   time.Time `gorm:"column:expiresAt;type:timestamp with time zone;not null" json:"expiresAt"`
	StartsAt    time.Time `gorm:"column:startsAt;type:timestamp with time zone;not null;default:now()" json:"startsAt"`
	Place       string    `gorm:"column:place;type:varchar(32);not null" json:"place"`
	Priority    string    `gorm:"column:priority;type:varchar(32);not null;default:'middle'" json:"priority"`
	Ratio       int       `gorm:"column:ratio;type:integer;not null;default:1" json:"ratio"`
	URL         string    `gorm:"column:url;type:varchar(1024);not null" json:"url"`
	ImageURL    string    `gorm:"column:imageUrl;type:varchar(1024);not null" json:"imageUrl"`
	Memo        string    `gorm:"column:memo;type:varchar(8192);not null;default:''" json:"memo"`
	DayOfWeek   int       `gorm:"column:dayOfWeek;type:integer;not null;default:0" json:"dayOfWeek"`
	IsSensitive bool      `gorm:"column:isSensitive;default:false" json:"isSensitive"`
}

func (Ad) TableName() string { return "ad" }

// RegistrationTicket represents the `registration_ticket` table.
type RegistrationTicket struct {
	ID          string     `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	Code        string     `gorm:"column:code;type:varchar(64);not null;uniqueIndex" json:"code"`
	ExpiresAt   *time.Time `gorm:"column:expiresAt;type:timestamp with time zone" json:"expiresAt"`
	CreatedByID *string    `gorm:"column:createdById;type:varchar(32)" json:"createdById"`
	UsedByID    *string    `gorm:"column:usedById;type:varchar(32)" json:"usedById"`
	UsedAt      *time.Time `gorm:"column:usedAt;type:timestamp with time zone" json:"usedAt"`
	PendingID   *string    `gorm:"column:pendingUserId;type:varchar(32)" json:"pendingUserId"`
}

func (RegistrationTicket) TableName() string { return "registration_ticket" }

// Relay represents the `relay` table.
type Relay struct {
	ID     string `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	Inbox  string `gorm:"column:inbox;type:varchar(512);not null;uniqueIndex" json:"inbox"`
	Status string `gorm:"column:status;type:varchar(32);not null;default:'requesting'" json:"status"`
}

func (Relay) TableName() string { return "relay" }

// AvatarDecoration represents the `avatar_decoration` table.
type AvatarDecoration struct {
	ID          string      `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UpdatedAt   *time.Time  `gorm:"column:updatedAt;type:timestamp with time zone" json:"updatedAt"`
	URL         string      `gorm:"column:url;type:varchar(1024);not null" json:"url"`
	Name        string      `gorm:"column:name;type:varchar(256);not null" json:"name"`
	Description string      `gorm:"column:description;type:varchar(2048);not null;default:''" json:"description"`
	RoleIDs     StringArray `gorm:"column:roleIdsThatCanBeUsedThisDecoration;type:varchar(128)[];default:'{}'" json:"roleIdsThatCanBeUsedThisDecoration"`
	// Category は upstream Misskey #17034 (= 2026.5.0) で導入された分類用 field。
	// nullable で、管理画面の avatar decoration グルーピングに使う。
	Category *string `gorm:"column:category;type:varchar(128)" json:"category"`
}

func (AvatarDecoration) TableName() string { return "avatar_decoration" }

// SystemWebhook represents the `system_webhook` table.
type SystemWebhook struct {
	ID           string      `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	IsActive     bool        `gorm:"column:isActive;default:true" json:"isActive"`
	UpdatedAt    time.Time   `gorm:"column:updatedAt;type:timestamp with time zone;default:now()" json:"updatedAt"`
	LatestSentAt *time.Time  `gorm:"column:latestSentAt;type:timestamp with time zone" json:"latestSentAt"`
	LatestStatus *int        `gorm:"column:latestStatus;type:integer" json:"latestStatus"`
	Name         string      `gorm:"column:name;type:varchar(255);not null" json:"name"`
	On           StringArray `gorm:"column:on;type:varchar(128)[];default:'{}'" json:"on"`
	URL          string      `gorm:"column:url;type:varchar(1024);not null" json:"url"`
	Secret       string      `gorm:"column:secret;type:varchar(1024);not null;default:''" json:"secret"`
}

func (SystemWebhook) TableName() string { return "system_webhook" }

// AbuseReportNotificationRecipient represents a notification recipient for abuse reports.
type AbuseReportNotificationRecipient struct {
	ID        string    `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	IsActive  bool      `gorm:"column:isActive;default:true" json:"isActive"`
	UpdatedAt time.Time `gorm:"column:updatedAt;type:timestamp with time zone;default:now()" json:"updatedAt"`
	Name      string    `gorm:"column:name;type:varchar(255);not null" json:"name"`
	Method    string    `gorm:"column:method;type:varchar(64);not null;default:'email'" json:"method"`
	// golden の userId? / systemWebhookId? は optional (非 null)。upstream packer は
	// method に対応しない側を omit するため、nil ポインタは null ではなく省略する
	// (omitempty)。method=email では userId のみ、webhook では systemWebhookId のみ
	// が出る (#1294)。
	UserID          *string `gorm:"column:userId;type:varchar(32)" json:"userId,omitempty"`
	SystemWebhookID *string `gorm:"column:systemWebhookId;type:varchar(32)" json:"systemWebhookId,omitempty"`
}

func (AbuseReportNotificationRecipient) TableName() string {
	return "abuse_report_notification_recipient"
}
