package model

import "time"

// IPLookupLog records one IP lookup performed by a moderator (#3106).
//
// **`moderation_log` とは別のテーブル。** あちらは保持期間を持たず永久に残るのに、
// ここに書くのは照会に使った IP そのもので、IP とアカウントの対応と同じだけ機密性が
// ある。専用テーブルにして日次の掃除で刈る。
type IPLookupLog struct {
	ID     string `gorm:"column:id;primaryKey;type:varchar(32)" json:"id"`
	UserID string `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	// Kind は `IPLookupKindIP` / `IPLookupKindRelated` / `IPLookupKindUserIPs` /
	// `IPLookupKindSignins`。
	Kind string `gorm:"column:kind;type:varchar(32);not null" json:"kind"`
	// IP は照会に使った正規形。利用者起点の照会では空。
	IP string `gorm:"column:ip;type:varchar(128);not null" json:"ip"`
	// TargetUserID は利用者起点の照会で対象にした利用者。IP 起点では空。
	TargetUserID string `gorm:"column:targetUserId;type:varchar(32);not null" json:"targetUserId"`
	SinceDays    int    `gorm:"column:sinceDays;not null" json:"sinceDays"`
	// ResultCount は返した候補の件数。**結果そのものは残さない** — 残すと、この表が
	// 第 2 の「IP とアカウントの対応」になる (#3106)。
	ResultCount int       `gorm:"column:resultCount;not null" json:"resultCount"`
	CreatedAt   time.Time `gorm:"column:createdAt;not null" json:"createdAt"`
}

func (IPLookupLog) TableName() string { return "ip_lookup_log" }

// Kinds recorded in IPLookupLog.Kind.
const (
	// IPLookupKindIP is `admin/ip/accounts` (#3104): an IP was given directly.
	IPLookupKindIP = "ip"
	// IPLookupKindRelated is `admin/ip/related-accounts` (#3105): a user was given.
	IPLookupKindRelated = "relatedAccounts"
	// IPLookupKindSignins is `admin/show-user` returning signin IPs (#3114).
	//
	// **upstream は policy を見ずに全件返す。** mk-go は `canSearchIpHistory`
	// を持つ相手にだけ返し、返したときだけ記録する (伏せた応答は開示が
	// 起きていないので記録しない)。
	IPLookupKindSignins = "signins"
	// IPLookupKindUserIPs is upstream's `admin/get-user-ips`.
	//
	// **upstream の口も監査する。** 返すのは同じ「利用者 ↔ IP の対応」なので、
	// ここを記録しないと**監査を迂回して IP を引ける経路**が 1 本残る (#3106)。
	// あちらは窓を取らず最新 30 件を返すので `SinceDays` は 0 になる。
	IPLookupKindUserIPs = "userIps"
)
