package i

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/move"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"golang.org/x/crypto/bcrypt"
)

// Move handles POST /api/i/move.
//
// Mastodon / Misskey 互換のアカウント引越し。moveToAccount には移行先の
// canonical URI (例: https://other.example/users/abc) または acct 形式
// (@user@host) を渡す。処理内容:
//
//  1. root ユーザーは移行不可 (NOT_ROOT_FORBIDDEN, #1546)
//  2. password が指定されていれば照合 (任意、後述)
//  3. moveToAccount が acct 形式なら canonical URI へ解決
//  4. mover が ALREADY_MOVED / URI 解決 / dst.alsoKnownAs 検証 / DB 書き込み + 配送
//
// password param について (#1546): 本家 move.ts は secure:true による session
// 再認証で代替し password param を持たない。公式 frontend (migration.vue) は
// moveToAccount のみ送るため、password 必須化すると公式移行フローが必ず 400 で
// 失敗する。drop-in / API 互換を最優先する方針 (CLAUDE.md) に従い password を
// 任意化し、送られた場合のみ照合する (再認証の強制は session-secure framework
// 実装まで保留)。
//
// acct 形式の解決は alsoKnownAs と同じ lookupAlsoKnownAsTarget (local DB の
// FindByURI / FindByUsernameLower) を再利用する。移行先は事前に alsoKnownAs で
// src を指している必要があり、その時点で local に解決済のため local lookup で足りる。
func (h *Handler) Move(c echo.Context) error {
	me := middleware.GetUser(c)
	var req struct {
		MoveToAccount string `json:"moveToAccount"`
		Password      string `json:"password"`
	}
	if err := c.Bind(&req); err != nil || req.MoveToAccount == "" {
		return apierr.JSONInvalidParam(c)
	}
	// root ユーザーは移行不可 (upstream move.ts: rootUserId === me.id)。
	// meta.rootUserId と user.isRoot の両方を見る (role_service.isRootUser と同 doctrine)。
	isRoot := me.IsRoot
	if h.metaRepo != nil {
		if m, err := h.metaRepo.Fetch(); err == nil && m.RootUserID != nil && *m.RootUserID == me.ID {
			isRoot = true
		}
	}
	if isRoot {
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"NOT_ROOT_FORBIDDEN", "This feature is not available for the root account.",
			"4362e8dc-731f-4ad8-a694-be2a88922a24",
		))
	}
	// password は任意。指定された場合のみ照合する (#1546)。
	if req.Password != "" {
		profile := h.userService.GetProfile(me.ID)
		if profile == nil || profile.Password == nil {
			return c.JSON(http.StatusBadRequest, apierr.Error(
				"ACCESS_DENIED", "No password set.",
				"1fb7cb09-d46a-4fff-b8df-057708cce513",
			))
		}
		if err := bcrypt.CompareHashAndPassword([]byte(*profile.Password), []byte(req.Password)); err != nil {
			return c.JSON(http.StatusBadRequest, apierr.Error(
				"INCORRECT_PASSWORD", "Incorrect password.",
				"932c904e-9460-45b7-9ce6-7ed33be7eb2c",
			))
		}
	}
	if h.mover == nil {
		return c.JSON(http.StatusNotImplemented, apierr.Error(
			"INTERNAL_ERROR",
			"Account move is not wired up.",
			"5d37dbcb-891e-41ca-a3d6-e690c97775ac",
		))
	}
	// acct 形式 (@user@host) は canonical URI へ解決してから mover に渡す。
	// URI 形式 (http/https) はそのまま渡す。
	moveTo := req.MoveToAccount
	if !strings.HasPrefix(moveTo, "http://") && !strings.HasPrefix(moveTo, "https://") {
		u := h.lookupAlsoKnownAsTarget(moveTo)
		if u == nil {
			return c.JSON(http.StatusNotFound, apierr.Error(
				"NO_SUCH_USER", "No such user.",
				"fcd2eef9-a9b2-4c4f-8624-038099e90aa5",
			))
		}
		uri := h.canonicalUserURI(u)
		if uri == "" {
			return c.JSON(http.StatusNotFound, apierr.Error(
				"NO_SUCH_USER", "No such user.",
				"fcd2eef9-a9b2-4c4f-8624-038099e90aa5",
			))
		}
		moveTo = uri
	}
	err := h.mover.Move(me, moveTo)
	switch {
	case err == nil:
		return h.Me(c)
	case errors.Is(err, move.ErrNoSuchUser):
		return c.JSON(http.StatusNotFound, apierr.Error(
			"NO_SUCH_USER", "No such user.",
			"fcd2eef9-a9b2-4c4f-8624-038099e90aa5",
		))
	case errors.Is(err, move.ErrAlreadyMoved):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"ALREADY_MOVED",
			"Account was already moved to another account.",
			"b234a14e-9ebe-4581-8000-074b3c215962",
		))
	case errors.Is(err, move.ErrDestinationForbids):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"DESTINATION_ACCOUNT_FORBIDS",
			"Destination account doesn't have proper 'Known As' alias, or has already moved.",
			"b5c90186-4ab0-49c8-9bba-a1f766282ba4",
		))
	case errors.Is(err, move.ErrRemoteSourceForbidden):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"INVALID_PARAM", "Remote users cannot initiate account move.",
			"3d81ceae-475f-4600-b2a8-2bc116157532",
		))
	default:
		return apierr.JSONInternalError(c)
	}
}
