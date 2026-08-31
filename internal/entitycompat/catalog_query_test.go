package entitycompat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// catalogFrom matches a FROM clause on a catalog view whose rows span schemas.
//
// `pg_indexes_size(` のような関数名を拾わないよう単語境界を要求する。
//
// **`information_schema.schemata` は入れない。** あれは schema の一覧そのもので、
// 自分の schema に絞る概念が無い (`pluginstore` が plugin 用 schema を列挙するのに
// 使っている)。`pg_class` / `pg_attribute` も対象外 — join の書き方が多様で単純な
// 述語検査に乗らないため。規則は CLAUDE.md / docs/testing.md 側に書いてある。
var catalogFrom = regexp.MustCompile(`(?i)\bfrom\s+(pg_indexes\b|information_schema\.(columns|tables)\b)`)

// scopedByPredicate matches a schema filter used as a predicate.
//
// **SELECT 句に列名があるだけでは不十分。** 単に `schemaname` を含むかで見ると
// `SELECT schemaname, indexdef FROM pg_indexes WHERE tablename = ?` が通って
// しまう (実際にこの gate の初版がその変異を見逃した)。列名は catalog によって
// 違う (`pg_indexes` は schemaname、`information_schema.*` は table_schema、
// `pg_namespace` を join する形は nspname)。
var scopedByPredicate = regexp.MustCompile(`(?i)\b(schemaname|table_schema|nspname)\s*(=|in\b)`)

// unscopedOptOut lets a query opt out when it deliberately looks across schemas.
//
// **理由を同じ行に書かせる。** `\s` は改行を含むので `\s*\S` だと次の行の
// 先頭にマッチしてしまい、理由なしのマーカーを通す (初版がそうだった)。
// 改行を除いた空白 `[^\S\n]` で受ける。
var unscopedOptOut = regexp.MustCompile(`--[^\S\n]*unscoped:[^\S\n]*\S`)

// gateOwnFile is skipped because its fixtures are deliberately unscoped queries.
//
// **除外したファイルに実クエリを書かないこと。** ここだけは gate の外にある。
const gateOwnFile = "internal/entitycompat/catalog_query_test.go"

// #2777: `pg_indexes` や `information_schema.*` は **schema を跨いで全件返す**。
// テスト DB は #2450 でパッケージごとに schema を分けているので、絞らないと
// 2 つ壊れる:
//
//   - 他 schema の同名オブジェクトを自分のものと取り違え、migration が適用されて
//     いなくても regression guard が緑になる (実測で 3 テストが空振りしていた —
//     `internal_repository_ts` の index 定義が返っていた)
//   - 他パッケージの `ApplyMigrations` が DDL 中だと
//     `could not open relation with OID (SQLSTATE XX000)` で落ちる。**CI の
//     required check (`test`) が不定期に赤くなる** (PR #2778 で実際に踏んだ)
//
// doc に書くだけだと再発する (17-19 schema ある限り条件は揃ったまま) ので、
// 静的に検出する。
//
// **文字列の切り出しは `go/parser` で行う。** backtick を機械的にペアリングする
// 初版は、コメントや rune literal に含まれる backtick でずれ、**gate 自身の
// ファイルで既にずれていた** (backtick 41 個 = 奇数)。ずれると「どのチャンクにも
// 入らない」クエリができて黙って無検査になる。
func TestCatalogQueriesAreSchemaScoped(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	checked := 0

	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if filepath.ToSlash(rel) == gateOwnFile {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			sql, uerr := strconv.Unquote(lit.Value)
			if uerr != nil || !catalogFrom.MatchString(sql) {
				return true
			}
			checked++
			if scopedByPredicate.MatchString(sql) || unscopedOptOut.MatchString(sql) {
				return true
			}
			t.Errorf("%s:%d: カタログを schema で絞っていないクエリがある (#2777)。\n"+
				"  `schemaname` / `table_schema` = current_schema() を WHERE に足すこと。\n"+
				"  意図的に schema を跨ぐ場合は SQL に `-- unscoped: <理由>` を書く。\n"+
				"  query: %s", rel, fset.Position(lit.Pos()).Line, strings.Join(strings.Fields(sql), " "))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	// 列挙が空なら「検査していないのに緑」になる。
	if checked == 0 {
		t.Fatal("カタログを引くクエリが 1 つも見つからない。gate が空振りしている")
	}
}

// gate 自身の判定を固定する。**これが無いと gate のロジックを弱める変異を
// 検出できない** — 実測で「列名を含むか」だけを見る初版に戻す変異が素通りした
// (対象クエリの SELECT 句に `schemaname` があるため)。
func TestCatalogQueryGate_Predicates(t *testing.T) {
	// ok = gate が「絞れている / 意図的に絞らない」と判定すること。
	for _, tc := range []struct {
		name string
		sql  string
		ok   bool
	}{
		{"WHERE で絞る", "SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND tablename = $1", true},
		{"IN でも絞れている", "SELECT indexdef FROM pg_indexes WHERE schemaname IN (current_schema())", true},
		{"information_schema は table_schema", "SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema()", true},
		{"nspname で絞る形", "SELECT 1 FROM information_schema.tables t JOIN x ON 1=1 WHERE nspname = current_schema()", true},
		{"SELECT 句にあるだけでは不足", "SELECT schemaname, indexdef FROM pg_indexes WHERE tablename = $1", false},
		{"絞りが無い", "SELECT indexdef FROM pg_indexes WHERE tablename = $1", false},
		{"information_schema の絞り無し", "SELECT 1 FROM information_schema.columns WHERE table_name = $1", false},
		{"schemata は対象外なので絞り無しでよい", "SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE $1", true},
		{"opt-out は理由が要る", "SELECT count(*) FROM pg_indexes -- unscoped:\n WHERE tablename = $1", false},
		{"理由付きの opt-out は通る", "SELECT count(*) FROM pg_indexes -- unscoped: 複数 schema を数える\n WHERE tablename = $1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 検査対象から外れているものは、それ自体が「通る」判定。
			if !catalogFrom.MatchString(tc.sql) {
				if !tc.ok {
					t.Fatalf("検査対象として拾えていない: %s", tc.sql)
				}
				return
			}
			got := scopedByPredicate.MatchString(tc.sql) || unscopedOptOut.MatchString(tc.sql)
			if got != tc.ok {
				t.Errorf("judged %v, want %v for: %s", got, tc.ok, tc.sql)
			}
		})
	}

	// 検査対象の切り出し。`pg_indexes_size(...)` は view ではなく関数、
	// `pg_indexes_backup` は別テーブル。どちらも `\b` が無いと誤検知する。
	for _, tc := range []struct {
		sql  string
		want bool
	}{
		{"SELECT indexdef FROM pg_indexes WHERE tablename = $1", true},
		{"SELECT 1 FROM information_schema.columns WHERE table_name = $1", true},
		{"SELECT pg_indexes_size(c.oid) FROM pg_class c", false},
		{"SELECT * FROM pg_indexes_backup", false},
		{"SELECT * FROM information_schema_backup", false},
		{"SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE $1", false},
	} {
		if got := catalogFrom.MatchString(tc.sql); got != tc.want {
			t.Errorf("catalogFrom matched %v, want %v for: %s", got, tc.want, tc.sql)
		}
	}
}
