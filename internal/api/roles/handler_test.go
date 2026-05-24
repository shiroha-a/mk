package roles_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/roles"
	corerole "github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRoleNotesQuery is a test double for roles.RoleNotesQuery.
type mockRoleNotesQuery struct {
	Notes []*model.Note
	Err   error
}

func (m *mockRoleNotesQuery) ListByRole(roleID string, limit int, sinceID, untilID string) ([]*model.Note, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	result := m.Notes
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func newTestHandler(t *testing.T) (*roles.Handler, *testutil.MockRoleRepository) {
	t.Helper()
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := testutil.NewMockRoleAssignmentRepository(roleRepo)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	svc := corerole.NewService(roleRepo, assignRepo, metaRepo, idGen)
	h := roles.NewHandler(svc, idGen)
	return h, roleRepo
}

func doPost(h func(echo.Context) error, body string) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_ = h(c)
	return rec
}

func TestList_PublicOnly(t *testing.T) {
	h, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Public", IsPublic: true}
	roleRepo.Roles["r2"] = &model.Role{ID: "r2", Name: "Private", IsPublic: false}
	rec := doPost(h.List, `{}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
}

func TestList_Empty(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doPost(h.List, `{}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// #1249: misskey_dart の RolesListResponse.fromJson が非null必須とする
// createdAt (String) / updatedAt (String) / canEditMembersByModerator (bool) /
// usersCount (num) が含まれること。欠落で roles 一覧が cast crash していた。
func TestList_FullShape(t *testing.T) {
	h, roleRepo := newTestHandler(t)
	idGen, _ := id.NewGenerator("aidx")
	roleID := idGen.Generate(time.Now())
	roleRepo.Roles[roleID] = &model.Role{
		ID: roleID, Name: "Public", IsPublic: true,
		UpdatedAt:                 time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		CanEditMembersByModerator: true,
	}
	rec := doPost(h.List, `{}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	r := resp[0]
	createdAt, ok := r["createdAt"].(string)
	assert.True(t, ok, "createdAt must be a non-null string")
	assert.NotEmpty(t, createdAt)
	assert.Equal(t, "2026-05-01T00:00:00.000Z", r["updatedAt"])
	assert.Equal(t, true, r["canEditMembersByModerator"])
	assert.Equal(t, float64(0), r["usersCount"])
}

func TestShow_Success(t *testing.T) {
	h, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Pub", IsPublic: true}
	rec := doPost(h.Show, `{"roleId":"r1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestShow_NotPublic(t *testing.T) {
	h, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Priv", IsPublic: false}
	rec := doPost(h.Show, `{"roleId":"r1"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestShow_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doPost(h.Show, `{"roleId":"ghost"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestShow_InvalidParam(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doPost(h.Show, `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUsers_Success(t *testing.T) {
	h, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", IsPublic: true}
	rec := doPost(h.Users, `{"roleId":"r1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUsers_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doPost(h.Users, `{"roleId":"ghost"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUsers_InvalidParam(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doPost(h.Users, `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

type failingListRoleSvc struct{}

func TestList_Error(t *testing.T) {
	roleRepo := &failingListRepo{testutil.NewMockRoleRepository()}
	assignRepo := testutil.NewMockRoleAssignmentRepository(roleRepo.MockRoleRepository)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, _ := id.NewGenerator("aidx")
	svc := corerole.NewService(roleRepo, assignRepo, metaRepo, idGen)
	h := roles.NewHandler(svc, idGen)
	rec := doPost(h.List, `{}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type failingListRepo struct {
	*testutil.MockRoleRepository
}

func (f *failingListRepo) List() ([]*model.Role, error) { return nil, assert.AnError }

// --- Notes ---

func TestNotes_InvalidParam(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doPost(h.Notes, `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNotes_RoleNotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doPost(h.Notes, `{"roleId":"ghost"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestNotes_NotPublic(t *testing.T) {
	h, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Private", IsPublic: false}
	rec := doPost(h.Notes, `{"roleId":"r1"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestNotes_NilQuery(t *testing.T) {
	h, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Public", IsPublic: true}
	rec := doPost(h.Notes, `{"roleId":"r1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var arr []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &arr))
	assert.Empty(t, arr)
}

func TestNotes_Success(t *testing.T) {
	h, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Public", IsPublic: true}
	mock := &mockRoleNotesQuery{
		Notes: []*model.Note{
			{ID: "n1", UserID: "u1", Text: strPtr("hello"), Visibility: "public"},
		},
	}
	h.SetNotesQuery(mock)
	rec := doPost(h.Notes, `{"roleId":"r1"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var arr []any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &arr))
	assert.Len(t, arr, 1)
}

func TestNotes_QueryError(t *testing.T) {
	h, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Public", IsPublic: true}
	mock := &mockRoleNotesQuery{Err: assert.AnError}
	h.SetNotesQuery(mock)
	rec := doPost(h.Notes, `{"roleId":"r1"}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestNotes_DefaultLimit(t *testing.T) {
	h, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Public", IsPublic: true}
	mock := &mockRoleNotesQuery{}
	h.SetNotesQuery(mock)
	rec := doPost(h.Notes, `{"roleId":"r1","limit":0}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNotes_LimitClamped(t *testing.T) {
	h, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Public", IsPublic: true}
	mock := &mockRoleNotesQuery{}
	h.SetNotesQuery(mock)
	rec := doPost(h.Notes, `{"roleId":"r1","limit":999}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func strPtr(s string) *string { return &s }

// stubBufferedReactions implements entity.BufferedReactionsReader. 戻り値が
// 空マップでも reactionReader() が non-nil を返す経路を踏ませる。
type stubBufferedReactions struct{}

func (stubBufferedReactions) GetBufferedMany(_ context.Context, _ []string) (map[string]map[string]int64, error) {
	return map[string]map[string]int64{}, nil
}

// SetInstanceRepo / SetEmojiRepo / SetReactionReader / SetNoteFieldResolver
// を wire した状態で Notes を呼び、各 setter が field を設定し lookup の
// non-nil 分岐 (instanceLookup / emojiLookup) を踏むことを確認する。これら
// setter は他 handler でも同じ pattern なので回帰検知の意味も兼ねる (#739)。
func TestSettersWireOptionalDeps(t *testing.T) {
	h, roleRepo := newTestHandler(t)
	roleRepo.Roles["r1"] = &model.Role{ID: "r1", Name: "Public", IsPublic: true}

	instanceRepo := testutil.NewMockInstanceRepository()
	h.SetInstanceRepo(instanceRepo)
	emojiRepo := testutil.NewMockEmojiRepository()
	h.SetEmojiRepo(emojiRepo)
	h.SetReactionReader(stubBufferedReactions{})
	h.SetNoteFieldResolver(nil) // Apply は r==nil で no-op

	mock := &mockRoleNotesQuery{
		Notes: []*model.Note{
			{ID: "n1", UserID: "u1", Text: strPtr("hello"), Visibility: "public"},
		},
	}
	h.SetNotesQuery(mock)
	rec := doPost(h.Notes, `{"roleId":"r1"}`)
	require.Equal(t, http.StatusOK, rec.Code)
}
