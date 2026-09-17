package admin_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiadmin "github.com/shiroha-a/mk/internal/api/admin"
	corerole "github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

// **`reset-password` / `unset-mfa` は「乗っ取れる操作」(#3037)。**
//
// 前者は新しいパスワードを応答に載せて返し、後者は 2FA を外す。2 つを続けて
// 叩くと、対象アカウントとしてサインインできる状態が完成する。
//
// 以前は「対象が管理者」だけを見ていたので、
//
//   - モデレーターが **system アカウント** (`instance.actor` 等) のパスワードを
//     発行できた
//   - モデレーターが**他のモデレーター**を乗っ取れた。相手が持つ凍結・削除・
//     ロール付与をそのまま使える
func TestCredentialResets_ProtectedTargets(t *testing.T) {
	calls := map[string]func(*apiadmin.Handler, string, *model.User) *httptest.ResponseRecorder{
		"reset-password": func(h *apiadmin.Handler, id string, actor *model.User) *httptest.ResponseRecorder {
			return doPost(h.ResetPassword, `{"userId":"`+id+`"}`, actor)
		},
		"unset-mfa": func(h *apiadmin.Handler, id string, actor *model.User) *httptest.ResponseRecorder {
			return doPost(h.UnsetMfa, `{"userId":"`+id+`"}`, actor)
		},
	}

	for _, tt := range []struct {
		name      string
		target    *model.User
		adminIDs  []string
		modIDs    []string
		actorID   string
		wantDeny  bool
		wantDenyR string
	}{
		{
			name:     "system アカウント (モデレーターが実行)",
			target:   &model.User{ID: "sys", Username: "instance.actor"},
			actorID:  "mod1",
			wantDeny: true,
		},
		{
			// **管理者でも塞ぐ。** 人がサインインする前提の無いアカウントに
			// サインインできる資格情報を作らない。
			name:     "system アカウント (管理者が実行)",
			target:   &model.User{ID: "sys", Username: "relay.actor"},
			adminIDs: []string{"boss"},
			actorID:  "boss",
			wantDeny: true,
		},
		{
			name:     "他のモデレーター (モデレーターが実行)",
			target:   &model.User{ID: "mod2", Username: "mod2"},
			modIDs:   []string{"mod1", "mod2"},
			actorID:  "mod1",
			wantDeny: true,
		},
		{
			// インシデント対応の経路は残す。
			name:     "他のモデレーター (管理者が実行)",
			target:   &model.User{ID: "mod2", Username: "mod2"},
			adminIDs: []string{"boss"},
			modIDs:   []string{"mod2"},
			actorID:  "boss",
		},
		{
			name:     "管理者 (モデレーターが実行)",
			target:   &model.User{ID: "boss", Username: "boss"},
			adminIDs: []string{"boss"},
			modIDs:   []string{"mod1"},
			actorID:  "mod1",
			wantDeny: true,
		},
		{
			name:    "普通の利用者",
			target:  &model.User{ID: "u1", Username: "u1"},
			modIDs:  []string{"mod1"},
			actorID: "mod1",
		},
		{
			// 自分自身のリセットは従来どおり通す。
			name:     "自分自身 (管理者)",
			target:   &model.User{ID: "boss", Username: "boss"},
			adminIDs: []string{"boss"},
			actorID:  "boss",
		},
	} {
		for callName, call := range calls {
			t.Run(tt.name+"/"+callName, func(t *testing.T) {
				h := newCredentialResetHandler(t, tt.target, tt.adminIDs, tt.modIDs)
				rec := call(h, tt.target.ID, &model.User{ID: tt.actorID})

				if tt.wantDeny {
					assert.Equal(t, http.StatusBadRequest, rec.Code, "保護対象を触れている")
					assert.Contains(t, rec.Body.String(), "ACCESS_DENIED")
					return
				}
				assert.NotContains(t, rec.Body.String(), "ACCESS_DENIED",
					"通るべき対象を弾いている: %s", rec.Body.String())
			})
		}
	}
}

// newCredentialResetHandler wires a handler where adminIDs / modIDs hold the
// corresponding role.
func newCredentialResetHandler(t *testing.T, target *model.User, adminIDs, modIDs []string) *apiadmin.Handler {
	t.Helper()
	h, users, _, roles, assigns := newTestHandlerWithAssign(t)
	users.Users[target.ID] = target

	roles.Roles["admin-role"] = &model.Role{ID: "admin-role", Target: model.RoleTargetManual, IsAdministrator: true}
	roles.Roles["mod-role"] = &model.Role{ID: "mod-role", Target: model.RoleTargetManual, IsModerator: true}
	for _, id := range adminIDs {
		require.NoError(t, assigns.Create(&model.RoleAssignment{ID: "a-" + id, UserID: id, RoleID: "admin-role"}))
	}
	for _, id := range modIDs {
		require.NoError(t, assigns.Create(&model.RoleAssignment{ID: "m-" + id, UserID: id, RoleID: "mod-role"}))
	}
	h.SetSecurityKeyRepo(newFakeSecurityKeyRepo())
	return h
}

// flakyUserRepo returns the user once, then fails — reproducing the window
// where the DB drops between the handler's lookup and isProtectedAccount's.
type flakyUserRepo struct {
	*testutil.MockUserRepository
	calls int
}

func (r *flakyUserRepo) FindByID(id string) (*model.User, error) {
	r.calls++
	if r.calls > 1 {
		return nil, errors.New("db is down")
	}
	return r.MockUserRepository.FindByID(id)
}

// **`isProtectedAccount` が引き直すので、その間に DB が落ちると保護が消える。**
//
// あちらは `FindByID` の失敗を「保護対象ではない」と扱う。handler は既に行を
// 引いているので、そちらの行でも形を見る (#3037)。この二重化が無いと、
// **DB の瞬断の窓で system アカウントのパスワードを発行できる**。
func TestCredentialResets_SystemAccountStaysProtectedWhenLookupFails(t *testing.T) {
	users := testutil.NewMockUserRepository()
	sys := &model.User{ID: "sys", Username: "instance.actor"}
	users.Users["sys"] = sys

	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := testutil.NewMockRoleAssignmentRepository(roleRepo)
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	roleSvc := corerole.NewService(roleRepo, assignRepo, metaRepo, idGen)
	flaky := &flakyUserRepo{MockUserRepository: users}
	h := apiadmin.NewHandler(signup.NewService(flaky, metaRepo, idGen), roleSvc, metaRepo, flaky, idGen)

	rec := doPost(h.ResetPassword, `{"userId":"sys"}`, &model.User{ID: "mod1"})

	assert.Equal(t, http.StatusBadRequest, rec.Code, "瞬断の窓で system アカウントを触れている")
	assert.Contains(t, rec.Body.String(), "ACCESS_DENIED")
	assert.Greater(t, flaky.calls, 1, "2 回目の lookup が起きていない (テストが空虚)")
}
