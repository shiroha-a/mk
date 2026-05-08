package sw

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// --- Mock Repositories ---

var errMock = assert.AnError

type mockSwRepo struct {
	subs      map[string]*model.SwSubscription // userID:endpoint -> sub
	createErr error
	updateErr error
	// findErr injects an arbitrary error from FindByUserAndEndpoint regardless
	// of map state. 非 NotFound DB error の handler 分岐 (#918 / #917 観測性
	// pattern) を test するため。
	findErr error
}

func newMockSwRepo() *mockSwRepo {
	return &mockSwRepo{subs: make(map[string]*model.SwSubscription)}
}

func (m *mockSwRepo) FindByUserAndEndpoint(userID, endpoint string) (*model.SwSubscription, error) {
	if m.findErr != nil {
		return nil, m.findErr
	}
	key := userID + ":" + endpoint
	if s, ok := m.subs[key]; ok {
		return s, nil
	}
	// 実 repo (gorm) と同じく ErrRecordNotFound を返す。handler は errors.Is で
	// not-found / DB error を区別する設計。
	return nil, gorm.ErrRecordNotFound
}

func (m *mockSwRepo) FindByUserID(userID string) ([]*model.SwSubscription, error) {
	out := make([]*model.SwSubscription, 0)
	for _, s := range m.subs {
		if s.UserID == userID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *mockSwRepo) Create(sub *model.SwSubscription) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.subs[sub.UserID+":"+sub.Endpoint] = sub
	return nil
}

func (m *mockSwRepo) Update(sub *model.SwSubscription) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.subs[sub.UserID+":"+sub.Endpoint] = sub
	return nil
}

func (m *mockSwRepo) DeleteByEndpoint(endpoint string) error {
	for k, s := range m.subs {
		if s.Endpoint == endpoint {
			delete(m.subs, k)
		}
	}
	return nil
}

func (m *mockSwRepo) DeleteByUserAndEndpoint(userID, endpoint string) error {
	delete(m.subs, userID+":"+endpoint)
	return nil
}

type mockMetaRepo struct {
	meta *model.Meta
}

func (m *mockMetaRepo) Fetch() (*model.Meta, error) {
	if m.meta != nil {
		return m.meta, nil
	}
	return nil, errMock
}
func (m *mockMetaRepo) Update(_ map[string]any) error { return nil }
func (m *mockMetaRepo) EnsureInitial(_ string) error  { return nil }

func newTestHandler() (*Handler, *mockSwRepo) {
	repo := newMockSwRepo()
	swKey := "test-sw-key"
	metaRepo := &mockMetaRepo{meta: &model.Meta{SwPublicKey: &swKey}}
	idGen, _ := id.NewGenerator("aidx")
	return NewHandler(repo, metaRepo, idGen), repo
}

func post(handler func(echo.Context) error, body string, user *model.User) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if user != nil {
		c.Set(string(middleware.UserContextKey), user)
	}
	_ = handler(c)
	return rec
}

// --- Register ---

func TestRegister_New(t *testing.T) {
	h, repo := newTestHandler()
	user := &model.User{ID: "u1"}
	rec := post(h.Register, `{"endpoint":"https://push.example/1","auth":"a1","publickey":"pk1"}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "subscribed", resp["state"])
	assert.Equal(t, "test-sw-key", resp["key"])
	assert.Len(t, repo.subs, 1)
}

func TestRegister_AlreadySubscribed(t *testing.T) {
	h, repo := newTestHandler()
	repo.subs["u1:https://push.example/1"] = &model.SwSubscription{
		ID: "s1", UserID: "u1", Endpoint: "https://push.example/1", SendReadMessage: true,
	}
	rec := post(h.Register, `{"endpoint":"https://push.example/1","auth":"a1","publickey":"pk1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "already-subscribed", resp["state"])
}

func TestRegister_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.Register, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRegister_CreateError(t *testing.T) {
	h, repo := newTestHandler()
	repo.createErr = errMock
	rec := post(h.Register, `{"endpoint":"https://push.example/1","auth":"a1","publickey":"pk1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRegister_NoMeta(t *testing.T) {
	repo := newMockSwRepo()
	metaRepo := &mockMetaRepo{} // meta is nil
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(repo, metaRepo, idGen)

	rec := post(h.Register, `{"endpoint":"https://push.example/1","auth":"a1","publickey":"pk1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Nil(t, resp["key"])
}

// --- ShowRegistration ---

func TestShowRegistration_Found(t *testing.T) {
	h, repo := newTestHandler()
	repo.subs["u1:https://push.example/1"] = &model.SwSubscription{
		UserID: "u1", Endpoint: "https://push.example/1", SendReadMessage: false,
	}
	rec := post(h.ShowRegistration, `{"endpoint":"https://push.example/1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "u1", resp["userId"])
}

// TestShowRegistration_NotFound: upstream Misskey TS と同じく該当 row なし
// は 204 No Content を返す。旧実装は 200 + JSON null だったが、drop-in 互換
// のため 204 に揃えた (#918)。
func TestShowRegistration_NotFound(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.ShowRegistration, `{"endpoint":"https://ghost"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.String(), "204 response should have empty body")
}

// TestShowRegistration_DBError は #918 review fix の regression guard。
// gorm.ErrRecordNotFound 以外の error (= DB 障害等) は 204 で潰さず、500 で
// 観測性を保つ (#917 federation/show-instance と同 pattern)。
func TestShowRegistration_DBError(t *testing.T) {
	h, repo := newTestHandler()
	repo.findErr = errors.New("connection reset by peer")
	rec := post(h.ShowRegistration, `{"endpoint":"https://x"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestShowRegistration_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.ShowRegistration, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- UpdateRegistration ---

func TestUpdateRegistration_Success(t *testing.T) {
	h, repo := newTestHandler()
	repo.subs["u1:https://push.example/1"] = &model.SwSubscription{
		UserID: "u1", Endpoint: "https://push.example/1", SendReadMessage: false,
	}
	rec := post(h.UpdateRegistration, `{"endpoint":"https://push.example/1","sendReadMessage":true}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["sendReadMessage"])
}

func TestUpdateRegistration_NoSendReadMessage(t *testing.T) {
	h, repo := newTestHandler()
	repo.subs["u1:https://push.example/1"] = &model.SwSubscription{
		UserID: "u1", Endpoint: "https://push.example/1", SendReadMessage: false,
	}
	rec := post(h.UpdateRegistration, `{"endpoint":"https://push.example/1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdateRegistration_NotFound(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.UpdateRegistration, `{"endpoint":"https://ghost"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestUpdateRegistration_DBError は #918 review fix の regression guard。
// gorm.ErrRecordNotFound 以外の error (= DB 障害等) は 404 で潰さず、500 で
// 観測性を保つ。
func TestUpdateRegistration_DBError(t *testing.T) {
	h, repo := newTestHandler()
	repo.findErr = errors.New("connection reset by peer")
	rec := post(h.UpdateRegistration, `{"endpoint":"https://x"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestUpdateRegistration_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.UpdateRegistration, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdateRegistration_UpdateError(t *testing.T) {
	h, repo := newTestHandler()
	repo.subs["u1:https://push.example/1"] = &model.SwSubscription{
		UserID: "u1", Endpoint: "https://push.example/1",
	}
	repo.updateErr = errMock
	rec := post(h.UpdateRegistration, `{"endpoint":"https://push.example/1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Unregister ---

func TestUnregister_WithUser(t *testing.T) {
	h, repo := newTestHandler()
	repo.subs["u1:https://push.example/1"] = &model.SwSubscription{
		UserID: "u1", Endpoint: "https://push.example/1",
	}
	rec := post(h.Unregister, `{"endpoint":"https://push.example/1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, repo.subs)
}

func TestUnregister_WithoutUser(t *testing.T) {
	h, repo := newTestHandler()
	repo.subs["u1:https://push.example/1"] = &model.SwSubscription{
		UserID: "u1", Endpoint: "https://push.example/1",
	}
	rec := post(h.Unregister, `{"endpoint":"https://push.example/1"}`, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, repo.subs)
}

func TestUnregister_InvalidParam(t *testing.T) {
	h, _ := newTestHandler()
	rec := post(h.Unregister, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
