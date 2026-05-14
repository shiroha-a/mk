package repository

import "strings"

// escapeSQLLikePattern escapes user-supplied input for use inside a SQL LIKE
// pattern. backslash / `%` / `_` の 3 文字を `\` で escape する。
//
// Use case: 「prefix match で 1 文字 wildcard `_` を含む host 名が意図せず
// 他の host にも hit する」みたいな ambiguity を排除する。upstream Misskey TS
// の `misc/sql-like-escape.ts` と同 semantics (#1054)。
//
// 戻り値の文字列は LIKE query の右側で `+ "%"` 等の wildcard 連結と共に使い、
// 必要に応じて `ESCAPE '\'` を SQL 側で明示すること。PostgreSQL 9.1+ では
// default escape character が `\` なので明示は冗長だが、portability のため
// 推奨。
func escapeSQLLikePattern(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	)
	return r.Replace(s)
}
