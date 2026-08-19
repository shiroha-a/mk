package entitycompat

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// docs/divergence.md は upstream との差分の一次資料で、diff-e2e の ignore-list を
// 足すときに「対応する記述があるか」を確認する運用になっている (CLAUDE.md §8)。
// 実態より少ない表を信じると、そこに載っていない差分を差分として認識しないまま
// 調査が進む。
//
// 実際に 3 箇所が静かにずれていた (#2634)。#2313 で分割アップロードの endpoint 4 件を
// 足したときにサマリだけ更新して内訳表を更新せず、#2332 / #2340 / instance_secret でも
// 同じことが起きた。**どれも人が数え直さない限り気付けない形**だったので、機械で
// 検証できるものは以下で固定する。
//
// 検証は 3 方向ある。どれか 1 つでも欠けると #2634 の再発を許す。
//
//	1. 見出しの件数 <-> 表の行 (§1-1 は件数列の合計、§2 は実 schema)
//	2. 見出しの件数 <-> その内訳、および直後の散文 (#2634 では 14 と 15 が隣接 2 行で矛盾していた)
//	3. 見出しの件数 <-> 冒頭サマリ (**#2634 の起点はサマリだけ更新して表を更新しなかったこと**。
//	   鏡像の「表だけ更新してサマリを更新しない」も同じだけ起きうる)
//
// §1-1 だけは実 schema にあたるものが無く内部整合しか見られない。upstream の
// endpoint 一覧自体は tools/apicompat が submodule から抽出できるが、
// **test-shards job は submodule を checkout しない** (.github/workflows/ci.yml で
// `submodules: recursive` を指定しているのは frontend-check だけ) ため、この
// パッケージのテストからは参照できない。

var (
	divergenceHeadingRe = regexp.MustCompile(`^### (\d+-\d+)\. .*?\((\d+)`)
	// §2-2 の見出しは `(23 = 実使用 20 + 未使用の残存 3)`。
	divergenceBreakdownRe = regexp.MustCompile(`\((\d+) = 実使用 (\d+) \+ 未使用の残存 (\d+)\)`)
	// §2-2 の直後の散文は `実際に読み書きするのは 20 件** (cherrypick 由来 3 + mk-go 独自 17)`。
	divergenceProseRe = regexp.MustCompile(`読み書きするのは (\d+) 件\*\* \(cherrypick 由来 (\d+) \+ mk-go 独自 (\d+)\)`)
	// サマリ表の `| DB テーブル | 10 (+ bookkeeping 2) | ...`。
	summaryTableRe = regexp.MustCompile(`^\| DB テーブル \| (\d+) \(\+ bookkeeping (\d+)\)`)
	// サマリ表の `| DB カラム | 17 (+ 未使用の残存列 3) | 3 |`。
	summaryColumnRe = regexp.MustCompile(`^\| DB カラム \| (\d+) \(\+ 未使用の残存列 (\d+)\) \| (\d+) \|`)
	// サマリ表の endpoint 行は `GET variant 23 + alias 3 + ...` という和の式。
	summaryEndpointRe = regexp.MustCompile(`^\| API endpoint \| (.*?) \| chat (\d+) \|`)
	// 和の各項は `名前 <数字>`。**項の末尾の数字だけ**を取る (行全体から数字を
	// 拾うと `(#2313)` のような注記が項として混ざる)。末尾が数字でない項があれば
	// 黙って無視せず false を返し、書式が変わったことを呼び出し側に知らせる。
	summaryTermRe = regexp.MustCompile(`(\d+)\s*$`)
)

// bookkeepingTables are the two drop-in / tooling tables counted separately from
// mk-go's own tables in the summary.
var bookkeepingTables = map[string]bool{"migrations": true, "schema_migrations": true}

// TestDivergenceDoc_EndpointCountMatchesTable asserts that §1-1's declared count
// matches both the sum of its table's count column and the summary's expression.
func TestDivergenceDoc_EndpointCountMatchesTable(t *testing.T) {
	lines := readDivergenceDoc(t)
	start, declared := findDivergenceSection(t, lines, "1-1")

	sum := 0
	var rows []string
	for _, line := range sectionLines(lines, start)[1:] {
		cells := tableRowCells(line)
		if len(cells) < 2 {
			continue
		}
		n, err := strconv.Atoi(cells[1])
		if err != nil {
			continue // ヘッダ行 / 区切り行
		}
		sum += n
		rows = append(rows, fmt.Sprintf("%s = %d", cells[0], n))
	}

	if sum != declared {
		t.Errorf(`docs/divergence.md §1-1 の見出しは %d だが、表の件数列の合計は %d:
  %s

見出しか表のどちらかが古い。endpoint を足したら**サマリだけでなく §1-1 の表にも
行を足す**こと (#2634 はこれを怠って 4 件ずれていた)。`,
			declared, sum, strings.Join(rows, "\n  "))
	}

	// 冒頭サマリの `GET variant 23 + alias 3 + ... | chat 15` も同じ総数を表す。
	mkTerms, chat, ok := summaryEndpointCounts(lines)
	if !ok {
		t.Fatal("docs/divergence.md のサマリ表の `| API endpoint |` 行を読めない " +
			"(行が無いか、和の各項が `名前 <数字>` の形になっていない)")
	}
	if got := mkTerms + chat; got != declared {
		t.Errorf(`docs/divergence.md サマリの endpoint 合計は %d (mk-go 独自 %d + chat %d) だが、§1-1 の見出しは %d。

**#2634 の起点はサマリだけを更新して §1-1 の表を更新しなかったこと。** 逆向き
(表だけ更新してサマリを据え置く) も同じだけ起きるので、両方を直すこと。`,
			got, mkTerms, chat, declared)
	}
}

// TestDivergenceDoc_TableCountMatchesSchema asserts that §2-1's declared count
// matches the number of tables the migrations create that upstream lacks.
func TestDivergenceDoc_TableCountMatchesSchema(t *testing.T) {
	own := ownTables(t)

	lines := readDivergenceDoc(t)
	start, declared := findDivergenceSection(t, lines, "2-1")
	if len(own) != declared {
		t.Errorf(`docs/divergence.md §2-1 の見出しは %d だが、migration が作る upstream 非存在テーブルは %d 件:
  %s

表に無いテーブルがあるか、逆に消えたテーブルが残っている。`,
			declared, len(own), strings.Join(own, "\n  "))
	}

	// 見出しが合っていても表に行が無ければ意味が無いので、各テーブルの行が
	// あることも見る。**節全体の部分文字列で探さないこと** — §2-1 の行は他の
	// テーブルを backtick で相互参照している (user_keypair_extra の行が
	// `user_keypair` を挙げる等) ので、行を消して散文で触れるだけで素通りする。
	for _, table := range own {
		if !divergenceRowNames(sectionLines(lines, start), table) {
			t.Errorf("docs/divergence.md §2-1 に %q の行が無い", table)
		}
	}

	// サマリの `DB テーブル N (+ bookkeeping M)`。
	mk, bookkeeping, ok := summaryTableCounts(lines)
	if !ok {
		t.Fatal("docs/divergence.md のサマリ表に `| DB テーブル |` の行が無い")
	}
	var wantBookkeeping int
	for _, table := range own {
		if bookkeepingTables[table] {
			wantBookkeeping++
		}
	}
	if mk+bookkeeping != declared || bookkeeping != wantBookkeeping {
		t.Errorf(`docs/divergence.md サマリの「DB テーブル %d (+ bookkeeping %d)」が §2-1 の見出し %d と合わない
(bookkeeping は %v の %d 件)。`,
			mk, bookkeeping, declared, sortedKeys(bookkeepingTables), wantBookkeeping)
	}
}

// TestDivergenceDoc_ColumnCountMatchesSchema asserts that §2-2's declared count
// matches the number of mk-go-only columns on tables upstream also has.
func TestDivergenceDoc_ColumnCountMatchesSchema(t *testing.T) {
	own := ownColumnsOnSharedTables(t)

	lines := readDivergenceDoc(t)
	start, declared := findDivergenceSection(t, lines, "2-2")
	if len(own) != declared {
		t.Errorf(`docs/divergence.md §2-2 の見出しは %d だが、upstream 共有テーブルの mk-go 独自カラムは %d 件:
  %s

**見出しの内訳 (実使用 N + 未使用の残存 M) も直すこと。** #2634 では見出しと
直後の散文で 14 と 15 が食い違っていた。`,
			declared, len(own), strings.Join(own, "\n  "))
	}

	// 行の存在は table と column の両方で見る。**列名だけで探すと、同名の列
	// (createdAt が app / auth_session / note_favorite の 3 箇所にある) が他の
	// 行に残っているせいで、行が丸ごと消えても素通りする。**
	for _, qualified := range own {
		dot := strings.IndexByte(qualified, '.')
		table, col := qualified[:dot], qualified[dot+1:]
		if !divergenceRowMentions(sectionLines(lines, start), table, col) {
			t.Errorf("docs/divergence.md §2-2 に %q の行が無い", qualified)
		}
	}

	// 未使用の残存列は schema_drift_test.go の allowlist がそのまま実数になる。
	unused := 0
	for qualified := range createOnlyAllowlist {
		if !contains(own, qualified) {
			t.Errorf("createOnlyAllowlist の %q が mk-go 独自カラムに含まれない (allowlist が古い)", qualified)
			continue
		}
		unused++
	}

	// 見出しの内訳と直後の散文。#2634 で矛盾していたのはまさにここ。
	section := strings.Join(sectionLines(lines, start), "\n")
	bm := divergenceBreakdownRe.FindStringSubmatch(lines[start])
	if bm == nil {
		t.Fatalf("§2-2 の見出しが `(N = 実使用 M + 未使用の残存 K)` の形でない: %q", lines[start])
	}
	total, inUse, residual := atoi(t, bm[1]), atoi(t, bm[2]), atoi(t, bm[3])
	if total != declared || inUse+residual != total {
		t.Errorf("§2-2 の見出しの内訳が破綻している: %d = %d + %d", total, inUse, residual)
	}
	if residual != unused {
		t.Errorf("§2-2 の見出しの「未使用の残存 %d」が createOnlyAllowlist の %d 件と合わない", residual, unused)
	}

	pm := divergenceProseRe.FindStringSubmatch(section)
	if pm == nil {
		t.Fatal("§2-2 の散文が `読み書きするのは N 件** (cherrypick 由来 A + mk-go 独自 B)` の形でない")
	}
	proseInUse, cherrypick, mkOwn := atoi(t, pm[1]), atoi(t, pm[2]), atoi(t, pm[3])
	if proseInUse != inUse {
		t.Errorf(`§2-2 の見出しの「実使用 %d」と散文の「読み書きするのは %d 件」が食い違う。

#2634 で見つかったのがこの形 (14 と 15 が隣接 2 行で矛盾)。`, inUse, proseInUse)
	}
	if cherrypick+mkOwn != proseInUse {
		t.Errorf("§2-2 の散文の内訳が破綻している: %d != %d + %d", proseInUse, cherrypick, mkOwn)
	}

	// サマリの `DB カラム N (+ 未使用の残存列 M) | C`。
	sMk, sResidual, sCherrypick, ok := summaryColumnCounts(lines)
	if !ok {
		t.Fatal("docs/divergence.md のサマリ表に `| DB カラム |` の行が無い")
	}
	if sMk != mkOwn || sResidual != residual || sCherrypick != cherrypick {
		t.Errorf(`docs/divergence.md サマリの「DB カラム %d (+ 未使用の残存列 %d) | %d」が
§2-2 の内訳 (mk-go 独自 %d / 残存 %d / cherrypick %d) と合わない。`,
			sMk, sResidual, sCherrypick, mkOwn, residual, cherrypick)
	}
}

// ownTables returns the tables mk-go's migrations create that upstream lacks.
func ownTables(t *testing.T) []string {
	t.Helper()
	upstream := loadUpstreamColumns(t)
	createOnly, _, _ := parseMigrations(t)

	var own []string
	for table := range createOnly {
		if _, shared := upstream[table]; shared {
			continue
		}
		// chart 系は upstream でも定義位置が違うだけ (models/ ではなく
		// core/chart/charts/entities/) なので独自ではない。doc 側にもその旨の
		// 注記がある。
		if strings.HasPrefix(table, "__chart__") || strings.HasPrefix(table, "__chart_day__") {
			continue
		}
		own = append(own, table)
	}
	sort.Strings(own)
	return own
}

// ownColumnsOnSharedTables returns `table.column` for every column mk-go's
// migrations leave on a table that upstream also has.
func ownColumnsOnSharedTables(t *testing.T) []string {
	t.Helper()
	upstream := loadUpstreamColumns(t)
	_, _, current := parseMigrations(t)

	var own []string
	for table, set := range current {
		up, shared := upstream[table]
		if !shared {
			continue
		}
		for col := range set {
			if !up[col] {
				own = append(own, table+"."+col)
			}
		}
	}
	sort.Strings(own)
	return own
}

// divergenceRowMentions reports whether some table row names both the table and
// the column, allowing either to be a `a` / `b` group.
func divergenceRowMentions(section []string, table, column string) bool {
	for _, line := range section {
		cells := tableRowCells(line)
		if len(cells) < 2 {
			continue
		}
		if cellNames(cells[0])[table] && cellNames(cells[1])[column] {
			return true
		}
	}
	return false
}

// divergenceRowNames reports whether some table row's first cell names the table.
func divergenceRowNames(section []string, table string) bool {
	for _, line := range section {
		cells := tableRowCells(line)
		if len(cells) > 0 && cellNames(cells[0])[table] {
			return true
		}
	}
	return false
}

// cellNames splits an "`a` / `b`" cell into the set {a, b}.
func cellNames(cell string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(cell, "/") {
		out[strings.Trim(strings.TrimSpace(part), "`")] = true
	}
	return out
}

func tableRowCells(line string) []string {
	if !strings.HasPrefix(line, "|") {
		return nil
	}
	if trimmed := strings.NewReplacer("|", "", " ", "", "-", "", ":", "").Replace(line); trimmed == "" {
		return nil // 区切り行
	}
	cells := strings.Split(strings.Trim(line, "|"), "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func summaryEndpointCounts(lines []string) (int, int, bool) {
	for _, line := range lines {
		m := summaryEndpointRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		mk := 0
		for _, term := range strings.Split(m[1], "+") {
			tm := summaryTermRe.FindStringSubmatch(term)
			if tm == nil {
				return 0, 0, false
			}
			v, err := strconv.Atoi(tm[1])
			if err != nil {
				return 0, 0, false
			}
			mk += v
		}
		chat, err := strconv.Atoi(m[2])
		return mk, chat, err == nil
	}
	return 0, 0, false
}

func summaryTableCounts(lines []string) (mk, bookkeeping int, ok bool) {
	for _, line := range lines {
		if m := summaryTableRe.FindStringSubmatch(line); m != nil {
			mk, err1 := strconv.Atoi(m[1])
			bk, err2 := strconv.Atoi(m[2])
			return mk, bk, err1 == nil && err2 == nil
		}
	}
	return 0, 0, false
}

func summaryColumnCounts(lines []string) (mk, residual, cherrypick int, ok bool) {
	for _, line := range lines {
		if m := summaryColumnRe.FindStringSubmatch(line); m != nil {
			a, err1 := strconv.Atoi(m[1])
			b, err2 := strconv.Atoi(m[2])
			c, err3 := strconv.Atoi(m[3])
			return a, b, c, err1 == nil && err2 == nil && err3 == nil
		}
	}
	return 0, 0, 0, false
}

func readDivergenceDoc(t *testing.T) []string {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join("..", "..", "docs", "divergence.md"))
	if err != nil {
		t.Fatalf("read docs/divergence.md: %v", err)
	}
	return strings.Split(string(blob), "\n")
}

// findDivergenceSection returns the heading's line index and the count declared
// in its parentheses.
func findDivergenceSection(t *testing.T, lines []string, section string) (int, int) {
	t.Helper()
	for i, line := range lines {
		m := divergenceHeadingRe.FindStringSubmatch(line)
		if m == nil || m[1] != section {
			continue
		}
		return i, atoi(t, m[2])
	}
	t.Fatalf("docs/divergence.md に §%s の見出し (件数付き) が無い", section)
	return 0, 0
}

// sectionLines returns the lines from the heading up to the next heading of any
// level.
func sectionLines(lines []string, start int) []string {
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "#") {
			return lines[start:i]
		}
	}
	return lines[start:]
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("数値として読めない: %q", s)
	}
	return n
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
