package repository

import (
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
	return errors.Is(err, ErrNotFound)
}
