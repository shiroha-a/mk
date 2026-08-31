package signup

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/shiroha-a/mk/internal/api/apierr"
	coresignup "github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/repository"
)

// SetUsernameLookups wires the repositories POST /api/username/available needs.
//
// **3 つとも見る必要がある。** 現在の利用者 (user)、過去に使われて解放された
// もの (used_usernames)、管理者が予約したもの (meta.preservedUsernames) の
// どれか 1 つでも当たれば使えない。1 つ落とすと**再登録で他人の旧アカウントに
// 化ける**経路が開く。
func (h *Handler) SetUsernameLookups(userRepo repository.UserRepository, usedRepo repository.UsedUsernameRepository) {
	h.userRepo = userRepo
	h.usedUsernameRepo = usedRepo
}

// UsernameAvailable reports whether a username can be registered.
// POST /api/username/available
func (h *Handler) UsernameAvailable(c echo.Context) error {
	var req struct {
		Username string `json:"username"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam())
	}
	// format 検証 (upstream localUsernameSchema、paramDef レベル拒否相当)。
	if !coresignup.ValidUsernameFormat(req.Username) {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam())
	}
	lower := strings.ToLower(req.Username)

	// **未配線なら「使えない」に倒す。** 空振りして available=true を返すと、
	// 実際には取れない名前を案内したうえ、既存アカウントの username を
	// 空いていると誤って見せることになる。
	if h.userRepo == nil || h.usedUsernameRepo == nil {
		return c.JSON(http.StatusOK, map[string]any{"available": false})
	}

	// (1) 既存 local user
	_, userErr := h.userRepo.FindByUsernameLower(lower, nil)
	existsUser := userErr == nil
	// (2) used_usernames (過去に使われ解放された username)
	usedExists, _ := h.usedUsernameRepo.Exists(lower)
	// (3) preservedUsernames (予約 username)
	preserved := false
	if h.metaRepo != nil {
		if m, err := h.metaRepo.Fetch(); err == nil && m != nil {
			preserved = coresignup.IsReservedUsername(lower, m.PreservedUsernames)
		}
	}

	available := !existsUser && !usedExists && !preserved
	return c.JSON(http.StatusOK, map[string]any{"available": available})
}
