package model

// UserKeypairExtra represents the `user_keypair_extra` table.
//
// mk-go 独自の add-only テーブルで、local user の Ed25519 鍵ペアを保存する。
// 既存 user_keypair (RSA) は upstream Misskey TS との drop-in 互換のため untouched
// とし、Ed25519 用は本テーブルに分離して持つ (#1067 / #1068)。
type UserKeypairExtra struct {
	UserID            string `gorm:"column:userId;type:varchar(32);primaryKey" json:"userId"`
	Ed25519PublicKey  string `gorm:"column:ed25519PublicKey;type:varchar(256);not null" json:"ed25519PublicKey"`
	Ed25519PrivateKey string `gorm:"column:ed25519PrivateKey;type:varchar(256);not null" json:"-"`

	// Relations
	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (UserKeypairExtra) TableName() string { return "user_keypair_extra" }
