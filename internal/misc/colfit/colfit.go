// Package colfit holds the primitives for fitting a remote-supplied string into
// a PostgreSQL varchar column.
//
// 規則そのもの (本文は切る / URL は値ごと捨てる / 身元は document ごと拒否) は
// docs/divergence.md の「リモート由来の文字列を列に入れるときの規則」にあり、
// ここはその**数え方**だけを持つ。以前は federation と instance に同型の
// ヘルパーが散っていて、rune / byte の数え方と NUL の扱いが分かれる土壌に
// なっていた (#2726。**内訳は docs/divergence.md に一本化してある** — 数を
// 2 箇所に書くと片方だけ古くなる)。
//
// 呼び出し側は「その列固有の判断とログ」を持つ薄い名前付きヘルパーを維持し、
// 中身だけここへ委譲する。
package colfit

import "strings"

// Fits reports whether s can be stored in a varchar(max) column.
//
// **rune で数える。** PostgreSQL の varchar はコードポイント数で数えるので、
// byte 長で見ると非 ASCII を含む値を必要以上に落とす。NUL は長さに関わらず
// SQLSTATE 22021 で弾かれるので別に見る。
func Fits(s string, max int) bool {
	return !strings.ContainsRune(s, 0) && len([]rune(s)) <= max
}

// StripNUL removes NUL code points from s.
//
// JSON の NUL エスケープは正当な入力で、Go の decoder は実 NUL バイトを作る。
// PostgreSQL の text 系列はこれを受け付けず (SQLSTATE 22021)、jsonb も拒否する
// (22P05)。同じ書き込みに乗っている他の列まで巻き添えになるので落とす。
func StripNUL(s string) string {
	if !strings.ContainsRune(s, 0) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r == 0 {
			return -1
		}
		return r
	}, s)
}

// TruncateRunes clips s to at most max runes.
//
// byte 単位で切ると壊れた UTF-8 を書くので rune で数える。max <= 0 なら切らない。
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	if runes := []rune(s); len(runes) > max {
		return string(runes[:max])
	}
	return s
}

// Text prepares a remote-supplied **body** string for a column: NUL を落とし、
// max rune で切る。max <= 0 なら切らない (無制限の text 列用)。
//
// **URL / ID には使わない。** 途中で切った URL は別物で、取りに行っても無駄な
// うえ壊れた参照を保存することになる。そちらは Fits で判定して値ごと捨てる。
func Text(raw string, max int) string {
	return TruncateRunes(StripNUL(raw), max)
}
