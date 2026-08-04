package model

import "time"

// RelayObservedUser records that a remote user was first observed through a
// subscribed relay (#2340).
//
// `user` に列を足さず別テーブルにしてある。TS は未知の列も無視するので列追加でも
// drop-in の復路は壊れないが、別テーブルなら TS 側から一切見えないため更に安全。
// `user` は連合・認証・API のあらゆる経路が触るホットテーブルでもある。
//
// 用途は孤児掃除の対象をリレー由来に限定すること。印が無いと、リレー購読前から
// 居る行やプロフィール閲覧・スレッド遡りで解決された行まで巻き込む。
type RelayObservedUser struct {
	UserID     string    `gorm:"column:userId;type:varchar(32);primaryKey" json:"userId"`
	ObservedAt time.Time `gorm:"column:observedAt;not null" json:"observedAt"`
}

func (RelayObservedUser) TableName() string { return "relay_observed_user" }
