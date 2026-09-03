package peercache_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/plugin"
	"github.com/shiroha-a/mk/plugin/peercache"
)

const testSchema = "plugin_peercache"

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// testDB opens the plugin schema this package owns.
//
// **専用 schema を使う。** 他のパッケージのテストと同じ DB を共有するので、
// テーブルを作り直す側が相手の前提を壊さないようにする (#2450 と同じ理由)。
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	base := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		envOr("TEST_DB_HOST", "localhost"), envOr("TEST_DB_PORT", "5432"),
		envOr("TEST_DB_USER", "mk"), envOr("TEST_DB_PASS", "mk"),
		envOr("TEST_DB_NAME", "misskey_test"))

	admin, err := sql.Open("pgx", base)
	require.NoError(t, err)
	defer admin.Close() //nolint:errcheck // 使い捨て
	if err := admin.Ping(); err != nil {
		t.Skipf("PostgreSQL に繋がりません: %v", err)
	}
	_, err = admin.Exec(`DROP SCHEMA IF EXISTS ` + testSchema + ` CASCADE`)
	require.NoError(t, err)
	_, err = admin.Exec(`CREATE SCHEMA ` + testSchema)
	require.NoError(t, err)

	db, err := sql.Open("pgx", base+" search_path="+testSchema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Close()
		if a, err := sql.Open("pgx", base); err == nil {
			_, _ = a.Exec(`DROP SCHEMA IF EXISTS ` + testSchema + ` CASCADE`)
			_ = a.Close()
		}
	})

	for _, m := range peercache.Migrations(1) {
		_, err := db.Exec(m.SQL)
		require.NoErrorf(t, err, "migration %d", m.Version)
	}
	return db
}

// fakeContext is the slice of plugin.Context the cache uses.
type fakeContext struct {
	plugin.Context
	peer *fakePeer
}

func (c *fakeContext) Peer() plugin.Peer        { return c.peer }
func (c *fakeContext) Logger() *slog.Logger     { return slog.Default() }
func (c *fakeContext) Go(fn func())             { fn() } // テストでは同期に走らせる
func newFakeContext(p *fakePeer) plugin.Context { return &fakeContext{peer: p} }

type sent struct {
	host    string
	payload any
}

type fakePeer struct {
	plugin.Peer
	has    map[string]bool
	sends  []sent
	next   int
	err    error
	hasErr bool
}

func (p *fakePeer) Has(context.Context, string) (bool, error) {
	if p.hasErr {
		return false, nil
	}
	return true, nil
}

func (p *fakePeer) Send(_ context.Context, host string, payload any) (string, error) {
	if p.has != nil && !p.has[host] {
		return "", fmt.Errorf("持っていません")
	}
	if p.err != nil {
		return "", p.err
	}
	p.next++
	p.sends = append(p.sends, sent{host: host, payload: payload})
	return fmt.Sprintf("id%d", p.next), nil
}

func newCache(t *testing.T, db *sql.DB, p *fakePeer, opts ...func(*peercache.Options)) *peercache.Cache {
	t.Helper()
	o := peercache.Options{
		Context: newFakeContext(p),
		DB:      db,
		Request: func(key string) any { return map[string]string{"key": key} },
	}
	for _, fn := range opts {
		fn(&o)
	}
	c, err := peercache.New(o)
	require.NoError(t, err)
	return c
}

// **初回は空で返り、取り寄せは裏で走る。** 描画を待たせないのがこの型の目的。
func TestCache_FirstLookupIsEmptyAndAsks(t *testing.T) {
	db := testDB(t)
	p := &fakePeer{}
	c := newCache(t, db, p)
	ctx := context.Background()

	got, err := c.Lookup(ctx, "other.example", "alice")
	require.NoError(t, err)
	assert.Nil(t, got, "初回は空")
	require.Len(t, p.sends, 1, "取り寄せを出す")
	assert.Equal(t, "other.example", p.sends[0].host)
	assert.Equal(t, map[string]string{"key": "alice"}, p.sends[0].payload)

	// 応答が届いたら次から出る。
	require.NoError(t, c.Store(ctx, "id1", map[string]int{"score": 7}, true))
	got, err = c.Lookup(ctx, "other.example", "alice")
	require.NoError(t, err)
	assert.JSONEq(t, `{"score":7}`, string(got))
	assert.Len(t, p.sends, 1, "期限内なら問い合わせ直さない")
}

// **空振りも覚える。** 覚えないと、そのプラグインを使っていない利用者の
// プロフィールを開くたびに相手へ問い合わせることになる。
func TestCache_RemembersNegative(t *testing.T) {
	db := testDB(t)
	p := &fakePeer{}
	c := newCache(t, db, p)
	ctx := context.Background()

	_, err := c.Lookup(ctx, "other.example", "bob")
	require.NoError(t, err)
	require.Len(t, p.sends, 1)
	require.NoError(t, c.Store(ctx, "id1", nil, false))

	for i := 0; i < 3; i++ {
		got, err := c.Lookup(ctx, "other.example", "bob")
		require.NoError(t, err)
		assert.Nil(t, got)
	}
	assert.Len(t, p.sends, 1, "否定 TTL の間は問い合わせ直さない")
}

// **否定は肯定より短く覚える。** 同じ TTL にすると、相手が後から使い始めても
// 長いあいだ気付けない。TTL の使い分けそのものを見る。
func TestCache_NegativeTTLIsSeparate(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	short := func(o *peercache.Options) {
		o.TTL = time.Hour
		o.NegativeTTL = time.Nanosecond
	}

	t.Run("negative expires on its own TTL", func(t *testing.T) {
		p := &fakePeer{}
		c := newCache(t, db, p, short)
		_, err := c.Lookup(ctx, "neg.example", "bob")
		require.NoError(t, err)
		require.NoError(t, c.Store(ctx, "id1", nil, false))
		_, err = db.Exec(`DELETE FROM peer_cache_pending`)
		require.NoError(t, err)

		_, err = c.Lookup(ctx, "neg.example", "bob")
		require.NoError(t, err)
		assert.Len(t, p.sends, 2, "否定 TTL が切れたら取り直す")
	})

	t.Run("positive keeps the long TTL", func(t *testing.T) {
		p := &fakePeer{}
		c := newCache(t, db, p, short)
		_, err := c.Lookup(ctx, "pos.example", "bob")
		require.NoError(t, err)
		require.NoError(t, c.Store(ctx, "id1", map[string]int{"score": 1}, true))
		_, err = db.Exec(`DELETE FROM peer_cache_pending`)
		require.NoError(t, err)

		got, err := c.Lookup(ctx, "pos.example", "bob")
		require.NoError(t, err)
		assert.JSONEq(t, `{"score":1}`, string(got))
		assert.Len(t, p.sends, 1, "肯定 TTL の間は取り直さない")
	})
}

// 期限切れでも**古いものは返す**。取り直しは裏で進む。
func TestCache_StaleValueIsStillReturned(t *testing.T) {
	db := testDB(t)
	p := &fakePeer{}
	c := newCache(t, db, p, func(o *peercache.Options) { o.TTL = time.Nanosecond })
	ctx := context.Background()

	_, err := c.Lookup(ctx, "other.example", "carol")
	require.NoError(t, err)
	require.NoError(t, c.Store(ctx, "id1", map[string]int{"score": 1}, true))

	// askInterval の抑止に掛からないよう pending を消しておく。
	_, err = db.Exec(`DELETE FROM peer_cache_pending`)
	require.NoError(t, err)

	got, err := c.Lookup(ctx, "other.example", "carol")
	require.NoError(t, err)
	assert.JSONEq(t, `{"score":1}`, string(got), "期限切れでも古いものを返す")
	assert.Len(t, p.sends, 2, "裏で取り直す")
}

// **同じ相手に群がらない。** 否定 TTL は応答が届いてから効くので、届く前に
// 大勢が同じプロフィールを開くと問い合わせが重なる。
func TestCache_DoesNotPileUpAsks(t *testing.T) {
	db := testDB(t)
	p := &fakePeer{}
	c := newCache(t, db, p)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, err := c.Lookup(ctx, "other.example", "dave")
		require.NoError(t, err)
	}
	assert.Len(t, p.sends, 1, "応答待ちの間は 1 回だけ")
}

// 同じ交換の応答は 2 回来ることがある (送信がキューに載るため)。2 回目は
// 知らない id として静かに捨てる。
func TestCache_StoreIsIdempotent(t *testing.T) {
	db := testDB(t)
	p := &fakePeer{}
	c := newCache(t, db, p)
	ctx := context.Background()

	_, err := c.Lookup(ctx, "other.example", "erin")
	require.NoError(t, err)
	require.NoError(t, c.Store(ctx, "id1", map[string]int{"score": 3}, true))
	require.NoError(t, c.Store(ctx, "id1", map[string]int{"score": 99}, true), "2 回目もエラーにしない")

	got, err := c.Lookup(ctx, "other.example", "erin")
	require.NoError(t, err)
	assert.JSONEq(t, `{"score":3}`, string(got), "最初の応答が残る")

	// 知らない id も静かに捨てる。
	require.NoError(t, c.Store(ctx, "unknown", map[string]int{"score": 0}, true))
}

func TestCache_Sweep(t *testing.T) {
	db := testDB(t)
	c := newCache(t, db, &fakePeer{})
	ctx := context.Background()

	_, err := db.Exec(`
		INSERT INTO peer_cache (host, key, payload, fetched_at, expires_at)
		VALUES ('old.example', 'x', 'null', now() - interval '8 days', now());
		INSERT INTO peer_cache_pending (id, host, key, created_at)
		VALUES ('stale', 'old.example', 'x', now() - interval '2 days');
	`)
	require.NoError(t, err)

	require.NoError(t, c.Sweep(ctx))
	var snapshots, pendings int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM peer_cache`).Scan(&snapshots))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM peer_cache_pending`).Scan(&pendings))
	assert.Equal(t, 0, snapshots)
	assert.Equal(t, 0, pendings)
}

func TestNew_RequiresOptions(t *testing.T) {
	db := testDB(t)
	ok := peercache.Options{Context: newFakeContext(&fakePeer{}), DB: db, Request: func(string) any { return nil }}

	for _, tt := range []struct {
		name  string
		mutin func(*peercache.Options)
	}{
		{"no context", func(o *peercache.Options) { o.Context = nil }},
		{"no db", func(o *peercache.Options) { o.DB = nil }},
		{"no request", func(o *peercache.Options) { o.Request = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := ok
			tt.mutin(&o)
			_, err := peercache.New(o)
			assert.Error(t, err)
		})
	}

	c, err := peercache.New(ok)
	require.NoError(t, err)
	assert.NotNil(t, c)
}

// 応答の JSON が null なら、found が true でも否定として覚える
// (相手が「持っている」と答えつつ中身が無い場合)。
func TestCache_NullPayloadIsNegative(t *testing.T) {
	db := testDB(t)
	p := &fakePeer{}
	c := newCache(t, db, p)
	ctx := context.Background()

	_, err := c.Lookup(ctx, "other.example", "frank")
	require.NoError(t, err)
	require.NoError(t, c.Store(ctx, "id1", nil, true))

	var payload []byte
	require.NoError(t, db.QueryRow(
		`SELECT payload FROM peer_cache WHERE host='other.example' AND key='frank'`).Scan(&payload))
	assert.JSONEq(t, `null`, string(payload))

	got, err := c.Lookup(ctx, "other.example", "frank")
	require.NoError(t, err)
	assert.Nil(t, got)

	// **否定 TTL で覚えること。** 肯定 TTL で覚えると、中身が無い相手を
	// 長いあいだ問い合わせ直さなくなる。
	var seconds float64
	require.NoError(t, db.QueryRow(`
		SELECT extract(epoch from (expires_at - fetched_at)) FROM peer_cache
		WHERE host='other.example' AND key='frank'`).Scan(&seconds))
	assert.InDelta(t, peercache.DefaultNegativeTTL.Seconds(), seconds, 1)
}

var _ = json.RawMessage(nil)

// 相手が同じプラグインを持っていなければ、静かに諦める (普通のこと)。
func TestCache_SkipsHostWithoutPlugin(t *testing.T) {
	db := testDB(t)
	p := &fakePeer{hasErr: true}
	c := newCache(t, db, p)

	got, err := c.Lookup(context.Background(), "other.example", "alice")
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Empty(t, p.sends, "問い合わせを出さない")

	var pendings int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM peer_cache_pending`).Scan(&pendings))
	assert.Equal(t, 0, pendings, "記録も残さない")
}

// Send が失敗しても Lookup はエラーにしない (描画は続く)。
func TestCache_SendFailureIsNotFatal(t *testing.T) {
	db := testDB(t)
	p := &fakePeer{err: assertErr{}}
	c := newCache(t, db, p)

	got, err := c.Lookup(context.Background(), "other.example", "alice")
	require.NoError(t, err)
	assert.Nil(t, got)

	var pendings int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM peer_cache_pending`).Scan(&pendings))
	assert.Equal(t, 0, pendings, "出せなかった問い合わせは記録しない")
}

// DB が壊れているときはエラーを返す (黙って空を出さない)。
func TestCache_ReportsDBErrors(t *testing.T) {
	db := testDB(t)
	c := newCache(t, db, &fakePeer{})
	ctx := context.Background()

	_, err := db.Exec(`DROP TABLE peer_cache`)
	require.NoError(t, err)
	_, err = c.Lookup(ctx, "other.example", "alice")
	assert.Error(t, err, "読めないことを隠さない")

	_, err = db.Exec(`DROP TABLE peer_cache_pending`)
	require.NoError(t, err)
	assert.Error(t, c.Store(ctx, "id1", nil, false))
	assert.Error(t, c.Sweep(ctx))
}

// JSON にできない payload はエラーになる。
func TestCache_StoreRejectsUnmarshalablePayload(t *testing.T) {
	db := testDB(t)
	p := &fakePeer{}
	c := newCache(t, db, p)
	ctx := context.Background()

	_, err := c.Lookup(ctx, "other.example", "alice")
	require.NoError(t, err)
	err = c.Store(ctx, "id1", make(chan int), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JSON")
}

type assertErr struct{}

func (assertErr) Error() string { return "だめ" }
