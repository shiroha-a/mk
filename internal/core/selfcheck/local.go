package selfcheck

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// LocalDeps are the dependencies the local checks inspect. どれも nil 可で、
// 渡されなかったものは skip になる (サーバー未起動でも config 検査だけ回せる
// ようにするため)。
type LocalDeps struct {
	DB *gorm.DB
	// DBErr は接続を開く時点で失敗した場合の理由。**「未配線」と「繋がらない」を
	// 区別する**ために持つ。両方 skip にすると、DB が落ちているのに
	// 「未配線」と表示されて運用者を誤誘導する。
	DBErr error
	Redis redis.UniversalClient
	// MigrationCount は同梱している up migration の本数。DB に記録された
	// version と突き合わせる。
	MigrationCount int
}

// CheckDatabase verifies connectivity and migration state.
//
// migration の適用漏れは**起動はするが一部機能だけ壊れる**という形で出るので、
// 起動できたことをもって正常とみなせない。
func CheckDatabase(ctx context.Context, deps LocalDeps) Result {
	const name = "database"
	if deps.DBErr != nil {
		return failResult(name, fmt.Sprintf("接続できない: %v", deps.DBErr),
			"PostgreSQL が起動しているか、`db` のホスト・認証情報を確認する")
	}
	if deps.DB == nil {
		return skipResult(name, "DB が未配線")
	}
	sqlDB, err := deps.DB.DB()
	if err != nil {
		return failResult(name, fmt.Sprintf("接続を取得できない: %v", err), "`db` の設定を確認する")
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return failResult(name, fmt.Sprintf("ping 失敗: %v", err),
			"PostgreSQL が起動しているか、`db` のホスト・認証情報を確認する")
	}

	var row struct {
		Version int64
		Dirty   bool
	}
	if err := deps.DB.WithContext(ctx).
		Raw(`SELECT version, dirty FROM schema_migrations LIMIT 1`).
		Scan(&row).Error; err != nil {
		return failResult(name, "schema_migrations を読めない",
			"`make migrate-up` を実行してマイグレーションを適用する")
	}
	if row.Dirty {
		return failResult(name, fmt.Sprintf("migration が dirty (version %d)", row.Version),
			"前回のマイグレーションが中断している。失敗した version を手当てしてから `schema_migrations.dirty` を false に戻す")
	}
	if deps.MigrationCount > 0 && row.Version < int64(deps.MigrationCount) {
		return failResult(name,
			fmt.Sprintf("適用済み version %d / 同梱 %d", row.Version, deps.MigrationCount),
			"`make migrate-up` で未適用のマイグレーションを当てる。適用漏れは「起動はするが一部機能だけ壊れる」形で出る")
	}
	return okResult(name, fmt.Sprintf("接続 ok / migration version %d", row.Version))
}

// CheckRootUser verifies that meta.rootUserId is set.
//
// **未設定だと `admin/accounts/create` が未認証で開いたままになる。** あの
// endpoint は `meta.rootUserId == nil && 未認証` を初回セットアップとみなす。
// `rootUserId` を書くのは signup service の初回セットアップ分岐だけで、公開
// `/api/signup` は本番でそこを通らないため、**登録を開放して通常の signup で
// 最初のアカウントを作ると永久に NULL のまま**になる。`update-meta` は
// `rootUserId` を protected として落とすので、管理者が手で埋めることもできない。
//
// handler 側にも「ローカル利用者が既に居るなら初回セットアップ扱いにしない」
// ガードを入れてあるので窓自体は閉じるが、**そこに依存している状態は運用者に
// 見えるべき**なので警告する。root が居ないと `IsAdministrator` の root 判定も
// 効かない。
func CheckRootUser(ctx context.Context, deps LocalDeps) Result {
	const name = "root user"
	if deps.DB == nil {
		return skipResult(name, "DB が未配線")
	}
	var rootID *string
	if err := deps.DB.WithContext(ctx).
		Raw(`SELECT "rootUserId" FROM meta LIMIT 1`).
		Scan(&rootID).Error; err != nil {
		return failResult(name, "meta を読めない", "DB の状態を確認する")
	}
	if rootID == nil || *rootID == "" {
		return failResult(name, "meta.rootUserId が未設定",
			"root を指名し直すには DB を直接更新する (`update-meta` は rootUserId を受け付けない)。未設定のままだと root 判定が効かず、初回セットアップ窓の判定がローカル利用者数のガードだけに依存する")
	}
	return okResult(name, "meta.rootUserId 設定済み")
}

// CheckRedis verifies connectivity.
func CheckRedis(ctx context.Context, deps LocalDeps) Result {
	const name = "redis"
	if deps.Redis == nil {
		return skipResult(name, "Redis が未配線")
	}
	if err := deps.Redis.Ping(ctx).Err(); err != nil {
		return failResult(name, fmt.Sprintf("ping 失敗: %v", err),
			"Redis が起動しているか、`redis` のホスト・認証情報を確認する")
	}
	return okResult(name, "接続 ok")
}

// Run executes every check and returns the aggregate report.
//
// 検査は**止めずに全部走らせる**。最初の失敗で打ち切ると、運用者は直しては
// 走らせ直すのを繰り返すことになる。
func Run(ctx context.Context, checker *Checker, deps LocalDeps) Report {
	results := []Result{checker.CheckConfig()}
	results = append(results,
		CheckDatabase(ctx, deps),
		CheckRootUser(ctx, deps),
		CheckRedis(ctx, deps),
		checker.CheckWebFinger(ctx),
		checker.CheckNodeInfo(ctx),
		checker.CheckActor(ctx),
		checker.CheckTLS(ctx),
	)
	return newReport(results)
}
