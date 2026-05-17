package i

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	coreemail "github.com/shiroha-a/mk/internal/core/email"
	miscsmtp "github.com/shiroha-a/mk/internal/misc/smtp"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"golang.org/x/crypto/bcrypt"
)

// generateVerifyCode returns a random 16-char hex code for email verification.
func generateVerifyCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// verifyURL builds the email verification URL.
func (h *Handler) verifyURL(code string) string {
	base := h.serverURL
	if base == "" {
		base = "https://localhost"
	}
	return base + "/verify-email/" + code
}

// UpdateEmail handles POST /api/i/update-email.
// 本家 Misskey と同じフロー:
//  1. パスワード検証 (必須)
//  2. email が null → emailRequiredForSignup ならエラー、そうでなければクリア
//  3. email が非null → フォーマット + banned + active/API 検証
//  4. profile の email / emailVerified / emailVerifyCode を更新
//  5. 新 email があれば確認メールを送信
func (h *Handler) UpdateEmail(c echo.Context) error {
	u := middleware.GetUser(c)
	var req struct {
		Password string  `json:"password"`
		Email    *string `json:"email"`
		Token    string  `json:"token"`
	}
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}

	profile := h.userService.GetProfile(u.ID)
	if profile == nil || profile.Password == nil {
		return apierr.JSONInternalError(c)
	}

	// 2FA gate: upstream Misskey TS (update-email.ts) は twoFactorEnabled
	// なら token 必須。mk-go では旧来 password だけで通っていたため、
	// password 漏洩 = 2FA bypass で email 乗っ取り → password reset で
	// account takeover まで到達可能だった (drop-in regression、認証強度
	// として最も影響が大きい経路の 1 つ)。verify2FAToken 経由なので
	// replay 保護も自動で効く。
	if profile.TwoFactorEnabled && !h.verify2FAToken(c.Request().Context(), profile, req.Token) {
		return c.JSON(http.StatusForbidden, apierr.InvalidToken())
	}

	// パスワード検証。upstream Misskey TS は ApiError(meta.errors.incorrectPassword)
	// を framework が 400 (= client error) に変換する (#885)。mk-go も
	// drop-in 互換のため 400 に揃える (旧 mk-go は 403)。
	if err := bcrypt.CompareHashAndPassword([]byte(*profile.Password), []byte(req.Password)); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INCORRECT_PASSWORD", "Incorrect password.", "e86c14a4-0da8-4571-8f36-8a2e9f9b3a00"))
	}

	fields := map[string]any{
		"emailVerified":   false,
		"emailVerifyCode": nil,
	}

	if req.Email == nil {
		// email クリア。emailRequiredForSignup 時はクリア不可。
		if h.metaRepo != nil {
			if m, err := h.metaRepo.Fetch(); err == nil && m.EmailRequiredForSignup {
				return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Email is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
			}
		}
		fields["email"] = nil
	} else {
		addr := *req.Email
		// email validation (banned + format + active/API)。verifymail / truemail
		// API への outbound は SSRF-safe + forward proxy 経由 (#638)。
		if h.metaRepo != nil {
			if m, err := h.metaRepo.Fetch(); err == nil {
				svc := coreemail.NewServiceWithClient(m, h.emailValidationClient)
				if verr := svc.Validate(c.Request().Context(), addr); verr != nil {
					return c.JSON(http.StatusBadRequest, apierr.Error("UNAVAILABLE", "Email is not available.", "a2defefb-f220-8849-0af6-17f816099323"))
				}
			}
		}
		fields["email"] = addr

		// 確認コード生成 + メール送信
		code := generateVerifyCode()
		fields["emailVerifyCode"] = code

		if h.emailSender != nil {
			lead := "Click the link to verify your email:"
			text, bodyHTML := coreemail.LinkText(lead, "Verify email", h.verifyURL(code))
			html := coreemail.WrapHTML(coreemail.HTMLWrapInput{
				SiteURL:          h.serverURL,
				Subject:          "Verify your email",
				EmailSettingsURL: h.serverURL + "/settings/email",
				BodyHTML:         bodyHTML,
			})
			go h.emailSender(addr, miscsmtp.Message{
				Subject: "Verify your email",
				Text:    text,
				HTML:    html,
			})
		}
	}

	if err := h.userService.UpdateProfileFields(u.ID, fields); err != nil {
		return apierr.JSONInternalError(c)
	}

	return h.Me(c)
}

// VerifyEmail handles POST /api/verify-email.
// emailVerifyCode が一致すれば emailVerified を true にする。
func (h *Handler) VerifyEmail(c echo.Context) error {
	var req struct {
		Code string `json:"code"`
	}
	if err := c.Bind(&req); err != nil || req.Code == "" {
		return apierr.JSONInvalidParam(c)
	}

	profile, err := h.userService.FindProfileByVerifyCode(req.Code)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_CODE", "No such code.", "1e53842e-b7f4-4e1c-8f1e-8d0a2d9b0c7e"))
	}

	if verr := h.userService.UpdateProfileFields(profile.UserID, map[string]any{
		"emailVerified":   true,
		"emailVerifyCode": nil,
	}); verr != nil {
		return apierr.JSONInternalError(c)
	}

	return c.NoContent(http.StatusNoContent)
}
