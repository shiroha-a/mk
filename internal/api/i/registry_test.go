package i

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// postRegistryWithScope posts to a registry handler with both the authenticated
// user and an AuthScope (app token / native) set in the context, so #1717
// domain-isolation can be exercised.
func postRegistryWithScope(h func(echo.Context) error, body string, user *model.User, scope *middleware.AuthScope) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), user)
	if scope != nil {
		c.Set(string(middleware.AuthScopeContextKey), scope)
	}
	_ = h(c)
	return rec
}

// firstRegistryItem returns the single stored item (tests store exactly one).
func firstRegistryItem(reg *testutil.MockRegistryRepository) *model.RegistryItem {
	for _, it := range reg.Items {
		return it
	}
	return nil
}

// #1717: app access token は registry domain を自分の token id に強制する。
func TestRegistrySet_AppTokenForcesDomain(t *testing.T) {
	h, _ := newExtraHandler(t)
	reg := testutil.NewMockRegistryRepository()
	h.SetRegistryRepo(reg)
	// client が domain を送っても app token は token id に上書きされる。
	rec := postRegistryWithScope(h.RegistrySet, `{"key":"theme","value":"dark","scope":["client"],"domain":"ignored.example"}`,
		stubUser, &middleware.AuthScope{IsApp: true, TokenID: "tok-1"})
	require.Equal(t, http.StatusNoContent, rec.Code)
	it := firstRegistryItem(reg)
	require.NotNil(t, it)
	require.NotNil(t, it.Domain)
	assert.Equal(t, "tok-1", *it.Domain, "app token は domain を token id に強制する")
}

// native token / cookie session は request の domain をそのまま使う。
func TestRegistrySet_NativeUsesRequestDomain(t *testing.T) {
	h, _ := newExtraHandler(t)
	reg := testutil.NewMockRegistryRepository()
	h.SetRegistryRepo(reg)
	// AuthScope 無し (native)。
	rec := postRegistryWithScope(h.RegistrySet, `{"key":"theme","value":"dark","scope":["client"],"domain":"client.example"}`,
		stubUser, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	it := firstRegistryItem(reg)
	require.NotNil(t, it)
	require.NotNil(t, it.Domain)
	assert.Equal(t, "client.example", *it.Domain)
}

// app token の set は domain が token id (非 nil) に強制されるため main へ
// publish されない。
func TestRegistrySet_AppTokenNoMainPublish(t *testing.T) {
	h, _ := newExtraHandler(t)
	reg := testutil.NewMockRegistryRepository()
	h.SetRegistryRepo(reg)
	pub := &stubIMainStreamPublisher{}
	h.SetMainStreamPublisher(pub)
	rec := postRegistryWithScope(h.RegistrySet, `{"key":"theme","value":"dark","scope":["client"]}`,
		stubUser, &middleware.AuthScope{IsApp: true, TokenID: "tok-1"})
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, pub.calls, "app token (domain=token id) の set は main へ broadcast しない")
}

// native token (domain なし) の set は main へ publish される (従来挙動)。
func TestRegistrySet_NativeMainPublish(t *testing.T) {
	h, _ := newExtraHandler(t)
	reg := testutil.NewMockRegistryRepository()
	h.SetRegistryRepo(reg)
	pub := &stubIMainStreamPublisher{}
	h.SetMainStreamPublisher(pub)
	rec := postRegistryWithScope(h.RegistrySet, `{"key":"theme","value":"dark","scope":["client"]}`, stubUser, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Len(t, pub.calls, 1)
	assert.Equal(t, "registryUpdated", pub.calls[0].eventType)
}

func TestRegistryGetDetail(t *testing.T) {
	h, _ := newExtraHandler(t)
	assert.Equal(t, http.StatusOK, postExtra(h.RegistryGetDetail, `{}`, stubUser).Code)
}

func TestRegistryKeys(t *testing.T) {
	h, _ := newExtraHandler(t)
	assert.Equal(t, http.StatusOK, postExtra(h.RegistryKeys, `{}`, stubUser).Code)
}

func TestRegistryScopesWithDomain(t *testing.T) {
	h, _ := newExtraHandler(t)
	assert.Equal(t, http.StatusOK, postExtra(h.RegistryScopesWithDomain, `{}`, stubUser).Code)
}

// #1546: scopes-with-domain は domain ごとに集約し scopes を string[][] で返す。
func TestRegistryScopesWithDomain_GroupedByDomain(t *testing.T) {
	h, _ := newExtraHandler(t)
	reg := testutil.NewMockRegistryRepository()
	dom := "client.example"
	require.NoError(t, reg.Set(&model.RegistryItem{ID: "r1", UserID: stubUser.ID, Key: "a", Scope: pq.StringArray{"client", "base"}, Domain: &dom}))
	require.NoError(t, reg.Set(&model.RegistryItem{ID: "r2", UserID: stubUser.ID, Key: "b", Scope: pq.StringArray{"client", "theme"}, Domain: &dom}))
	require.NoError(t, reg.Set(&model.RegistryItem{ID: "r3", UserID: stubUser.ID, Key: "c", Scope: pq.StringArray{"main"}}))
	h.SetRegistryRepo(reg)

	rec := postExtra(h.RegistryScopesWithDomain, `{}`, stubUser)
	require.Equal(t, http.StatusOK, rec.Code)
	var out []struct {
		Domain *string    `json:"domain"`
		Scopes [][]string `json:"scopes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	byDomain := map[string][][]string{}
	for _, e := range out {
		k := "nil"
		if e.Domain != nil {
			k = *e.Domain
		}
		byDomain[k] = e.Scopes
	}
	require.Contains(t, byDomain, "client.example")
	assert.Len(t, byDomain["client.example"], 2, "同一 domain の scope を scopes 配列に束ねる")
	require.Contains(t, byDomain, "nil")
	assert.Len(t, byDomain["nil"], 1)
}

// #1546: scope 要素が ^[a-zA-Z0-9_]+$ に反すると INVALID_PARAM。
func TestRegistryScopePatternRejected(t *testing.T) {
	h, _ := newExtraHandler(t)
	h.SetRegistryRepo(testutil.NewMockRegistryRepository())
	// RegistryGetDetail (registry.go) と RegistryGetAll (handler.go) の両系統を確認。
	assert.Equal(t, http.StatusBadRequest, postExtra(h.RegistryGetDetail, `{"key":"k","scope":["bad scope"]}`, stubUser).Code)
	assert.Equal(t, http.StatusBadRequest, postExtra(h.RegistryGetAll, `{"scope":["has.dot"]}`, stubUser).Code)
	assert.Equal(t, http.StatusBadRequest, postExtra(h.RegistryKeys, `{"scope":["nope!"]}`, stubUser).Code)
	// 正常な scope は通る。
	assert.Equal(t, http.StatusOK, postExtra(h.RegistryKeys, `{"scope":["client","base_1"]}`, stubUser).Code)
}

// --- P4-6 (#166): i/registry/* ---

func TestRegistryGetDetail_Found(t *testing.T) {
	h, _ := newExtraHandler(t)
	reg := testutil.NewMockRegistryRepository()
	require.NoError(t, reg.Set(&model.RegistryItem{
		ID: "r1", UserID: stubUser.ID, Key: "theme", Value: datatypes.JSON(`"dark"`),
	}))
	h.SetRegistryRepo(reg)
	rec := postExtra(h.RegistryGetDetail, `{"key":"theme"}`, stubUser)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// #1948-10: get-detail の updatedAt は upstream toISOString() (.000Z)。
// raw time.Time の RFC3339Nano だと wire-byte が乖離する。
func TestRegistryGetDetail_UpdatedAtFormat(t *testing.T) {
	h, _ := newExtraHandler(t)
	reg := testutil.NewMockRegistryRepository()
	require.NoError(t, reg.Set(&model.RegistryItem{
		ID: "r1", UserID: stubUser.ID, Key: "theme", Value: datatypes.JSON(`"dark"`),
		UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}))
	h.SetRegistryRepo(reg)
	rec := postExtra(h.RegistryGetDetail, `{"key":"theme"}`, stubUser)
	require.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "2026-01-02T03:04:05.000Z", got["updatedAt"], "updatedAt は .000Z 形式 (#1948-10)")
}

func TestRegistryGetDetail_NotFound(t *testing.T) {
	h, _ := newExtraHandler(t)
	h.SetRegistryRepo(testutil.NewMockRegistryRepository())
	rec := postExtra(h.RegistryGetDetail, `{"key":"ghost"}`, stubUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// 本家互換: JSON で scope を省略した場合でも、default の empty-scope アイテムに
// マッチすること (nil スライスが SQL NULL に化けると常に miss する bug の
// regression test — Devin review #175)。
func TestRegistryGetDetail_OmittedScopeFindsDefaultItem(t *testing.T) {
	h, _ := newExtraHandler(t)
	reg := testutil.NewMockRegistryRepository()
	// Scope を明示せず作成 (Set は item.Scope をそのまま使う)。デフォルト
	// の空スライス状態で保存することを mock で再現する。
	require.NoError(t, reg.Set(&model.RegistryItem{
		ID: "r1", UserID: stubUser.ID, Key: "theme", Value: datatypes.JSON(`"dark"`),
		Scope: []string{},
	}))
	h.SetRegistryRepo(reg)
	// scope 無しリクエスト
	rec := postExtra(h.RegistryGetDetail, `{"key":"theme"}`, stubUser)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRegistryKeys_OmittedScopeFindsDefaultItem(t *testing.T) {
	h, _ := newExtraHandler(t)
	reg := testutil.NewMockRegistryRepository()
	require.NoError(t, reg.Set(&model.RegistryItem{
		ID: "r1", UserID: stubUser.ID, Key: "alpha", Value: datatypes.JSON(`"x"`),
		Scope: []string{},
	}))
	h.SetRegistryRepo(reg)
	rec := postExtra(h.RegistryKeys, `{}`, stubUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got []string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Contains(t, got, "alpha")
}

func TestRegistryKeys_ReturnsKeysArray(t *testing.T) {
	h, _ := newExtraHandler(t)
	reg := testutil.NewMockRegistryRepository()
	require.NoError(t, reg.Set(&model.RegistryItem{ID: "r1", UserID: stubUser.ID, Key: "a", Value: datatypes.JSON(`"x"`)}))
	require.NoError(t, reg.Set(&model.RegistryItem{ID: "r2", UserID: stubUser.ID, Key: "b", Value: datatypes.JSON(`"y"`)}))
	h.SetRegistryRepo(reg)
	rec := postExtra(h.RegistryKeys, `{}`, stubUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got []string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Len(t, got, 2)
}

func TestRegistryScopesWithDomain_ReturnsDistinctPairs(t *testing.T) {
	h, _ := newExtraHandler(t)
	reg := testutil.NewMockRegistryRepository()
	require.NoError(t, reg.Set(&model.RegistryItem{ID: "r1", UserID: stubUser.ID, Key: "a", Value: datatypes.JSON(`"x"`), Scope: []string{"client"}}))
	require.NoError(t, reg.Set(&model.RegistryItem{ID: "r2", UserID: stubUser.ID, Key: "b", Value: datatypes.JSON(`"y"`), Scope: []string{"client"}}))
	require.NoError(t, reg.Set(&model.RegistryItem{ID: "r3", UserID: stubUser.ID, Key: "c", Value: datatypes.JSON(`"z"`), Scope: []string{"default"}}))
	h.SetRegistryRepo(reg)
	rec := postExtra(h.RegistryScopesWithDomain, `{}`, stubUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got []struct {
		Domain *string    `json:"domain"`
		Scopes [][]string `json:"scopes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	// #1546: domain (ここでは nil のみ) ごとに集約され、2 distinct scope が
	// scopes 配列に束ねられる。
	require.Len(t, got, 1)
	assert.Nil(t, got[0].Domain)
	assert.Len(t, got[0].Scopes, 2)
}
