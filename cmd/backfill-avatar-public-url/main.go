// Command backfill-avatar-public-url rewrites stored `user.avatarUrl` /
// `user.bannerUrl` to the public (metadata-stripped) variant of the drive file
// the row already points at.
//
// 既存の行は原本 (`drive_file.url`) を指している。原本はアップロードされた
// バイト列そのままで EXIF / XMP が載っており、`avatarUrl` はタイムラインや
// ActivityPub の actor icon に出るため、設定したときのまま公開され続ける。
// 書き込み側の修正は以後の更新にしか効かないので、既存行はこれで流し直す。
//
// **SQL migration では書けない。** 「webpublic があればそちら、無ければ原本」
// という判定は `entity.WebpublicOrOriginalURL` が持っており、列を跨いだ
// 条件付きの写しになる (`backfill-note-tags` が NFKC で同じ理由)。
//
// **対象はローカル利用者だけ。** upstream はリモートのアイコンを drive に
// 保存して `avatarId` を書くので、TS から引き継いだ DB にはリモート利用者の
// 古い id が残っている。host で絞らないと、mk-go が actor から取り直した
// 現在の URL を TS 時代のキャッシュへ巻き戻す。
//
// 冪等なので途中で失敗しても再実行して安全。まず -dry-run で件数を見積もること。
//
//	backfill-avatar-public-url -config .config/default.yml -dry-run
//	backfill-avatar-public-url -config .config/default.yml -batch 1000 -sleep-ms 200
//	backfill-avatar-public-url -config .config/default.yml -from <last-user-id>
package main

import (
	"flag"
	"log"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/maintenance"
)

func main() {
	cfgPath := flag.String("config", "/app/.config/default.yml", "path to mk-go config file")
	batchSize := flag.Int("batch", 1000, "rows per keyset batch")
	sleepMs := flag.Int("sleep-ms", 100, "sleep between batches to limit DB load")
	fromID := flag.String("from", "", "resume from this user id (exclusive)")
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
	var scanned, updated int
	for {
		res, err := maintenance.BackfillAvatarPublicURLBatch(db, cursor, *batchSize, *dryRun)
		if err != nil {
			// **件数とカーソルの基準を揃える。** 失敗したバッチの途中までを
			// scanned に足すと、-from が指す位置 (そのバッチの開始) と食い違う。
			// 冪等なのでこの位置から流し直して安全。
			log.Fatalf("backfill failed (scanned=%d before this batch; resume with -from %q): %v",
				scanned, cursor, err)
		}
		scanned += res.Scanned
		updated += res.Updated
		if res.Scanned == 0 {
			break
		}
		cursor = res.LastID
		log.Printf("scanned=%d updated=%d cursor=%s", scanned, updated, cursor)
		if *sleepMs > 0 {
			time.Sleep(time.Duration(*sleepMs) * time.Millisecond)
		}
	}

	mode := "applied"
	if *dryRun {
		mode = "dry-run"
	}
	log.Printf("done (%s): scanned=%d updated=%d", mode, scanned, updated)
}
