package i

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Apps handles POST /api/i/apps.
//
// 本家 Misskey の i/apps と同 shape (access_token + app の JOIN) を返す。
// 旧実装は誤って空配列を返しており frontend "Apps" 一覧が常に空だったため
// #587 で本実装化した。Misskey TS 本家 ref:
// packages/backend/src/server/api/endpoints/i/apps.ts。
//
// sort 値は +/-createdAt / +/-lastUsedAt。未指定時は token.id ASC (本家 default)。
// 各 token の name / permission / description は app へフォールバックする
// (例: token.name が NULL なら app.name を使う、本家と同じ ?? チェーン)。
func (h *Handler) Apps(c echo.Context) error {
	if h.accessTokenRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	u := middleware.GetUser(c)
	var req struct {
		Sort string `json:"sort"`
	}
	_ = c.Bind(&req)
	tokens, err := h.accessTokenRepo.ListByUserIDPreloadApp(u.ID, req.Sort)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	const tsFormat = "2006-01-02T15:04:05.000Z"
	out := make([]map[string]any, 0, len(tokens))
	for _, t := range tokens {
		entry := map[string]any{
			"id":         t.ID,
			"name":       coalesceString(t.Name, appName(t.App)),
			"lastUsedAt": formatNullableTime(t.LastUsedAt, tsFormat),
			"permission": []string(tokenPermission(t)),
			"iconUrl":    t.IconURL,
			"description": coalesceString(
				t.Description,
				appDescription(t.App),
			),
		}
		if ct, err := h.idGen.ParseTime(t.ID); err == nil {
			entry["createdAt"] = ct.UTC().Format(tsFormat)
		}
		out = append(out, entry)
	}
	return c.JSON(http.StatusOK, out)
}

// coalesceString は本家の `a ?? b` チェーン相当。a が nil/"" なら b を返す。
func coalesceString(a, b *string) *string {
	if a != nil && *a != "" {
		return a
	}
	return b
}

func appName(a *model.App) *string {
	if a == nil {
		return nil
	}
	return &a.Name
}

func appDescription(a *model.App) *string {
	if a == nil {
		return nil
	}
	return &a.Description
}

// tokenPermission は本家と同じ分岐: app があれば app.permission、無ければ
// token.permission (本家 i/apps.ts:98)。
func tokenPermission(t *model.AccessToken) []string {
	if t.App != nil {
		return []string(t.App.Permission)
	}
	return []string(t.Permission)
}

// formatNullableTime は *time.Time が nil の場合 nil を返し、そうでなければ
// 指定 layout で UTC 文字列に整形する。
func formatNullableTime(t *time.Time, layout string) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(layout)
}

// AuthorizedApps handles POST /api/i/authorized-apps.
//
// upstream authorized-apps.ts は `appId IS NOT NULL` の access_token を
// limit/offset/sort 付きで引き、各 token を appEntityService.pack(token.appId,
// me, {detail:true}) で **App entity** に変換して返す。旧 mk-go 実装は
// access_token 自身の shape ({id=token.id, description, iconUrl, lastUsedAt
// など}) を返しており、本家 res schema ({id=app.id, name, callbackUrl,
// permission, isAuthorized}) と非互換だった (#1555)。
//
// isAuthorized は upstream で accessTokensRepository.countBy({appId, userId})>0。
// ここで列挙している token 自体が該当 (appId, userId) ペアの存在を保証するので
// 常に true。requireCredential なので me は必ず存在する。
func (h *Handler) AuthorizedApps(c echo.Context) error {
	if h.accessTokenRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	u := middleware.GetUser(c)
	var req struct {
		Limit  *int   `json:"limit"`
		Offset int    `json:"offset"`
		Sort   string `json:"sort"`
	}
	_ = c.Bind(&req)
	// limit: default 10, clamp 1..100 (authorized-apps.ts paramDef)。
	limit := 10
	if req.Limit != nil {
		limit = *req.Limit
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	// sort enum ['desc','asc'] default 'desc' (= id DESC)。'asc' のみ昇順。
	sortAsc := req.Sort == "asc"
	tokens, err := h.accessTokenRepo.ListAuthorizedApps(u.ID, limit, offset, sortAsc)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(tokens))
	for _, t := range tokens {
		// appId NOT NULL filter 済だが、app 行が削除済なら Preload が nil を返す。
		// upstream は findOneByOrFail で reject するが、mk-go は graceful に skip。
		if t.App == nil {
			continue
		}
		out = append(out, map[string]any{
			"id":           t.App.ID,
			"name":         t.App.Name,
			"callbackUrl":  t.App.CallbackURL,
			"permission":   []string(t.App.Permission),
			"isAuthorized": true,
		})
	}
	return c.JSON(http.StatusOK, out)
}

// RevokeToken handles POST /api/i/revoke-token.
// TS互換: tokenId (ID) または token (ハッシュ文字列) のどちらかで指定可。
// 指定 access_token が自分の所有であれば削除する。
func (h *Handler) RevokeToken(c echo.Context) error {
	if h.accessTokenRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	u := middleware.GetUser(c)
	var req struct {
		TokenID string `json:"tokenId"`
		Token   string `json:"token"`
	}
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	if req.TokenID == "" && req.Token == "" {
		return apierr.JSONInvalidParam(c)
	}
	var tok *model.AccessToken
	var err error
	if req.TokenID != "" {
		tok, err = h.accessTokenRepo.FindByID(req.TokenID)
	} else {
		// hash 列 (miauth: sha256(token)) と token 列 (raw, app/auth) を 1 query
		// で OR 検索する。middleware と同じ pattern (#910 / #913)。これにより
		// app/auth flow で発行された access token も raw token 文字列で revoke
		// できる。
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(req.Token)))
		tok, err = h.accessTokenRepo.FindByHashOrToken(hash, req.Token)
	}
	if err != nil {
		return c.NoContent(http.StatusNoContent)
	}
	if tok.UserID != u.ID {
		return apierr.JSONAccessDenied(c)
	}
	if err := h.accessTokenRepo.DeleteByID(tok.ID); err != nil {
		return apierr.JSONInternalError(c)
	}
	// auth middleware の token cache に entry が残っていると revoke の効果が
	// cache TTL (= 30 秒) 遅延して上流互換でなくなるため、regenerate-token
	// (#884) と同じく invalidator が wire されている場合は即時削除する。
	if h.authInvalidator != nil && tok.Token != "" {
		h.authInvalidator.InvalidateToken(tok.Token)
	}
	return c.NoContent(http.StatusNoContent)
}
