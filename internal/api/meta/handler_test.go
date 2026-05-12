package meta

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestHandler() (*Handler, *testutil.MockMetaRepository) {
	cfg := &config.Config{
		Version: config.MisskeyVersion,
		URL:     "https://misskey.example.com",
	}
	metaRepo := testutil.NewMockMetaRepository()
	h := NewHandler(cfg, metaRepo)
	return h, metaRepo
}

func TestMeta(t *testing.T) {
	h, metaRepo := newTestHandler()

	name := "Test Instance"
	desc := "A test instance"
	maintainerName := "admin"
	metaRepo.Meta = &model.Meta{
		ID:             "x",
		Name:           &name,
		Description:    &desc,
		MaintainerName: &maintainerName,
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.Meta(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, "Test Instance", resp["name"])
	assert.Equal(t, "A test instance", resp["description"])
	assert.Equal(t, "admin", resp["maintainerName"])
	assert.Equal(t, config.MisskeyVersion, resp["version"])
	assert.Equal(t, "https://misskey.example.com", resp["uri"])
	assert.Equal(t, float64(3000), resp["maxNoteTextLength"])

	features, ok := resp["features"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, features["miauth"])
}

func TestMeta_NoMeta(t *testing.T) {
	h, _ := newTestHandler()
	// metaRepo.Metaはnil（未設定）

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.Meta(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestPing(t *testing.T) {
	h, _ := newTestHandler()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/ping", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.Ping(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["pong"])
}

// requireSetup は rootUserId が nil のとき true を返す。
func TestMeta_RequireSetupWhenNoRootUser(t *testing.T) {
	h, repo := newTestHandler()
	repo.Meta = &model.Meta{ID: "x"} // RootUserID = nil

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["requireSetup"])
}

// requireSetup は rootUserId が設定済みのとき false を返す。
func TestMeta_RequireSetupFalseWhenRootExists(t *testing.T) {
	h, repo := newTestHandler()
	rootID := "root123"
	repo.Meta = &model.Meta{ID: "x", RootUserID: &rootID}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["requireSetup"])
}

// --- Branding / UI exposure (issue #50) ---

// /api/meta が meta テーブルの新フィールド (mascot / app192/512 / themes /
// inquiryUrl / clientOptions / ga) を返すことを確認する。
func TestMeta_BrandingFieldsExposed(t *testing.T) {
	h, repo := newTestHandler()
	mascot := "https://example.com/mascot.png"
	app192 := "https://example.com/app192.png"
	app512 := "https://example.com/app512.png"
	srvErr := "https://example.com/err.png"
	notFound := "https://example.com/404.png"
	info := "https://example.com/info.png"
	dlt := "{\"name\":\"light\"}"
	ddt := "{\"name\":\"dark\"}"
	inquiry := "https://example.com/inquiry"
	ga := "G-XXXXXX"
	repo.Meta = &model.Meta{
		ID:                           "x",
		MascotImageURL:               &mascot,
		App192IconURL:                &app192,
		App512IconURL:                &app512,
		ServerErrorImageURL:          &srvErr,
		NotFoundImageURL:             &notFound,
		InfoImageURL:                 &info,
		DefaultLightTheme:            &dlt,
		DefaultDarkTheme:             &ddt,
		InquiryURL:                   &inquiry,
		GoogleAnalyticsMeasurementID: &ga,
		ClientOptions:                []byte(`{"entrancePageStyle":"simple","showTimelineForVisitor":true}`),
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Meta(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, mascot, resp["mascotImageUrl"])
	assert.Equal(t, app192, resp["app192IconUrl"])
	assert.Equal(t, app512, resp["app512IconUrl"])
	assert.Equal(t, srvErr, resp["serverErrorImageUrl"])
	assert.Equal(t, notFound, resp["notFoundImageUrl"])
	assert.Equal(t, info, resp["infoImageUrl"])
	assert.Equal(t, dlt, resp["defaultLightTheme"])
	assert.Equal(t, ddt, resp["defaultDarkTheme"])
	assert.Equal(t, inquiry, resp["inquiryUrl"])
	assert.Equal(t, ga, resp["googleAnalyticsMeasurementId"])

	co, _ := resp["clientOptions"].(map[string]any)
	assert.Equal(t, "simple", co["entrancePageStyle"])
	assert.Equal(t, true, co["showTimelineForVisitor"])
}

// mascotImageUrl 未設定時は legacy /assets/ai.png にフォールバック。
func TestMeta_MascotFallback(t *testing.T) {
	h, repo := newTestHandler()
	repo.Meta = &model.Meta{ID: "x"} // mascot 未設定

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "/assets/ai.png", resp["mascotImageUrl"])
}

// 空文字 mascot もフォールバックされる。
func TestMeta_MascotEmptyFallback(t *testing.T) {
	h, repo := newTestHandler()
	empty := ""
	repo.Meta = &model.Meta{ID: "x", MascotImageURL: &empty}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "/assets/ai.png", resp["mascotImageUrl"])
}

// clientOptions が空 jsonb のとき、空 map にフォールバックする。
func TestMeta_ClientOptionsEmpty(t *testing.T) {
	h, repo := newTestHandler()
	repo.Meta = &model.Meta{ID: "x"} // ClientOptions = nil

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	co, ok := resp["clientOptions"].(map[string]any)
	assert.True(t, ok)
	assert.Empty(t, co)
}

// clientOptions が壊れた jsonb のとき、空 map にフォールバックして 200 を返す。
func TestMeta_ClientOptionsMalformed(t *testing.T) {
	h, repo := newTestHandler()
	repo.Meta = &model.Meta{ID: "x", ClientOptions: []byte(`not json`)}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	co, ok := resp["clientOptions"].(map[string]any)
	assert.True(t, ok)
	assert.Empty(t, co)
}

// --- Ads / notesPerOneAd (issue #52) ---

// stubAdRepo implements repository.AdRepository for tests without a real DB.
type stubAdRepo struct {
	ads []*model.Ad
	err error
}

func (s *stubAdRepo) ListActive(_ time.Time) ([]*model.Ad, error) {
	return s.ads, s.err
}
func (s *stubAdRepo) Create(_ *model.Ad) error                      { return nil }
func (s *stubAdRepo) FindByID(_ string) (*model.Ad, error)          { return nil, nil }
func (s *stubAdRepo) List(_, _ int) ([]*model.Ad, error)            { return nil, nil }
func (s *stubAdRepo) UpdateFields(_ string, _ map[string]any) error { return nil }
func (s *stubAdRepo) Delete(_ string) error                         { return nil }

// /api/meta returns the active ads list from AdRepository and notesPerOneAd
// from meta. Frontend reads these and handles injection.
func TestMeta_AdsExposed(t *testing.T) {
	h, metaRepo := newTestHandler()
	metaRepo.Meta = &model.Meta{ID: "x", NotesPerOneAd: 5}
	h.SetAdRepo(&stubAdRepo{ads: []*model.Ad{
		{ID: "ad1", URL: "https://example.com", Place: "square", Ratio: 1, ImageURL: "https://example.com/i.png", DayOfWeek: 0},
		{ID: "ad2", URL: "https://e2.example.com", Place: "horizontal", Ratio: 2, ImageURL: "https://e2.example.com/i.png", DayOfWeek: 3},
	}})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, float64(5), resp["notesPerOneAd"])

	ads, ok := resp["ads"].([]any)
	require.True(t, ok, "ads should be an array")
	require.Len(t, ads, 2)
	first := ads[0].(map[string]any)
	assert.Equal(t, "ad1", first["id"])
	assert.Equal(t, "square", first["place"])
	assert.Equal(t, float64(1), first["ratio"])
}

// AdRepository が未設定の場合 (既存テスト互換) は空配列が返る。
func TestMeta_AdsUnwiredReturnsEmpty(t *testing.T) {
	h, metaRepo := newTestHandler()
	metaRepo.Meta = &model.Meta{ID: "x", NotesPerOneAd: 0}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	ads, ok := resp["ads"].([]any)
	require.True(t, ok)
	assert.Empty(t, ads)
	assert.Equal(t, float64(0), resp["notesPerOneAd"])
}

// AdRepository がエラーを返したとき、meta エンドポイント全体は落とさずに
// ads を空として継続する (DB 障害で /api/meta 全体が 500 になると起動ページ
// がロードできない)。
func TestMeta_AdsRepoErrorFallsBackToEmpty(t *testing.T) {
	h, metaRepo := newTestHandler()
	metaRepo.Meta = &model.Meta{ID: "x"}
	h.SetAdRepo(&stubAdRepo{err: assert.AnError})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	ads, ok := resp["ads"].([]any)
	require.True(t, ok)
	assert.Empty(t, ads)
}

func TestMeta_DetailFalse(t *testing.T) {
	h, metaRepo := newTestHandler()
	name := "Instance"
	metaRepo.Meta = &model.Meta{ID: "x", Name: &name, EnableHcaptcha: true}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", strings.NewReader(`{"detail":false}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	// 基本フィールドが含まれる
	assert.Equal(t, "Instance", resp["name"])
	assert.Equal(t, true, resp["enableHcaptcha"])
	assert.Equal(t, config.MisskeyVersion, resp["version"])
	// 省かれるフィールド
	assert.Nil(t, resp["features"])
	assert.Nil(t, resp["policies"])
	assert.Nil(t, resp["clientOptions"])
	assert.Nil(t, resp["noteSearchableScope"])
}

// TestMeta_FeaturesTimelineFlags verifies that features.localTimeline and
// features.globalTimeline are exposed from the default role policies, so the
// frontend can decide whether to render LTL/GTL tabs.
func TestMeta_FeaturesTimelineFlags(t *testing.T) {
	h, metaRepo := newTestHandler()
	metaRepo.Meta = &model.Meta{ID: "x"}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	features, ok := resp["features"].(map[string]any)
	require.True(t, ok, "features should be present when detail=true")
	assert.Equal(t, true, features["localTimeline"], "ltlAvailable default is true")
	assert.Equal(t, true, features["globalTimeline"], "gtlAvailable default is true")
}

// upstream Misskey #17341 (= 2026.5.0 fix / triage #1008): noteSearchableScope は
// "local" を返すのが fulltextSearch.provider="meilisearch" かつ meilisearch.scope=
// "local" のときだけ、それ以外は全て "global"。旧 mk-go は default "local" を
// 返していたが、upstream 修正に合わせて default "global" に揃える。
//
// TestMeta_NoteSearchableScope_DefaultGlobal covers the fallback path where
// no search config exists: scope defaults to "global".
func TestMeta_NoteSearchableScope_DefaultGlobal(t *testing.T) {
	h, metaRepo := newTestHandler()
	metaRepo.Meta = &model.Meta{ID: "x"}
	// config.FulltextSearch / config.Meilisearch are nil in newTestHandler().

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "global", resp["noteSearchableScope"])
}

// fulltextSearch.provider が "sqlLike" (= meilisearch を使っていない) のとき、
// meilisearch.scope="local" が誤って反映されないこと (= upstream #17341 bug 本体)。
func TestMeta_NoteSearchableScope_SqlLikeIgnoresMeilisearchScope(t *testing.T) {
	cfg := &config.Config{
		Version:        "2026.5.1",
		URL:            "https://misskey.example.com",
		FulltextSearch: &config.FulltextSearchOptions{Provider: "sqlLike"},
		Meilisearch:    &config.MeilisearchOptions{Host: "ms", Port: "7700", Scope: "local"},
	}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	h := NewHandler(cfg, metaRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "global", resp["noteSearchableScope"],
		"sqlLike provider 時は meilisearch.scope を無視して global を返す (upstream #17341)")
}

// fulltextSearch.provider="meilisearch" + meilisearch.scope="local" → "local"。
// 唯一の "local" 返却ケース。
func TestMeta_NoteSearchableScope_MeilisearchLocal(t *testing.T) {
	cfg := &config.Config{
		Version:        "2026.5.1",
		URL:            "https://misskey.example.com",
		FulltextSearch: &config.FulltextSearchOptions{Provider: "meilisearch"},
		Meilisearch:    &config.MeilisearchOptions{Host: "ms", Port: "7700", Scope: "local"},
	}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	h := NewHandler(cfg, metaRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "local", resp["noteSearchableScope"])
}

// fulltextSearch.provider="meilisearch" + meilisearch.scope="global" → "global"。
// meilisearch でも明示的に global scope を選んでいるケース。
func TestMeta_NoteSearchableScope_MeilisearchGlobal(t *testing.T) {
	cfg := &config.Config{
		Version:        "2026.5.1",
		URL:            "https://misskey.example.com",
		FulltextSearch: &config.FulltextSearchOptions{Provider: "meilisearch"},
		Meilisearch:    &config.MeilisearchOptions{Host: "ms", Port: "7700", Scope: "global"},
	}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	h := NewHandler(cfg, metaRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "global", resp["noteSearchableScope"])
}

func TestPolicyBool(t *testing.T) {
	cases := []struct {
		name     string
		policies map[string]any
		key      string
		want     bool
	}{
		{"present true", map[string]any{"k": true}, "k", true},
		{"present false", map[string]any{"k": false}, "k", false},
		{"missing", map[string]any{}, "k", false},
		{"non-bool type", map[string]any{"k": "yes"}, "k", false},
		{"nil value", map[string]any{"k": nil}, "k", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, PolicyBool(tc.policies, tc.key))
		})
	}
}

// --- Phase 7-5c follow-up (#256): providesTarball / sentryForFrontend /
//     proxyAccountName のフィールド追加 ---

// providesTarballはconfig.PublishTarballInsteadOfProvideRepositoryUrlをそのまま返す。
func TestMeta_ProvidesTarball(t *testing.T) {
	cfg := &config.Config{
		Version: config.MisskeyVersion,
		URL:     "https://misskey.example.com",
		PublishTarballInsteadOfProvideRepositoryUrl: true,
	}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	h := NewHandler(cfg, metaRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["providesTarball"])
}

func TestMeta_ProvidesTarball_DefaultFalse(t *testing.T) {
	h, repo := newTestHandler()
	repo.Meta = &model.Meta{ID: "x"}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["providesTarball"])
}

// sentryForFrontendはconfig.SentryForFrontend mapをそのまま返し、空なら null。
func TestMeta_SentryForFrontend_Populated(t *testing.T) {
	cfg := &config.Config{
		Version: config.MisskeyVersion,
		URL:     "https://misskey.example.com",
		SentryForFrontend: map[string]any{
			"options": map[string]any{"dsn": "https://sentry.example/1"},
		},
	}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	h := NewHandler(cfg, metaRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	sentry, ok := resp["sentryForFrontend"].(map[string]any)
	require.True(t, ok)
	opts, _ := sentry["options"].(map[string]any)
	assert.Equal(t, "https://sentry.example/1", opts["dsn"])
}

func TestMeta_SentryForFrontend_EmptyReturnsNil(t *testing.T) {
	h, repo := newTestHandler()
	repo.Meta = &model.Meta{ID: "x"}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Nil(t, resp["sentryForFrontend"])
}

// proxyAccountNameはresolverが値を返せばそれをexpose、解決不能ならnull。
func TestMeta_ProxyAccountName_Resolved(t *testing.T) {
	h, repo := newTestHandler()
	repo.Meta = &model.Meta{ID: "x"}
	h.SetProxyAccountResolver(func() (string, bool) {
		return "instance.proxy", true
	})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "instance.proxy", resp["proxyAccountName"])
}

func TestMeta_ProxyAccountName_ResolverReturnsFalse(t *testing.T) {
	h, repo := newTestHandler()
	repo.Meta = &model.Meta{ID: "x"}
	h.SetProxyAccountResolver(func() (string, bool) { return "", false })

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Nil(t, resp["proxyAccountName"])
}

func TestMeta_ProxyAccountName_NoResolver(t *testing.T) {
	h, repo := newTestHandler()
	repo.Meta = &model.Meta{ID: "x"}
	// resolver未設定時はnil返却 (tests / pre-setup でのデフォルト動作)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/meta", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Meta(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Nil(t, resp["proxyAccountName"])
}
