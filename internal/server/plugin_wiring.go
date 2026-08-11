package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/shiroha-a/mk/internal/pluginstore"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/plugin"
)

// pluginRoutePrefix namespaces every plugin endpoint.
//
// Misskey 本体のエンドポイント空間とは**必ず分ける**。upstream が将来同名の
// エンドポイントを追加したときに衝突すると、API 互換 (本プロジェクトの最優先
// 方針) が壊れるため。
const pluginRoutePrefix = "/plugin/"

// setupPlugins registers the given plugins.
//
// ロールに応じて登録先を出し分ける (#2459)。HTTP を担わないプロセスがルートを
// 登録したり、キューを担わないプロセスがジョブを登録したりしないようにする。
//
// 一覧と storage の生成を引数で受けるのは、グローバルレジストリや実 DB を直接
// 触るとテストから差し替えられないため。plugin.Registered() を呼ぶのも、実
// 接続を開くのも呼び出し側の責務。
func (s *Server) setupPlugins(api *echo.Group, plugins []plugin.Definition, openStorage pluginStorageOpener) error {
	if len(plugins) == 0 {
		return nil
	}
	for _, def := range plugins {
		settings := s.config.Plugins[def.Name]

		// **無効なプラグインは登録しない。** ビルドには含まれたままなので、
		// 問題が起きたプラグインを再ビルドせず再起動だけで止められる (#2482)。
		// 障害対応中に再ビルドと再デプロイを要求する形では使い物にならない。
		if !pluginEnabled(settings) {
			slog.Info("plugin disabled", "name", def.Name, "version", def.Version)
			continue
		}

		pctx := &pluginContext{
			name: def.Name,
			logger: slog.With(
				slog.String("plugin", def.Name),
				slog.String("pluginVersion", def.Version),
			),
			api:    &pluginAPI{echo: s.echo, userRepo: s.userRepo, host: requestHostFor(s.config.URL)},
			config: pluginConfig(settings),
		}

		// ストレージはロールに関係なく渡す。Routes からも Jobs からも使うため。
		//
		// **起動時に開く。** 遅延させると、接続できない設定でも起動してしまい、
		// 最初のリクエストまで問題が表面化しない。
		storage, err := openStorage(def.Name)
		if err != nil {
			return fmt.Errorf("plugin %q: %w", def.Name, err)
		}
		pctx.storage = storage

		if def.Routes != nil && s.role.RunsServer() {
			r := &pluginRouter{group: api.Group(pluginRoutePrefix + def.Name)}
			if err := def.Routes(pctx, r); err != nil {
				return fmt.Errorf("plugin %q: ルート登録に失敗しました: %w", def.Name, err)
			}
		}
		if def.Jobs != nil && s.role.RunsQueue() {
			j := &pluginJobs{name: def.Name, server: s.queueServer, scheduler: s.queueScheduler}
			if err := def.Jobs(pctx, j); err != nil {
				return fmt.Errorf("plugin %q: ジョブ登録に失敗しました: %w", def.Name, err)
			}
			if err := j.err; err != nil {
				return fmt.Errorf("plugin %q: ジョブ登録に失敗しました: %w", def.Name, err)
			}
		}

		slog.Info("plugin loaded",
			"name", def.Name, "version", def.Version,
			"routes", def.Routes != nil && s.role.RunsServer(),
			"jobs", def.Jobs != nil && s.role.RunsQueue())
	}
	return nil
}

// requestHostFor extracts the Host header value to use for in-process API
// calls. 絶対 URL を組み立てる handler があるので、空のままにしない。
func requestHostFor(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "localhost"
	}
	return u.Host
}

// --- Context ---

type pluginContext struct {
	name    string
	logger  *slog.Logger
	api     plugin.API
	storage plugin.Storage
	config  plugin.Config
}

func (c *pluginContext) Name() string            { return c.name }
func (c *pluginContext) Logger() *slog.Logger    { return c.logger }
func (c *pluginContext) API() plugin.API         { return c.api }
func (c *pluginContext) Storage() plugin.Storage { return c.storage }
func (c *pluginContext) Config() plugin.Config   { return c.config }

// --- Config ---

// enabledKey is the one setting mk-go interprets itself.
const enabledKey = "enabled"

// pluginEnabled reports whether the plugin should be registered.
//
// **既定は有効。** ビルドに含めた時点で運営者は意図してそのプラグインを選んで
// いるので、動かすのに追加の設定を要求しない。`enabled: false` を書いたときだけ
// 止める。
//
// bool 以外が書かれていた場合も有効として扱う。設定の書き間違いで機能が黙って
// 消える方が、無効化が効かないより厄介なため。
func pluginEnabled(settings map[string]any) bool {
	v, ok := settings[enabledKey]
	if !ok {
		return true
	}
	b, ok := v.(bool)
	if !ok {
		return true
	}
	return b
}

// pluginConfig wraps the plugin's settings, minus the reserved key.
func pluginConfig(settings map[string]any) plugin.Config {
	rest := make(map[string]any, len(settings))
	for k, v := range settings {
		if k == enabledKey {
			continue
		}
		rest[k] = v
	}
	return &pluginConfigValue{settings: rest}
}

type pluginConfigValue struct {
	settings map[string]any
}

// Unmarshal decodes the settings into v.
//
// **JSON を経由する。** YAML から読んだ値は map[string]any になっており、
// 構造体へ入れるには変換が要る。encoding/json を使えば `json` タグという
// Go では馴染みのある方法で書けて、mapstructure のような追加の作法を
// プラグイン作者に要求しない。
func (c *pluginConfigValue) Unmarshal(v any) error {
	if len(c.settings) == 0 {
		// 何も書かれていなければ v を変更しない。呼び出し側が入れた既定値が
		// 残るようにする。
		return nil
	}
	raw, err := json.Marshal(c.settings)
	if err != nil {
		return fmt.Errorf("plugin: 設定を変換できません: %w", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("plugin: 設定を読み込めません: %w", err)
	}
	return nil
}

// --- Storage ---

// pluginStorageOpener creates the storage handle for one plugin.
type pluginStorageOpener func(name string) (plugin.Storage, error)

// dbBackedStorage returns the production opener. 開いた pool は Shutdown で
// 閉じられるよう hook に登録する。
func (s *Server) dbBackedStorage() pluginStorageOpener {
	return func(name string) (plugin.Storage, error) {
		store, err := pluginstore.Open(s.config.DSN(), name, 0)
		if err != nil {
			return nil, err
		}
		s.registerShutdownHook(func(context.Context) { _ = store.Close() })
		return &pluginStorage{store: store}, nil
	}
}

// pluginStorage adapts pluginstore.Store to the public interface.
//
// 公開面は `plugin.Migration` を受けるが内部は `pluginstore.Migration` を使う。
// 同じ形でも型を分けるのは、内部側に項目を足したくなったときに公開面を巻き込ま
// ないため。
type pluginStorage struct {
	store *pluginstore.Store
}

func (s *pluginStorage) DB() *sql.DB { return s.store.DB() }

func (s *pluginStorage) Migrate(ctx context.Context, migrations []plugin.Migration) error {
	conv := make([]pluginstore.Migration, len(migrations))
	for i, m := range migrations {
		conv[i] = pluginstore.Migration{Version: m.Version, SQL: m.SQL}
	}
	return s.store.Migrate(ctx, conv)
}

// Go runs fn with panic recovery.
//
// **プラグインが素の `go` を書くとプロセスごと落ちる。** Go は他 goroutine の
// panic を回収できず、echo の Recover middleware では止められない。
func (c *pluginContext) Go(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				c.logger.Error("plugin の goroutine が panic しました", "panic", r)
			}
		}()
		fn()
	}()
}

// --- Router ---

type pluginRouter struct {
	group *echo.Group
}

func (r *pluginRouter) GET(path string, h plugin.Handler)  { r.group.GET(path, wrapPluginHandler(h)) }
func (r *pluginRouter) POST(path string, h plugin.Handler) { r.group.POST(path, wrapPluginHandler(h)) }

// wrapPluginHandler adapts a plugin handler to echo.
//
// エラーは **StatusError のときだけ** メッセージを返す。素の error を返すと
// プラグインの内部事情 (DB のエラー文字列など) がそのまま外に出るため、
// ログにだけ残して汎用メッセージを返す。
func wrapPluginHandler(h plugin.Handler) echo.HandlerFunc {
	return func(c echo.Context) error {
		res, err := h(&pluginRequest{c: c})
		if err != nil {
			var se *plugin.StatusError
			if errors.As(err, &se) {
				return c.JSON(se.Status, map[string]any{"error": map[string]any{"message": se.Message}})
			}
			slog.Error("plugin handler がエラーを返しました", "path", c.Path(), "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]any{
				"error": map[string]any{"message": "Internal error."},
			})
		}
		if res == nil {
			return c.NoContent(http.StatusNoContent)
		}
		return c.JSON(http.StatusOK, res)
	}
}

// --- Request ---

type pluginRequest struct {
	c echo.Context
}

func (r *pluginRequest) Context() context.Context { return r.c.Request().Context() }
func (r *pluginRequest) Bind(v any) error         { return json.NewDecoder(r.c.Request().Body).Decode(v) }
func (r *pluginRequest) Param(name string) string { return r.c.Param(name) }
func (r *pluginRequest) Query(name string) string { return r.c.QueryParam(name) }

// UserID returns the authenticated user's ID, or "" when unauthenticated.
// auth.Authenticate() はグローバル middleware なので、プラグインのルートでも
// 解決済みになっている。
func (r *pluginRequest) UserID() string {
	if u := middleware.GetUser(r.c); u != nil {
		return u.ID
	}
	return ""
}

// --- API ---

type pluginAPI struct {
	echo     *echo.Echo
	userRepo repository.UserRepository
	host     string
}

func (a *pluginAPI) Anonymous() plugin.Caller {
	return &pluginCaller{api: a}
}

func (a *pluginAPI) AsUser(userID string) plugin.Caller {
	return &pluginCaller{api: a, userID: userID}
}

type pluginCaller struct {
	api    *pluginAPI
	userID string
}

// Call dispatches an mk-go REST endpoint in-process.
//
// **loopback HTTP ではなく echo に直接 ServeHTTP する。** ネットワークを介さない
// のでソケットも TLS も要らない一方、middleware chain は本物のリクエストと
// 完全に同じものを通る。認証・可視性・レート制限を別実装で再現する必要が無く、
// mk-go 側を変えても自動的に追従する。
//
// レート制限も同じように適用される。プラグインが高頻度で呼ぶと、その利用者の
// 制限に掛かる。
func (c *pluginCaller) Call(ctx context.Context, endpoint string, params any) (json.RawMessage, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return nil, err
	}
	body := []byte("{}")
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("plugin: %s のパラメータを JSON 化できません: %w", endpoint, err)
		}
		body = b
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/"+endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("plugin: %s のリクエストを組み立てられません: %w", endpoint, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Host = c.api.host
	// RemoteAddr を空にすると、IP を見る middleware が解釈に困る。
	req.RemoteAddr = "127.0.0.1:0"

	if c.userID != "" {
		token, err := c.nativeToken()
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	// httptest.NewRecorder は stdlib の ResponseWriter 実装。WriteHeader の
	// 暗黙呼び出しまで含めて正しく振る舞うので、自前で書き直さない。
	rec := httptest.NewRecorder()
	c.api.echo.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close() //nolint:errcheck // in-memory recorder
	raw := rec.Body.Bytes()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, &plugin.APIError{Endpoint: endpoint, Status: res.StatusCode, Body: json.RawMessage(raw)}
	}
	if len(raw) == 0 {
		return nil, nil
	}
	return json.RawMessage(raw), nil
}

// nativeToken resolves the user's native login token.
//
// アプリ用のアクセストークンではなく native token を使う。AsUser は「その利用者
// として振る舞う」ものなので、scope で制限された third-party token では意味が
// 変わってしまう (secure endpoint も通らない)。
func (c *pluginCaller) nativeToken() (string, error) {
	if c.api.userRepo == nil {
		return "", fmt.Errorf("plugin: AsUser を使えません (user repository が未配線です)")
	}
	u, err := c.api.userRepo.FindByID(c.userID)
	if err != nil {
		return "", fmt.Errorf("plugin: AsUser(%s) の利用者を取得できません: %w", c.userID, err)
	}
	if u == nil {
		return "", fmt.Errorf("plugin: AsUser(%s) の利用者が存在しません", c.userID)
	}
	if u.Host != nil {
		return "", fmt.Errorf("plugin: AsUser(%s) はリモート利用者です (ローカル利用者のみ指定できます)", c.userID)
	}
	if u.Token == nil || *u.Token == "" {
		return "", fmt.Errorf("plugin: AsUser(%s) の利用者はトークンを持ちません", c.userID)
	}
	return *u.Token, nil
}

// validateEndpoint rejects anything that is not a plain endpoint name.
//
// プラグインが組み立てた文字列がそのまま path になるので、`../` で /api の外へ
// 出られないようにする。
func validateEndpoint(endpoint string) error {
	if endpoint == "" {
		return fmt.Errorf("plugin: endpoint が空です")
	}
	if strings.HasPrefix(endpoint, "/") || strings.Contains(endpoint, "..") ||
		strings.ContainsAny(endpoint, "?#") {
		return fmt.Errorf("plugin: endpoint %q が不正です (例: \"notes/show\")", endpoint)
	}
	return nil
}

// --- Jobs ---

type pluginJobs struct {
	name      string
	server    *queue.Server
	scheduler *queue.Scheduler
	// err holds the first registration failure. プラグイン側の callback を
	// エラーで汚さないため、ここに溜めて呼び出し元がまとめて検査する。
	err error
}

// taskType namespaces a plugin job so it cannot collide with mk-go's own types.
func (j *pluginJobs) taskType(name string) string {
	return "plugin:" + j.name + ":" + name
}

func (j *pluginJobs) Handle(name string, h plugin.JobHandler) {
	if j.err != nil {
		return
	}
	if name == "" {
		j.err = fmt.Errorf("ジョブ名が空です")
		return
	}
	if j.server == nil {
		j.err = fmt.Errorf("ジョブ %q を登録できません (queue server が未配線です)", name)
		return
	}
	j.server.Handle(j.taskType(name), func(ctx context.Context, t driver.Task) error {
		return h(ctx, t.Payload())
	})
}

func (j *pluginJobs) Schedule(cron string, name string, payload any) {
	if j.err != nil {
		return
	}
	if j.scheduler == nil {
		j.err = fmt.Errorf("ジョブ %q をスケジュールできません (scheduler が未配線です)", name)
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		j.err = fmt.Errorf("ジョブ %q のペイロードを JSON 化できません: %w", name, err)
		return
	}
	if err := j.scheduler.RegisterPluginJob(cron, j.taskType(name), body); err != nil {
		j.err = fmt.Errorf("ジョブ %q をスケジュールできません: %w", name, err)
	}
}
