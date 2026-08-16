package model

import "time"

// SignupApplication status values.
//
// 状態遷移は core/signupapplication.Service が持つ。
//
//	無し       -> pending                (申請)
//	pending    -> approved / rejected    (審査)
//	approved   -> completed / expired    (登録 / 放置)
//	pending    -> expired                (放置)
//	rejected / expired / completed       (終端。再申請は別レコード)
const (
	SignupApplicationPending   = "pending"
	SignupApplicationApproved  = "approved"
	SignupApplicationRejected  = "rejected"
	SignupApplicationExpired   = "expired"
	SignupApplicationCompleted = "completed"
)

// SignupApplication represents the `signup_application` table (#2555).
//
// One row is one request to join the instance, keyed by a contact account on
// another Misskey server. 承認待ちを `user` 行として持たないための箱で、
// 承認後の登録時に初めて `user` が作られる。
type SignupApplication struct {
	ID string `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`

	// 連絡先。**一致判定に使うのは ContactHost と ContactRemoteID だけ。**
	// ContactUsername は表示専用で、相手サーバーでの改名で変わりうる。
	ContactHost     string `gorm:"column:contactHost;type:varchar(128);not null" json:"contactHost"`
	ContactRemoteID string `gorm:"column:contactRemoteId;type:varchar(32);not null" json:"contactRemoteId"`
	ContactUsername string `gorm:"column:contactUsername;type:varchar(128);not null" json:"contactUsername"`

	Status string  `gorm:"column:status;type:varchar(16);not null;default:'pending'" json:"status"`
	Reason *string `gorm:"column:reason;type:varchar(2048)" json:"reason"`

	// TicketID is the registration_ticket minted on approval. コードは利用者に
	// 渡さず、登録時にサーバー内部で消費する。
	TicketID *string `gorm:"column:ticketId;type:varchar(32)" json:"ticketId"`

	CreatedAt time.Time `gorm:"column:createdAt;type:timestamp with time zone;not null" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updatedAt;type:timestamp with time zone;not null" json:"updatedAt"`
	ExpiresAt time.Time `gorm:"column:expiresAt;type:timestamp with time zone;not null" json:"expiresAt"`

	ProcessedByID *string    `gorm:"column:processedById;type:varchar(32)" json:"processedById"`
	ProcessedAt   *time.Time `gorm:"column:processedAt;type:timestamp with time zone" json:"processedAt"`
	UsedByID      *string    `gorm:"column:usedById;type:varchar(32)" json:"usedById"`
}

func (SignupApplication) TableName() string { return "signup_application" }

// IsLive reports whether the application still occupies its contact, i.e. a
// second application from the same contact would collide with it.
//
// 部分一意インデックス (`IDX_signup_application_live_contact`) の条件と
// **同じ集合を返すこと**。ずれると、DB が弾く組み合わせをサービス層が通そうとして
// 一意制約違反が利用者に露出する。
func (a *SignupApplication) IsLive() bool {
	return a.Status == SignupApplicationPending || a.Status == SignupApplicationApproved
}
