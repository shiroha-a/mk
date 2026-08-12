package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/plugin"
)

// noopStorage is the storage opener used by wiring tests. ルーティングの検証に
// 実 DB は要らない (ストレージ自体は internal/pluginstore のテストが見る)。
func noopStorage(string) (plugin.Storage, error) { return nil, nil }

// pluginDef builds a definition for wiring tests without touching the global
// registry. setupPlugins が一覧を引数で受けるので、レジストリを汚さずに済む。
func pluginDef(name string, routes func(plugin.Context, plugin.Router) error, jobs func(plugin.Context, plugin.Jobs) error) plugin.Definition {
	return plugin.Definition{Name: name, APIVersion: plugin.APIVersion, Routes: routes, Jobs: jobs}
}

func TestRequestHostFor(t *testing.T) {
	assert.Equal(t, "example.com", requestHostFor("https://example.com"))
	assert.Equal(t, "example.com:3000", requestHostFor("https://example.com:3000/"))
	// 空にしないこと。絶対 URL を組み立てる handler が壊れる。
	assert.Equal(t, "localhost", requestHostFor(""))
	assert.Equal(t, "localhost", requestHostFor("://bad"))
}

// **`../` で /api の外へ出られないこと。** endpoint はプラグインが組み立てた
// 文字列がそのまま path になる。
func TestValidateEndpoint(t *testing.T) {
	require.NoError(t, validateEndpoint("notes/show"))
	require.NoError(t, validateEndpoint("i"))

	for _, bad := range []string{"", "/notes/show", "../admin/x", "a/../../b", "notes/show?x=1", "notes#frag"} {
		assert.Errorf(t, validateEndpoint(bad), "%q は拒否される", bad)
	}
}

// --- handler wrapping ---

func serveWrapped(t *testing.T, h plugin.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	e.POST("/x", wrapPluginHandler(h))
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestWrapPluginHandler_JSONResult(t *testing.T) {
	rec := serveWrapped(t, func(plugin.Request) (any, error) {
		return map[string]string{"hello": "world"}, nil
	}, "{}")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"hello":"world"}`, rec.Body.String())
}

func TestWrapPluginHandler_NilResultIsNoContent(t *testing.T) {
	rec := serveWrapped(t, func(plugin.Request) (any, error) { return nil, nil }, "{}")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.String())
}

func TestWrapPluginHandler_StatusError(t *testing.T) {
	rec := serveWrapped(t, func(plugin.Request) (any, error) {
		return nil, plugin.ErrNotFound("そんなものは無い")
	}, "{}")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "そんなものは無い")
}

// **素の error はメッセージを外に出さない。** プラグインの内部事情 (DB のエラー
// 文字列など) がそのまま利用者に見えるのを防ぐ。
func TestWrapPluginHandler_PlainErrorIsRedacted(t *testing.T) {
	rec := serveWrapped(t, func(plugin.Request) (any, error) {
		return nil, errors.New("pq: password authentication failed for user \"mk\"")
	}, "{}")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "password")
	assert.Contains(t, rec.Body.String(), "Internal error.")
}

// wrapped した StatusError も status が効くこと (errors.As 経由)。
func TestWrapPluginHandler_WrappedStatusError(t *testing.T) {
	rec := serveWrapped(t, func(plugin.Request) (any, error) {
		return nil, fmt.Errorf("文脈: %w", plugin.Errorf(http.StatusForbidden, "だめ"))
	}, "{}")

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "だめ")
}

// --- Blob ---

// バイナリ応答は JSON エンコードせずそのまま書く。画像プロキシの用途。
func TestWrapPluginHandler_Blob(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	rec := serveWrapped(t, func(plugin.Request) (any, error) {
		return plugin.Blob{
			ContentType:  "image/png",
			Body:         png,
			CacheControl: "public, max-age=86400",
		}, nil
	}, "{}")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, png, rec.Body.Bytes(), "JSON にせずそのまま返す")
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assert.Equal(t, "public, max-age=86400", rec.Header().Get("Cache-Control"))
}

// **nosniff を必ず付ける。** 外部から取得したものをそのまま流す用途なので、
// ブラウザの MIME 推測で意図しない解釈をされる余地を残さない。
func TestWrapPluginHandler_BlobAlwaysSetsNosniff(t *testing.T) {
	rec := serveWrapped(t, func(plugin.Request) (any, error) {
		return plugin.Blob{Body: []byte("x")}, nil
	}, "{}")

	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	// ContentType 未指定なら octet-stream (推測させない)。
	assert.Contains(t, rec.Header().Get("Content-Type"), echo.MIMEOctetStream)
}

// CacheControl 未指定なら /api 既定の Cache-Control を壊さない。
func TestWrapPluginHandler_BlobWithoutCacheControl(t *testing.T) {
	e := echo.New()
	api := e.Group("/api")
	api.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set("Cache-Control", "private, max-age=0")
			return next(c)
		}
	})
	api.GET("/b", wrapPluginHandler(func(plugin.Request) (any, error) {
		return plugin.Blob{ContentType: "image/png", Body: []byte("x")}, nil
	}))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/b", nil))
	assert.Equal(t, "private, max-age=0", rec.Header().Get("Cache-Control"))
}

// --- Request ---

func TestPluginRequest_Accessors(t *testing.T) {
	e := echo.New()
	var got struct {
		body  map[string]string
		param string
		query string
		user  string
	}
	e.POST("/u/:id", wrapPluginHandler(func(r plugin.Request) (any, error) {
		require.NoError(t, r.Bind(&got.body))
		got.param = r.Param("id")
		got.query = r.Query("q")
		got.user = r.UserID()
		require.NotNil(t, r.Context())
		return nil, nil
	}))

	req := httptest.NewRequest(http.MethodPost, "/u/abc?q=zzz", strings.NewReader(`{"k":"v"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, map[string]string{"k": "v"}, got.body)
	assert.Equal(t, "abc", got.param)
	assert.Equal(t, "zzz", got.query)
	// 認証 middleware を通していないので未認証扱い。
	assert.Empty(t, got.user)
}

// --- Context.Go ---

// **プラグインが素の go を書くとプロセスごと落ちる。** Go() は回収すること。
func TestPluginContext_Go_RecoversPanic(t *testing.T) {
	c := &pluginContext{name: "t", logger: slog.Default()}

	var wg sync.WaitGroup
	wg.Add(1)
	c.Go(func() {
		defer wg.Done()
		panic("boom")
	})

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Go() が返ってこない")
	}
}

func TestPluginContext_Accessors(t *testing.T) {
	api := &pluginAPI{}
	st := &pluginStorage{}
	c := &pluginContext{name: "gameinfo", logger: slog.Default(), api: api, storage: st}
	assert.Equal(t, "gameinfo", c.Name())
	assert.NotNil(t, c.Logger())
	assert.Same(t, api, c.API())
	assert.Same(t, st, c.Storage())
}

// **ストレージを開けなかったら起動を失敗させる。** 遅延させると接続できない
// 設定でも起動し、最初のリクエストまで問題が表面化しない。
func TestSetupPlugins_PropagatesStorageError(t *testing.T) {
	def := pluginDef("p", func(plugin.Context, plugin.Router) error { return nil }, nil)

	s, api := newPluginTestServer(config.RoleServer)
	err := s.setupPlugins(api, []plugin.Definition{def},
		func(string) (plugin.Storage, error) { return nil, errors.New("DB に繋がらない") })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB に繋がらない")
}

// ストレージはロールに関係なく渡す (Routes からも Jobs からも使うため)。
func TestSetupPlugins_StorageIsGivenToBothRoles(t *testing.T) {
	for _, role := range []config.ProcessRole{config.RoleServer, config.RoleQueue} {
		t.Run(string(role), func(t *testing.T) {
			var seen plugin.Storage
			def := pluginDef("p",
				func(c plugin.Context, _ plugin.Router) error { seen = c.Storage(); return nil },
				func(c plugin.Context, _ plugin.Jobs) error { seen = c.Storage(); return nil },
			)

			want := &pluginStorage{}
			s, api := newPluginTestServer(role)
			_ = s.setupPlugins(api, []plugin.Definition{def},
				func(string) (plugin.Storage, error) { return want, nil })

			assert.Same(t, want, seen)
		})
	}
}

// --- API caller ---

// stubUserRepo returns a fixed user for AsUser resolution.
type stubUserRepo struct {
	repository.UserRepository
	user *model.User
	err  error
}

func (r *stubUserRepo) FindByID(string) (*model.User, error) { return r.user, r.err }

// apiTestEcho builds an echo that echoes back what it received, so the test can
// assert on the auth header and body the caller produced.
func apiTestEcho() *echo.Echo {
	e := echo.New()
	e.POST("/api/echo", func(c echo.Context) error {
		var body map[string]any
		_ = json.NewDecoder(c.Request().Body).Decode(&body)
		return c.JSON(http.StatusOK, map[string]any{
			"auth": c.Request().Header.Get("Authorization"),
			"host": c.Request().Host,
			"body": body,
		})
	})
	e.POST("/api/boom", func(c echo.Context) error {
		return c.JSON(http.StatusNotFound, map[string]any{"error": map[string]any{"code": "NO_SUCH_NOTE"}})
	})
	e.POST("/api/empty", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
	return e
}

func TestPluginCaller_Anonymous(t *testing.T) {
	api := &pluginAPI{echo: apiTestEcho(), host: "example.com"}

	raw, err := api.Anonymous().Call(context.Background(), "echo", map[string]string{"a": "b"})
	require.NoError(t, err)

	var got struct {
		Auth string         `json:"auth"`
		Host string         `json:"host"`
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Empty(t, got.Auth, "匿名呼び出しに Authorization を付けない")
	assert.Equal(t, "example.com", got.Host)
	assert.Equal(t, map[string]any{"a": "b"}, got.Body)
}

// params が nil でも空オブジェクトを送ること。null を送ると本体側の bind が
// 落ちる endpoint がある。
func TestPluginCaller_NilParamsSendsEmptyObject(t *testing.T) {
	api := &pluginAPI{echo: apiTestEcho(), host: "h"}

	raw, err := api.Anonymous().Call(context.Background(), "echo", nil)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"body":{}`)
}

func TestPluginCaller_AsUserSendsNativeToken(t *testing.T) {
	tok := "0123456789abcdef"
	api := &pluginAPI{
		echo:     apiTestEcho(),
		host:     "h",
		userRepo: &stubUserRepo{user: &model.User{ID: "u1", Token: &tok}},
	}

	raw, err := api.AsUser("u1").Call(context.Background(), "echo", nil)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Bearer "+tok)
}

func TestPluginCaller_AsUser_Rejects(t *testing.T) {
	host := "remote.example"
	empty := ""
	tests := []struct {
		name string
		repo repository.UserRepository
		want string
	}{
		{"未配線", nil, "未配線"},
		{"存在しない", &stubUserRepo{user: nil}, "存在しません"},
		{"取得失敗", &stubUserRepo{err: errors.New("db down")}, "取得できません"},
		{"リモート利用者", &stubUserRepo{user: &model.User{ID: "u1", Host: &host}}, "リモート"},
		{"トークン無し", &stubUserRepo{user: &model.User{ID: "u1"}}, "トークンを持ちません"},
		{"トークン空", &stubUserRepo{user: &model.User{ID: "u1", Token: &empty}}, "トークンを持ちません"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &pluginAPI{echo: apiTestEcho(), host: "h", userRepo: tt.repo}
			_, err := api.AsUser("u1").Call(context.Background(), "echo", nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// 非 2xx は APIError にする。**本文を落とさない** — Misskey のエラーコードが
// 読めないと原因を切り分けられない。
func TestPluginCaller_NonSuccessBecomesAPIError(t *testing.T) {
	api := &pluginAPI{echo: apiTestEcho(), host: "h"}

	_, err := api.Anonymous().Call(context.Background(), "boom", nil)
	require.Error(t, err)

	var ae *plugin.APIError
	require.True(t, errors.As(err, &ae))
	assert.Equal(t, http.StatusNotFound, ae.Status)
	assert.Equal(t, "boom", ae.Endpoint)
	assert.Contains(t, string(ae.Body), "NO_SUCH_NOTE")
}

func TestPluginCaller_EmptyBody(t *testing.T) {
	api := &pluginAPI{echo: apiTestEcho(), host: "h"}
	raw, err := api.Anonymous().Call(context.Background(), "empty", nil)
	require.NoError(t, err)
	assert.Nil(t, raw)
}

func TestPluginCaller_RejectsBadEndpoint(t *testing.T) {
	api := &pluginAPI{echo: apiTestEcho(), host: "h"}
	_, err := api.Anonymous().Call(context.Background(), "../secret", nil)
	require.Error(t, err)
}

func TestPluginCaller_RejectsUnmarshalableParams(t *testing.T) {
	api := &pluginAPI{echo: apiTestEcho(), host: "h"}
	_, err := api.Anonymous().Call(context.Background(), "echo", make(chan int))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JSON 化できません")
}

// --- Jobs ---

func TestPluginJobs_NamespacesTaskType(t *testing.T) {
	j := &pluginJobs{name: "gameinfo"}
	// 本体の task type と衝突しない形であること。
	assert.Equal(t, "plugin:gameinfo:refresh", j.taskType("refresh"))
}

func TestPluginJobs_RecordsErrors(t *testing.T) {
	// queue server が未配線なら登録失敗を溜める。
	j := &pluginJobs{name: "p"}
	j.Handle("x", func(context.Context, json.RawMessage) error { return nil })
	require.Error(t, j.err)
	assert.Contains(t, j.err.Error(), "未配線")

	// 最初のエラーが保持され、後続で上書きされないこと。
	first := j.err
	j.Handle("y", func(context.Context, json.RawMessage) error { return nil })
	assert.Same(t, first, j.err)
}

func TestPluginJobs_RejectsEmptyName(t *testing.T) {
	j := &pluginJobs{name: "p"}
	j.Handle("", nil)
	require.Error(t, j.err)
	assert.Contains(t, j.err.Error(), "ジョブ名が空")
}

func TestPluginJobs_ScheduleRequiresScheduler(t *testing.T) {
	j := &pluginJobs{name: "p"}
	j.Schedule("* * * * *", "x", nil)
	require.Error(t, j.err)
	assert.Contains(t, j.err.Error(), "scheduler")
}

func TestPluginJobs_ScheduleRejectsUnmarshalablePayload(t *testing.T) {
	j := &pluginJobs{name: "p", scheduler: &queue.Scheduler{}}
	j.Schedule("* * * * *", "x", make(chan int))
	require.Error(t, j.err)
	assert.Contains(t, j.err.Error(), "JSON 化できません")
}

// --- setupPlugins ---

// newPluginTestServer builds the minimal Server that setupPlugins touches.
func newPluginTestServer(role config.ProcessRole) (*Server, *echo.Group) {
	e := echo.New()
	s := &Server{echo: e, config: &config.Config{URL: "https://example.com"}, role: role}
	return s, e.Group("/api")
}

func TestSetupPlugins_NoPluginsIsNoop(t *testing.T) {
	s, api := newPluginTestServer(config.RoleBoth)
	require.NoError(t, s.setupPlugins(api, nil, noopStorage))
}

// ロールに応じて Routes / Jobs の呼び出しを出し分けること (#2459)。
func TestSetupPlugins_RoleGating(t *testing.T) {
	tests := []struct {
		role       config.ProcessRole
		wantRoutes bool
		wantJobs   bool
	}{
		{config.RoleBoth, true, true},
		{config.RoleServer, true, false},
		{config.RoleQueue, false, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			var routesCalled, jobsCalled bool
			def := pluginDef("p",
				func(plugin.Context, plugin.Router) error { routesCalled = true; return nil },
				func(plugin.Context, plugin.Jobs) error { jobsCalled = true; return nil },
			)

			s, api := newPluginTestServer(tt.role)
			// queue server は未配線なので、ジョブを登録するロールでは失敗する。
			// ここで見たいのは「呼ばれたかどうか」。
			_ = s.setupPlugins(api, []plugin.Definition{def}, noopStorage)

			assert.Equal(t, tt.wantRoutes, routesCalled, "Routes")
			assert.Equal(t, tt.wantJobs, jobsCalled, "Jobs")
		})
	}
}

// **登録に失敗したら起動を失敗させる。** 黙って無効のまま動かすと、機能が
// 消えた原因が分からない。
func TestSetupPlugins_PropagatesRouteError(t *testing.T) {
	def := pluginDef("p", func(plugin.Context, plugin.Router) error { return errors.New("だめ") }, nil)

	s, api := newPluginTestServer(config.RoleServer)
	err := s.setupPlugins(api, []plugin.Definition{def}, noopStorage)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ルート登録に失敗")
}

// ジョブ登録の失敗も起動を止めること。plugin.Jobs はエラーを溜める形なので、
// callback が nil を返しても取りこぼさない。
func TestSetupPlugins_PropagatesJobError(t *testing.T) {
	def := pluginDef("p", nil, func(_ plugin.Context, j plugin.Jobs) error {
		j.Handle("x", func(context.Context, json.RawMessage) error { return nil })
		return nil // callback 自体は成功を返す
	})

	s, api := newPluginTestServer(config.RoleQueue)
	err := s.setupPlugins(api, []plugin.Definition{def}, noopStorage)
	require.Error(t, err, "queue server 未配線を取りこぼさない")
	assert.Contains(t, err.Error(), "ジョブ登録に失敗")
}

// プラグインのルートが /api/plugin/<name>/ 配下に生えること。本体の
// エンドポイント空間と衝突させない。
func TestSetupPlugins_RoutesAreNamespaced(t *testing.T) {
	def := pluginDef("gameinfo", func(_ plugin.Context, r plugin.Router) error {
		r.GET("/status", func(plugin.Request) (any, error) {
			return map[string]string{"ok": "yes"}, nil
		})
		r.POST("/refresh", func(plugin.Request) (any, error) { return nil, nil })
		return nil
	}, nil)

	s, api := newPluginTestServer(config.RoleServer)
	require.NoError(t, s.setupPlugins(api, []plugin.Definition{def}, noopStorage))

	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugin/gameinfo/status", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"ok":"yes"}`, rec.Body.String())

	rec = httptest.NewRecorder()
	s.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/plugin/gameinfo/refresh", nil))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}
