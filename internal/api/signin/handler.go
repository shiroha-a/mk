package signin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/captcha"
	"github.com/shiroha-a/mk/internal/core/twofactor"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/datatypes"
)

// IPLogger records user IPs on successful authentication.
type IPLogger interface {
	Upsert(userID, ip string) error
}

// MainStreamPublisher emits real-time events to a single user's `main`
// WebSocket channel. Used here to publish the `signin` event so other
// sessions / devices of the same user learn about the new login.
// 循環依存を避けるためinterfaceで受け取る(実装はinternal/stream)。
type MainStreamPublisher interface {
	PublishMainEvent(userID, eventType string, body any)
}

// Handler handles signin-related API endpoints.
type Handler struct {
	userRepo            repository.UserRepository
	webauthnSvc         *twofactor.WebAuthnService
	securityKeyRepo     repository.UserSecurityKeyRepository
	captchaSvc          *captcha.Service
	ipLogger            IPLogger
	ipLoggingOn         bool
	signinRepo          repository.SigninRepository
	idGen               id.Generator
	mainStreamPublisher MainStreamPublisher
	totpReplayGuard     twofactor.ReplayGuard
	loginNotifier       LoginNotifier
}

// LoginNotifier records a 'login' notification on signin success (#1559)。
// 循環依存を避けるため interface で受け取る (実装は core/notification.Hook)。
type LoginNotifier interface {
	OnLogin(userID string)
}

// SetLoginNotifier attaches a LoginNotifier so successful signins create a
// 'login' notification (upstream SigninService)。
func (h *Handler) SetLoginNotifier(n LoginNotifier) {
	h.loginNotifier = n
}

// SetIPLogger attaches an IPLogger and enables IP logging.
func (h *Handler) SetIPLogger(logger IPLogger, enabled bool) {
	h.ipLogger = logger
	h.ipLoggingOn = enabled
}

// NewHandler creates a new signin Handler.
func NewHandler(userRepo repository.UserRepository) *Handler {
	return &Handler{userRepo: userRepo}
}

// SetSigninRepo attaches a SigninRepository for recording login history.
func (h *Handler) SetSigninRepo(repo repository.SigninRepository, idGen id.Generator) {
	h.signinRepo = repo
	h.idGen = idGen
}

// SetCaptcha attaches a CaptchaService. When set, signin-flow verifies
// the captcha token on the password step (same as original Misskey).
func (h *Handler) SetCaptcha(svc *captcha.Service) {
	h.captchaSvc = svc
}

// SetWebAuthn attaches optional WebAuthn dependencies to enable 2FA login
// via security keys. Without it the handler still supports TOTP and backup
// codes (and falls back to single-factor when 2FA is disabled).
func (h *Handler) SetWebAuthn(svc *twofactor.WebAuthnService, repo repository.UserSecurityKeyRepository) {
	h.webauthnSvc = svc
	h.securityKeyRepo = repo
}

// SetMainStreamPublisher attaches a publisher used to emit the `signin`
// event after a successful login. Optional — nil disables emit.
func (h *Handler) SetMainStreamPublisher(p MainStreamPublisher) {
	h.mainStreamPublisher = p
}

// SetTOTPReplayGuard wires the per-user replay guard used to refuse a
// TOTP code that was already consumed within its acceptance window
// (RFC 6238 §5.2). nil disables the protection (= upstream-compatible
// fallback for unit tests / dev environments without Redis).
func (h *Handler) SetTOTPReplayGuard(g twofactor.ReplayGuard) {
	h.totpReplayGuard = g
}

// Signin handles POST /api/signin.
// Misskey フロントエンドのログインフロー:
// 1. username のみ → { finished: false, next: "password" }
// 2. username + password → { finished: true, id: "...", i: "token" }
//
// Note: `/api/signin` (legacy) と `/api/signin-flow` で step 1 の `next` 値が
// 異なる。legacy はユーザの 2FA 設定に関わらず `'password'` 固定で返すのに対し、
// `/signin-flow` は 2FA 無効ユーザに対して `'captcha'` を返す (TS upstream 互換)。
// TS upstream 互換は `/signin-flow` のみ提供する。
func (h *Handler) Signin(c echo.Context) error {
	var req struct {
		Username string  `json:"username"`
		Password *string `json:"password"`
	}
	if err := c.Bind(&req); err != nil || req.Username == "" {
		return c.JSON(http.StatusBadRequest, errBody("6cc579cc-885d-43d8-95c2-b8c7fc963280"))
	}

	// ユーザー検索
	user, err := h.userRepo.FindByUsernameLower(req.Username, nil)
	if err != nil {
		return c.JSON(http.StatusNotFound, errBody("6cc579cc-885d-43d8-95c2-b8c7fc963280"))
	}

	if user.IsSuspended {
		return c.JSON(http.StatusForbidden, errBody("e03a5f46-d309-4865-9b69-56282d94e1eb"))
	}

	// Step 1: パスワードなしの場合、次のステップを返す
	if req.Password == nil {
		return c.JSON(http.StatusOK, map[string]any{
			"finished": false,
			"next":     "password",
		})
	}

	// Step 2: パスワード検証
	profile, err := h.userRepo.FindProfileByUserID(user.ID)
	if err != nil || profile.Password == nil {
		return c.JSON(http.StatusForbidden, errBody("932c904e-9460-45b7-9ce6-7ed33be7eb2c"))
	}

	if err := bcrypt.CompareHashAndPassword([]byte(*profile.Password), []byte(*req.Password)); err != nil {
		return h.fail(c, user, http.StatusForbidden, "932c904e-9460-45b7-9ce6-7ed33be7eb2c")
	}

	// #2106 H2 (CRITICAL): 2FA 有効ユーザーには password のみで token を発行しない。
	// レガシー /api/signin は 2 要素を完遂できない (req に token/credential が無い)
	// ため challenge を返して /signin-flow へ誘導する。これを欠くと 2FA が完全に
	// バイパスされ password 漏洩だけでアカウントが乗っ取られる (SigninFlow は正しく
	// 2FA を扱う、本 endpoint のみが穴だった)。
	if profile.TwoFactorEnabled {
		next := "totp"
		if h.webauthnSvc != nil && h.securityKeyRepo != nil {
			if ks, kerr := h.securityKeyRepo.ListByUser(user.ID); kerr == nil && len(ks) > 0 {
				next = "passkey"
			}
		}
		return c.JSON(http.StatusOK, map[string]any{
			"finished": false,
			"next":     next,
		})
	}

	// 認証成功 (2FA 無し)
	return h.ok(c, user)
}

// SigninFlow handles POST /api/signin-flow.
// TS本家のmulti-step ログインフロー (Misskey 2026.x):
//
//	Step 1: { username }                          → 2FA 有: { next: "password" }、無: { next: "captcha" }
//	Step 2: { username, password }                → 2FA 無し: { finished, id, i }
//	                                              → 鍵あり: { next: "passkey", authRequest }
//	                                              → 鍵なし: { next: "totp" }
//	Step 3 (TOTP):  { username, password, token } → { finished, id, i }
//	Step 3 (BU):    { username, password, token } → backup code 検証
//	Step 3 (Key):   { username, password, credential } → WebAuthn assertion 検証
//
// Misskey TS upstream の SigninApiService と完全互換。フロントの MkSignin.vue は
// `next` 値で switch するので、`'captcha' | 'password' | 'totp' | 'passkey'`
// 以外は受け付けない (#705)。
func (h *Handler) SigninFlow(c echo.Context) error {
	var req struct {
		Username   string          `json:"username"`
		Password   *string         `json:"password"`
		Token      *string         `json:"token"`
		Credential json.RawMessage `json:"credential"`
		// CAPTCHA tokens — フロントエンドは有効な provider の token だけ送る。
		HcaptchaResponse    string `json:"hcaptcha-response"`
		RecaptchaResponse   string `json:"g-recaptcha-response"`
		TurnstileResponse   string `json:"turnstile-response"`
		McaptchaResponse    string `json:"m-captcha-response"`
		TestcaptchaResponse string `json:"testcaptcha-response"`
	}
	if err := c.Bind(&req); err != nil || req.Username == "" {
		return c.JSON(http.StatusBadRequest, errBody("6cc579cc-885d-43d8-95c2-b8c7fc963280"))
	}

	// ユーザー検索 (小文字で検索)
	user, err := h.userRepo.FindByUsernameLower(req.Username, nil)
	if err != nil {
		return c.JSON(http.StatusNotFound, errBody("6cc579cc-885d-43d8-95c2-b8c7fc963280"))
	}

	if user.IsSuspended {
		return c.JSON(http.StatusForbidden, errBody("e03a5f46-d309-4865-9b69-56282d94e1eb"))
	}

	// Step 1: パスワード未提供 → 次のステップを指示
	// TS upstream: 2FA 有効 → "password"、2FA 無効 → "captcha" (captcha 設定の有無に
	// 関わらず常に 'captcha' を返す。フロントが captcha widget の表示要否を
	// instance meta で判定する)。
	if req.Password == nil {
		next := "password"
		if p, perr := h.userRepo.FindProfileByUserID(user.ID); perr == nil && p != nil {
			if !p.TwoFactorEnabled {
				next = "captcha"
			}
		}
		return c.JSON(http.StatusOK, map[string]any{
			"finished": false,
			"next":     next,
		})
	}

	// Step 2: パスワード検証
	profile, err := h.userRepo.FindProfileByUserID(user.ID)
	if err != nil || profile.Password == nil {
		return c.JSON(http.StatusForbidden, errBody("932c904e-9460-45b7-9ce6-7ed33be7eb2c"))
	}

	passwordOK := bcrypt.CompareHashAndPassword([]byte(*profile.Password), []byte(*req.Password)) == nil

	// CAPTCHA 検証 (password step 完了後、2FA 無しの場合のみ)。
	// 本家 Misskey と同じく 2FA 有効なユーザーはキーデバイスが人間性を担保する
	// ため CAPTCHA をスキップする。
	if !profile.TwoFactorEnabled && h.captchaSvc != nil {
		tokens := captcha.CaptchaTokens{
			Hcaptcha:    req.HcaptchaResponse,
			Recaptcha:   req.RecaptchaResponse,
			Turnstile:   req.TurnstileResponse,
			Mcaptcha:    req.McaptchaResponse,
			Testcaptcha: req.TestcaptchaResponse,
		}
		if err := h.captchaSvc.Verify(c.Request().Context(), tokens); err != nil {
			// upstream は captcha 失敗時に Fastify-style reply error を返す
			// (SigninApiService.ts:186-213 の 5 provider どれも `throw new
			// FastifyReplyError(400, err)`)。mk-go も同 shape に揃える (#810)。
			return apierr.FastifyReply(c, http.StatusBadRequest, "CAPTCHA_FAILED")
		}
	}

	if !profile.TwoFactorEnabled {
		if !passwordOK {
			return h.fail(c, user, http.StatusForbidden, "932c904e-9460-45b7-9ce6-7ed33be7eb2c")
		}
		return h.ok(c, user)
	}

	// 2FA 経路: TwoFactorEnabled が立っていたら 2 要素を要求する。
	// security key を持っていれば WebAuthn step を最初に提示する (assertion challenge)。
	hasKeys := false
	var keys []*model.UserSecurityKey
	if h.webauthnSvc != nil && h.securityKeyRepo != nil {
		ks, err := h.securityKeyRepo.ListByUser(user.ID)
		if err == nil && len(ks) > 0 {
			hasKeys = true
			keys = ks
		}
	}

	// Step 3 (TOTP / Backup): token フィールドで 2FA を検証する。
	if req.Token != nil && *req.Token != "" {
		if !passwordOK {
			return h.fail(c, user, http.StatusForbidden, "932c904e-9460-45b7-9ce6-7ed33be7eb2c")
		}
		// まず TOTP を試す。失敗したらバックアップコードにフォールバック。
		// ValidateWithReplay は RFC 6238 §5.2 に従い同コードの 2 回目以降
		// (acceptance window 内) を拒否する (mk-go 独自 hardening、upstream
		// Misskey TS は持たない)。
		if profile.TwoFactorSecret != nil && twofactor.ValidateWithReplay(c.Request().Context(), h.totpReplayGuard, user.ID, *req.Token, *profile.TwoFactorSecret) {
			return h.ok(c, user)
		}
		if remaining, berr := twofactor.ConsumeBackupCode([]string(profile.TwoFactorBackupSecret), *req.Token); berr == nil {
			_ = h.userRepo.UpdateProfile(user.ID, map[string]any{
				"twoFactorBackupSecret": pq.StringArray(remaining),
			})
			return h.ok(c, user)
		}
		return h.fail(c, user, http.StatusForbidden, "cdf1235b-ac71-46d4-a3a6-84ccce48df6f")
	}

	// Step 3 (Key): credential が来ていれば WebAuthn assertion を検証する。
	if len(req.Credential) > 0 {
		if !passwordOK && !profile.UsePasswordLessLogin {
			return h.fail(c, user, http.StatusForbidden, "932c904e-9460-45b7-9ce6-7ed33be7eb2c")
		}
		if !hasKeys || h.webauthnSvc == nil {
			return h.fail(c, user, http.StatusForbidden, "93b86c4b-72f9-40eb-9815-798928603d1e")
		}
		httpReq, werr := wrapWebAuthnRequest(c.Request(), req.Credential)
		if werr != nil {
			return c.JSON(http.StatusBadRequest, errBody("ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
		}
		cred, werr := h.webauthnSvc.FinishLogin(c.Request().Context(), user, keys, httpReq)
		if werr != nil {
			// 失敗の root cause (challenge mismatch / origin / credential format
			// 等) は webauthn library 内部で発生する。frontend には汎用 403 を
			// 返すが backend log には何で落ちたかを残しておくと #707 系の
			// 「パスキーの検証に失敗しました」報告の調査に役立つ。
			slog.Warn("signin: webauthn FinishLogin failed", "userId", user.ID, "err", werr)
			return h.fail(c, user, http.StatusForbidden, "93b86c4b-72f9-40eb-9815-798928603d1e")
		}
		// counter 更新 (clone 検出用)
		if h.securityKeyRepo != nil {
			_ = h.securityKeyRepo.UpdateCounter(encodeCredID(cred.ID), int64(cred.Authenticator.SignCount))
		}
		return h.ok(c, user)
	}

	// 2FA が必要だが token / credential いずれも来ていない。
	// password が違う場合はここで弾く (challenge を発行しない)。
	if !passwordOK {
		return h.fail(c, user, http.StatusForbidden, "932c904e-9460-45b7-9ce6-7ed33be7eb2c")
	}
	// security key 保有なら assertion challenge を発行する (`next: 'passkey'`)。
	// 本家 Misskey の SigninApiService と同じく `authRequest` フィールドに
	// PublicKeyCredentialRequestOptionsJSON を入れる。go-webauthn が返す
	// CredentialAssertion は `{publicKey: ...}` で 1 段ラップされているので
	// 内側の Response (= PublicKeyCredentialRequestOptions) だけを送る。
	if hasKeys && h.webauthnSvc != nil {
		assertion, werr := h.webauthnSvc.BeginLogin(c.Request().Context(), user, keys)
		if werr == nil {
			return c.JSON(http.StatusOK, map[string]any{
				"finished":    false,
				"next":        "passkey",
				"authRequest": assertion.Response,
			})
		}
	}
	// 鍵が無いか BeginLogin が失敗 → TOTP に fallback。
	return c.JSON(http.StatusOK, map[string]any{
		"finished": false,
		"next":     "totp",
	})
}

// ok returns the standard "logged in" response. 認証経路 (TOTP / WebAuthn /
// pwd-only) すべてで同じ shape を返したいのでヘルパに切り出している。
func (h *Handler) ok(c echo.Context, user *model.User) error {
	h.RecordSuccessfulSignin(user.ID, c.RealIP(), c.Request().Header.Clone())
	token := ""
	if user.Token != nil {
		token = *user.Token
	}
	return c.JSON(http.StatusOK, map[string]any{
		"finished": true,
		"id":       user.ID,
		"i":        token,
	})
}

// RecordSuccessfulSignin fires the side-effects of a successful login:
// IP logging, signin history (success:true) + main-stream publish, and the
// 'login' notification. Shared by every signin path (password / 2FA / passkey)
// and by account creation (signup / signup-pending) so a freshly created
// account also gets a login-history entry + notification, matching upstream
// SigninService.signin (#1804)。全て非同期で発火し signin/signup のレイテンシに
// 乗せない。headers は呼び出し元が Clone 済みのものを渡す前提
// (Echo が request を再利用する前にコピーする)。
//
// NOTE: upstream の new-login email は mk-go の signin 自体が未実装のため本 helper
// にも含めない (signin/signup 双方の今後の課題)。
func (h *Handler) RecordSuccessfulSignin(userID, ip string, headers http.Header) {
	if h.ipLoggingOn && h.ipLogger != nil {
		go h.ipLogger.Upsert(userID, ip)
	}
	if h.signinRepo != nil && h.idGen != nil {
		go h.recordSignin(userID, ip, headers, true)
	}
	if h.loginNotifier != nil {
		go h.loginNotifier.OnLogin(userID)
	}
}

// sanitizeHeaders removes sensitive headers in-place before persisting.
// 呼び出し元でClone済みのヘッダーを受け取る前提
func sanitizeHeaders(h http.Header) {
	h.Del("Authorization")
	h.Del("Cookie")
	h.Del("Set-Cookie")
}

// recordSignin persists a signin record asynchronously. success=false は
// 認証失敗履歴 (upstream SigninApiService.fail() の signins.insert) で、i/signin-history
// に表示される security 機能。失敗時は main へ publish しない (成功ログインのみ
// 他デバイスへ通知する upstream SigninService の挙動に揃える、#1776)。
func (h *Handler) recordSignin(userID, ip string, headers http.Header, success bool) {
	sanitizeHeaders(headers)
	hdrs, err := json.Marshal(headers)
	if err != nil {
		hdrs = []byte("{}")
	}
	now := time.Now()
	s := &model.Signin{
		ID:      h.idGen.Generate(now),
		UserID:  userID,
		IP:      ip,
		Headers: datatypes.JSON(hdrs),
		Success: success,
	}
	if err := h.signinRepo.Create(s); err != nil {
		slog.Warn("failed to record signin", "userId", userID, "success", success, "err", err)
		return
	}
	if !success {
		return
	}
	// TS本家 SigninService.ts:49 と同じく、signin成功後にmainへpublishする。
	// multi-device間で新規ログインを即時同期する用途。
	if h.mainStreamPublisher != nil {
		h.mainStreamPublisher.PublishMainEvent(userID, "signin", entity.PackSignin(s, h.idGen))
	}
}

// fail records a failed signin (success:false) for the already-resolved user
// and returns the error response. upstream SigninApiService.fail() は password
// 不一致 / 2FA 失敗 / passkey passwordless 無効 等の認証失敗で signins に
// success:false を insert してから error を返す (#1776)。user/signinRepo 未配線時は
// 記録を skip して error だけ返す (= 旧挙動の superset、test fixture を壊さない)。
func (h *Handler) fail(c echo.Context, user *model.User, status int, errID string) error {
	if user != nil && h.signinRepo != nil && h.idGen != nil {
		hdrs := c.Request().Header.Clone()
		go h.recordSignin(user.ID, c.RealIP(), hdrs, false)
	}
	return c.JSON(status, errBody(errID))
}

func errBody(id string) map[string]any {
	return map[string]any{
		"error": map[string]any{"id": id},
	}
}

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

// encodeCredID returns the storage key (base64url) for a webauthn credential
// id. Centralizing the encoding here avoids drift between handler_2fa.go's
// CredentialToModel and signin's counter update path.
func encodeCredID(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}
