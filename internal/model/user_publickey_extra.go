package model

// UserPublickeyExtra represents the `user_publickey_extra` table.
//
// mk-go 独自の add-only テーブルで、リモート user の追加公開鍵 (Ed25519 等) を
// keyId 単位で保存する。actor JSON の assertionMethod[] (FEP-521a Multikey) から
// parse した鍵を格納し、HTTP Signature verify と outbound deliver 時の capability
// 判定に使う (#1067 / #1068)。
//
// 既存 user_publickey (RSA, userId が PK で 1 行のみ) と異なり、本テーブルは
// (userId, keyId) 複合 PK で 1 actor が複数鍵を持つことを許容する。
type UserPublickeyExtra struct {
	UserID string `gorm:"column:userId;type:varchar(32);primaryKey" json:"userId"`
	KeyID  string `gorm:"column:keyId;type:varchar(256);primaryKey" json:"keyId"`
	KeyPEM string `gorm:"column:keyPem;type:varchar(4096);not null" json:"keyPem"`
	Alg    string `gorm:"column:alg;type:varchar(32);not null" json:"alg"`

	// Relations
	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (UserPublickeyExtra) TableName() string { return "user_publickey_extra" }

// AlgEd25519 is the canonical "alg" column value for Ed25519 entries.
const AlgEd25519 = "ed25519"
