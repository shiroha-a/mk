package roles_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/api/roles"
	corerole "github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const assignmentInternalErrorJSON = `{"error":{"message":"Internal error.","code":"INTERNAL_ERROR","id":"5d37dbcb-891e-41ca-a3d6-e690c97775ac","kind":"server"}}`
const assignmentNoSuchRoleJSON = `{"error":{"message":"No such role.","code":"NO_SUCH_ROLE","id":"4a3b21e8-9c54-4d7a-b1e2-8f6c9d0e5a71","kind":"client"}}`

type assignmentFailingRoleRepo struct{ repository.RoleRepository }

func (r *assignmentFailingRoleRepo) FindByID(string) (*model.Role, error) {
	return nil, errors.New("SELECT role: connection failed")
}

type assignmentExactRepo struct {
	*testutil.MockRoleAssignmentRepository
	err       error
	findCalls int
	listCalls int
	userID    string
	roleID    string
}

func (r *assignmentExactRepo) FindActive(userID, roleID string, at time.Time) (*model.RoleAssignment, error) {
	r.findCalls++
	r.userID, r.roleID = userID, roleID
	if r.err != nil {
		return nil, r.err
	}
	return r.MockRoleAssignmentRepository.FindActive(userID, roleID, at)
}

func (r *assignmentExactRepo) ListByUser(userID string) ([]*model.RoleAssignment, error) {
	r.listCalls++
	return r.MockRoleAssignmentRepository.ListByUser(userID)
}

func assignmentHandler(t *testing.T, roleRepo repository.RoleRepository, assignRepo repository.RoleAssignmentRepository) *roles.Handler {
	t.Helper()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	return roles.NewHandler(corerole.NewService(roleRepo, assignRepo, metaRepo, idGen), idGen)
}

func assignmentFixture(t *testing.T) (*roles.Handler, *assignmentExactRepo, *testutil.MockRoleRepository) {
	t.Helper()
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := &assignmentExactRepo{MockRoleAssignmentRepository: testutil.NewMockRoleAssignmentRepository(roleRepo)}
	return assignmentHandler(t, roleRepo, assignRepo), assignRepo, roleRepo
}

func postAssignment(h *roles.Handler, body, viewerID string) *httptest.ResponseRecorder {
	vc := newCtxWithViewer(body, viewerID)
	_ = h.AssignmentShow(vc.ctx)
	return vc.rec
}

func TestAssignmentShow_PublicRole(t *testing.T) {
	h, assignments, rolesRepo := assignmentFixture(t)
	rolesRepo.Roles["role"] = &model.Role{ID: "role", IsPublic: true, CanEditMembersByModerator: true}

	rec := postAssignment(h, `{"roleId":"role"}`, "viewer")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"assigned":false,"expiresAt":null,"role":{"id":"role","isPublic":true,"canEditMembersByModerator":true}}`, rec.Body.String())
	assert.Equal(t, 1, assignments.findCalls)
	assert.Equal(t, "viewer", assignments.userID)
	assert.Equal(t, "role", assignments.roleID)
	assert.Zero(t, assignments.listCalls)
}

func TestAssignmentShow_PrivateActiveAssignment(t *testing.T) {
	h, assignments, rolesRepo := assignmentFixture(t)
	rolesRepo.Roles["private"] = &model.Role{ID: "private", IsPublic: false}
	expires := time.Date(2026, 8, 20, 3, 4, 5, 0, time.UTC)
	assignments.Assignments["viewer:private"] = &model.RoleAssignment{UserID: "viewer", RoleID: "private", ExpiresAt: &expires}

	rec := postAssignment(h, `{"roleId":"private"}`, "viewer")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"assigned":true,"expiresAt":"2026-08-20T03:04:05.000Z","role":{"id":"private","isPublic":false,"canEditMembersByModerator":false}}`, rec.Body.String())
}

func TestAssignmentShow_PrivateInactiveMatchesMissing(t *testing.T) {
	for _, tc := range []struct {
		name       string
		createRole bool
		expiresAt  *time.Time
	}{
		{name: "missing"},
		{name: "unassigned", createRole: true},
		{name: "expired", createRole: true, expiresAt: func() *time.Time { v := time.Now().Add(-time.Hour); return &v }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, assignments, rolesRepo := assignmentFixture(t)
			if tc.createRole {
				rolesRepo.Roles["secret"] = &model.Role{ID: "secret", IsPublic: false, CanEditMembersByModerator: true}
			}
			if tc.expiresAt != nil {
				assignments.Assignments["viewer:secret"] = &model.RoleAssignment{UserID: "viewer", RoleID: "secret", ExpiresAt: tc.expiresAt}
			}

			rec := postAssignment(h, `{"roleId":"secret"}`, "viewer")
			require.Equal(t, http.StatusBadRequest, rec.Code)
			assert.JSONEq(t, assignmentNoSuchRoleJSON, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "secret")
			assert.NotContains(t, rec.Body.String(), "isPublic")
		})
	}
}

func TestAssignmentShow_AssignmentFailurePrecedesRoleDisclosure(t *testing.T) {
	for _, roleExists := range []bool{false, true} {
		roleRepo := testutil.NewMockRoleRepository()
		if roleExists {
			roleRepo.Roles["secret"] = &model.Role{ID: "secret", IsPublic: false}
		}
		assignRepo := &assignmentExactRepo{
			MockRoleAssignmentRepository: testutil.NewMockRoleAssignmentRepository(roleRepo),
			err:                          errors.New("SELECT assignment: connection failed"),
		}
		h := assignmentHandler(t, roleRepo, assignRepo)

		rec := postAssignment(h, `{"roleId":"secret"}`, "viewer")
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.JSONEq(t, assignmentInternalErrorJSON, rec.Body.String())
		assert.NotContains(t, rec.Body.String(), "secret")
		assert.NotContains(t, rec.Body.String(), "SELECT")
	}
}

func TestAssignmentShow_RolePersistenceFailure(t *testing.T) {
	base := testutil.NewMockRoleRepository()
	assignRepo := &assignmentExactRepo{MockRoleAssignmentRepository: testutil.NewMockRoleAssignmentRepository(base)}
	h := assignmentHandler(t, &assignmentFailingRoleRepo{RoleRepository: base}, assignRepo)

	rec := postAssignment(h, `{"roleId":"secret"}`, "viewer")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.JSONEq(t, assignmentInternalErrorJSON, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "SELECT")
}

func TestAssignmentShow_InvalidParam(t *testing.T) {
	h, _, _ := assignmentFixture(t)
	rec := postAssignment(h, `{}`, "viewer")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"INVALID_PARAM"`)
}
