package plugin

import (
	"context"
	"database/sql"
)

// Storage is a plugin's own persistent storage.
//
// # 触れる範囲
//
// プラグインは**自分の schema だけ**を読み書きする。mk-go 本体のテーブル
// (`note` / `user` 等) には読み取りも含めてアクセスしない。
//
// 読み取りまで禁止するのは慎重さではなく具体的な危険による。**ノートの可視性
// 判定はアプリケーション側にあり DB には無い**ため、
//
//	SELECT * FROM note ORDER BY "reactionsCount" DESC
//
// と書くと非公開ノートも入る。ランキングを作るつもりでフォロワー限定や
// ダイレクトの投稿を数えてしまう。本体のデータが要るときは [API] を使うこと
// (同じ集計でも可視性が自動的に効く)。
//
// 接続の `search_path` は自分の schema に固定されているので、素直に書いていれば
// 本体のテーブルは見えない。
//
// # トランザクションについて
//
// **ここへの書き込みと [API] の呼び出しは同じトランザクションに入れられない。**
// 境界を跨いだトランザクションはプラグインのストレージを mk-go の DB セッション
// に結合させてしまう。プラグインは冪等に書くこと。
type Storage interface {
	// DB returns the connection pool for this plugin's schema.
	//
	// **`*sql.DB` を返すのは意図的。** mk-go 内部は GORM を使っているが、それは
	// 本体の選択であって契約ではない。標準ライブラリの型に留めることで、
	// 本体が ORM を替えてもプラグインは壊れない。GORM を使いたい場合は
	// プラグイン側で `gorm.Open(postgres.New(postgres.Config{Conn: db}))` の
	// ように包むこと。
	DB() *sql.DB

	// Migrate applies migrations that have not been applied yet, in version
	// order. 起動のたびに呼んでよい (適用済みのものは飛ばす)。
	//
	// role 分割 (#2459) で複数プロセスが同時に起動しても直列化されるので、
	// 呼び出し側で排他を書く必要は無い。
	Migrate(ctx context.Context, migrations []Migration) error
}

// Migration is one versioned schema change.
//
// SQL をファイルではなくコードで宣言するのは、プラグインがビルド時に組み込まれる
// ため実行時にファイルが手元にあるとは限らないから。外部ファイルに置きたい場合は
// プラグイン側で `go:embed` して渡す。
type Migration struct {
	// Version must be positive and unique within the plugin. 一度適用した
	// version の SQL を書き換えても再実行されないので、変更は新しい version で
	// 行うこと。
	Version int

	// SQL is executed in a single transaction.
	SQL string
}
