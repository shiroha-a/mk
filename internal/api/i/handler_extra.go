package i

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"golang.org/x/crypto/bcrypt"
)

// ChangePassword handles POST /api/i/change-password.
func (h *Handler) ChangePassword(c echo.Context) error {
	u := middleware.GetUser(c)
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
		Token           string `json:"token"`
	}
	if err := c.Bind(&req); err != nil || req.CurrentPassword == "" || req.NewPassword == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "currentPassword and newPassword are required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	profile := h.userService.GetProfile(u.ID)
	if profile == nil || profile.Password == nil {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "No password set.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	}

	// 2FA gate: upstream Misskey TS (change-password.ts) は twoFactorEnabled
	// なら token 必須 + twoFactorAuthenticate で検証する。mk-go では旧来
	// password だけで通っていたため password 漏洩 = 2FA bypass で password
	// 変更可能だった (drop-in regression)。verify2FAToken 経由なので RFC
	// 6238 §5.2 の replay 保護も自動で効く。
	if profile.TwoFactorEnabled && !h.verify2FAToken(c.Request().Context(), profile, req.Token) {
		return c.JSON(http.StatusForbidden, apierr.InvalidToken())
	}

	// upstream Misskey TS は raw `throw new Error('authentication failed')` を
	// framework が 401 に変換する (#885)。mk-go も drop-in 互換のため 401
	// に揃える (旧 mk-go は 403 を返していた)。
	if err := bcrypt.CompareHashAndPassword([]byte(*profile.Password), []byte(req.CurrentPassword)); err != nil {
		return c.JSON(http.StatusUnauthorized, apierr.Error("INCORRECT_PASSWORD", "Incorrect password.", "932c904e-9460-45b7-9ce6-7ed33be7eb2c"))
	}

	// bcrypt は 73 byte 以上の password で ErrPasswordTooLong を返す。Node 側
	// (upstream Misskey TS) は silent truncation するが、Go では error で
	// 弾かれて空 hash が DB に書かれてしまうため明示的に 400 にする (#1075)。
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		if errors.Is(err, bcrypt.ErrPasswordTooLong) {
			return c.JSON(http.StatusBadRequest, apierr.Error("PASSWORD_TOO_LONG", "Password is too long.", "f2e62243-b80a-43e4-9b17-329b422deded"))
		}
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	hashStr := string(hash)
	if err := h.userService.UpdateProfileFields(u.ID, map[string]any{"password": hashStr}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// DeleteAccount handles POST /api/i/delete-account.
func (h *Handler) DeleteAccount(c echo.Context) error {
	u := middleware.GetUser(c)
	var req struct {
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	profile := h.userService.GetProfile(u.ID)
	if profile == nil || profile.Password == nil {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "No password set.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	}

	// 2FA gate: upstream Misskey TS (delete-account.ts) は twoFactorEnabled
	// なら token 必須。mk-go では旧来 password だけで通っていたため、
	// password 漏洩 = 2FA bypass で account 削除可能だった (drop-in
	// regression)。verify2FAToken 経由なので replay 保護も自動で効く。
	if profile.TwoFactorEnabled && !h.verify2FAToken(c.Request().Context(), profile, req.Token) {
		return c.JSON(http.StatusForbidden, apierr.InvalidToken())
	}

	// upstream Misskey TS は raw `throw new Error('incorrect password')` を
	// framework が 401 に変換する (#885)。mk-go も drop-in 互換のため 401
	// に揃える (旧 mk-go は 403 を返していた)。
	if err := bcrypt.CompareHashAndPassword([]byte(*profile.Password), []byte(req.Password)); err != nil {
		return c.JSON(http.StatusUnauthorized, apierr.Error("INCORRECT_PASSWORD", "Incorrect password.", "932c904e-9460-45b7-9ce6-7ed33be7eb2c"))
	}

	// isSuspended + isDeleted を true に設定 (論理削除)
	if err := h.userService.UpdateUserFields(u.ID, map[string]any{
		"isSuspended": true,
		"isDeleted":   true,
	}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// 論理削除直後の auth bypass を防ぐ (#962 P0 + #967)。本 user は web /
	// mobile / 3rd party app など複数 device で session を持ち得るので、
	// 本 request の token のみ invalidate (#963 で当初実装) では他 device
	// の token cache が最大 30s stale 続け、削除済 user 名義での操作が可能。
	// admin/suspend-user (#966) と同 shape の self-bypass に対応するため、
	// user-level invalidate を呼んで自分の全 device session を 1 query で
	// 失効させる。self-delete は user lifetime で高々 1 度の操作なので、
	// 本 helper の O(N) cache scan cost は許容範囲。helper の詳細は
	// handler.go の invalidateUserTokenCache を参照。
	h.invalidateUserTokenCache(u.ID)
	return c.NoContent(http.StatusNoContent)
}

// Favorites handles POST /api/i/favorites.
//
// CherryPick / Misskey 本家互換で sinceId / untilId による keyset pagination
// を受け付ける (#424)。フロントの無限スクロールは untilId を毎ページ送って
// くるので、cursor 無視だと同一ページが永久ループする。
func (h *Handler) Favorites(c echo.Context) error {
	u := middleware.GetUser(c)
	var req struct {
		Limit   int    `json:"limit"`
		SinceID string `json:"sinceId"`
		UntilID string `json:"untilId"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.favoriteRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	favs, err := h.favoriteRepo.ListByUser(u.ID, req.UntilID, req.SinceID, req.Limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// PackNotes 経由で renote / reply の embed まで含めて Instance / emoji
	// 解決を一括適用する。favorite を NoteEntity と対応づけるために note ID
	// → index の map を作り、後段の favorite イテレートで参照する (#416)。
	notes := make([]*model.Note, 0, len(favs))
	for _, f := range favs {
		if f.Note != nil {
			notes = append(notes, f.Note)
		}
	}
	packed := entity.PackNotes(c.Request().Context(), notes, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
	// /api/i/favorites は認証 path なので u が viewer。pinned notes と同じく
	// 自分の myReaction / Channel / Files を埋める (#426)。
	h.fieldRes.Apply(packed, u)
	byID := make(map[string]*entity.NoteEntity, len(packed))
	for i := range packed {
		byID[packed[i].ID] = &packed[i]
	}

	result := make([]map[string]any, 0, len(favs))
	for _, f := range favs {
		item := map[string]any{
			"id":     f.ID,
			"noteId": f.NoteID,
			// Misskey 本家互換のため createdAt を含める (#424 Devin review)。
			"createdAt": f.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if f.Note != nil {
			if pn, ok := byID[f.Note.ID]; ok {
				item["note"] = *pn
			}
		}
		result = append(result, item)
	}
	return c.JSON(http.StatusOK, result)
}

// NotificationsGrouped handles POST /api/i/notifications-grouped.
// フロントエンドがブート直後に呼ぶ。簡易版として空配列を返す。
func (h *Handler) NotificationsGrouped(c echo.Context) error {
	return c.JSON(http.StatusOK, []any{})
}

// RegenerateToken handles POST /api/i/regenerate-token.
func (h *Handler) RegenerateToken(c echo.Context) error {
	u := middleware.GetUser(c)
	var req struct {
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	profile := h.userService.GetProfile(u.ID)
	if profile == nil || profile.Password == nil {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "No password set.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	}

	// upstream Misskey TS は raw `throw new Error('incorrect password')` を
	// framework が 401 に変換する (#885)。mk-go も drop-in 互換のため 401
	// に揃える (旧 mk-go は 403 を返していた)。
	if err := bcrypt.CompareHashAndPassword([]byte(*profile.Password), []byte(req.Password)); err != nil {
		return c.JSON(http.StatusUnauthorized, apierr.Error("INCORRECT_PASSWORD", "Incorrect password.", "932c904e-9460-45b7-9ce6-7ed33be7eb2c"))
	}

	b := make([]byte, 8)
	_, _ = rand.Read(b)
	newToken := hex.EncodeToString(b)

	var oldToken string
	if u.Token != nil {
		oldToken = *u.Token
	}
	if err := h.userService.UpdateUserFields(u.ID, map[string]any{"token": newToken}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// 旧 token が auth middleware の cache で生き残ると regenerate の主目的
	// (= 旧 token 失効) が機能しない (#884)。invalidator が wire されている
	// 場合は old token entry を即時削除する。production では router で必ず
	// wire される。
	if h.authInvalidator != nil && oldToken != "" {
		h.authInvalidator.InvalidateToken(oldToken)
	}
	// TS本家 regenerate-token.ts:60 と同じく、token再生成成功後にmainへ
	// publishする。body無し(type のみで、他セッションはtoken無効化を
	// 検知してログアウトする用途)。
	if h.mainStreamPublisher != nil {
		h.mainStreamPublisher.PublishMainEvent(u.ID, "myTokenRegenerated", nil)
	}
	// upstream Misskey TS は body なしの 204 No Content を返す (= 新 token
	// は myTokenRegenerated WS event 経由で client に伝達する設計、#883)。
	// 旧 mk-go は 200 + {token} で同期的に返していたが drop-in 互換のため
	// 揃える。
	return c.NoContent(http.StatusNoContent)
}

// ClaimAchievement handles POST /api/i/claim-achievement.
// ユーザーの実績を記録する。既に獲得済みの場合は何もしない。
func (h *Handler) ClaimAchievement(c echo.Context) error {
	u := middleware.GetUser(c)
	var req struct {
		Name string `json:"name"`
	}
	if err := c.Bind(&req); err != nil || req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "name is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	profile := h.userService.GetProfile(u.ID)
	if profile == nil {
		return c.NoContent(http.StatusNoContent)
	}

	// 既存の実績をパース
	var achievements []map[string]any
	if profile.Achievements != nil {
		_ = json.Unmarshal(profile.Achievements, &achievements)
	}

	// 既に獲得済みか確認
	for _, a := range achievements {
		if a["name"] == req.Name {
			return c.NoContent(http.StatusNoContent)
		}
	}

	// 新しい実績を追加
	achievements = append(achievements, map[string]any{
		"name":       req.Name,
		"unlockedAt": time.Now().UnixMilli(),
	})
	data, _ := json.Marshal(achievements)

	if err := h.userService.UpdateProfileFields(u.ID, map[string]any{"achievements": string(data)}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	return c.NoContent(http.StatusNoContent)
}
