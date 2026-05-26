package invite

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/entitycompat/shapetest"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errStub = errors.New("stub error")

// stubInvitePolicy is a minimal RolePolicyProvider double.
type stubInvitePolicy struct {
	policies map[string]any
}

func (s *stubInvitePolicy) GetUserPolicies(_ string) map[string]any { return s.policies }

func newTestHandler(t *testing.T) (*Handler, *testutil.MockRegistrationTicketRepository) {
	t.Helper()
	repo := testutil.NewMockRegistrationTicketRepository()
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	return NewHandler(repo, idGen), repo
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

// --- Create -----------------------------------------------------------------

func TestCreate_NoPolicyProviderSucceeds(t *testing.T) {
	h, repo := newTestHandler(t)
	rec := post(h.Create, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, repo.Tickets, 1)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp["code"])
	// inviteExpirationTime 未設定 → expiresAt は null
	assert.Nil(t, resp["expiresAt"])
	assert.Equal(t, false, resp["used"])
	assert.NotEmpty(t, resp["createdAt"])
	shapetest.Assert(t, "InviteCode", resp) // L3 (#1270)
}

func TestCreate_LimitExceeded(t *testing.T) {
	h, repo := newTestHandler(t)
	// 既に 2 invite 保有 (CountByCreatorSince で count される)
	user := "u1"
	repo.Tickets["t1"] = &model.RegistrationTicket{ID: "z_t1", CreatedByID: &user}
	repo.Tickets["t2"] = &model.RegistrationTicket{ID: "z_t2", CreatedByID: &user}
	h.SetRolePolicyProvider(&stubInvitePolicy{policies: map[string]any{
		"inviteLimit":      2,
		"inviteLimitCycle": 60 * 24, // 1 day, ensure sinceID is well before now
	}})
	rec := post(h.Create, `{}`, &model.User{ID: user})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "EXCEEDED_LIMIT_OF_CREATE_INVITE_CODE")
	assert.Contains(t, rec.Body.String(), "8b165dd3-6f37-4557-8db1-73175d63c641")
}

func TestCreate_LimitPassesUnderLimit(t *testing.T) {
	h, _ := newTestHandler(t)
	h.SetRolePolicyProvider(&stubInvitePolicy{policies: map[string]any{
		"inviteLimit":      10,
		"inviteLimitCycle": 60,
	}})
	rec := post(h.Create, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code, "limit 内なら通常成功")
}

func TestCreate_LimitZeroOrUnsetIsUnlimited(t *testing.T) {
	h, _ := newTestHandler(t)
	h.SetRolePolicyProvider(&stubInvitePolicy{policies: map[string]any{
		"inviteLimit": 0, // upstream falsy = skip
	}})
	rec := post(h.Create, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code, "policy 0 は upstream falsy 相当で gate skip")
}

func TestCreate_ExpiresAtSetFromPolicy(t *testing.T) {
	h, repo := newTestHandler(t)
	h.SetRolePolicyProvider(&stubInvitePolicy{policies: map[string]any{
		"inviteExpirationTime": 60, // 60 minutes
	}})
	rec := post(h.Create, `{}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, repo.Tickets, 1)
	var saved *model.RegistrationTicket
	for _, t := range repo.Tickets {
		saved = t
	}
	require.NotNil(t, saved.ExpiresAt, "expiresAt は inviteExpirationTime > 0 で設定される")
}

// countErrRepo は CountByCreatorSince だけ error を返す stub。
type countErrRepo struct {
	*testutil.MockRegistrationTicketRepository
}

func (r *countErrRepo) CountByCreatorSince(_, _ string) (int64, error) { return 0, errStub }

func TestCreate_CountErrorReturns500(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&countErrRepo{testutil.NewMockRegistrationTicketRepository()}, idGen)
	h.SetRolePolicyProvider(&stubInvitePolicy{policies: map[string]any{"inviteLimit": 5}})
	rec := post(h.Create, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// failCreateRepo は Create だけ error を返す stub。
type failCreateRepo struct {
	*testutil.MockRegistrationTicketRepository
}

func (r *failCreateRepo) Create(_ *model.RegistrationTicket) error { return errStub }

func TestCreate_RepoCreateError(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&failCreateRepo{testutil.NewMockRegistrationTicketRepository()}, idGen)
	rec := post(h.Create, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- List -------------------------------------------------------------------

func TestList_Success(t *testing.T) {
	h, repo := newTestHandler(t)
	user := "u1"
	repo.Tickets["t1"] = &model.RegistrationTicket{ID: "z_a1", Code: "code-a1", CreatedByID: &user}
	repo.Tickets["t2"] = &model.RegistrationTicket{ID: "z_a2", Code: "code-a2", CreatedByID: &user}
	otherUser := "u2"
	repo.Tickets["t3"] = &model.RegistrationTicket{ID: "z_a3", Code: "code-a3", CreatedByID: &otherUser}
	rec := post(h.List, `{"limit":50}`, &model.User{ID: user})
	require.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Len(t, out, 2, "他 user の invite は除外される")
}

func TestList_Empty(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := post(h.List, `{}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]", strings.TrimSpace(rec.Body.String()))
}

// listErrRepo は ListByCreator だけ error を返す stub。
type listErrRepo struct {
	*testutil.MockRegistrationTicketRepository
}

func (r *listErrRepo) ListByCreator(_, _, _ string, _ int) ([]*model.RegistrationTicket, error) {
	return nil, errStub
}

func TestList_RepoErrorReturnsEmptyArray(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&listErrRepo{testutil.NewMockRegistrationTicketRepository()}, idGen)
	rec := post(h.List, `{}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code, "DB error 時も 200 + 空 array で graceful degrade する")
	assert.Equal(t, "[]", strings.TrimSpace(rec.Body.String()))
}

// --- Delete -----------------------------------------------------------------

func TestDelete_Success(t *testing.T) {
	h, repo := newTestHandler(t)
	user := "u1"
	repo.Tickets["t1"] = &model.RegistrationTicket{ID: "t1", CreatedByID: &user}
	rec := post(h.Delete, `{"inviteId":"t1"}`, &model.User{ID: user})
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.NotContains(t, repo.Tickets, "t1")
}

func TestDelete_InvalidParam(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := post(h.Delete, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDelete_NotFoundReturnsNoContent(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := post(h.Delete, `{"inviteId":"ghost"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code, "not found は idempotent に 204")
}

func TestDelete_AccessDeniedWhenNotCreator(t *testing.T) {
	h, repo := newTestHandler(t)
	owner := "u2"
	repo.Tickets["t1"] = &model.RegistrationTicket{ID: "t1", CreatedByID: &owner}
	rec := post(h.Delete, `{"inviteId":"t1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "ACCESS_DENIED")
}

// failDeleteRepo は Delete だけ error を返す stub。
type failDeleteRepo struct {
	*testutil.MockRegistrationTicketRepository
}

func (r *failDeleteRepo) Delete(_ string) error { return errStub }

func TestDelete_RepoErrorReturns500(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	mock := testutil.NewMockRegistrationTicketRepository()
	user := "u1"
	mock.Tickets["t1"] = &model.RegistrationTicket{ID: "t1", CreatedByID: &user}
	h := NewHandler(&failDeleteRepo{mock}, idGen)
	rec := post(h.Delete, `{"inviteId":"t1"}`, &model.User{ID: user})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Limit ------------------------------------------------------------------

func TestLimit_UnlimitedWhenNoPolicy(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := post(h.Limit, `{}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Nil(t, out["remaining"], "provider 未配線は null = 無制限")
}

func TestLimit_UnlimitedWhenInviteLimitZero(t *testing.T) {
	h, _ := newTestHandler(t)
	h.SetRolePolicyProvider(&stubInvitePolicy{policies: map[string]any{"inviteLimit": 0}})
	rec := post(h.Limit, `{}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Nil(t, out["remaining"], "policy 0 は falsy で null = 無制限")
}

func TestLimit_ReturnsRemaining(t *testing.T) {
	h, repo := newTestHandler(t)
	user := "u1"
	// 既に 3 invite (= z_a* で時刻 prefix > sinceID になるよう)
	repo.Tickets["t1"] = &model.RegistrationTicket{ID: "z_a1", CreatedByID: &user}
	repo.Tickets["t2"] = &model.RegistrationTicket{ID: "z_a2", CreatedByID: &user}
	repo.Tickets["t3"] = &model.RegistrationTicket{ID: "z_a3", CreatedByID: &user}
	h.SetRolePolicyProvider(&stubInvitePolicy{policies: map[string]any{
		"inviteLimit":      10,
		"inviteLimitCycle": 60 * 24,
	}})
	rec := post(h.Limit, `{}`, &model.User{ID: user})
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	// json.Unmarshal は int を float64 にする
	assert.Equal(t, float64(7), out["remaining"], "10 - 3 = 7")
}

func TestLimit_RemainingClampedToZero(t *testing.T) {
	h, repo := newTestHandler(t)
	user := "u1"
	for i := 0; i < 5; i++ {
		repo.Tickets[string(rune('a'+i))] = &model.RegistrationTicket{
			ID:          "z_" + string(rune('a'+i)),
			CreatedByID: &user,
		}
	}
	h.SetRolePolicyProvider(&stubInvitePolicy{policies: map[string]any{
		"inviteLimit":      3,
		"inviteLimitCycle": 60 * 24,
	}})
	rec := post(h.Limit, `{}`, &model.User{ID: user})
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, float64(0), out["remaining"], "limit < count でも 0 で打ち切り")
}

func TestLimit_CountErrorReturns500(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(&countErrRepo{testutil.NewMockRegistrationTicketRepository()}, idGen)
	h.SetRolePolicyProvider(&stubInvitePolicy{policies: map[string]any{"inviteLimit": 5}})
	rec := post(h.Limit, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
