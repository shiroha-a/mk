// Command backfill-note-tags normalizes existing note.tags to the NFKC +
// lowercase form that NoteCreateService now stores for new notes (#1948-18 /
// #2013). It is needed because search-by-tag normalizes the query, so old
// uppercase / full-width tags would otherwise stop matching.
//
// 本番 (大きな note テーブル) では負荷を考慮し、--batch / --sleep-ms で速度を
// 絞り、maintenance window で段階実行すること。--from で中断地点から再開できる。
// 冪等なので途中失敗しても --from で安全に再開でき、再実行しても既正規化 note は
// touch しない。まず --dry-run で更新件数を見積もってから実行するのを推奨する。
//
// 性能上の注意: WHERE 句の `cardinality(tags) > 0` に対応する index は無いため、
// tagged note が疎なテーブルでは LIMIT 前に空 tag 行の heap filter が走り、各 batch の
// レイテンシが膨らみうる。事前に --dry-run で総走査コストを実測し、必要なら一時的に
// `CREATE INDEX CONCURRENTLY ... ON note (id) WHERE cardinality(tags) > 0` を貼って
// から実行 (完了後 DROP) することを検討する。
//
// なお >32 個の case-variant 重複 tag を持つ古い note では、backfill 結果が「今 fresh
// に作成した場合の値」と完全一致しないことがある (旧 Extract は case-insensitive dedup、
// 現 NormalizeNoteTags は case-sensitive dedup)。全 tag は正しく正規化され検索可能で、
// 余分に残るのは marginal tag のみ (検索上は寛容側) なのでデータ破損ではない。
//
//	backfill-note-tags -config .config/default.yml -dry-run
//	backfill-note-tags -config .config/default.yml -batch 1000 -sleep-ms 200
//	backfill-note-tags -config .config/default.yml -from <last-id>   # 再開
package main

import (
	"flag"
	"log"
	"time"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/maintenance"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	cfgPath := flag.String("config", "/app/.config/default.yml", "path to mk-go config file")
	batchSize := flag.Int("batch", 1000, "notes per keyset batch")
	sleepMs := flag.Int("sleep-ms", 100, "sleep between batches to limit DB load")
	fromID := flag.String("from", "", "resume from note id (exclusive)")
	dryRun := flag.Bool("dry-run", false, "count changes without writing")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	db, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{})
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	cursor := *fromID
	var totalScanned, totalUpdated int
	for {
		res, err := maintenance.BackfillNoteTagsBatch(db, cursor, *batchSize, *dryRun)
		if err != nil {
			log.Fatalf("backfill batch (cursor=%q): %v", cursor, err)
		}
		if res.Scanned == 0 {
			break
		}
		cursor = res.LastID
		totalScanned += res.Scanned
		totalUpdated += res.Updated
		log.Printf("batch scanned=%d updated=%d cursor=%s (total scanned=%d updated=%d)",
			res.Scanned, res.Updated, cursor, totalScanned, totalUpdated)
		if *sleepMs > 0 {
			time.Sleep(time.Duration(*sleepMs) * time.Millisecond)
		}
	}
	mode := "applied"
	if *dryRun {
		mode = "dry-run (no writes)"
	}
	log.Printf("done [%s]: scanned=%d updated=%d", mode, totalScanned, totalUpdated)
}
