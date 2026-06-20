package nodeinfo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersion2_1(t *testing.T) {
	h := NewHandler(&config.Config{Version: "0.0.0", Host: "example.com"})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/nodeinfo/2.1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Version2_1(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "2.1", resp["version"])
	sw := resp["software"].(map[string]any)
	assert.Equal(t, "mk-go", sw["name"])
	assert.Equal(t, config.MkGoVersion, sw["version"])
	// #1925: 2.1 は homepage=repository=softwareRepository。
	assert.Equal(t, softwareRepository, sw["homepage"])
	assert.Equal(t, softwareRepository, sw["repository"])
}

// admin で設定した Meta.Name / Description がnodeinfo.metadata に反映される
// (#348)。これが効かないとリモートが受け取る nodeName はconfig default
// (= host) のまま更新されない。
func TestVersion2_1_ReflectsMetaName(t *testing.T) {
	metaRepo := testutil.NewMockMetaRepository()
	name := "My Custom Instance"
	desc := "hello world"
	mn := "admin"
	me := "admin@example.com"
	metaRepo.Meta = &model.Meta{
		ID: "x", Name: &name, Description: &desc,
		MaintainerName: &mn, MaintainerEmail: &me,
	}
	h := NewHandler(&config.Config{Version: "0.0.0", Host: "fallback.example"})
	h.SetMetaRepo(metaRepo)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/nodeinfo/2.1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Version2_1(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	meta := resp["metadata"].(map[string]any)
	assert.Equal(t, "My Custom Instance", meta["nodeName"])
	assert.Equal(t, "hello world", meta["nodeDescription"])
	maint := meta["maintainer"].(map[string]any)
	assert.Equal(t, "admin", maint["name"])
	assert.Equal(t, "admin@example.com", maint["email"])
}

// #1777 [HIGH]: /.well-known/nodeinfo が advertise する /nodeinfo/2.0 を実際に配信し、
// schema 2.0 には無い software.repository を省略する (2.1 では出す)。
func TestVersion2_0(t *testing.T) {
	h := NewHandler(&config.Config{Version: "0.0.0", Host: "example.com"})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/nodeinfo/2.0", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Version2_0(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "2.0", resp["version"])
	sw := resp["software"].(map[string]any)
	assert.Equal(t, "mk-go", sw["name"])
	_, hasRepo := sw["repository"]
	assert.False(t, hasRepo, "schema 2.0 は software.repository を含めない")
	// #1925: 2.0 は repository を delete するが homepage は残す。
	assert.Equal(t, softwareRepository, sw["homepage"], "schema 2.0 も software.homepage を含む")

	// 対照: 2.1 は repository を含む。
	rec21 := httptest.NewRecorder()
	require.NoError(t, h.Version2_1(e.NewContext(httptest.NewRequest(http.MethodGet, "/nodeinfo/2.1", nil), rec21)))
	var resp21 map[string]any
	require.NoError(t, json.Unmarshal(rec21.Body.Bytes(), &resp21))
	_, has21 := resp21["software"].(map[string]any)["repository"]
	assert.True(t, has21, "schema 2.1 は software.repository を含む")
}

// #1777 [MEDIUM]: metadata block が upstream の追加フィールド (themeColor /
// nodeAdmins / langs / disable*Timeline / captcha / maxNoteTextLength /
// proxyAccountName 等) を出す。
func TestVersion2_1_FullMetadata(t *testing.T) {
	metaRepo := testutil.NewMockMetaRepository()
	name := "Inst"
	tos := "https://example.com/tos"
	theme := "#abcdef"
	metaRepo.Meta = &model.Meta{
		ID: "x", Name: &name,
		TermsOfServiceURL:      &tos,
		ThemeColor:             &theme,
		Langs:                  []string{"ja-JP", "en-US"},
		EnableHcaptcha:         true,
		EmailRequiredForSignup: true,
		DisableRegistration:    true,
	}
	h := NewHandler(&config.Config{Version: "0.0.0", Host: "fallback.example"})
	h.SetMetaRepo(metaRepo)
	h.SetProxyAccountResolver(func() (string, bool) { return "proxy.bot", true })

	e := echo.New()
	rec := httptest.NewRecorder()
	require.NoError(t, h.Version2_1(e.NewContext(httptest.NewRequest(http.MethodGet, "/nodeinfo/2.1", nil), rec)))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	meta := resp["metadata"].(map[string]any)

	assert.Equal(t, "#abcdef", meta["themeColor"])
	assert.Equal(t, []any{"ja-JP", "en-US"}, meta["langs"])
	assert.Equal(t, "https://example.com/tos", meta["tosUrl"])
	assert.Equal(t, true, meta["enableHcaptcha"])
	assert.Equal(t, true, meta["emailRequiredForSignup"])
	assert.Equal(t, true, meta["disableRegistration"])
	assert.Equal(t, float64(maxNoteTextLength), meta["maxNoteTextLength"])
	assert.Equal(t, "proxy.bot", meta["proxyAccountName"])
	// nodeAdmins は [{name,email}] 配列。
	admins := meta["nodeAdmins"].([]any)
	require.Len(t, admins, 1)
	// disable*Timeline は policy 由来 (default policy では both available=true → false)。
	assert.Equal(t, false, meta["disableLocalTimeline"])
	assert.Equal(t, false, meta["disableGlobalTimeline"])
}

// themeColor 未設定時は upstream default '#86b300' を出す。
func TestVersion2_1_ThemeColorDefault(t *testing.T) {
	h := NewHandler(&config.Config{Version: "0.0.0", Host: "example.com"})
	e := echo.New()
	rec := httptest.NewRecorder()
	require.NoError(t, h.Version2_1(e.NewContext(httptest.NewRequest(http.MethodGet, "/nodeinfo/2.1", nil), rec)))
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, defaultThemeColor, resp["metadata"].(map[string]any)["themeColor"])
}

// Meta未設定時は cfg.Host が nodeName に入る。
func TestVersion2_1_FallbackToConfigHost(t *testing.T) {
	h := NewHandler(&config.Config{Version: "0.0.0", Host: "example.com"})
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/nodeinfo/2.1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Version2_1(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	meta := resp["metadata"].(map[string]any)
	assert.Equal(t, "example.com", meta["nodeName"])
}

// #403: nodeinfo usage 統計値が repo 経由で埋まる。
func TestVersion2_1_UsageStatsFromRepos(t *testing.T) {
	now := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()

	// Local users 計4人: active 月内2 / 半年内3 / それ以外1。
	within1Month := now.AddDate(0, 0, -10)
	within6Month := now.AddDate(0, -3, 0)
	beyond6Month := now.AddDate(0, -7, 0)
	userRepo.Users["u1"] = &model.User{ID: "u1", Host: nil, LastActiveDate: &within1Month}
	userRepo.Users["u2"] = &model.User{ID: "u2", Host: nil, LastActiveDate: &within1Month}
	userRepo.Users["u3"] = &model.User{ID: "u3", Host: nil, LastActiveDate: &within6Month}
	// active 外の local user (6ヶ月前)
	userRepo.Users["u4"] = &model.User{ID: "u4", Host: nil, LastActiveDate: &beyond6Month}
	// remote user (count 対象外)
	remoteHost := "remote.example"
	userRepo.Users["r1"] = &model.User{ID: "r1", Host: &remoteHost, LastActiveDate: &within1Month}

	// Local notes: 5本、内 2本が reply
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1", UserHost: nil}
	noteRepo.Notes["n2"] = &model.Note{ID: "n2", UserID: "u1", UserHost: nil}
	replyA := "n1"
	noteRepo.Notes["n3"] = &model.Note{ID: "n3", UserID: "u2", UserHost: nil, ReplyID: &replyA}
	noteRepo.Notes["n4"] = &model.Note{ID: "n4", UserID: "u3", UserHost: nil}
	noteRepo.Notes["n5"] = &model.Note{ID: "n5", UserID: "u3", UserHost: nil, ReplyID: &replyA}
	// remote note (対象外)
	noteRepo.Notes["r1"] = &model.Note{ID: "r1", UserID: "ru", UserHost: &remoteHost}

	h := NewHandler(&config.Config{Version: "0.0.0", Host: "example.com"})
	h.SetUsageRepos(userRepo, noteRepo)
	h.SetClock(func() time.Time { return now })

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/nodeinfo/2.1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Version2_1(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	usage := resp["usage"].(map[string]any)
	users := usage["users"].(map[string]any)
	assert.Equal(t, float64(4), users["total"]) // u1-u4 local
	assert.Equal(t, float64(2), users["activeMonth"])
	assert.Equal(t, float64(3), users["activeHalfyear"])
	assert.Equal(t, float64(5), usage["localPosts"])
	assert.Equal(t, float64(2), usage["localComments"])
}

// repo のerror path を通して slog.Warn 分岐を cover する。他の test
// file と同じく pointer embedding で mock を埋め込んで method promotion を
// 正しく働かせる。
type failingUserRepoForCount struct {
	*testutil.MockUserRepository
}

func (f *failingUserRepoForCount) CountLocalUsers() (int64, error) {
	return 0, errForTest
}

func (f *failingUserRepoForCount) CountLocalUsersActiveSince(time.Time) (int64, error) {
	return 0, errForTest
}

type failingNoteRepoForCount struct {
	*testutil.MockNoteRepository
}

func (f *failingNoteRepoForCount) CountLocalNotes() (int64, error)    { return 0, errForTest }
func (f *failingNoteRepoForCount) CountLocalComments() (int64, error) { return 0, errForTest }

var errForTest = errTest{}

type errTest struct{}

func (errTest) Error() string { return "test error" }

func TestVersion2_1_UsageStatsCountErrorsFallbackToZero(t *testing.T) {
	h := NewHandler(&config.Config{Version: "0.0.0", Host: "example.com"})
	h.SetUsageRepos(
		&failingUserRepoForCount{MockUserRepository: testutil.NewMockUserRepository()},
		&failingNoteRepoForCount{MockNoteRepository: testutil.NewMockNoteRepository()},
	)
	h.SetClock(func() time.Time { return time.Unix(0, 0) })

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/nodeinfo/2.1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Version2_1(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	usage := resp["usage"].(map[string]any)
	users := usage["users"].(map[string]any)
	assert.Equal(t, float64(0), users["total"])
	assert.Equal(t, float64(0), users["activeMonth"])
	assert.Equal(t, float64(0), users["activeHalfyear"])
	assert.Equal(t, float64(0), usage["localPosts"])
	assert.Equal(t, float64(0), usage["localComments"])
}

// 未配線時は 0 埋め
func TestVersion2_1_UsageStatsZeroWhenReposMissing(t *testing.T) {
	h := NewHandler(&config.Config{Version: "0.0.0", Host: "example.com"})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/nodeinfo/2.1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Version2_1(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	usage := resp["usage"].(map[string]any)
	users := usage["users"].(map[string]any)
	assert.Equal(t, float64(0), users["total"])
	assert.Equal(t, float64(0), users["activeMonth"])
	assert.Equal(t, float64(0), users["activeHalfyear"])
	assert.Equal(t, float64(0), usage["localPosts"])
	assert.Equal(t, float64(0), usage["localComments"])
}
