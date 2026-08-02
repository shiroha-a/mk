package entitycompat

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestMigrationIdempotency_RequiresIfExists は、migration の DDL が
// `IF NOT EXISTS` / `IF EXISTS` 付きで書かれていることを強制する。
//
// mk-go の migration は「新規 DB」と「Misskey TS が作った既存 DB」の両方に
// 対して流れる (docs/migration-from-ts.md)。TS 製 DB には upstream が既に
// 作ったテーブル / 列 / index があるので、条件なしの DDL は
//
//	column "category" of relation "avatar_decoration" already exists
//
// で落ちて migration が dirty 停止する。実際 000048 (avatar_decoration.category)
// が upstream 2026.5.0 の AddCategoryToAvatarDecorations と衝突して、
// 2026.5.0 以降の TS 製 DB からの drop-in が**完走しない**状態だった (#2246)。
//
// 新規 DB でしか試さないと絶対に踏まないので、静的に縛る。
func TestMigrationIdempotency_RequiresIfExists(t *testing.T) {
	// コメント行を除いた本文だけを見る (日本語コメント中の "CREATE TABLE" 等に
	// 反応しないようにする)。
	commentRe := regexp.MustCompile(`(?m)^\s*--.*$`)

	// Go の RE2 は否定先読みを持たないので、DDL の直後を捕まえてから Go 側で
	// guard の有無を見る。
	checks := []struct {
		name string
		re   *regexp.Regexp
		want string
	}{
		{"CREATE TABLE", regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(.{0,20})`), "IF NOT EXISTS"},
		{"ADD COLUMN", regexp.MustCompile(`(?is)ADD\s+COLUMN\s+(.{0,20})`), "IF NOT EXISTS"},
		{"CREATE INDEX", regexp.MustCompile(`(?is)CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(.{0,20})`), "IF NOT EXISTS"},
		{"DROP COLUMN", regexp.MustCompile(`(?is)DROP\s+COLUMN\s+(.{0,16})`), "IF EXISTS"},
		{"DROP TABLE", regexp.MustCompile(`(?is)DROP\s+TABLE\s+(.{0,16})`), "IF EXISTS"},
		{"DROP INDEX", regexp.MustCompile(`(?is)DROP\s+INDEX\s+(?:CONCURRENTLY\s+)?(.{0,16})`), "IF EXISTS"},
	}

	files, err := filepath.Glob(filepath.Join("..", "..", "migration", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found (err=%v)", err)
	}
	sort.Strings(files)

	var findings []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		body := commentRe.ReplaceAllString(string(raw), "")
		// DO ブロック内は pg_indexes を見て条件分岐する動的 DDL なので対象外。
		// `EXECUTE format(...)` を静的に判定できない。
		if strings.Contains(body, "DO $$") {
			continue
		}
		for _, c := range checks {
			for _, m := range c.re.FindAllStringSubmatch(body, -1) {
				tail := strings.ToUpper(strings.Join(strings.Fields(m[1]), " "))
				if strings.HasPrefix(tail, c.want) {
					continue
				}
				findings = append(findings,
					filepath.Base(f)+": "+c.name+" に "+c.want+" が無い ("+strings.TrimSpace(m[1])+")")
			}
		}
	}

	if len(findings) > 0 {
		t.Errorf(`drop-in で落ちる DDL が %d 件:
  %s

mk-go の migration は Misskey TS が作った既存 DB にも流れる。upstream が
既に作ったテーブル / 列 / index と衝突すると migration が dirty 停止し、
drop-in 手順そのものが完走しなくなる。IF NOT EXISTS / IF EXISTS を付けること。`,
			len(findings), strings.Join(findings, "\n  "))
	}
}
