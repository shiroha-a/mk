package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/core/fsck"
)

// fsckTimeout bounds the whole run. 集計クエリは全表走査になるので、
// healthcheck 系より長めに取る。
const fsckTimeout = 10 * time.Minute

// runFsck checks counter drift and returns the process exit code.
//
// **既定は読み取り専用。** fix が真のときだけ書き戻す。孤児行は報告に留める
// (カウンタは元データから導けるが、削除した行は復元できない)。
func runFsck(configPath string, fix bool) int {
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fsck: 設定を読めない: %v\n", err)
		return 1
	}
	db, err := openDoctorDB(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fsck: DB に接続できない: %v\n", err)
		return 1
	}
	defer closeDoctorDB(db)

	ctx, cancel := context.WithTimeout(context.Background(), fsckTimeout)
	defer cancel()

	report, err := fsck.Run(ctx, db, fsck.Options{Fix: fix})
	if err != nil {
		fmt.Fprintf(os.Stderr, "fsck: %v\n", err)
		return 1
	}
	printFsckReport(report, fix)

	if report.OK() {
		return 0
	}
	// 修正したなら成功扱い。孤児だけが残っている場合は「対応が要る」ので 1。
	if fix && len(report.Orphans) == 0 {
		return 0
	}
	return 1
}

// printFsckReport writes the human-facing summary.
func printFsckReport(r fsck.Report, fix bool) {
	fmt.Println()
	if len(r.Drifts) == 0 {
		fmt.Println("  カウンタのずれは見つかりませんでした。")
	} else {
		// 全件は出さない。数千件になると読めないので、内訳と先頭だけ示す。
		byColumn := map[string]int{}
		for _, d := range r.Drifts {
			byColumn[d.Table+"."+d.Column]++
		}
		fmt.Printf("  カウンタのずれ: %d 件\n", len(r.Drifts))
		for k, n := range byColumn {
			fmt.Printf("    %-24s %d 件\n", k, n)
		}
		fmt.Println()
		for i, d := range r.Drifts {
			if i >= 5 {
				fmt.Printf("    ... 他 %d 件\n", len(r.Drifts)-i)
				break
			}
			fmt.Printf("    %s.%s  id=%s  記録 %d → 実際 %d\n",
				d.Table, d.Column, d.ID, d.Stored, d.Actual)
		}
	}

	if len(r.Orphans) > 0 {
		fmt.Println("\n  孤児行 (自動削除はしません)")
		for _, o := range r.Orphans {
			fmt.Printf("    %-12s %s: %d 件\n", o.Table, o.Reason, o.Count)
		}
		fmt.Println("    削除は影響を確認した上で手動で行ってください。")
	}

	fmt.Println()
	switch {
	case fix && r.Repaired > 0:
		fmt.Printf("  %d 件のカウンタを修正しました。\n", r.Repaired)
	case !fix && len(r.Drifts) > 0:
		fmt.Println("  修正するには -fix を付けて再実行してください。")
	}
	fmt.Println()
}
