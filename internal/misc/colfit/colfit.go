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

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Fits reports whether s can be stored in a varchar(max) column.
//
// **rune で数える。** PostgreSQL の varchar はコードポイント数で数えるので、
// byte 長で見ると非 ASCII を含む値を必要以上に落とす。NUL と不正な UTF-8 は
// 長さに関わらず SQLSTATE 22021 で弾かれるので `Storable` で別に見る。
func Fits(s string, max int) bool {
	return Storable(s) && len([]rune(s)) <= max
}

// Storable reports whether s can be stored in a PostgreSQL text column **at
// all**, ignoring how wide the column is.
//
// 幅を知らない場所でも同じ判定を使えるようにするための分離 (#3025)。カーソルや
// id のように「列に入るかは分かっているが幅は関係ない」値は、`Fits` に適当な
// max を渡すと**比較できるだけの値まで落とす**ことになる (`id < ?` は長さに
// 関わらず成立する)。
//
// **NUL を含む値は SELECT の bind parameter にも載せられない。** 手元の simple
// protocol では SQLSTATE 08P01 (protocol_violation)、本番の pgx extended
// protocol では 22021 (character_not_in_repertoire) になる (#2726 / #3025)。
//
// **不正な UTF-8 も同じく落ちる。** UTF8 エンコーディングのデータベースは
// 不正なバイト列を受け付けず、比較の右辺に置くだけで SQLSTATE 22021
// (`invalid byte sequence for encoding "UTF8"`) でクエリごと落とす。実測した
// 3 形 — 孤立した継続バイト (`0x80`)、UTF-8 で符号化した孤立サロゲート
// (`0xED 0xA0 0x80`)、`0xFF` — がいずれも同じ SQLSTATE を返す。
//
// **NUL だけを見ていたので素通りしていた。** 両方を見ていたのは
// `internal/core/mediaproxy` の 1 箇所だけで、`id.NormalizeCursor` も
// `internal/repository` の guard 群もここを通るため、`%80` を 1 つ混ぜるだけで
// 同じ 500 を起こせた。**JSON body からは来ない** (Go の decoder が不正な
// UTF-8 も孤立サロゲートのエスケープも U+FFFD に置き換える) が、クエリ
// パラメータとパス要素は percent-decode した生のバイト列がそのまま届く。
func Storable(s string) bool {
	return !strings.ContainsRune(s, 0) && utf8.ValidString(s)
}

// ToStorable coerces s into a value a PostgreSQL text column accepts: NUL を
// 落とし、不正な UTF-8 を U+FFFD へ置き換える。
//
// JSON の NUL エスケープは正当な入力で、Go の decoder は実 NUL バイトを作る。
// PostgreSQL の text 系列はこれを受け付けず (SQLSTATE 22021)、jsonb も拒否する
// (22P05)。同じ書き込みに乗っている他の列まで巻き添えになるので落とす。
//
// **UTF-8 の矯正を先に行う。** 後にすると、NUL を落とした結果できた不正な
// バイト列が残りうる。置き換えであって削除ではないのは、**`Text` が rune で
// 数えて切る**ため — 黙って詰めると「何文字目で切れたか」が入力と対応しなく
// なる。
//
// 値ごと捨てる側の判定は `Storable` / `Fits`。どちらを使うかは
// docs/divergence.md の「リモート由来の文字列を列に入れるときの規則」にある
// (本文は切る / URL は値ごと捨てる)。
func ToStorable(s string) string {
	if Storable(s) {
		return s
	}
	valid := strings.ToValidUTF8(s, "\uFFFD")
	return strings.Map(func(r rune) rune {
		if r == 0 {
			return -1
		}
		return r
	}, valid)
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

// Text prepares a remote-supplied **body** string for a column: NUL を落として
// 不正な UTF-8 を矯正し、max rune で切る。max <= 0 なら切らない (無制限の
// text 列用)。
//
// **URL / ID には使わない。** 途中で切った URL は別物で、取りに行っても無駄な
// うえ壊れた参照を保存することになる。そちらは Fits で判定して値ごと捨てる。
func Text(raw string, max int) string {
	return TruncateRunes(ToStorable(raw), max)
}

// JSONStorable reports whether a JSON document can be stored in a `jsonb`
// column.
//
// **PostgreSQL の jsonb は NUL を受け付けない。** テキストとしての NUL エスケープ
// (JSON の `\u` 表記) も拒否され、SQLSTATE 22P05
// (`unsupported Unicode escape sequence`) でクエリごと落ちる。text 列の
// 22021 とは番号が違うだけで、症状は同じ 500。
//
// **生バイト列を検索しない。** wire 上の NUL はエスケープされた 6 文字で
// 届くので、実 NUL バイトを探しても見つからない。逆に文字列の中に現れる
// 「バックスラッシュ自体がエスケープされた」形は**ただの 6 文字**なので、
// 文字列検索では誤検出する。値をほどいて歩くのが唯一正しい。
//
// **キーも見る。** jsonb はオブジェクトのキーにも同じ制約を掛ける。
//
// **構文エラーは通す。** ここは「列に入るか」だけを見る述語で、パースの
// 可否は呼び出し側の既存の扱いに任せる (`json.RawMessage` は親の Unmarshal が
// 構文を検査済み)。
func JSONStorable(raw []byte) bool {
	if len(raw) == 0 {
		return true
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return true
	}
	return jsonValueStorable(v)
}

// jsonValueStorable walks a decoded JSON value looking for an unstorable string.
func jsonValueStorable(v any) bool {
	switch t := v.(type) {
	case string:
		return Storable(t)
	case map[string]any:
		for k, item := range t {
			if !Storable(k) || !jsonValueStorable(item) {
				return false
			}
		}
	case []any:
		for _, item := range t {
			if !jsonValueStorable(item) {
				return false
			}
		}
	}
	return true
}
