package model

// AuthSession represents the `auth_session` table for MiAuth flow.
//
// createdAt 列は持たない。理由は model.App と同じ (#2243)。
type AuthSession struct {
	ID     string  `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	Token  string  `gorm:"column:token;type:varchar(128);not null" json:"token"`
	UserID *string `gorm:"column:userId;type:varchar(32)" json:"userId"`
	AppID  string  `gorm:"column:appId;type:varchar(32);not null" json:"appId"`

	// Relations
	App  *App  `gorm:"foreignKey:AppID" json:"app,omitempty"`
	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (AuthSession) TableName() string { return "auth_session" }
