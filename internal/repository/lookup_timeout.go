package repository

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

// lookupStatementTimeout bounds one moderation lookup at the database (#3106).
//
// **上限を 2 つ持つ理由が違う。** 走査の上限 (起点 50 本 × 候補 200 件) は
// 「返す量」を決めるもので、こちらは「**その上限の中でも遅いとき**に何秒で諦めるか」。
// index が効かない構成・想定外のデータ分布・DB 側の競合では、有界な問い合わせでも
// 待たされる。待たせ続けると worker が詰まるので、DB 側で切る。
//
// 10 秒。実測では最悪ケース (200 万行、1 IP に 10 万アカウント) でも 1 秒未満なので、
// 通常の照会がこれに当たることはない。**当たったら 500 になる** — 途中までの結果を
// 返すと「これで全部」と読まれる (#2792 / #3066 §9)。
const lookupStatementTimeout = 10 * time.Second

// withStatementTimeout runs fn inside a transaction whose statements are
// bounded by d.
//
// **読み取りしかしないが `READ ONLY` では開いていない。** `sql.TxOptions` を
// 渡していないので、`fn` が書いても PostgreSQL は止めない。止めているのは
// 呼び出し側の規約と、必ず Rollback すること。
//
// **`SET LOCAL` はトランザクションの中でしか効かない。** セッションに掛けると
// 接続プールを通じて無関係なクエリまで切ることになる。読み取りしかしないので
// 最後は必ず Rollback する (Commit との差は無く、失敗経路と揃う)。
//
// **値はプレースホルダで渡せない。** PostgreSQL の `SET` は定数しか受け付けない。
// 埋め込むのはこのパッケージの定数から導いた整数だけで、外部入力は混ざらない。
//
// **モデレーションの照会は必ずこれを通す。** 通さない経路が 1 本でもあると、
// そこだけ待たせ放題になる。束縛は `TestModerationLookups_AreBoundedByTimeout`
// が**テーブルロックで実測**する (呼び出しを外すと待ち続けて落ちる)。
func withStatementTimeout(db *gorm.DB, d time.Duration, fn func(tx *gorm.DB) error) error {
	// **`dbresolver.Read` を明示する。** dbresolver は**トランザクションの中では
	// 一切振り分けない** (`switchGuess` などが全て `!isTransaction` で囲まれている)
	// ので、素の `Begin()` にすると primary で張られる。包む前は raw SELECT が
	// `switchGuess` でレプリカへ行っていたため、**この機能でいちばん重いクエリ
	// (起点 50 本 × 候補 200 件) が黙って primary へ移る**。clause は `Begin` の
	// 前に ConnPool を解決するので、これで元の振り先に戻る。
	//
	// レプリカ未設定 (`dbReplications: false`) なら callback が登録されていないので
	// この clause は no-op。
	tx := db.Clauses(dbresolver.Read).Begin()
	if tx.Error != nil {
		return fmt.Errorf("lookup: begin: %w", tx.Error)
	}
	defer tx.Rollback()
	ms := int(d / time.Millisecond)
	if err := tx.Exec(fmt.Sprintf("SET LOCAL statement_timeout TO %d", ms)).Error; err != nil {
		return fmt.Errorf("lookup: set timeout: %w", err)
	}
	return fn(tx)
}
