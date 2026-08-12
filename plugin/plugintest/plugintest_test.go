package plugintest_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/plugin"
	"github.com/shiroha-a/mk/plugin/plugintest"
)

// samplePlugin exercises every part of the harness.
var samplePlugin = plugin.Definition{
	Name:       "sample",
	APIVersion: plugin.APIVersion,
	Migrations: []plugin.Migration{
		{Version: 1, SQL: `CREATE TABLE items (id serial PRIMARY KEY, label text)`},
	},
	Routes: func(ctx plugin.Context, r plugin.Router) error {
		var cfg struct {
			Label string `json:"label"`
		}
		cfg.Label = "既定"
		if err := ctx.Config().Unmarshal(&cfg); err != nil {
			return err
		}

		r.GET("/name", func(plugin.Request) (any, error) {
			return map[string]string{"name": ctx.Name(), "label": cfg.Label}, nil
		})
		r.POST("/echo", func(req plugin.Request) (any, error) {
			var body struct {
				Msg string `json:"msg"`
			}
			if err := req.Bind(&body); err != nil {
				return nil, err
			}
			return map[string]any{
				"msg": body.Msg, "user": req.UserID(),
				"id": req.Param("id"), "q": req.Query("q"),
			}, nil
		})
		r.POST("/boom", func(plugin.Request) (any, error) {
			return nil, errors.New("boom")
		})
		return nil
	},
	Jobs: func(_ plugin.Context, j plugin.Jobs) error {
		j.Handle("tick", func(_ context.Context, payload json.RawMessage) error {
			if string(payload) == `{"fail":true}` {
				return errors.New("failed")
			}
			return nil
		})
		j.Schedule("*/5 * * * *", "tick", map[string]any{"a": 1})
		return nil
	},
}

func TestRoutes_RegistersByMethodAndPath(t *testing.T) {
	h := plugintest.New(t).Routes(samplePlugin)

	assert.Contains(t, h, "GET /name")
	assert.Contains(t, h, "POST /echo")
}

func TestCall_PassesRequestFields(t *testing.T) {
	h := plugintest.New(t).Routes(samplePlugin)

	res, err := h.Call(t, "POST /echo", plugintest.Request{
		UserID: "u1",
		Body:   `{"msg":"hi"}`,
		Params: map[string]string{"id": "abc"},
		Query:  map[string]string{"q": "zzz"},
	})
	require.NoError(t, err)

	m := res.(map[string]any)
	assert.Equal(t, "hi", m["msg"])
	assert.Equal(t, "u1", m["user"])
	assert.Equal(t, "abc", m["id"])
	assert.Equal(t, "zzz", m["q"])
}

// Body 未指定でも Bind が壊れないこと (空 body を明示しないテストが書ける)。
func TestCall_EmptyBodyIsUsable(t *testing.T) {
	h := plugintest.New(t).Routes(samplePlugin)

	res, err := h.Call(t, "POST /echo", plugintest.Request{UserID: "u1"})
	require.NoError(t, err)
	assert.Empty(t, res.(map[string]any)["msg"])
}

func TestCall_PropagatesHandlerError(t *testing.T) {
	h := plugintest.New(t).Routes(samplePlugin)

	_, err := h.Call(t, "POST /boom", plugintest.Request{})
	require.Error(t, err)
}

// 設定は Unmarshal に渡る。未設定なら呼び出し側の既定値が残る。
func TestWithConfig(t *testing.T) {
	withCfg := plugintest.New(t).WithConfig(map[string]any{"label": "設定値"}).Routes(samplePlugin)
	res, err := withCfg.Call(t, "GET /name", plugintest.Request{})
	require.NoError(t, err)
	assert.Equal(t, "設定値", res.(map[string]string)["label"])

	noCfg := plugintest.New(t).Routes(samplePlugin)
	res, err = noCfg.Call(t, "GET /name", plugintest.Request{})
	require.NoError(t, err)
	assert.Equal(t, "既定", res.(map[string]string)["label"])
}

func TestWithName(t *testing.T) {
	h := plugintest.New(t).WithName("mine").Routes(samplePlugin)

	res, err := h.Call(t, "GET /name", plugintest.Request{})
	require.NoError(t, err)
	assert.Equal(t, "mine", res.(map[string]string)["name"])

	// 既定は "test"。
	h2 := plugintest.New(t).Routes(samplePlugin)
	res, err = h2.Call(t, "GET /name", plugintest.Request{})
	require.NoError(t, err)
	assert.Equal(t, "test", res.(map[string]string)["name"])
}

func TestJobs_CapturesHandlersAndSchedules(t *testing.T) {
	j := plugintest.New(t).Jobs(samplePlugin)

	require.Contains(t, j.Handlers, "tick")
	require.Len(t, j.Schedules, 1)
	assert.Equal(t, "*/5 * * * *", j.Schedules[0].Cron)
	assert.Equal(t, "tick", j.Schedules[0].Name)
	assert.Equal(t, map[string]any{"a": 1}, j.Schedules[0].Payload)
}

func TestJobs_Run(t *testing.T) {
	j := plugintest.New(t).Jobs(samplePlugin)

	require.NoError(t, j.Run(t, "tick", ""))
	require.Error(t, j.Run(t, "tick", `{"fail":true}`))
}

// Routes / Jobs を持たない Definition でも落ちない。
func TestEmptyDefinition(t *testing.T) {
	def := plugin.Definition{Name: "x", APIVersion: plugin.APIVersion}

	assert.Empty(t, plugintest.New(t).Routes(def))
	assert.Empty(t, plugintest.New(t).Jobs(def).Handlers)
}

func TestContext_GoRunsSynchronously(t *testing.T) {
	ran := false
	plugintest.New(t).Context().Go(func() { ran = true })
	// 非同期だと検証の前に終わっていない競合が入る。
	assert.True(t, ran)
}

func TestContext_APIDefaultsToNil(t *testing.T) {
	assert.Nil(t, plugintest.New(t).Context().API())

	api := &stubAPI{}
	assert.Same(t, api, plugintest.New(t).WithAPI(api).Context().API())
}

type stubAPI struct{}

func (s *stubAPI) Anonymous() plugin.Caller    { return nil }
func (s *stubAPI) AsUser(string) plugin.Caller { return nil }

func TestContext_LoggerIsUsable(t *testing.T) {
	assert.NotNil(t, plugintest.New(t).Context().Logger())
}

// DB 未設定で Migrate を呼ぶと、理由が分かるエラーになる。
func TestStorage_WithoutDB(t *testing.T) {
	err := plugintest.New(t).Context().Storage().Migrate(context.Background(),
		[]plugin.Migration{{Version: 1, SQL: "SELECT 1"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithDB")
}

// 壊れた JSON を渡したら Bind がエラーになる (握り潰さない)。
func TestCall_MalformedBodyIsError(t *testing.T) {
	h := plugintest.New(t).Routes(samplePlugin)

	_, err := h.Call(t, "POST /echo", plugintest.Request{Body: `{"msg":`})
	require.Error(t, err)
}

// Ctx を渡すとハンドラ側の Context() に届くこと。
func TestCall_PassesContext(t *testing.T) {
	type ctxKey struct{}
	def := plugin.Definition{
		Name: "c", APIVersion: plugin.APIVersion,
		Routes: func(_ plugin.Context, r plugin.Router) error {
			r.POST("/ctx", func(req plugin.Request) (any, error) {
				return req.Context().Value(ctxKey{}), nil
			})
			return nil
		},
	}
	h := plugintest.New(t).Routes(def)

	res, err := h.Call(t, "POST /ctx", plugintest.Request{
		Ctx: context.WithValue(context.Background(), ctxKey{}, "v"),
	})
	require.NoError(t, err)
	assert.Equal(t, "v", res)

	// 未指定なら Background になる (nil にしない)。
	res, err = h.Call(t, "POST /ctx", plugintest.Request{})
	require.NoError(t, err)
	assert.Nil(t, res)
}

// 変換できない設定はエラーとして返る (黙って空にしない)。
func TestConfig_UnconvertibleValueIsError(t *testing.T) {
	var v struct{}
	err := plugintest.New(t).
		WithConfig(map[string]any{"ch": make(chan int)}).
		Context().Config().Unmarshal(&v)
	require.Error(t, err)
}

// --- DB を使う経路 ---

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

const testSchema = "plugin_plugintest"

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	base := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		envOr("TEST_DB_HOST", "localhost"), envOr("TEST_DB_PORT", "5432"),
		envOr("TEST_DB_USER", "mk"), envOr("TEST_DB_PASS", "mk"),
		envOr("TEST_DB_NAME", "misskey_test"))

	admin, err := sql.Open("pgx", base)
	require.NoError(t, err)
	defer admin.Close() //nolint:errcheck // 使い捨て
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
	return db
}

func TestWithDB_AppliesMigrations(t *testing.T) {
	db := testDB(t)
	plugintest.New(t).WithDB(db).Routes(samplePlugin)

	var n int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'items'`,
		testSchema).Scan(&n))
	assert.Equal(t, 1, n)
}

// **本番と同じ適用管理をすること。** 素朴に実行するフェイクだと 2 回目で
// "already exists" になり、Routes と Jobs を続けて呼べない。
func TestWithDB_MigrationsAreIdempotent(t *testing.T) {
	db := testDB(t)

	plugintest.New(t).WithDB(db).Routes(samplePlugin)
	plugintest.New(t).WithDB(db).Jobs(samplePlugin) // 2 回目
	plugintest.New(t).WithDB(db).Routes(samplePlugin)
}

// プラグインは渡した接続をそのまま受け取る (差し替えたフェイクではない)。
func TestWithDB_HandsThroughTheConnection(t *testing.T) {
	db := testDB(t)
	assert.Same(t, db, plugintest.New(t).WithDB(db).Context().Storage().DB())
}
