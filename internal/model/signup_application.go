package model

import (
	"time"

	"gorm.io/datatypes"
)

// SignupApplication status values.
//
// 状態遷移は core/signupapplication.Service が持つ。
//
//	無し       -> pending                (申請)
//	pending    -> approved / rejected    (審査)
//	approved   -> completed              (登録)
//	pending    -> expired                (放置)
//	approved   -> expired                (放置)
//
// rejected / expired / completed は終端。再申請は別レコードになる。
const (
	SignupApplicationPending   = "pending"
	SignupApplicationApproved  = "approved"
	SignupApplicationRejected  = "rejected"
	SignupApplicationExpired   = "expired"
	SignupApplicationCompleted = "completed"
)

// SignupApplication represents the `signup_application` table (#2555 / #2569).
//
// One row is one request to join the instance. 承認待ちを `user` 行として持たない
// ための箱で、承認後の登録時に初めて `user` が作られる。
//
// 本人性は**申請時に発行するクレームコード**が担保する。外部サーバーには一切
// 依存しない (#2568)。
type SignupApplication struct {
	ID string `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`

	// ClaimCodeHash is the SHA-256 (hex) of the code handed to the applicant.
	//
	// **平文では持たない。** コードは状態確認と登録の両方の入口なので、平文で
	// 保存すると DB が漏れた時点で全申請が乗っ取れる。
	ClaimCodeHash string `gorm:"column:claimCodeHash;type:varchar(64);not null;uniqueIndex" json:"-"`

	Status string `gorm:"column:status;type:varchar(16);not null;default:'pending'" json:"status"`

	// Answers holds the submitted answers with the label each was asked under
	// (#2570). **ラベルを同梱するのが要点** — 定義を後から変えても、既存申請が
	// どの設問への答えだったか分かる。
	Answers datatypes.JSON `gorm:"column:answers;type:jsonb;not null;default:'[]'" json:"answers"`

	// TicketID is the registration_ticket minted on registration. コードは
	// 利用者に渡さず、登録時にサーバー内部で消費する。
	TicketID *string `gorm:"column:ticketId;type:varchar(32)" json:"ticketId"`

	CreatedAt time.Time `gorm:"column:createdAt;type:timestamp with time zone;not null" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updatedAt;type:timestamp with time zone;not null" json:"updatedAt"`

	// ExpiresAt は申請の有効期限。**短くしないこと** — 申請者は自分でコードを
	// 持って戻ってくるので、気づくまでに間が空く。
	ExpiresAt time.Time `gorm:"column:expiresAt;type:timestamp with time zone;not null" json:"expiresAt"`

	// 監査用。誰がいつ審査したか、最終的に誰が登録したか。
	ProcessedByID *string    `gorm:"column:processedById;type:varchar(32)" json:"processedById"`
	ProcessedAt   *time.Time `gorm:"column:processedAt;type:timestamp with time zone" json:"processedAt"`
	UsedByID      *string    `gorm:"column:usedById;type:varchar(32)" json:"usedById"`
}

func (SignupApplication) TableName() string { return "signup_application" }

// IsLive reports whether the application is still actionable.
func (a *SignupApplication) IsLive() bool {
	return a.Status == SignupApplicationPending || a.Status == SignupApplicationApproved
}
