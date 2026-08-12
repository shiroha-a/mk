// Package plugintest provides fakes for testing mk-go plugins.
//
// プラグインのルートやジョブを、mk-go を起動せずに直接呼べるようにする。
//
//	func TestMyRoute(t *testing.T) {
//	    h := plugintest.New(t).
//	        WithConfig(map[string]any{"apiKey": "test"}).
//	        Routes(myplugin.Plugin)
//
//	    res, err := h["POST /me"].Call(plugintest.Request{UserID: "u1", Body: `{}`})
//	    ...
//	}
//
// # なぜ用意するか
//
// [plugin.Context] 系はすべて interface なので、作者が自分でフェイクを書くこと
// もできる。ただし実際に書いてみると 80 行ほど必要で、その分だけ「テストを
// 書かない」方に倒れる (#2484 で実際のプラグインを書いて確かめた)。
//
// # ストレージについて
//
// [Harness.WithDB] を使うと、渡した接続をそのままプラグインへ渡し、
// [plugin.Definition.Migrations] を適用する。**フェイクの DB は用意しない** —
// SQL の挙動を模した偽物は本物とずれ、通ったのに本番で落ちる形のテストになる。
// DB を使うプラグインは実際の PostgreSQL に対してテストすること。
package plugintest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/shiroha-a/mk/internal/pluginstore"
	"github.com/shiroha-a/mk/plugin"
)

// Harness builds the fake environment a plugin sees.
type Harness struct {
	t       *testing.T
	name    string
	config  map[string]any
	db      *sql.DB
	api     plugin.API
	migrate bool
}

// New starts a harness. The plugin name defaults to "test".
func New(t *testing.T) *Harness {
	t.Helper()
	return &Harness{t: t, name: "test", config: map[string]any{}}
}

// WithName sets the plugin name reported by Context.Name.
func (h *Harness) WithName(name string) *Harness {
	h.name = name
	return h
}

// WithConfig sets the settings Context.Config returns.
//
// **キーは設定ファイルに書いたままの形で渡してよい。** 本番では Viper が
// キーを小文字化するが、[plugin.Config] は encoding/json 経由で構造体に入れる
// ため大文字小文字を問わない。
func (h *Harness) WithConfig(values map[string]any) *Harness {
	h.config = values
	return h
}

// WithDB hands the plugin a real database connection and applies its
// migrations. 接続の後片付けは呼び出し側の責務。
func (h *Harness) WithDB(db *sql.DB) *Harness {
	h.db = db
	h.migrate = true
	return h
}

// WithAPI sets what Context.API returns. 未設定なら API() は nil を返すので、
// mk-go の API を呼ぶプラグインは自分でフェイクを渡すこと.
func (h *Harness) WithAPI(api plugin.API) *Harness {
	h.api = api
	return h
}

// Context returns the fake plugin.Context.
func (h *Harness) Context() plugin.Context {
	return &fakeContext{h: h}
}

// Routes runs def.Routes and returns the registered handlers, keyed by
// "<METHOD> <path>" (e.g. "POST /me").
func (h *Harness) Routes(def plugin.Definition) Handlers {
	h.t.Helper()
	h.applyMigrations(def)

	r := &fakeRouter{handlers: Handlers{}}
	if def.Routes == nil {
		return r.handlers
	}
	if err := def.Routes(h.Context(), r); err != nil {
		h.t.Fatalf("plugintest: Routes に失敗しました: %v", err)
	}
	return r.handlers
}

// Jobs runs def.Jobs and returns the registered job handlers and schedules.
func (h *Harness) Jobs(def plugin.Definition) *JobSet {
	h.t.Helper()
	h.applyMigrations(def)

	j := &fakeJobs{set: &JobSet{Handlers: map[string]plugin.JobHandler{}}}
	if def.Jobs == nil {
		return j.set
	}
	if err := def.Jobs(h.Context(), j); err != nil {
		h.t.Fatalf("plugintest: Jobs に失敗しました: %v", err)
	}
	return j.set
}

// applyMigrations mirrors what mk-go does before the callbacks run.
func (h *Harness) applyMigrations(def plugin.Definition) {
	h.t.Helper()
	if !h.migrate || len(def.Migrations) == 0 {
		return
	}
	if err := h.Context().Storage().Migrate(context.Background(), def.Migrations); err != nil {
		h.t.Fatalf("plugintest: migration に失敗しました: %v", err)
	}
}

// Handlers maps "<METHOD> <path>" to a registered handler.
type Handlers map[string]plugin.Handler

// Call invokes the handler for key with req. 未登録のキーはテストを落とす
// (typo で「呼んだつもり」になるのを防ぐ)。
func (hs Handlers) Call(t *testing.T, key string, req Request) (any, error) {
	t.Helper()
	h, err := hs.lookup(key)
	if err != nil {
		t.Fatal(err)
	}
	return h(req.build())
}

// lookup は Call から切り出した部分。testing.T は struct で外部から差し替え
// られないため、Fatal を通る経路はテストできない。判定だけを分けて検証する。
func (hs Handlers) lookup(key string) (plugin.Handler, error) {
	h, ok := hs[key]
	if !ok {
		return nil, fmt.Errorf("plugintest: %q は登録されていません (登録済み: %v)", key, hs.keys())
	}
	return h, nil
}

func (hs Handlers) keys() []string {
	out := make([]string, 0, len(hs))
	for k := range hs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// JobSet holds what a plugin registered through plugin.Jobs.
type JobSet struct {
	// Handlers maps job name to handler.
	Handlers map[string]plugin.JobHandler
	// Schedules records the cron entries, in registration order.
	Schedules []Schedule
}

// Schedule is one registered cron entry.
type Schedule struct {
	Cron    string
	Name    string
	Payload any
}

// Run invokes a registered job handler.
func (j *JobSet) Run(t *testing.T, name string, payload string) error {
	t.Helper()
	h, err := j.lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	if payload == "" {
		payload = "null"
	}
	return h(context.Background(), json.RawMessage(payload))
}

// lookup は Run から切り出した部分 (Handlers.lookup と同じ理由)。
func (j *JobSet) lookup(name string) (plugin.JobHandler, error) {
	h, ok := j.Handlers[name]
	if !ok {
		return nil, fmt.Errorf("plugintest: ジョブ %q は登録されていません", name)
	}
	return h, nil
}

// Request describes one fake HTTP request.
type Request struct {
	// UserID is what plugin.Request.UserID returns. 空文字は未認証。
	UserID string
	// Body is the raw JSON request body.
	Body string
	// Params are path parameters.
	Params map[string]string
	// Query are query-string values.
	Query map[string]string
	// Ctx overrides the request context.
	Ctx context.Context
}

func (r Request) build() plugin.Request {
	ctx := r.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	body := r.Body
	if body == "" {
		body = "{}"
	}
	return &fakeRequest{ctx: ctx, userID: r.UserID, body: body, params: r.Params, query: r.Query}
}

// --- 実装 ---

type fakeContext struct{ h *Harness }

func (c *fakeContext) Name() string          { return c.h.name }
func (c *fakeContext) Logger() *slog.Logger  { return slog.Default() }
func (c *fakeContext) API() plugin.API       { return c.h.api }
func (c *fakeContext) Config() plugin.Config { return &fakeConfig{values: c.h.config} }
func (c *fakeContext) Storage() plugin.Storage {
	return &fakeStorage{db: c.h.db}
}

// Go runs fn synchronously. テストで非同期にすると、検証の前に終わっていない
// 競合が入る。
func (c *fakeContext) Go(fn func()) { fn() }

type fakeStorage struct{ db *sql.DB }

func (s *fakeStorage) DB() *sql.DB { return s.db }

// Migrate applies the plugin's migrations.
//
// **本番と同じ実装を使う。** 素朴に実行するフェイクにすると適用済みの管理が
// 無くなり、2 回目で "already exists" になる (実際に踏んだ)。本番と違う挙動の
// フェイクは、テストが通ったのに本番で落ちる形の嘘をつく。
func (s *fakeStorage) Migrate(ctx context.Context, ms []plugin.Migration) error {
	if s.db == nil {
		return fmt.Errorf("plugintest: DB が設定されていません (WithDB を使ってください)")
	}
	conv := make([]pluginstore.Migration, len(ms))
	for i, m := range ms {
		conv[i] = pluginstore.Migration{Version: m.Version, SQL: m.SQL}
	}
	return pluginstore.Apply(ctx, s.db, conv)
}

type fakeConfig struct{ values map[string]any }

func (c *fakeConfig) Unmarshal(v any) error {
	if len(c.values) == 0 {
		return nil
	}
	raw, err := json.Marshal(c.values)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

type fakeRouter struct{ handlers Handlers }

func (r *fakeRouter) GET(path string, h plugin.Handler)  { r.handlers["GET "+path] = h }
func (r *fakeRouter) POST(path string, h plugin.Handler) { r.handlers["POST "+path] = h }

type fakeJobs struct{ set *JobSet }

func (j *fakeJobs) Handle(name string, h plugin.JobHandler) { j.set.Handlers[name] = h }

func (j *fakeJobs) Schedule(cron string, name string, payload any) {
	j.set.Schedules = append(j.set.Schedules, Schedule{Cron: cron, Name: name, Payload: payload})
}

type fakeRequest struct {
	ctx    context.Context
	userID string
	body   string
	params map[string]string
	query  map[string]string
}

func (r *fakeRequest) Context() context.Context { return r.ctx }
func (r *fakeRequest) UserID() string           { return r.userID }
func (r *fakeRequest) Param(k string) string    { return r.params[k] }
func (r *fakeRequest) Query(k string) string    { return r.query[k] }

func (r *fakeRequest) Bind(v any) error {
	dec := json.NewDecoder(strings.NewReader(r.body))
	if err := dec.Decode(v); err != nil && err != io.EOF {
		return err
	}
	return nil
}
