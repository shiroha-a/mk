// Package avatardecorations serves POST /api/get-avatar-decorations — the
// catalog of avatar decorations a user can put on their icon.
package avatardecorations

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	corerole "github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/model"
)

// RoleSetProvider returns the set of role IDs that currently exist.
//
// **具体的な role service ではなく関数で受ける。** 要るのは「この role はまだ
// 生きているか」だけで、role 管理の全機能ではない。
type RoleSetProvider func() (map[string]bool, error)

// Handler serves the avatar decoration catalog.
type Handler struct {
	db    *gorm.DB
	roles RoleSetProvider
}

// NewHandler creates a Handler.
//
// db が nil なら空配列を返す。roles が nil / エラーなら role フィルタを
// 飛ばして verbatim で返す (下の Get のコメントを参照)。
func NewHandler(db *gorm.DB, roles RoleSetProvider) *Handler {
	return &Handler{db: db, roles: roles}
}

// Get returns every avatar decoration.
//
// POST /api/get-avatar-decorations
//
// upstream get-avatar-decorations.ts は roleIdsThatCanBeUsedThisDecoration を
// 現存ロールのみに filter して削除済ロール ID を除去する (#1543)。
//
// **role 一覧を引けないときは filter を飛ばす。** 空集合として扱うと
// **全 roleId が落ちて、ロール限定のデコレーションが誰にも使えなくなる**。
// 古い ID が残るほうが害が小さい。
func (h *Handler) Get(c echo.Context) error {
	if h.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	var decorations []model.AvatarDecoration
	if err := h.db.Find(&decorations).Error; err != nil {
		return c.JSON(http.StatusOK, []any{})
	}

	var existingRoles map[string]bool
	filter := false
	if h.roles != nil {
		if set, err := h.roles(); err == nil {
			existingRoles, filter = set, true
		}
	}

	out := make([]map[string]any, 0, len(decorations))
	for _, d := range decorations {
		roleIDs := []string(d.RoleIDs)
		if filter {
			roleIDs = corerole.FilterExistingRoleIDs(roleIDs, existingRoles)
		}
		out = append(out, map[string]any{
			"id":                                 d.ID,
			"name":                               d.Name,
			"description":                        d.Description,
			"url":                                d.URL,
			"roleIdsThatCanBeUsedThisDecoration": roleIDs,
			// upstream Misskey #17034 (= 2026.5.0) で追加された category field
			// もここで返す。nullable なので null のままも許容。
			"category": d.Category,
		})
	}
	return c.JSON(http.StatusOK, out)
}
