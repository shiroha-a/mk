package model

import "gorm.io/datatypes"

// AbuseUserReport represents the `abuse_user_report` table.
type AbuseUserReport struct {
	ID             string  `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	TargetUserID   string  `gorm:"column:targetUserId;type:varchar(32);not null" json:"targetUserId"`
	ReporterID     string  `gorm:"column:reporterId;type:varchar(32);not null" json:"reporterId"`
	AssigneeID     *string `gorm:"column:assigneeId;type:varchar(32)" json:"assigneeId"`
	Resolved       bool    `gorm:"column:resolved;default:false" json:"resolved"`
	Forwarded      bool    `gorm:"column:forwarded;default:false" json:"forwarded"`
	Comment        string  `gorm:"column:comment;type:varchar(2048);default:''" json:"comment"`
	ModerationNote string  `gorm:"column:moderationNote;type:varchar(8192);default:''" json:"moderationNote"`
	ResolvedAs     *string `gorm:"column:resolvedAs;type:varchar(16)" json:"resolvedAs"`
	TargetUserHost *string `gorm:"column:targetUserHost;type:varchar(128)" json:"targetUserHost"`
	ReporterHost   *string `gorm:"column:reporterHost;type:varchar(128)" json:"reporterHost"`

	// Relations
	TargetUser *User `gorm:"foreignKey:TargetUserID" json:"targetUser,omitempty"`
	Reporter   *User `gorm:"foreignKey:ReporterID" json:"reporter,omitempty"`
	Assignee   *User `gorm:"foreignKey:AssigneeID" json:"assignee,omitempty"`
}

func (AbuseUserReport) TableName() string { return "abuse_user_report" }

// ModerationLog represents the `moderation_log` table.
type ModerationLog struct {
	ID     string         `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UserID string         `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	Type   string         `gorm:"column:type;type:varchar(128);not null" json:"type"`
	Info   datatypes.JSON `gorm:"column:info;type:jsonb;default:'{}'" json:"info"`

	// Relations
	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (ModerationLog) TableName() string { return "moderation_log" }

// ModerationLogFilter parameterises ModerationLogRepository.List, mirroring
// upstream admin/show-moderation-logs paramDef (#1539). SinceID/UntilID are the
// id-cursor (sinceDate/untilDate are normalized to ids by the handler); Type /
// UserID narrow by log type / actor; Search is an info::text ILIKE match.
type ModerationLogFilter struct {
	Limit   int
	SinceID string
	UntilID string
	Type    string
	UserID  string
	Search  string
}
