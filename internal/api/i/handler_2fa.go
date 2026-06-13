package i

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/twofactor"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"golang.org/x/crypto/bcrypt"
)

// wrapWebAuthnRequest builds a fresh *http.Request whose body is the
// browser-supplied attestation/assertion JSON. go-webauthn parses the body
// directly off the request, so we cannot pass through the original Echo
// request (its body has already been consumed by Bind()).
func wrapWebAuthnRequest(orig *http.Request, body json.RawMessage) (*http.Request, error) {
	req, err := http.NewRequestWithContext(orig.Context(), http.MethodPost, orig.URL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = orig.Header.Clone()
	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(bytes.NewReader(body))
	return req, nil
}

// TwoFARegister handles POST /api/i/2fa/register.
// TOTP秘密鍵を生成してtempSecretに保存、QRコードURLを返す。
func (h *Handler) TwoFARegister(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	profile := h.userService.GetProfile(user.ID)
	if profile == nil || profile.Password == nil {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "No password set.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	}
	// 2FA gate: upstream Misskey TS (2fa/register.ts:68/80) は twoFactorEnabled
	// なら token 必須 (= 既に 2FA 有効な user が secret を上書き再登録するケース)。
	// **必ず password check より先**: upstream は token-then-password 順なので、
	// wrong-password + wrong-token 同時送信時に upstream は INVALID_TOKEN を返す
	// (= mk-go も同じ shape にしないと frontend の error UI 分岐が崩れる)。
	// mk-go では旧来 password だけで通っていたため、password 漏洩 = 既存 2FA を
	// 攻撃者がコントロールする secret に置き換え可能だった。
	if profile.TwoFactorEnabled && !h.verify2FAToken(c.Request().Context(), profile, req.Token) {
		return c.JSON(http.StatusForbidden, apierr.InvalidToken())
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*profile.Password), []byte(req.Password)); err != nil {
		return c.JSON(http.StatusForbidden, apierr.Error("INCORRECT_PASSWORD", "Incorrect password.", "78d6c839-20c9-4c66-b90a-fc0542168b48"))
	}

	// issuer は upstream (register.ts) が config.host (instance hostname) を使う。
	// authenticator アプリのラベルとレスポンスの issuer field を一致させる (#1555)。
	issuer := h.twoFAIssuer()
	secret, uri, err := twofactor.GenerateSecret(issuer, user.Username)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// frontend (settings/2fa.qrdialog.vue) は `qr` を `<img :src=...>` で
	// 読み込むため、otpauth URI ではなく PNG data URL に変換する必要がある
	// (#697)。Misskey TS upstream の QRCode.toDataURL(url) と同形式。
	qrDataURL, err := twofactor.QRDataURL(uri)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// tempSecretに保存 (doneで確認後にsecretに移動)
	_ = h.userService.UpdateProfileFields(user.ID, map[string]any{"twoFactorTempSecret": secret})

	return c.JSON(http.StatusOK, map[string]any{
		"qr":     qrDataURL,
		"url":    uri,
		"secret": secret,
		"label":  user.Username,
		"issuer": issuer,
	})
}

// twoFAIssuer returns the TOTP issuer (instance hostname) derived from the
// configured server URL, matching upstream's config.host (register.ts)。
// serverURL 未配線 / parse 失敗時は "Misskey" にフォールバックする。
func (h *Handler) twoFAIssuer() string {
	if h.serverURL != "" {
		if u, err := url.Parse(h.serverURL); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return "Misskey"
}

// TwoFADone handles POST /api/i/2fa/done.
// TOTPコードを検証し、2FAを有効化する。同時にバックアップコードを生成して
// 一度だけレスポンスに含めて返す (ユーザーが控えておかないと二度と見られない)。
func (h *Handler) TwoFADone(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Token string `json:"token"`
	}
	if err := c.Bind(&req); err != nil || req.Token == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "token is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	profile := h.userService.GetProfile(user.ID)
	if profile == nil || profile.TwoFactorTempSecret == nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "2FA registration not started.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	// ValidateWithReplay は同一コードの replay を refuse する (RFC 6238 §5.2)。
	// temp secret は確認後すぐ permanent secret に昇格されるため strictly
	// 必要ではないが、3 経路で挙動を揃える方が監査・テスト時の予測可能性が
	// 高い。副作用: TwoFADone 成功直後 (replay guard TTL = TOTP acceptance
	// window 内) に同じ code で signin を試みると replay として 403 になる
	// (permanent secret に同 value で昇格するため Redis slot に hit する)。
	// 次の TOTP step に進めば解消する。
	if !twofactor.ValidateWithReplay(c.Request().Context(), h.totpReplayGuard, user.ID, req.Token, *profile.TwoFactorTempSecret) {
		return c.JSON(http.StatusForbidden, apierr.InvalidToken())
	}

	// バックアップコードを生成。crypto/rand 失敗は実質起きないが、起きた場合は
	// 2FA セットアップ自体を中断して 500 を返す。
	backupCodes, err := twofactor.GenerateBackupCodes()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Failed to generate backup codes.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// tempSecretをsecretに移動、2FAを有効化、backup codes を保存
	_ = h.userService.UpdateProfileFields(user.ID, map[string]any{
		"twoFactorSecret":       *profile.TwoFactorTempSecret,
		"twoFactorTempSecret":   nil,
		"twoFactorEnabled":      true,
		"twoFactorBackupSecret": pq.StringArray(backupCodes),
	})

	// upstream (done.ts) は 2FA 有効化後に meUpdated を publish して UI を更新する
	// (twoFactorEnabled の反映)。key 系 handler と挙動を揃える (#1555)。
	h.publishMeUpdated(user.ID)

	return c.JSON(http.StatusOK, map[string]any{
		"backupCodes": backupCodes,
	})
}

// TwoFAUnregister handles POST /api/i/2fa/unregister.
// 2FAを無効化する。
func (h *Handler) TwoFAUnregister(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	profile := h.userService.GetProfile(user.ID)
	if profile == nil || profile.Password == nil {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "No password set.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	}
	// 2FA gate: upstream Misskey TS (2fa/unregister.ts:53/65) は twoFactorEnabled
	// なら token 必須。**必ず password check より先**: upstream は token-then-
	// password 順なので、wrong-password + wrong-token 同時送信時 upstream は
	// INVALID_TOKEN を返す (= mk-go も同じ shape にしないと drop-in 違反)。
	// mk-go では旧来 password だけで通っていたため、password 漏洩 = 2FA bypass
	// で 2FA 無効化 → 以後の sensitive 操作も password だけで通る連鎖が成立
	// していた。
	if profile.TwoFactorEnabled && !h.verify2FAToken(c.Request().Context(), profile, req.Token) {
		return c.JSON(http.StatusForbidden, apierr.InvalidToken())
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*profile.Password), []byte(req.Password)); err != nil {
		return c.JSON(http.StatusForbidden, apierr.Error("INCORRECT_PASSWORD", "Incorrect password.", "7add0395-9901-4098-82f9-4f67af65f775"))
	}

	_ = h.userService.UpdateProfileFields(user.ID, map[string]any{
		"twoFactorSecret":       nil,
		"twoFactorEnabled":      false,
		"twoFactorBackupSecret": pq.StringArray(nil),
	})

	// upstream (unregister.ts) は 2FA 解除後に meUpdated を publish する (#1555)。
	h.publishMeUpdated(user.ID)

	return c.NoContent(http.StatusNoContent)
}

// --- WebAuthn handlers ---
//
// 共通プリチェック: パスワード再確認 + WebAuthn 依存性チェック。
// すべてのキー管理エンドポイントは現在のパスワードを要求する (本家 Misskey と
// 同じく直近の意図確認のため)。WebAuthn 依存性が未注入なら 503 を返す。
//
// 第 3 戻り値は「呼び出し側が処理を続行してよいか」のフラグ。false のとき
// レスポンスは既にこの helper 内で書き込まれているので、caller はそのまま
// nil を return する。
// incorrectPwID は INCORRECT_PASSWORD の endpoint 固有 id。upstream は
// register-key / key-done 等で別 id を割り当てるため、caller が渡す。
func (h *Handler) requireWebAuthn(c echo.Context, password, incorrectPwID string) (*model.User, *model.UserProfile, bool) {
	if h.webauthnSvc == nil || h.securityKeyRepo == nil {
		_ = c.JSON(http.StatusServiceUnavailable, apierr.Error("UNAVAILABLE", "WebAuthn is not configured.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		return nil, nil, false
	}
	user := middleware.GetUser(c)
	if user == nil {
		_ = c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
		return nil, nil, false
	}
	profile := h.userService.GetProfile(user.ID)
	if profile == nil || profile.Password == nil {
		_ = c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "No password set.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
		return nil, nil, false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*profile.Password), []byte(password)); err != nil {
		_ = c.JSON(http.StatusForbidden, apierr.Error("INCORRECT_PASSWORD", "Incorrect password.", incorrectPwID))
		return nil, nil, false
	}
	return user, profile, true
}

// verify2FAToken validates a TOTP token (or single-use backup code) against
// the given profile. Backup codes are consumed in-place via UpdateProfileFields.
// Returns true if the token authenticates the user.
//
// Misskey TS upstream の UserAuthService.twoFactorAuthenticate と同じ挙動:
// backup code に hit したら使い捨てで消費し、それ以外は TOTP として検証する。
// TOTP 経路は ValidateWithReplay で同一コードの replay を refuse する
// (RFC 6238 §5.2 / mk-go 独自 hardening、upstream Misskey TS は持たない)。
func (h *Handler) verify2FAToken(ctx context.Context, profile *model.UserProfile, token string) bool {
	if token == "" {
		return false
	}
	if remaining, err := twofactor.ConsumeBackupCode([]string(profile.TwoFactorBackupSecret), token); err == nil {
		_ = h.userService.UpdateProfileFields(profile.UserID, map[string]any{
			"twoFactorBackupSecret": pq.StringArray(remaining),
		})
		return true
	}
	if profile.TwoFactorSecret == nil {
		return false
	}
	return twofactor.ValidateWithReplay(ctx, h.totpReplayGuard, profile.UserID, token, *profile.TwoFactorSecret)
}

// twoFAKeyNameMaxLen mirrors upstream の paramDef `name: { minLength: 1,
// maxLength: 30 }`。frontend (`os.inputText`) も 30 文字制限を掛けているが、
// API 直叩き経路の defense-in-depth として backend でも enforce する。
const twoFAKeyNameMaxLen = 30

// TwoFARegisterKey handles POST /api/i/2fa/register-key.
// 1 段階目: パスワード + 2FA token 認証 + WebAuthn registration challenge を返す。
// レスポンスは Misskey TS upstream と同じく PublicKeyCredentialCreationOptions
// そのもの (frontend は `{publicKey: ...}` で wrap して使う) (#698)。
//
// 前提: ユーザーは既に TOTP で 2FA を有効化済みであること。security key は
// 2FA の上に重ねる factor なので未有効化なら TWO_FACTOR_NOT_ENABLED を返す。
//
// 認証順序: requireWebAuthn で password を先に検証してから 2FA token を verify
// する。upstream は逆順 (token → password) だが、片方だけ正しい状態の検出可否は
// 対称なので情報リーク的に等価。`requireWebAuthn` を共有する都合で password 先に
// 揃えている。
//
// upstream Misskey #17354 (= 2026.5.1 / triage #1013): 同 endpoint は upstream
// 2026.5.1 でも refactor (passkey library 更新 / response schema 簡略化) で
// 残っており削除されていない。triage 当時 `git show --stat` の純減 line 数を
// 削除と誤読したが、実際は file は保持。mk-go は WebAuthn を go-webauthn lib で
// 実装しており upstream の @simplewebauthn lib 更新は無関係なので、本 handler
// は upstream と独立に維持する。
func (h *Handler) TwoFARegisterKey(c echo.Context) error {
	var req struct {
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	user, profile, ok := h.requireWebAuthn(c, req.Password, "38769596-efe2-4faf-9bec-abbb3f2cd9ba")
	if !ok {
		return nil
	}
	if profile.TwoFactorEnabled && !h.verify2FAToken(c.Request().Context(), profile, req.Token) {
		return c.JSON(http.StatusForbidden, apierr.InvalidToken())
	}
	if !profile.TwoFactorEnabled {
		return c.JSON(http.StatusForbidden, apierr.Error("TWO_FACTOR_NOT_ENABLED", "2fa not enabled.", "bf32b864-449b-47b8-974e-f9a5468546f1"))
	}

	existing, err := h.securityKeyRepo.ListByUser(user.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	creation, err := h.webauthnSvc.BeginRegistration(c.Request().Context(), user, existing)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Failed to begin registration.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// frontend は `parseCreationOptionsFromJSON({publicKey: <res>})` で読むので、
	// 内側の PublicKeyCredentialCreationOptions だけを返す。
	return c.JSON(http.StatusOK, creation.Response)
}

// TwoFAKeyDone handles POST /api/i/2fa/key-done.
// 2 段階目: ブラウザから返ってきた attestation response を検証して
// UserSecurityKey 行を作成する。リクエストボディは upstream 互換の
// `{ password, token, name, credential }`。`credential` は browser の
// `PublicKeyCredential.toJSON()` 出力 (ParsedCredentialCreationData フォーマット)。
// session は user 単位で 1 件保持されているので sessionId は不要 (#698)。
//
// 認証順序は TwoFARegisterKey と同じ (password → 2FA token)。
func (h *Handler) TwoFAKeyDone(c echo.Context) error {
	var req struct {
		Password   string          `json:"password"`
		Token      string          `json:"token"`
		Name       string          `json:"name"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" || len(req.Credential) == 0 {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password / credential are required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	if req.Name == "" {
		req.Name = "Security Key"
	}
	if len(req.Name) > twoFAKeyNameMaxLen {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "name is too long.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	user, profile, ok := h.requireWebAuthn(c, req.Password, "0d7ec6d2-e652-443e-a7bf-9ee9a0cd77b0")
	if !ok {
		return nil
	}
	if profile.TwoFactorEnabled && !h.verify2FAToken(c.Request().Context(), profile, req.Token) {
		return c.JSON(http.StatusForbidden, apierr.InvalidToken())
	}
	if !profile.TwoFactorEnabled {
		return c.JSON(http.StatusForbidden, apierr.Error("TWO_FACTOR_NOT_ENABLED", "2fa not enabled.", "798d6847-b1ed-4f9c-b1f9-163c42655995"))
	}

	existing, err := h.securityKeyRepo.ListByUser(user.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// go-webauthn の FinishRegistration は *http.Request からボディを読むので、
	// JSON-RPC 経由で受け取った credential を新しい http.Request にラップして渡す。
	httpReq, err := wrapWebAuthnRequest(c.Request(), req.Credential)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "invalid credential payload.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	cred, err := h.webauthnSvc.FinishRegistration(c.Request().Context(), user, existing, httpReq)
	if err != nil {
		slog.Warn("2fa: webauthn FinishRegistration failed", "userId", user.ID, "err", err)
		return c.JSON(http.StatusForbidden, apierr.RegistrationFailed())
	}

	key := twofactor.CredentialToModel(cred, user.ID, req.Name)
	if err := h.securityKeyRepo.Create(key); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Failed to persist key.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// security key 1 つ以上 → securityKeysAvailable=true。本家と同じ挙動。
	_ = h.userService.UpdateProfileFields(user.ID, map[string]any{
		"securityKeysAvailable": true,
	})

	// upstream Misskey TS と同じく `meUpdated` を publish して frontend の
	// `$i` (current user 状態) を即時更新する (#707)。これが無いと UI の
	// 設定→セキュリティ→パスキー一覧に登録直後の鍵が出ず、ユーザーは
	// 「登録できなかった」と誤認する。
	h.publishMeUpdated(user.ID)

	return c.JSON(http.StatusOK, map[string]any{
		"id":   key.ID,
		"name": key.Name,
	})
}

// publishMeUpdated は upstream `meUpdated` event を main stream に流す。
// 失敗は best-effort で握り潰す (publishing は副次的なので main flow を
// 止めない)。userService.ShowByID で User + Profile を 1 度に取得する。
//
// frontend (main-boot.ts) は payload を updateCurrentAccountPartial で
// 部分 merge するので、本 helper が送る MeDetailed の field がそのまま
// `$i` に反映される。upstream Misskey TS の meUpdated stream は
// MeDetailed shape を流すため、entity.PackMeDetailed で isExplorable /
// noCrawle / preventAiLearning など self-view-only field も含めて送る
// (#968)。なお usePasswordLessLogin など UserProfile 由来でも MeDetailed
// に含まれない field は publishMeUpdatedPartial で個別 publish する
// (#758 経緯)。
func (h *Handler) publishMeUpdated(userID string) {
	if h.mainStreamPublisher == nil {
		return
	}
	bundle, err := h.userService.ShowByID(userID)
	if err != nil {
		slog.Warn("2fa: meUpdated: load user failed", "userId", userID, "err", err)
		return
	}
	// 生 PackMeDetailed だと unread 系が default (0/[]/false) で出て、frontend の
	// updateCurrentAccountPartial が `$i` の実 unread 状態を clobber する。
	// meDetailedWithUnread で fillUnreadFields を通して実値を送る (#1258 fu)。
	packed := h.meDetailedWithUnread(context.Background(), bundle.User, bundle.Profile)
	h.mainStreamPublisher.PublishMainEvent(userID, "meUpdated", packed)
}

// publishMeUpdatedPartial は specific field のみを `meUpdated` payload と
// して送る。frontend は updateCurrentAccountPartial で部分 merge する
// ので、entity.PackUserDetailed が含まない private profile field を
// 更新した endpoint がこの helper で当該 field だけ publish できる
// (#758)。fields が空 / mainStreamPublisher 未配線なら no-op。
func (h *Handler) publishMeUpdatedPartial(userID string, fields map[string]any) {
	if h.mainStreamPublisher == nil || len(fields) == 0 {
		return
	}
	h.mainStreamPublisher.PublishMainEvent(userID, "meUpdated", fields)
}

// TwoFARemoveKey handles POST /api/i/2fa/remove-key.
// 自分の鍵 1 つを削除する。残り 0 件になったら securityKeysAvailable を false にする。
//
// 注: requireWebAuthn は使わず checks を inline している。upstream Misskey
// TS (2fa/remove-key.ts:57/69) は **TOTP gate → password check** 順なので、
// password を先に check する requireWebAuthn だと wrong-password + wrong-
// token 時の error code が upstream と drift する (INCORRECT_PASSWORD vs
// INVALID_TOKEN)。drop-in 互換性のため upstream 順で並べ直す。
func (h *Handler) TwoFARemoveKey(c echo.Context) error {
	var req struct {
		Password     string `json:"password"`
		Token        string `json:"token"`
		CredentialID string `json:"credentialId"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" || req.CredentialID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password and credentialId are required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	if h.webauthnSvc == nil || h.securityKeyRepo == nil {
		return c.JSON(http.StatusServiceUnavailable, apierr.Error("UNAVAILABLE", "WebAuthn is not configured.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	user := middleware.GetUser(c)
	if user == nil {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	}
	profile := h.userService.GetProfile(user.ID)
	if profile == nil || profile.Password == nil {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "No password set.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	}
	// 2FA gate (passkey 削除は 2FA の物理 factor を 1 つ抜く操作なので強い
	// 認証が必要)。**必ず password check より先** に置く (upstream 順)。
	if profile.TwoFactorEnabled && !h.verify2FAToken(c.Request().Context(), profile, req.Token) {
		return c.JSON(http.StatusForbidden, apierr.InvalidToken())
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*profile.Password), []byte(req.Password)); err != nil {
		return c.JSON(http.StatusForbidden, apierr.Error("INCORRECT_PASSWORD", "Incorrect password.", "141c598d-a825-44c8-9173-cfb9d92be493"))
	}

	if err := h.securityKeyRepo.Delete(req.CredentialID, user.ID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.NoSuchKey())
	}

	if remaining, err := h.securityKeyRepo.CountByUser(user.ID); err == nil && remaining == 0 {
		_ = h.userService.UpdateProfileFields(user.ID, map[string]any{
			"securityKeysAvailable": false,
			"usePasswordLessLogin":  false,
		})
	}
	// upstream 互換: 削除でも `meUpdated` を publish して frontend UI を即時更新 (#707)。
	h.publishMeUpdated(user.ID)
	return c.NoContent(http.StatusNoContent)
}

// TwoFAUpdateKey handles POST /api/i/2fa/update-key.
// 鍵の表示名を変更する。
func (h *Handler) TwoFAUpdateKey(c echo.Context) error {
	var req struct {
		Password     string `json:"password"`
		CredentialID string `json:"credentialId"`
		Name         string `json:"name"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" || req.CredentialID == "" || req.Name == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "password / credentialId / name are required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	user, _, ok := h.requireWebAuthn(c, req.Password, "932c904e-9460-45b7-9ce6-7ed33be7eb2c")
	if !ok {
		return nil
	}
	// upstream (update-key.ts) は key not-found (NO_SUCH_KEY) と not-owned
	// (ACCESS_DENIED) を区別する。UpdateName は両者を ErrRecordNotFound に
	// 畳むため、先に FindByID して所有権を判定する (#1555)。
	key, err := h.securityKeyRepo.FindByID(req.CredentialID)
	if err != nil || key == nil {
		return c.JSON(http.StatusNotFound, apierr.NoSuchKey())
	}
	if key.UserID != user.ID {
		return c.JSON(http.StatusForbidden, apierr.AccessDenied())
	}
	if err := h.securityKeyRepo.UpdateName(req.CredentialID, user.ID, req.Name); err != nil {
		return c.JSON(http.StatusNotFound, apierr.NoSuchKey())
	}
	// 表示名変更も meUpdated で UI 反映 (#707)。
	h.publishMeUpdated(user.ID)
	return c.NoContent(http.StatusNoContent)
}

// TwoFAPasswordLess handles POST /api/i/2fa/password-less.
// `usePasswordLessLogin` フラグを切り替える。WebAuthn 鍵が登録されていないと
// 有効化できない (パスワードレスログインの前提が成立しないため)。
//
// upstream Misskey TS は paramDef を `{value: boolean}` (required: ['value'])
// で password を要求しない (#758)。secure endpoint なので RequireAuth
// middleware で session ベース認証が済んでいる前提。mk-go も合わせて
// password 必須を撤回する。
func (h *Handler) TwoFAPasswordLess(c echo.Context) error {
	var req struct {
		Value bool `json:"value"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "invalid request body.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	user := middleware.GetUser(c)
	if user == nil {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	}
	if req.Value {
		// security key が無いと passwordless 有効化不可。upstream は同じ
		// branch で profile を `usePasswordLessLogin=false` に巻き戻して
		// から error を返すので mk-go も合わせる。WebAuthn 未配線
		// (securityKeyRepo == nil) は鍵 0 件と等価扱い。
		hasKey := false
		if h.securityKeyRepo != nil {
			if n, err := h.securityKeyRepo.CountByUser(user.ID); err == nil && n > 0 {
				hasKey = true
			}
		}
		if !hasKey {
			_ = h.userService.UpdateProfileFields(user.ID, map[string]any{
				"usePasswordLessLogin": false,
			})
			return c.JSON(http.StatusBadRequest, apierr.NoSecurityKey())
		}
	}
	_ = h.userService.UpdateProfileFields(user.ID, map[string]any{
		"usePasswordLessLogin": req.Value,
	})
	// usePasswordLessLogin は /api/i 経路の private profile field 群に属し、
	// entity.PackUserDetailed が含まないため publishMeUpdated (UserDetailed
	// publish) では frontend の $i に反映されない (#758)。partial helper で
	// 当該 field だけ送る。
	h.publishMeUpdatedPartial(user.ID, map[string]any{
		"usePasswordLessLogin": req.Value,
	})
	return c.NoContent(http.StatusNoContent)
}
