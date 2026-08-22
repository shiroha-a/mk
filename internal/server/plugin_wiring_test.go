package server

import (
	"context"
	"database/sql"
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
	corerole "github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/shiroha-a/mk/plugin"
	"gorm.io/datatypes"
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
	e.POST("/x", wrapPluginHandler(h, nil))
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

type customPluginErrorCode struct {
	err  error
	code string
}

func (e customPluginErrorCode) Error() string { return e.err.Error() }

func (e customPluginErrorCode) Unwrap() error { return e.err }

func (e customPluginErrorCode) PluginErrorCode() string { return e.code }

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

func TestWrapPluginHandler_UncodedStatusErrorPreservesLegacyShape(t *testing.T) {
	rec := serveWrapped(t, func(plugin.Request) (any, error) {
		return nil, &plugin.StatusError{Status: http.StatusNotFound, Message: "そんなものは無い"}
	}, "{}")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "{\"error\":{\"message\":\"そんなものは無い\"}}\n", rec.Body.String())
}

func TestWrapPluginHandler_StatusErrorWithCode(t *testing.T) {
	rec := serveWrapped(t, func(plugin.Request) (any, error) {
		return nil, plugin.NewCodedStatusError(http.StatusConflict, "更新競合", "REVISION_CONFLICT")
	}, "{}")

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.JSONEq(t, `{"error": {"message":"更新競合", "code":"REVISION_CONFLICT"}}`, rec.Body.String())
}

func TestWrapPluginHandler_StatusErrorWithInvalidCodeFallsBackToLegacy(t *testing.T) {
	rec := serveWrapped(t, func(plugin.Request) (any, error) {
		return nil, plugin.NewCodedStatusError(http.StatusBadRequest, "secret", "invalid code")
	}, "{}")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "secret")
	assert.NotContains(t, rec.Body.String(), "invalid code")
	assert.NotContains(t, rec.Body.String(), "\"code\"")
}

func TestWrapPluginHandler_CustomWrappedStatusErrorHasNoCode(t *testing.T) {
	rec := serveWrapped(t, func(plugin.Request) (any, error) {
		return nil, customPluginErrorCode{
			err:  plugin.ErrNotFound("secret"),
			code: "CUSTOM_CODE",
		}
	}, "{}")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, `{"error":{"message":"secret"}}`, strings.TrimSpace(rec.Body.String()))
	assert.NotContains(t, rec.Body.String(), "\"code\"")
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
	// 素の error に code を付けない。内部事情 (DB のエラー文字列) がそのまま
	// 外に出るのを防ぐため。
	assert.NotContains(t, rec.Body.String(), "code")
}

// エラーチェインしても code を潰さず透過する。
func TestWrapPluginHandler_WrappedCodedStatusError(t *testing.T) {
	rec := serveWrapped(t, func(plugin.Request) (any, error) {
		return nil, fmt.Errorf("ctx: %w", plugin.NewCodedStatusError(http.StatusConflict, "更新競合", "REVISION_CONFLICT"))
	}, "{}")

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.JSONEq(t, `{"error": {"message":"更新競合", "code":"REVISION_CONFLICT"}}`, rec.Body.String())
}

func TestWrapPluginHandler_ErrorsJoinDoesNotMixStatusAndCustomCode(t *testing.T) {
	rec := serveWrapped(t, func(plugin.Request) (any, error) {
		return nil, errors.Join(
			plugin.Errorf(http.StatusForbidden, "権限がありません"),
			customPluginErrorCode{
				err:  plugin.Errorf(http.StatusConflict, "更新競合"),
				code: "CUSTOM_WRAPPER_CODE",
			},
		)
	}, "{}")

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, `{"error":{"message":"権限がありません"}}`, strings.TrimSpace(rec.Body.String()))
	assert.NotContains(t, rec.Body.String(), "\"code\"")
}

// wrapped した StatusError も status が効くこと (errors.As 経由)。
func TestWrapPluginHandler_WrappedStatusError(t *testing.T) {
	rec := serveWrapped(t, func(plugin.Request) (any, error) {
		return nil, fmt.Errorf("文脈: %w", plugin.Errorf(http.StatusForbidden, "だめ"))
	}, "{}")

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "だめ")
}

// --- 権限 ---

// stubRoles answers role checks without a real role service.
type stubRoles struct{ mod, admin bool }

func (r *stubRoles) IsModerator(string) bool     { return r.mod }
func (r *stubRoles) IsAdministrator(string) bool { return r.admin }

// **未認証・未配線では false。** 判定できないときに true を返すと、権限の穴が
// 「動いているように見える」形で残る。
func TestPluginRequest_RolesDefaultToFalse(t *testing.T) {
	e := echo.New()
	var gotMod, gotAdmin bool
	e.POST("/x", wrapPluginHandler(func(r plugin.Request) (any, error) {
		gotMod, gotAdmin = r.IsModerator(), r.IsAdministrator()
		return nil, nil
	}, nil))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	assert.False(t, gotMod)
	assert.False(t, gotAdmin)
}

// roles が配線されていても、未認証なら false (ID が無いので問い合わせない)。
func TestPluginRequest_UnauthenticatedIsNotModerator(t *testing.T) {
	e := echo.New()
	var got bool
	e.POST("/x", wrapPluginHandler(func(r plugin.Request) (any, error) {
		got = r.IsModerator()
		return nil, nil
	}, &stubRoles{mod: true, admin: true}))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	assert.False(t, got, "未認証を moderator 扱いしない")
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
	}, nil))

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
	}, nil))

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

func TestPluginGoGate_ConcurrentOpenAndGoRunsExactlyOnce(t *testing.T) {
	gate := &pluginGoGate{}
	runs := make(chan int, 66)
	c := &pluginContext{
		name: "concurrent", logger: slog.Default(), goGate: gate,
		goStart: func(fn func()) { fn() },
	}
	// release処理がlock中にplugin関数を実行すると、nested Goがdeadlockする。
	c.Go(func() {
		runs <- 64
		c.Go(func() { runs <- 65 })
	})

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c.Go(func() { runs <- i })
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		gate.open()
	}()
	close(start)
	wg.Wait()

	seen := make(map[int]int, 66)
	for range 66 {
		seen[<-runs]++
	}
	for i := range 66 {
		require.Equal(t, 1, seen[i])
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

type recordingPluginStorage struct {
	events *[]string
}

func (*recordingPluginStorage) DB() *sql.DB { return nil }

func (s *recordingPluginStorage) Migrate(context.Context, []plugin.Migration) error {
	*s.events = append(*s.events, "migrations")
	return nil
}

func newEffectivePolicyTestService(t *testing.T) (*corerole.Service, *testutil.MockRoleRepository, *testutil.MockRoleAssignmentRepository) {
	t.Helper()
	roleRepo := testutil.NewMockRoleRepository()
	assignmentRepo := testutil.NewMockRoleAssignmentRepository(roleRepo)
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	return corerole.NewService(roleRepo, assignmentRepo, testutil.NewMockMetaRepository(), idGen), roleRepo, assignmentRepo
}

func TestSetupPlugins_EffectivePolicyStartupOrderAndRegistration(t *testing.T) {
	var events []string
	def := plugin.Definition{
		Name:       "policy",
		APIVersion: plugin.APIVersion,
		Migrations: []plugin.Migration{{Version: 1, SQL: "SELECT 1"}},
		EffectivePolicies: func(plugin.Context, plugin.EffectivePolicyInvalidator) (plugin.EffectivePolicyRegistration, error) {
			events = append(events, "factory")
			return plugin.EffectivePolicyRegistration{
				Keys: []string{"canSearchNotes"},
				Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
					return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 1, Value: true}}, nil
				},
			}, nil
		},
		Routes: func(plugin.Context, plugin.Router) error {
			events = append(events, "routes")
			return nil
		},
		Jobs: func(plugin.Context, plugin.Jobs) error {
			events = append(events, "jobs")
			return nil
		},
	}

	svc, _, _ := newEffectivePolicyTestService(t)
	s, api := newPluginTestServer(config.RoleBoth)
	s.roleService = svc
	err := s.setupPlugins(api, []plugin.Definition{def}, func(string) (plugin.Storage, error) {
		events = append(events, "storage")
		return &recordingPluginStorage{events: &events}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"storage", "migrations", "factory", "routes", "jobs"}, events)

	policies, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, true, policies["canSearchNotes"], "factory registration must be stored before startup completes")
}

func TestSetupPlugins_PreparesAllPluginsBeforeAnyCallback(t *testing.T) {
	var events []string
	definition := func(name string) plugin.Definition {
		return plugin.Definition{
			Name:       name,
			APIVersion: plugin.APIVersion,
			Migrations: []plugin.Migration{{Version: 1, SQL: "SELECT 1"}},
			EffectivePolicies: func(plugin.Context, plugin.EffectivePolicyInvalidator) (plugin.EffectivePolicyRegistration, error) {
				events = append(events, name+" factory")
				return plugin.EffectivePolicyRegistration{
					Keys: []string{"canSearchNotes"},
					Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
						return nil, nil
					},
				}, nil
			},
			Routes: func(plugin.Context, plugin.Router) error {
				events = append(events, name+" routes")
				return nil
			},
			Jobs: func(plugin.Context, plugin.Jobs) error {
				events = append(events, name+" jobs")
				return nil
			},
		}
	}

	svc, _, _ := newEffectivePolicyTestService(t)
	s, api := newPluginTestServer(config.RoleBoth)
	s.roleService = svc
	err := s.setupPlugins(api, []plugin.Definition{definition("first"), definition("second")}, func(name string) (plugin.Storage, error) {
		events = append(events, name+" storage")
		return &recordingPluginStorage{events: &events}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"first storage", "migrations", "first factory",
		"second storage", "migrations", "second factory",
		"first routes", "first jobs", "second routes", "second jobs",
	}, events)
}

func TestSetupPlugins_SuccessLeavesGoQueuedForConstructorFinalization(t *testing.T) {
	started := make(chan struct{}, 1)
	def := pluginDef("queued", func(ctx plugin.Context, _ plugin.Router) error {
		ctx.Go(func() { started <- struct{}{} })
		return nil
	}, nil)
	s, api := newPluginTestServer(config.RoleServer)
	s.pluginGoStarter = func(fn func()) { fn() }

	require.NoError(t, s.setupPlugins(api, []plugin.Definition{def}, noopStorage))
	require.NotNil(t, s.pluginGoGate)
	select {
	case <-started:
		t.Fatal("setupPlugins released Go work before constructor finalization")
	default:
	}
	s.pluginGoGate.open()
	select {
	case <-started:
	default:
		t.Fatal("queued Go work was not released")
	}
}

func TestSetupPlugins_DiscardsGoWorkWhenLaterActivationFails(t *testing.T) {
	tests := []struct {
		name   string
		role   config.ProcessRole
		first  plugin.Definition
		second plugin.Definition
	}{
		{
			name: "routes", role: config.RoleServer,
			first: pluginDef("first", func(ctx plugin.Context, _ plugin.Router) error {
				ctx.Go(func() {})
				return nil
			}, nil),
			second: pluginDef("second", func(plugin.Context, plugin.Router) error {
				return errors.New("later route activation failed")
			}, nil),
		},
		{
			name: "jobs", role: config.RoleQueue,
			first: pluginDef("first", nil, func(ctx plugin.Context, _ plugin.Jobs) error {
				ctx.Go(func() {})
				return nil
			}),
			second: pluginDef("second", nil, func(plugin.Context, plugin.Jobs) error {
				return errors.New("later job activation failed")
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			var captured plugin.Context
			if tt.first.Routes != nil {
				tt.first.Routes = func(ctx plugin.Context, _ plugin.Router) error {
					captured = ctx
					ctx.Go(func() { started <- struct{}{} })
					return nil
				}
			} else {
				tt.first.Jobs = func(ctx plugin.Context, _ plugin.Jobs) error {
					captured = ctx
					ctx.Go(func() { started <- struct{}{} })
					return nil
				}
			}

			s, api := newPluginTestServer(tt.role)
			s.pluginGoStarter = func(fn func()) { fn() }
			require.Error(t, s.setupPlugins(api, []plugin.Definition{tt.first, tt.second}, noopStorage))
			require.NotNil(t, captured)
			captured.Go(func() { started <- struct{}{} })
			select {
			case <-started:
				t.Fatal("plugin Go work started before every activation succeeded")
			default:
			}
		})
	}
}

type pluginLogWriter chan string

func (w pluginLogWriter) Write(p []byte) (int, error) {
	w <- string(p)
	return len(p), nil
}

func TestPluginGoGate_QueuedGoRetainsPanicRecovery(t *testing.T) {
	logs := make(pluginLogWriter, 1)
	gate := &pluginGoGate{}
	c := &pluginContext{
		name: "panic-plugin",
		logger: slog.New(slog.NewTextHandler(logs, nil)).With(
			slog.String("plugin", "panic-plugin"),
			slog.String("pluginVersion", "test"),
		),
		goGate:  gate,
		goStart: func(fn func()) { fn() },
	}
	c.Go(func() { panic("private panic payload") })
	gate.open()

	logLine := <-logs
	require.Contains(t, logLine, "plugin=panic-plugin")
	require.Contains(t, logLine, "panic=\"private panic payload\"")
}

type policyBoundaryServer struct {
	onHandle func(string)
}

func (s *policyBoundaryServer) Handle(taskType string, _ driver.HandlerFunc) {
	s.onHandle(taskType)
}
func (*policyBoundaryServer) Start() error { return nil }
func (*policyBoundaryServer) Shutdown()    {}

type policyBoundaryDriver struct {
	server driver.Server
}

func (*policyBoundaryDriver) Client() driver.Client       { return nil }
func (d *policyBoundaryDriver) Server() driver.Server     { return d.server }
func (*policyBoundaryDriver) Inspector() driver.Inspector { return nil }
func (*policyBoundaryDriver) Scheduler() driver.Scheduler { return nil }
func (*policyBoundaryDriver) Close() error                { return nil }
func (*policyBoundaryDriver) WorkerCount(string) int      { return 0 }
func (*policyBoundaryDriver) Resize(string, int) error    { return nil }

func TestSetupPlugins_EffectivePolicyIsRegisteredAtJobRegistrationBoundary(t *testing.T) {
	svc, _, _ := newEffectivePolicyTestService(t)
	boundaryReached := false
	queueDriver := &policyBoundaryDriver{server: &policyBoundaryServer{onHandle: func(taskType string) {
		boundaryReached = true
		assert.Equal(t, "plugin:policy:refresh", taskType)
		policies, err := svc.GetUserPoliciesChecked("queue-user")
		require.NoError(t, err)
		assert.Equal(t, true, policies["canSearchNotes"], "provider must already be registered when the queue accepts the job handler")
	}}}
	def := plugin.Definition{
		Name:       "policy",
		APIVersion: plugin.APIVersion,
		EffectivePolicies: func(plugin.Context, plugin.EffectivePolicyInvalidator) (plugin.EffectivePolicyRegistration, error) {
			return plugin.EffectivePolicyRegistration{
				Keys: []string{"canSearchNotes"},
				Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
					return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Value: true}}, nil
				},
			}, nil
		},
		Jobs: func(_ plugin.Context, jobs plugin.Jobs) error {
			jobs.Handle("refresh", func(context.Context, json.RawMessage) error { return nil })
			return nil
		},
	}

	s, api := newPluginTestServer(config.RoleQueue)
	s.roleService = svc
	s.queueServer = queue.NewServer(queueDriver)
	require.NoError(t, s.setupPlugins(api, []plugin.Definition{def}, noopStorage))
	assert.True(t, boundaryReached)
}

func TestSetupPlugins_EffectivePolicyFactoryFailureIsFixedAndRedacted(t *testing.T) {
	def := plugin.Definition{
		Name:       "secret-plugin-id",
		APIVersion: plugin.APIVersion,
		EffectivePolicies: func(plugin.Context, plugin.EffectivePolicyInvalidator) (plugin.EffectivePolicyRegistration, error) {
			return plugin.EffectivePolicyRegistration{}, errors.New("raw provider failure for user-secret")
		},
	}
	svc, _, _ := newEffectivePolicyTestService(t)
	s, api := newPluginTestServer(config.RoleServer)
	s.roleService = svc

	err := s.setupPlugins(api, []plugin.Definition{def}, noopStorage)
	require.ErrorIs(t, err, errPluginEffectivePolicySetup)
	assert.Equal(t, errPluginEffectivePolicySetup.Error(), err.Error())
	assert.NotContains(t, err.Error(), "secret-plugin-id")
	assert.NotContains(t, err.Error(), "user-secret")
}

func TestSetupPlugins_EffectivePolicyFactoryPanicIsFixedAndAbortsStartup(t *testing.T) {
	tests := []struct {
		name       string
		panicValue any
	}{
		{
			name:       "string panic",
			panicValue: "secret-plugin-id secret-policy-key 01K2SECRETUSERID",
		},
		{
			name:       "error panic",
			panicValue: errors.New("secret-plugin-id secret-policy-key 01K2SECRETUSERID"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events []string
			def := plugin.Definition{
				Name:       "secret-plugin-id",
				APIVersion: plugin.APIVersion,
				Migrations: []plugin.Migration{{Version: 1, SQL: "SELECT 1"}},
				EffectivePolicies: func(plugin.Context, plugin.EffectivePolicyInvalidator) (plugin.EffectivePolicyRegistration, error) {
					events = append(events, "factory")
					panic(tt.panicValue)
				},
				Routes: func(plugin.Context, plugin.Router) error {
					events = append(events, "routes")
					return nil
				},
				Jobs: func(plugin.Context, plugin.Jobs) error {
					events = append(events, "jobs")
					return nil
				},
			}
			svc, _, _ := newEffectivePolicyTestService(t)
			s, api := newPluginTestServer(config.RoleBoth)
			s.roleService = svc

			err := s.setupPlugins(api, []plugin.Definition{def}, func(string) (plugin.Storage, error) {
				events = append(events, "storage")
				return &recordingPluginStorage{events: &events}, nil
			})

			require.ErrorIs(t, err, errPluginEffectivePolicySetup)
			assert.Equal(t, "plugin effective policy provider setup failed", err.Error())
			assert.Equal(t, []string{"storage", "migrations", "factory"}, events)
			assert.NotContains(t, err.Error(), "secret-plugin-id")
			assert.NotContains(t, err.Error(), "secret-policy-key")
			assert.NotContains(t, err.Error(), "01K2SECRETUSERID")
		})
	}
}

func TestSetupPlugins_DoesNotRecoverPanicsOutsideEffectivePolicyFactory(t *testing.T) {
	tests := []struct {
		name string
		role config.ProcessRole
		def  plugin.Definition
		open pluginStorageOpener
	}{
		{
			name: "migration",
			role: config.RoleBoth,
			def: plugin.Definition{
				Name:       "policy",
				APIVersion: plugin.APIVersion,
				Migrations: []plugin.Migration{{Version: 1, SQL: "SELECT 1"}},
			},
			open: func(string) (plugin.Storage, error) {
				return &panicPluginStorage{value: "migration panic"}, nil
			},
		},
		{
			name: "routes",
			role: config.RoleServer,
			def: plugin.Definition{
				Name:       "policy",
				APIVersion: plugin.APIVersion,
				Routes: func(plugin.Context, plugin.Router) error {
					panic("routes panic")
				},
			},
			open: noopStorage,
		},
		{
			name: "jobs",
			role: config.RoleQueue,
			def: plugin.Definition{
				Name:       "policy",
				APIVersion: plugin.APIVersion,
				Jobs: func(plugin.Context, plugin.Jobs) error {
					panic("jobs panic")
				},
			},
			open: noopStorage,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, api := newPluginTestServer(tt.role)
			assert.PanicsWithValue(t, tt.name+" panic", func() {
				_ = s.setupPlugins(api, []plugin.Definition{tt.def}, tt.open)
			})
		})
	}
}

type panicPluginStorage struct {
	value any
}

func (*panicPluginStorage) DB() *sql.DB { return nil }

func (s *panicPluginStorage) Migrate(context.Context, []plugin.Migration) error {
	panic(s.value)
}

func TestSetupPlugins_NilEffectivePolicyServiceIsFixedAndAbortsStartup(t *testing.T) {
	var routesCalled, jobsCalled bool
	def := plugin.Definition{
		Name:       "secret-plugin-id",
		APIVersion: plugin.APIVersion,
		EffectivePolicies: func(plugin.Context, plugin.EffectivePolicyInvalidator) (plugin.EffectivePolicyRegistration, error) {
			return plugin.EffectivePolicyRegistration{}, nil
		},
		Routes: func(plugin.Context, plugin.Router) error { routesCalled = true; return nil },
		Jobs:   func(plugin.Context, plugin.Jobs) error { jobsCalled = true; return nil },
	}
	s, api := newPluginTestServer(config.RoleBoth)

	err := s.setupPlugins(api, []plugin.Definition{def}, noopStorage)
	require.ErrorIs(t, err, errPluginEffectivePolicySetup)
	assert.Equal(t, "plugin effective policy provider setup failed", err.Error())
	assert.NotContains(t, err.Error(), "secret-plugin-id")
	assert.False(t, routesCalled)
	assert.False(t, jobsCalled)
}

func TestSetupPlugins_EffectivePolicyRegistrationFailureAbortsBeforeRoutes(t *testing.T) {
	var routesCalled bool
	def := plugin.Definition{
		Name:       "secret-plugin-id",
		APIVersion: plugin.APIVersion,
		EffectivePolicies: func(plugin.Context, plugin.EffectivePolicyInvalidator) (plugin.EffectivePolicyRegistration, error) {
			return plugin.EffectivePolicyRegistration{
				Keys: []string{"secret-policy-key"},
				Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
					return nil, nil
				},
			}, nil
		},
		Routes: func(plugin.Context, plugin.Router) error {
			routesCalled = true
			return nil
		},
	}
	svc, _, _ := newEffectivePolicyTestService(t)
	s, api := newPluginTestServer(config.RoleServer)
	s.roleService = svc

	err := s.setupPlugins(api, []plugin.Definition{def}, noopStorage)
	require.ErrorIs(t, err, errPluginEffectivePolicySetup)
	assert.Equal(t, errPluginEffectivePolicySetup.Error(), err.Error())
	assert.NotContains(t, err.Error(), "secret-plugin-id")
	assert.NotContains(t, err.Error(), "secret-policy-key")
	assert.False(t, routesCalled)
}

func TestRoleEffectivePolicyInvalidator_InvalidateUserDelegatesToService(t *testing.T) {
	svc, roleRepo, assignmentRepo := newEffectivePolicyTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":true}}`))}
	assignmentRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}
	assert.Equal(t, true, svc.GetUserPolicies("u1")["canSearchNotes"])
	roleRepo.Roles["r1"].Policies = datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":false}}`))

	err := (&roleEffectivePolicyInvalidator{service: svc}).InvalidateUser(context.Background(), "u1")
	require.NoError(t, err)
	assert.Equal(t, false, svc.GetUserPolicies("u1")["canSearchNotes"])
}

func TestRoleEffectivePolicyInvalidator_InvalidateRoleDelegatesToService(t *testing.T) {
	svc, roleRepo, assignmentRepo := newEffectivePolicyTestService(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Policies: datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":true}}`))}
	assignmentRepo.Assignments["u1:r1"] = &model.RoleAssignment{ID: "a1", UserID: "u1", RoleID: "r1"}
	assignmentRepo.Assignments["u2:r1"] = &model.RoleAssignment{ID: "a2", UserID: "u2", RoleID: "r1"}
	assert.Equal(t, true, svc.GetUserPolicies("u1")["canSearchNotes"])
	assert.Equal(t, true, svc.GetUserPolicies("u2")["canSearchNotes"])
	roleRepo.Roles["r1"].Policies = datatypes.JSON([]byte(`{"canSearchNotes":{"useDefault":false,"priority":0,"value":false}}`))

	err := (&roleEffectivePolicyInvalidator{service: svc}).InvalidateRole(context.Background(), "r1")
	require.NoError(t, err)
	assert.Equal(t, false, svc.GetUserPolicies("u1")["canSearchNotes"])
	assert.Equal(t, false, svc.GetUserPolicies("u2")["canSearchNotes"])
}

// --- serverPluginInfos (#2497) ---

func TestServerPluginInfos_ReflectsDefinitionsAndConfig(t *testing.T) {
	routes := func(plugin.Context, plugin.Router) error { return nil }
	jobs := func(plugin.Context, plugin.Jobs) error { return nil }
	defs := []plugin.Definition{
		{
			Name: "status", Version: "1.2.0", APIVersion: plugin.APIVersion,
			Routes: routes, Jobs: jobs,
			EffectivePolicies: func(plugin.Context, plugin.EffectivePolicyInvalidator) (plugin.EffectivePolicyRegistration, error) {
				return plugin.EffectivePolicyRegistration{}, nil
			},
			Migrations: []plugin.Migration{{Version: 1, SQL: "SELECT 1"}, {Version: 2, SQL: "SELECT 2"}},
		},
		{Name: "gameinfo", APIVersion: plugin.APIVersion, Routes: routes},
	}
	// **キーは小文字で渡す。** Viper は YAML のキーを全て小文字化するので、
	// 実運用の config.Plugins に camelCase のキーは現れない (-config-dump の
	// `status.maxlength` 表示と同じ)。テストが camelCase を「仕様」として
	// 見せないようにする。
	settings := map[string]map[string]any{
		"status": {"enabled": false, "maxlength": 30, "apikey": "secret"},
	}

	infos := serverPluginInfos(defs, settings)
	require.Len(t, infos, 2)

	assert.Equal(t, "status", infos[0].Name)
	assert.Equal(t, "1.2.0", infos[0].Version)
	assert.Equal(t, plugin.APIVersion, infos[0].APIVersion)
	assert.False(t, infos[0].Enabled, "enabled: false を反映する")
	assert.True(t, infos[0].Routes)
	assert.True(t, infos[0].Jobs)
	assert.True(t, infos[0].EffectivePolicies)
	assert.Equal(t, 2, infos[0].Migrations)
	assert.Equal(t, "plugin_status", infos[0].Schema)
	// enabled は予約キーなので除外、残りはソート済みのキー名のみ (値は出さない)。
	assert.Equal(t, []string{"apikey", "maxlength"}, infos[0].ConfigKeys)

	assert.Equal(t, "gameinfo", infos[1].Name)
	assert.True(t, infos[1].Enabled, "設定が無ければ既定で有効")
	assert.True(t, infos[1].Routes)
	assert.False(t, infos[1].Jobs)
	assert.False(t, infos[1].EffectivePolicies)
	assert.Equal(t, 0, infos[1].Migrations)
	assert.Empty(t, infos[1].ConfigKeys)
}

func TestServerPluginInfos_EmptyIsEmpty(t *testing.T) {
	assert.Empty(t, serverPluginInfos(nil, nil))
}
