package entitycompat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestSchemaDrift_CreateOnlyColumns guards against a drop-in-only failure mode.
//
// mk-go の migration が列を `CREATE TABLE IF NOT EXISTS` の中でしか定義して
// いない場合、その CREATE は **Misskey TS が既に作ったテーブルに対しては
// no-op** になる。upstream にも存在する列なら TS 側が作っているので問題ない
// が、upstream に無い列は TS 製 DB にだけ生えず、mk-go がその列を読み書き
// すると drop-in 環境でのみ
// `column "..." of relation "..." does not exist` で落ちる。
//
// 実際に app.createdAt / auth_session.createdAt / clip.notesCount の 3 本が
// この形で紛れ込んでいた (#2243)。`ALTER TABLE ... ADD COLUMN` で追加した列は
// 両方の shape で冪等に効くので対象外。
//
// golden snapshot は `go run ./tools/schemadrift` で再生成する
// (third_party/misskey を bump したら実行すること)。
func TestSchemaDrift_CreateOnlyColumns(t *testing.T) {
	upstream := loadUpstreamColumns(t)
	createOnly, alterAdded := parseMigrations(t)

	var findings []string
	for table, cols := range createOnly {
		up, known := upstream[table]
		if !known {
			// upstream に対応 entity が無いテーブル (chart 系 / mk-go 独自
			// テーブル)。TS が作ることは無いので CREATE は必ず実際に走る。
			continue
		}
		for _, col := range cols {
			if alterAdded[table][col] {
				continue // ALTER で冪等追加済 = drop-in 安全
			}
			if up[col] {
				continue // upstream にもある = TS 製 DB でも存在する
			}
			if _, ok := createOnlyAllowlist[table+"."+col]; ok {
				continue
			}
			findings = append(findings, table+"."+col)
		}
	}
	sort.Strings(findings)

	if len(findings) > 0 {
		t.Errorf(`CREATE TABLE 内でしか定義されていない upstream 非存在カラムが %d 件:
  %s

これらは Misskey TS が作った DB には生えないため、mk-go が読み書きすると
drop-in 環境でのみ失敗する。次のいずれかで解消すること:
  (a) その列への依存を外す (upstream に無い = 本来不要なはず。#2243 の方針)
  (b) どうしても必要なら ALTER TABLE ... ADD COLUMN IF NOT EXISTS の
      migration を足す (000039_dropin_compat.up.sql と同じ方式)`,
			len(findings), strings.Join(findings, "\n  "))
	}
}

// createOnlyAllowlist は「fresh な mk-go DB にだけ存在するが、mk-go の
// どのコードからも読み書きされない」列。TS 製 DB に生えなくても実害が無い
// ため gate の対象外にする。
//
// **列を新たに使い始めるときは必ずここから外すこと。** 使うのであれば
// ALTER TABLE ... ADD COLUMN IF NOT EXISTS の migration が必要になる。
var createOnlyAllowlist = map[string]string{
	// upstream が 1697420555911-deleteCreatedAt で DROP 済み。mk-go も #2243 で
	// model から除去し、INSERT/SELECT のどちらでも参照しなくなった。既存の
	// mk-go DB には NOT NULL DEFAULT now() の列が残るが未使用。
	"app.createdAt":          "#2243: 未使用 (upstream は DROP 済み)",
	"auth_session.createdAt": "#2243: 未使用 (upstream は DROP 済み)",
	// 旧・非正規化カウンタ。#2243 で撤去し、件数は upstream 同様
	// clip_note の実カウントで算出する。
	"clip.notesCount": "#2243: 未使用 (clip_note の実カウントに移行)",
}

func loadUpstreamColumns(t *testing.T) map[string]map[string]bool {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join("testdata", "golden_upstream_columns.json"))
	if err != nil {
		t.Fatalf("read golden: %v (run `go run ./tools/schemadrift`)", err)
	}
	var raw map[string][]string
	if err := json.Unmarshal(blob, &raw); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("golden snapshot is empty; run `go run ./tools/schemadrift`")
	}
	out := make(map[string]map[string]bool, len(raw))
	for table, cols := range raw {
		set := make(map[string]bool, len(cols))
		for _, c := range cols {
			set[c] = true
		}
		out[table] = set
	}
	return out
}

var (
	createTableRe = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?"([^"]+)"\s*\((.*?)\n\);`)
	createColRe   = regexp.MustCompile(`^"([^"]+)"\s+\S`)
	alterAddRe    = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+"([^"]+)"\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?"([^"]+)"`)
	alterDropRe   = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+"([^"]+)"\s+DROP\s+COLUMN\s+(?:IF\s+EXISTS\s+)?"([^"]+)"`)
)

// parseMigrations returns (table -> columns defined inside CREATE TABLE) and
// (table -> columns that some migration ALTERs in or drops).
func parseMigrations(t *testing.T) (map[string][]string, map[string]map[string]bool) {
	t.Helper()
	dir := filepath.Join("..", "..", "migration")
	files, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found under %s (err=%v)", dir, err)
	}
	sort.Strings(files)

	createOnly := map[string][]string{}
	seen := map[string]map[string]bool{}
	altered := map[string]map[string]bool{}
	mark := func(m map[string]map[string]bool, table, col string) {
		if m[table] == nil {
			m[table] = map[string]bool{}
		}
		m[table][col] = true
	}

	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		text := string(src)
		for _, m := range createTableRe.FindAllStringSubmatch(text, -1) {
			table, body := m[1], m[2]
			for _, line := range strings.Split(body, "\n") {
				cm := createColRe.FindStringSubmatch(strings.TrimSpace(line))
				if cm == nil {
					continue
				}
				if seen[table] == nil {
					seen[table] = map[string]bool{}
				}
				if seen[table][cm[1]] {
					continue
				}
				seen[table][cm[1]] = true
				createOnly[table] = append(createOnly[table], cm[1])
			}
		}
		for _, m := range alterAddRe.FindAllStringSubmatch(text, -1) {
			mark(altered, m[1], m[2])
		}
		for _, m := range alterDropRe.FindAllStringSubmatch(text, -1) {
			mark(altered, m[1], m[2])
		}
	}
	return createOnly, altered
}
