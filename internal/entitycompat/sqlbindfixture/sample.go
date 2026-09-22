// Package sqlbindfixture holds artificial format strings used to pin the
// scanner in internal/entitycompat.
//
// **本番コードに違反の形が 1 つも無いので、人工ソースが要る。** 検出対象が 0 件の
// 状態では、走査が壊れても「違反なし」と区別が付かない (実測: 書式の収集を潰しても
// production だけを見る検査は緑のままだった)。
//
// **`testdata/` に置かない。** あちらは Go ツールチェーンが無視するので `go vet` の
// 書式検査も型検査も通らず、動詞と引数の対応が崩れた期待値を書いても気付けない。
//
// ここの関数はどこからも呼ばない。ゲートがソースとして読むためだけに在る。
package sqlbindfixture

import "fmt"

// UnsafeQuotedValue splices a value into a quoted string literal instead of
// binding it. The gate must flag this shape.
func UnsafeQuotedValue(v string) string {
	return fmt.Sprintf(`SELECT * FROM "t" WHERE "c" = '%s'`, v)
}

// UnsafeConcatenatedFormat splices a value into a quoted literal in a format
// that is split across lines. The gate must flag this shape too.
//
// **書式を `+` で折り返すのは長い SQL では普通の書き方**で、第 1 引数の
// 文字列リテラルだけを見る形だと**その書式が丸ごと検査対象から消える** (実測)。
// 畳んでから判定していることをここで固定する。**畳めるのは定数だけ**なので、
// 書式の途中に変数を連結した形は射程外 (ゲートの doc コメントに明記してある)。
func UnsafeConcatenatedFormat(v string) string {
	return fmt.Sprintf(`UPDATE "t" SET `+
		`"c" = '%s' `+
		`WHERE "id" = 1`, v)
}

// SafePlaceholder interpolates only the column name and binds the value. The
// gate must not flag this shape.
func SafePlaceholder(col string) string {
	return fmt.Sprintf(`UPDATE "t" SET "%s" = array_cat("%s", ?::varchar[])`, col, col)
}

// SafeConcatenatedFormat splits a safe format across lines. The gate must not
// flag this shape, so folding does not create a false positive on its own.
func SafeConcatenatedFormat(col string) string {
	return fmt.Sprintf(`SELECT "%s" FROM "t" `+
		`WHERE "id" = ? AND "k" <> 'literal'`, col)
}

// SafeEmptyLiteralThenVerb closes an empty string literal before the next verb.
// The gate must not flag this shape.
//
// 素朴な正規表現 (`'` の後ろに動詞) はここで誤検知する。空のリテラル
// (クォート 2 つ) は開いて閉じるので、後続の `%s` はリテラルの外側にある。
// 本番にも同じ形が在る。
//
// コメントにクォートを 2 つ続けて書かないこと: gofmt が doc コメントの中でだけ
// typographic quote へ書き換える (b7c419f9 と同じ形)。
func SafeEmptyLiteralThenVerb(col string) string {
	return fmt.Sprintf(`SELECT %s FROM "t" WHERE "h" <> '' ORDER BY %s ASC`, col, col)
}

// SafeEscapedPercentInLiteral keeps a literal percent inside a LIKE pattern.
// The gate must not flag this shape.
//
// `%%` は書式動詞ではなくリテラルの `%` なので、クォートの内側に在っても値を
// 差し込んではいない。
func SafeEscapedPercentInLiteral(col string) string {
	return fmt.Sprintf(`SELECT %s FROM "t" WHERE "n" LIKE '%%x%%'`, col)
}
