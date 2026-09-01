package repository

import (
	"database/sql"
	"errors"

	"gorm.io/gorm"
)

// ErrNotFound is returned when a lookup finds no row.
//
// GORM の `ErrRecordNotFound` の別名。**呼び出し側が gorm を import せずに
// 種別を判定できる**ようにするための公開面で、値そのものは同じなので
// `errors.Is(err, gorm.ErrRecordNotFound)` と互換。
var ErrNotFound = gorm.ErrRecordNotFound

// IsNotFound reports whether err means "the row does not exist".
//
// **これで判定しないと DB 障害が 4xx に化ける。** repository は GORM の error を
// そのまま返すので、`err != nil` をまとめて not-found 扱いにすると接続断や
// syntax error まで「そんなノートは無い」になる。クライアントからは区別できず、
// 監視でも 5xx が立たない (#2792)。
//
// upstream は `.findOneBy` の結果が `null` かどうかで判定するので、DB 障害は
// 例外として 500 に化ける。こちらもそれに合わせる。
func IsNotFound(err error) bool {
	// **`sql.ErrNoRows` も見る。** 現状 production code の repository はすべて
	// GORM 経由だが、どこかが `database/sql` / pgx 直叩きに変わったとき
	// `IsNotFound` が黙って false を返すと、**本物の not-found が 500 になる**。
	// 4xx → 500 の向きに倒したぶん、この誤りは表に出やすい。
	return errors.Is(err, ErrNotFound) || errors.Is(err, sql.ErrNoRows)
}
