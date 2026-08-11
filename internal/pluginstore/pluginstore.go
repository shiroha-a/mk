// Package pluginstore gives each plugin its own PostgreSQL schema (#2481).
//
// # なぜ schema を分けるのか
//
// mk-go の migration は連番 (`000074_...`) で、プラグインをこの列に載せられない。
// 2 つのプラグインが同時に次の番号を要求して衝突する。schema を分ければ、各
// プラグインが本体の連番と干渉せず独立した migration を持てる。
//
// 手法は #2450 でテスト用に導入したものと同じ (`search_path` を DSN に張る)。
// pgx は未知の keyword を runtime parameter として送るので、接続ごとに適用され、
// pool が新しい接続を張っても schema が変わらない。
//
// # 分離の強さについて
//
// `search_path` は**事故を防ぐ仕組みであって、権限による強制ではない**。
// プラグインが `public.note` のように明示的に修飾すれば mk-go のテーブルに
// 到達できる。プラグインは同一プロセスの Go コードで、そもそも設定ファイルから
// 認証情報を読めるため、ここを権限で塞いでも意味は薄い。
//
// 重要なのは「素直に書いたら本体のテーブルに触れない」ことで、これは満たす。
// **ノートの可視性判定はアプリケーション側の 4 箇所にあり DB には無い**ので、
// `SELECT * FROM note` が通ってしまうと非公開ノートが混ざる。
package pluginstore

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql 用の pgx driver
)

// SchemaPrefix namespaces plugin schemas so they never collide with mk-go's
// own objects (which live in `public`).
const SchemaPrefix = "plugin_"

// defaultMaxConns bounds one plugin's pool.
//
// プラグインごとに専用 pool を持つので、数が増えると接続数が線形に増える。
// 共有 pool でクエリごとに `SET search_path` する方式は、設定が同じ接続を使う
// 他の利用者に漏れる古典的なバグ源なので採らない。代わりに 1 つあたりを小さく
// 抑える。
const defaultMaxConns = 4

// Store is one plugin's storage handle.
type Store struct {
	name   string
	schema string
	db     *sql.DB
}

// Open creates the plugin's schema if needed and returns a pool pinned to it.
//
// baseDSN は mk-go 本体と同じ接続文字列 (config.Config.DSN())。
func Open(baseDSN, name string, maxConns int) (*Store, error) {
	schema, err := SchemaName(name)
	if err != nil {
		return nil, err
	}
	if maxConns <= 0 {
		maxConns = defaultMaxConns
	}

	// schema を作るための接続は search_path を張らない (まだ存在しないため)。
	boot, err := sql.Open("pgx", baseDSN)
	if err != nil {
		return nil, fmt.Errorf("pluginstore: %s の接続を開けません: %w", name, err)
	}
	defer boot.Close() //nolint:errcheck // bootstrap 用の使い捨て

	// identifier は placeholder にできないので、SchemaName で検証済みの値だけを
	// 埋め込む。
	if _, err := boot.Exec(`CREATE SCHEMA IF NOT EXISTS ` + quoteIdent(schema)); err != nil {
		return nil, fmt.Errorf("pluginstore: schema %s を作成できません: %w", schema, err)
	}

	db, err := sql.Open("pgx", baseDSN+" search_path="+schema)
	if err != nil {
		return nil, fmt.Errorf("pluginstore: %s の接続を開けません: %w", name, err)
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)

	return &Store{name: name, schema: schema, db: db}, nil
}

// DB returns the pool. search_path はこの schema に固定されている。
func (s *Store) DB() *sql.DB { return s.db }

// Schema returns the PostgreSQL schema name.
func (s *Store) Schema() string { return s.schema }

// Close releases the pool.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Migration is one versioned schema change.
type Migration struct {
	Version int
	SQL     string
}

// Migrate applies migrations that have not run yet, in version order.
//
// **プロセスが複数あっても安全に動く必要がある。** role 分割 (#2459) で server と
// queue が別プロセスになると、両方が同時に起動して同じ migration を流そうとする。
// advisory lock で直列化する。
func (s *Store) Migrate(ctx context.Context, migrations []Migration) error {
	if len(migrations) == 0 {
		return nil
	}
	ordered, err := validateMigrations(migrations)
	if err != nil {
		return fmt.Errorf("plugin %s: %w", s.name, err)
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("plugin %s: migration 用の接続を取得できません: %w", s.name, err)
	}
	defer conn.Close() //nolint:errcheck // 解放のみ

	lockKey := advisoryLockKey(s.schema)
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		return fmt.Errorf("plugin %s: migration のロックを取得できません: %w", s.name, err)
	}
	defer func() {
		// 解放に失敗しても migration の成否は変えない (接続を閉じれば自動で外れる)。
		_, _ = conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, lockKey)
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version bigint PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("plugin %s: migration 管理表を作成できません: %w", s.name, err)
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return fmt.Errorf("plugin %s: %w", s.name, err)
	}

	for _, m := range ordered {
		if _, done := applied[m.Version]; done {
			continue
		}
		// 1 migration = 1 transaction。途中で失敗したものが半分だけ適用された
		// 状態で記録されると、次回の起動で復旧できない。
		if err := applyOne(ctx, conn, m); err != nil {
			return fmt.Errorf("plugin %s: migration %d に失敗しました: %w", s.name, m.Version, err)
		}
	}
	return nil
}

// applyOne runs one migration and records it atomically.
func applyOne(ctx context.Context, conn *sql.Conn, m Migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // commit 済みなら no-op

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, m.Version); err != nil {
		return err
	}
	return tx.Commit()
}

// appliedVersions reads the versions already recorded.
func appliedVersions(ctx context.Context, conn *sql.Conn) (map[int]struct{}, error) {
	rows, err := conn.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("適用済み migration を読めません: %w", err)
	}
	defer rows.Close() //nolint:errcheck // 読み取りのみ

	out := map[int]struct{}{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("適用済み migration を読めません: %w", err)
		}
		out[v] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("適用済み migration を読めません: %w", err)
	}
	return out, nil
}

// validateMigrations sorts by version and rejects duplicates.
//
// 重複した version を許すと、片方だけ適用されて「適用済み」と記録され、もう
// 片方が永久に流れない。順序も固定する (宣言順に依存させない)。
func validateMigrations(in []Migration) ([]Migration, error) {
	out := make([]Migration, len(in))
	copy(out, in)
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })

	for i, m := range out {
		if m.Version <= 0 {
			return nil, fmt.Errorf("migration の version は 1 以上である必要があります (%d)", m.Version)
		}
		if strings.TrimSpace(m.SQL) == "" {
			return nil, fmt.Errorf("migration %d の SQL が空です", m.Version)
		}
		if i > 0 && out[i-1].Version == m.Version {
			return nil, fmt.Errorf("migration の version %d が重複しています", m.Version)
		}
	}
	return out, nil
}

// SchemaName maps a plugin name to its schema, validating the identifier.
//
// identifier は SQL の placeholder にできないため文字列として埋め込むしかない。
// plugin.Definition 側でも名前を検証しているが、**ここでも検証する**: 埋め込みの
// 直前で確かめないと、呼び出し経路が増えたときに検証の無い経路ができる。
func SchemaName(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("pluginstore: プラグイン名が空です")
	}
	if len(name) > 32 {
		return "", fmt.Errorf("pluginstore: プラグイン名 %q が長すぎます", name)
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(name)-1:
		default:
			return "", fmt.Errorf("pluginstore: プラグイン名 %q が不正です (小文字英数字とハイフンのみ)", name)
		}
	}
	// ハイフンは識別子として引用が必要になるので、schema 名では _ に倒す。
	return SchemaPrefix + strings.ReplaceAll(name, "-", "_"), nil
}

// quoteIdent wraps an identifier in double quotes.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// advisoryLockKey derives a stable lock id from the schema name.
func advisoryLockKey(schema string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(schema))
	// 上位ビットを落として正の値にする (負値でも動くが、ログで読みにくい)。
	return int64(h.Sum64() & 0x7fffffffffffffff)
}
