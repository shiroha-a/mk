package model

import "time"

// EmojiApplicationQuotaReset represents the `emoji_application_quota_reset`
// table (#2962).
//
// One row is one manual reset of a user's rolling emoji application quota.
//
// **申請の行は触らない。** 枠を空けるために履歴を消すと、過去の判断 (#2960 が
// 審査の材料として出している却下理由・同じ画像かどうか) も同時に消える。
// 代わりに「いつ戻したか」を持ち、期間内の件数を
// `max(期間の開始日時, 最後のリセット日時)` 以降で数える。
//
// **1 行 1 操作で積む。** 上書きにすると誰がいつ何回戻したかが残らない。
type EmojiApplicationQuotaReset struct {
	ID     string `gorm:"column:id;primaryKey" json:"id"`
	UserID string `gorm:"column:userId" json:"userId"`
	// ResetByID は操作したモデレーター。**申請者向けには出さない** —
	// 誰が審査したかを出さない #2934 の判断と揃える。
	ResetByID string `gorm:"column:resetById" json:"resetById"`
	// Reason は必須。監査ログに残る唯一の文脈。
	Reason string `gorm:"column:reason" json:"reason"`
	// CreatedAt がリセットの時刻そのもの。**`resetAt` を別に持たない** —
	// 2 つあると片方だけ更新されたときに食い違う。
	CreatedAt time.Time `gorm:"column:createdAt" json:"createdAt"`
}

// TableName maps to the Misskey-style table name.
func (EmojiApplicationQuotaReset) TableName() string {
	return "emoji_application_quota_reset"
}
