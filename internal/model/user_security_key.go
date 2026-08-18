package model

import (
	"time"
)

// UserSecurityKey represents the `user_security_key` table — registered
// WebAuthn / FIDO2 credentials for a user.
type UserSecurityKey struct {
	// ID is the variable-length credential ID returned by
	// navigator.credentials.create(). Stored as base64url-encoded text so
	// JSON round-trips work cleanly.
	ID                   string      `gorm:"column:id;type:varchar;primaryKey" json:"id"`
	UserID               string      `gorm:"column:userId;type:varchar(32);not null;index" json:"userId"`
	Name                 string      `gorm:"column:name;type:varchar(30);not null" json:"name"`
	PublicKey            string      `gorm:"column:publicKey;type:varchar;not null;index" json:"publicKey"`
	Counter              int64       `gorm:"column:counter;type:bigint;not null;default:0" json:"counter"`
	LastUsed             time.Time   `gorm:"column:lastUsed;type:timestamp with time zone;not null;default:now()" json:"lastUsed"`
	CredentialDeviceType *string     `gorm:"column:credentialDeviceType;type:varchar(32)" json:"credentialDeviceType"`
	CredentialBackedUp   *bool       `gorm:"column:credentialBackedUp" json:"credentialBackedUp"`
	Transports           StringArray `gorm:"column:transports;type:varchar(32)[]" json:"transports"`

	// Relations
	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// TableName returns the schema table name.
func (UserSecurityKey) TableName() string { return "user_security_key" }
