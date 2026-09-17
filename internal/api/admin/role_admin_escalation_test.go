package admin_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/model"
)

// **モデレーターが管理者ロールを付け外しできないこと (#3037)。**
//
// `canEditMembersByModerator` は「モデレーターがメンバーを編集してよいか」
// しか見ない。管理者ロールにそのチェックが立っていると、モデレーターは自分
// 自身にそのロールを付けられる = 管理者へ昇格できた。ロールの作成・更新は
// 管理者専用なのに、付け外しだけがこの穴で抜けていた。
//
// **upstream (`assign.ts:69-76`) も同じ状態だが、そちらに合わせない。**
// mk-go を厳しい側に倒して `docs/divergence.md` に記録してある。
func TestRolesAssign_ModeratorCannotGrantAdministratorRole(t *testing.T) {
	h, users, _, roles, assigns := newTestHandlerWithAssign(t)
	users.Users["mod1"] = &model.User{ID: "mod1"}
	roles.Roles["admin-role"] = &model.Role{
		ID:                        "admin-role",
		Target:                    model.RoleTargetManual,
		IsAdministrator:           true,
		CanEditMembersByModerator: true, // ここが立っているのが前提
	}

	rec := doPost(h.RolesAssign, `{"userId":"mod1","roleId":"admin-role"}`, &model.User{ID: "mod1"})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "ACCESS_DENIED")
	assigned, err := assigns.Exists("mod1", "admin-role")
	require.NoError(t, err)
	assert.False(t, assigned, "モデレーターが自分に管理者ロールを付けられている")
}

// **unassign も塞ぐ。** 管理者ロールを外せるなら、モデレーターが管理者を
// 降格させて実質の最上位になれる。
func TestRolesUnassign_ModeratorCannotRevokeAdministratorRole(t *testing.T) {
	h, users, _, roles, assigns := newTestHandlerWithAssign(t)
	users.Users["admin1"] = &model.User{ID: "admin1"}
	roles.Roles["admin-role"] = &model.Role{
		ID:                        "admin-role",
		Target:                    model.RoleTargetManual,
		IsAdministrator:           true,
		CanEditMembersByModerator: true,
	}
	require.NoError(t, assigns.Create(&model.RoleAssignment{ID: "a1", UserID: "admin1", RoleID: "admin-role"}))

	rec := doPost(h.RolesUnassign, `{"userId":"admin1","roleId":"admin-role"}`, &model.User{ID: "mod1"})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "ACCESS_DENIED")
	assigned, err := assigns.Exists("admin1", "admin-role")
	require.NoError(t, err)
	assert.True(t, assigned, "モデレーターが管理者ロールを剥奪できている")
}

// **管理者自身は従来どおり付けられる。** これが無いと「管理者ロールは誰も
// 付けられない」実装でも上の 2 つが通る。
func TestRolesAssign_AdministratorCanGrantAdministratorRole(t *testing.T) {
	h, users, _, roles, assigns := newTestHandlerWithAssign(t)
	users.Users["target"] = &model.User{ID: "target"}
	roles.Roles["admin-role"] = &model.Role{
		ID:              "admin-role",
		Target:          model.RoleTargetManual,
		IsAdministrator: true,
	}
	// 実行者を実際の管理者にする (role service は割り当てから判定する)。
	require.NoError(t, assigns.Create(&model.RoleAssignment{ID: "a0", UserID: "boss", RoleID: "admin-role"}))

	rec := doPost(h.RolesAssign, `{"userId":"target","roleId":"admin-role"}`, &model.User{ID: "boss"})

	require.Equal(t, http.StatusNoContent, rec.Code, "管理者が管理者ロールを付けられない")
	assigned, err := assigns.Exists("target", "admin-role")
	require.NoError(t, err)
	assert.True(t, assigned)
}

// **管理者ロール以外は従来どおり。** モデレーターが
// `canEditMembersByModerator` の立った普通のロールを付けられること。
func TestRolesAssign_ModeratorStillGrantsOrdinaryRole(t *testing.T) {
	h, users, _, roles, assigns := newTestHandlerWithAssign(t)
	users.Users["u1"] = &model.User{ID: "u1"}
	roles.Roles["ordinary"] = &model.Role{
		ID:                        "ordinary",
		Target:                    model.RoleTargetManual,
		CanEditMembersByModerator: true,
	}

	rec := doPost(h.RolesAssign, `{"userId":"u1","roleId":"ordinary"}`, &model.User{ID: "mod1"})

	require.Equal(t, http.StatusNoContent, rec.Code)
	assigned, err := assigns.Exists("u1", "ordinary")
	require.NoError(t, err)
	assert.True(t, assigned)
}
