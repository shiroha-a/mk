package maintenance

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/shiroha-a/mk/internal/misc/idnhost"
)

// HostColumn identifies one remote-host column to normalize.
//
// KeysetColumn is what the batch paginates on. It is usually the primary key;
// `instance_signature_capability` has no separate id so it paginates on the
// host column itself (see HostColumns).
type HostColumn struct {
	Table        string
	KeysetColumn string
	Column       string
}

// HostColumns lists every column that stores a remote host.
//
// **全部 WHERE 句で host 検索に使われている** (`note.userHost` は timeline の
// instance-mute、`following.*Host` は配送先の列挙、`emoji.host` は (name, host) の
// 逆引き、など)。1 つ漏らすとそこだけ非正規化のまま残り、同じ症状が別の場所で出る。
//
// 順序は依存関係ではなく、影響の大きい順 (acct 解決 → 連合ゲート → 表示)。
var HostColumns = []HostColumn{
	{Table: "user", KeysetColumn: "id", Column: "host"},
	{Table: "instance", KeysetColumn: "id", Column: "host"},
	{Table: "instance_signature_capability", KeysetColumn: "host", Column: "host"},
	{Table: "emoji", KeysetColumn: "id", Column: "host"},
	{Table: "following", KeysetColumn: "id", Column: "followerHost"},
	{Table: "following", KeysetColumn: "id", Column: "followeeHost"},
	{Table: "note", KeysetColumn: "id", Column: "userHost"},
	{Table: "drive_file", KeysetColumn: "id", Column: "userHost"},
	{Table: "user_profile", KeysetColumn: "userId", Column: "userHost"},
}

// HostBackfillResult reports the outcome of one keyset batch.
type HostBackfillResult struct {
	Scanned   int    // rows inspected this batch
	Updated   int    // rows whose host changed (0 when dryRun)
	Conflicts int    // rows skipped because the normalized value already exists
	LastKey   string // greatest keyset value seen; "" when no rows remained
}

// BackfillHostColumnBatch normalizes one keyset batch of a remote-host column
// to the form hostFromURI now stores (idna.ToASCII(lowercase), UTS#46).
//
// 既存行は `url.Parse` の生の host で保存されており、`Mixed.Example` のような
// 表記のまま残る。acct 解決は読み取り側の両当たり (hostCandidates) で救っているが、
// 連合ゲートや timeline の instance-mute は完全一致なので取りこぼす (#2706)。
//
// **PostgreSQL に IDNA 変換が無い**ので SQL migration では書けない。`lower()` だけ
// では `パイ.example` → `xn--eckve.example` を作れないため、app-level のバッチにする
// (`cmd/backfill-note-tags` と同じ理由)。
//
// **衝突はマージせず skip して数える。** 同じリモートが表記違いで 2 行に増えている
// 場合、正規化すると一意制約 (`user` の (usernameLower, host) / `instance` の host /
// `emoji` の (name, host)) に当たる。1 行に畳むには note / following / drive_file
// など多数の FK を張り替える必要があり、バッチでやるには危険すぎる。運用者が
// Conflicts の件数を見て手当てを判断する。
//
// 戻り値の LastKey を次回の fromKey に渡せば再開できる。冪等なので、途中で失敗しても
// 再実行して安全。dryRun は UPDATE をせず件数だけ数える。
func BackfillHostColumnBatch(db *gorm.DB, col HostColumn, fromKey string, batchSize int, dryRun bool) (HostBackfillResult, error) {
	if batchSize <= 0 {
		batchSize = 1000
	}
	if err := col.validate(); err != nil {
		return HostBackfillResult{}, err
	}
	type row struct {
		Key  string
		Host string
	}
	var rows []row
	// 列名は HostColumns の固定値しか通らない (validate 済み) が、識別子として
	// quote しておく。
	sel := fmt.Sprintf(`SELECT %s AS key, %s AS host FROM %s WHERE %s > ? AND %s IS NOT NULL AND %s <> '' ORDER BY %s ASC LIMIT ?`,
		quoteIdent(col.KeysetColumn), quoteIdent(col.Column), quoteIdent(col.Table),
		quoteIdent(col.KeysetColumn), quoteIdent(col.Column), quoteIdent(col.Column),
		quoteIdent(col.KeysetColumn))
	if err := db.Raw(sel, fromKey, batchSize).Scan(&rows).Error; err != nil {
		return HostBackfillResult{}, err
	}

	res := HostBackfillResult{}
	upd := fmt.Sprintf(`UPDATE %s SET %s = ? WHERE %s = ? AND %s = ?`,
		quoteIdent(col.Table), quoteIdent(col.Column),
		quoteIdent(col.KeysetColumn), quoteIdent(col.Column))
	for _, r := range rows {
		res.Scanned++
		res.LastKey = r.Key
		normalized := idnhost.Puny(r.Host)
		if normalized == r.Host {
			continue
		}
		if dryRun {
			res.Updated++
			continue
		}
		// **1 行ずつ独立させる。** 衝突した 1 行でバッチ全体を巻き戻さない。
		err := db.Exec(upd, normalized, r.Key, r.Host).Error
		if err != nil {
			if isUniqueViolation(err) {
				res.Conflicts++
				continue
			}
			return res, err
		}
		res.Updated++
	}
	return res, nil
}

// validate rejects anything not in HostColumns so the formatted SQL can only
// ever contain identifiers this package chose.
func (c HostColumn) validate() error {
	for _, known := range HostColumns {
		if known == c {
			return nil
		}
	}
	return fmt.Errorf("maintenance: unknown host column %s.%s", c.Table, c.Column)
}

// quoteIdent quotes a SQL identifier. 入力は HostColumns の固定値だけなので
// エスケープの必要は無いが、`user` のような予約語を含むので quote は要る。
func quoteIdent(name string) string { return `"` + name + `"` }

// isUniqueViolation reports whether err is a PostgreSQL unique_violation.
//
// GORM の TranslateError は有効化していないので、driver の error を直接見る。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return errors.Is(err, gorm.ErrDuplicatedKey)
}
