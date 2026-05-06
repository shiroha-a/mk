package signup

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/captcha"
	coreemail "github.com/shiroha-a/mk/internal/core/email"
	"github.com/shiroha-a/mk/internal/core/role"
	coresignup "github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	miscsmtp "github.com/shiroha-a/mk/internal/misc/smtp"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// TicketStore abstracts registration_ticket DB operations for testability.
type TicketStore interface {
	FindByCode(code string) (*model.RegistrationTicket, error)
	MarkUsed(ticketID, userID string) error
}

// Handler handles POST /api/signup.
type Handler struct {
	signupService *coresignup.Service
	metaRepo      repository.MetaRepository
	idGen         id.Generator
	captchaSvc    *captcha.Service // optional
	ticketStore   TicketStore      // optional, invitation code検証用
	testMode      bool             // true のとき disableRegistration / captcha をバイパス (本家 TS と同じ)
	// emailSender は EmailRequiredForSignup フローで確認メールを送る callback。
	// router 配線時に SMTP infra を closure に閉じ込めて注入する。未設定の場合は
	// 確認メールが送られないだけで pending row 自体は作る (テスト用)。
	// text と html 両方を受けて multipart/alternative で送出する想定 (html 空なら
	// text/plain only)。
	emailSender func(to string, msg miscsmtp.Message)
	// serverURL は確認 link の base。emailSender とセットで設定。
	serverURL string
	// emailValidationClient は verifymail / truemail SaaS API への outbound
	// に使う SSRF-safe HTTP client (#638)。nil ならデフォルト client が使われる。
	emailValidationClient *http.Client
}

// SetTestMode enables test-mode bypass (本家 `process.env.NODE_ENV !== 'test'` 相当).
func (h *Handler) SetTestMode(v bool) {
	h.testMode = v
}

// NewHandler creates a new signup Handler.
func NewHandler(signupService *coresignup.Service, metaRepo repository.MetaRepository, idGen id.Generator) *Handler {
	return &Handler{signupService: signupService, metaRepo: metaRepo, idGen: idGen}
}

// SetCaptcha attaches a CaptchaService for signup verification.
func (h *Handler) SetCaptcha(svc *captcha.Service) {
	h.captchaSvc = svc
}

// SetTicketStore attaches a TicketStore for invitation code validation.
func (h *Handler) SetTicketStore(ts TicketStore) {
	h.ticketStore = ts
}

// SetEmailSender wires a callback used to send signup confirmation emails.
// reset-password handler と同じ pattern。serverURL は 確認 link 生成に使う。
// callback は text + html 両 body を受け、multipart/alternative で送る (#600 item 4)。
func (h *Handler) SetEmailSender(serverURL string, send func(to string, msg miscsmtp.Message)) {
	h.serverURL = serverURL
	h.emailSender = send
}

// SetEmailValidationClient wires the outbound HTTP client used by verifymail
// / truemail siteverify calls (#638). production では SSRF-safe + forward
// proxy 経由の client を渡すこと。
func (h *Handler) SetEmailValidationClient(c *http.Client) {
	h.emailValidationClient = c
}

type signupRequest struct {
	Username       string `json:"username"`
	Password       string `json:"password"`
	EmailAddress   string `json:"emailAddress"`
	InvitationCode string `json:"invitationCode"`
	// CAPTCHA tokens (フィールド名はTS版スキーマに準拠)
	HcaptchaResponse    string `json:"hcaptcha-response"`
	RecaptchaResponse   string `json:"g-recaptcha-response"`
	TurnstileResponse   string `json:"turnstile-response"`
	McaptchaResponse    string `json:"m-captcha-response"`
	TestcaptchaResponse string `json:"testcaptcha-response"`
}

// duplicatedUsernameError は username 重複時の error response を返す。
//
// upstream Misskey TS は \`/api/signup\` の username 重複を 400 +
// DUPLICATED_USERNAME で返す (third_party の SignupApiService.ts:174)。
// mk-go も同 status / code に揃える (#798)。UUID は mk-go 内部の identifier
// として既存値を保持 (= TS は UUID を返さない / frontend は code で
// switch するので互換性に影響なし)。
//
// signup-pending 経路 / 通常 signup / signup-pending promote の 3 箇所から
// 参照されるので helper 化している。
func duplicatedUsernameError(c echo.Context) error {
	return c.JSON(http.StatusBadRequest, apierr.Error(
		"DUPLICATED_USERNAME",
		"Username already exists.",
		"0a504947-b888-4a99-9f62-8c4a0f3a3dab",
	))
}

// Signup handles POST /api/signup.
func (h *Handler) Signup(c echo.Context) error {
	var req signupRequest
	if err := c.Bind(&req); err != nil || req.Username == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	meta, err := h.metaRepo.Fetch()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// 登録無効時はinvitation code必須 (テストモードではバイパス — 本家 TS 互換)
	var ticket *model.RegistrationTicket
	if !h.testMode && meta.DisableRegistration {
		t, vErr := h.validateInvitationCode(req.InvitationCode)
		if vErr != nil {
			return c.JSON(http.StatusBadRequest, apierr.Error("INVITATION_CODE_INVALID", "Invalid invitation code.", "11e71a03-43c4-4a99-92cf-bb7e2c581998"))
		}
		ticket = t
	}

	// CAPTCHA検証 (テストモードではスキップ)
	if !h.testMode && h.captchaSvc != nil {
		tokens := captcha.CaptchaTokens{
			Hcaptcha:    req.HcaptchaResponse,
			Recaptcha:   req.RecaptchaResponse,
			Turnstile:   req.TurnstileResponse,
			Mcaptcha:    req.McaptchaResponse,
			Testcaptcha: req.TestcaptchaResponse,
		}
		if err := h.captchaSvc.Verify(c.Request().Context(), tokens); err != nil {
			return c.JSON(http.StatusBadRequest, apierr.Error("CAPTCHA_FAILED", "Captcha verification failed.", "bdc32ef5-b0f4-40c0-b767-673b2e3e1f5a"))
		}
	}

	// emailRequiredForSignup=true: 即時に user は作らず user_pending に積み、
	// 確認メールを送って /api/signup-pending で本登録させる Misskey TS 互換フロー。
	// testMode は完全バイパスしてフロント test での通常 signup を妨げない。
	if !h.testMode && meta.EmailRequiredForSignup {
		if req.EmailAddress == "" {
			return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "emailAddress is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
		}
		if verr := validateEmailWithMeta(c.Request().Context(), meta, req.EmailAddress, h.emailValidationClient); verr != nil {
			return c.JSON(http.StatusBadRequest, apierr.Error("UNAVAILABLE", "Email is not available.", "a25440a9-451e-41de-b291-00a8f29fbca6"))
		}
		// 招待制併用時は ticket.ID を pending row に保存しておき、
		// PromotePending 完了時に MarkUsed で消費する (#600 item 5)。
		var ticketID *string
		if ticket != nil {
			ticketID = &ticket.ID
		}
		pending, perr := h.signupService.CreatePending(req.Username, req.EmailAddress, req.Password, ticketID)
		if perr != nil {
			if perr == coresignup.ErrUsernameAlreadyExists {
				return duplicatedUsernameError(c)
			}
			if perr == coresignup.ErrInvalidUsername {
				return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid username.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
			}
			if perr == coresignup.ErrUsernameReserved {
				return c.JSON(http.StatusBadRequest, apierr.Error("USED_USERNAME", "That username is reserved.", "4b54bee6-2c25-42c3-a10f-7d0d1fbd91f9"))
			}
			return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
		if h.emailSender != nil {
			siteName := "Misskey"
			if meta.Name != nil && *meta.Name != "" {
				siteName = *meta.Name
			}
			confirmURL := h.signupConfirmURL(pending.Code)
			lead := "Welcome to " + siteName + "! Click the link to complete your signup:"
			text, bodyHTML := coreemail.LinkText(lead, "Complete signup", confirmURL)
			html := coreemail.WrapHTML(coreemail.HTMLWrapInput{
				SiteName: siteName,
				SiteURL:  h.serverURL,
				Subject:  "Confirm your account",
				BodyHTML: bodyHTML,
			})
			go h.emailSender(req.EmailAddress, miscsmtp.Message{
				Subject: "Confirm your account",
				Text:    text,
				HTML:    html,
			})
		}
		// TS 互換: 本体は何も返さない (frontend は確認メールを待つ)。
		return c.NoContent(http.StatusNoContent)
	}

	result, err := h.signupService.Signup(req.Username, req.Password, false)
	if err != nil {
		if err == coresignup.ErrUsernameAlreadyExists {
			return duplicatedUsernameError(c)
		}
		if err == coresignup.ErrInvalidUsername {
			return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid username.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
		}
		if err == coresignup.ErrUsernameReserved {
			return c.JSON(http.StatusBadRequest, apierr.Error("USED_USERNAME", "That username is reserved.", "4b54bee6-2c25-42c3-a10f-7d0d1fbd91f9"))
		}
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// invitation code使用済みにする
	if ticket != nil && h.ticketStore != nil {
		_ = h.ticketStore.MarkUsed(ticket.ID, result.User.ID)
	}

	return c.JSON(http.StatusOK, packSignupResponse(result.User, result.Token, h.idGen))
}

// SignupPending handles POST /api/signup-pending. Misskey TS 互換: code を
// 受け取り、対応する user_pending を本登録に昇格して { id, i: token } を返す。
func (h *Handler) SignupPending(c echo.Context) error {
	var req struct {
		Code string `json:"code"`
	}
	if err := c.Bind(&req); err != nil || req.Code == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "code is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	result, err := h.signupService.PromotePending(req.Code)
	if err != nil {
		switch err {
		case coresignup.ErrPendingNotFound:
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_CODE", "No such pending registration.", "1e53842e-b7f4-4e1c-8f1e-8d0a2d9b0c7e"))
		case coresignup.ErrPendingExpired:
			return c.JSON(http.StatusGone, apierr.Error("EXPIRED", "Pending registration has expired.", "9c2bc685-fa0a-4e6f-bf6f-5f4f8c0c3a3a"))
		case coresignup.ErrUsernameAlreadyExists:
			return duplicatedUsernameError(c)
		case coresignup.ErrInvitationAlreadyUsed:
			return c.JSON(http.StatusConflict, apierr.Error("INVITATION_ALREADY_USED", "Invitation already used.", "5b81b5e2-2c0b-4d8a-9b71-1a3e1d4d3f6a"))
		case coresignup.ErrInvitationRevoked:
			// admin が ticket を削除した状態 (#610 item 2)。AlreadyUsed と区別。
			return c.JSON(http.StatusGone, apierr.Error("INVITATION_REVOKED", "Invitation has been revoked.", "9b1aa3e7-f8e7-4c92-8d7c-2c0e5b9d8a4b"))
		default:
			return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
	}
	// 招待コード消費 — Service の tx 経路では tx 内で MarkUsed 済 (#604)。
	// 非 tx 経路 (mock テスト等) のみ handler 側で best-effort consume。
	// Service が consume したかは SignupResult.InvitationTicketConsumed で判別。
	if result.InvitationTicketID != nil && !result.InvitationTicketConsumed && h.ticketStore != nil {
		_ = h.ticketStore.MarkUsed(*result.InvitationTicketID, result.User.ID)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"id": result.User.ID,
		"i":  result.Token,
	})
}

// signupConfirmURL builds the email confirmation link Misskey TS uses
// (`/signup-complete/<code>`)。frontend がこの path を hand-off する想定。
func (h *Handler) signupConfirmURL(code string) string {
	base := h.serverURL
	if base == "" {
		base = "https://localhost"
	}
	return base + "/signup-complete/" + code
}

// validateEmailWithMeta runs the same validators i/UpdateEmail uses, so
// signup と email 変更で挙動が乖離しないようにする (banned domains / format /
// active check 等)。client は verifymail / truemail への outbound に使う
// SSRF-safe HTTP client (#638)。nil なら http.DefaultClient フォールバック。
func validateEmailWithMeta(ctx context.Context, meta *model.Meta, addr string, client *http.Client) error {
	svc := coreemail.NewServiceWithClient(meta, client)
	return svc.Validate(ctx, addr)
}

// validateInvitationCode checks the ticket store for a valid invitation code.
func (h *Handler) validateInvitationCode(code string) (*model.RegistrationTicket, error) {
	if code == "" || h.ticketStore == nil {
		return nil, errInvalidCode
	}
	ticket, err := h.ticketStore.FindByCode(code)
	if err != nil {
		return nil, errInvalidCode
	}
	if ticket.UsedByID != nil {
		return nil, errInvalidCode
	}
	if ticket.ExpiresAt != nil && ticket.ExpiresAt.Before(time.Now()) {
		return nil, errInvalidCode
	}
	return ticket, nil
}

// packSignupResponse builds a MeDetailed + token response for a newly created user.
func packSignupResponse(u *model.User, token string, idGen id.Generator) map[string]any {
	detailed := entity.PackUserDetailed(u, nil, idGen)
	return map[string]any{
		// UserLite
		"id":                detailed.ID,
		"name":              detailed.Name,
		"username":          detailed.Username,
		"host":              detailed.Host,
		"avatarUrl":         detailed.AvatarURL,
		"avatarBlurhash":    detailed.AvatarBlurhash,
		"avatarDecorations": detailed.AvatarDecorations,
		"isBot":             detailed.IsBot,
		"isCat":             detailed.IsCat,
		"emojis":            detailed.Emojis,
		"onlineStatus":      detailed.OnlineStatus,
		"badgeRoles":        detailed.BadgeRoles,
		// UserDetailed
		"bannerUrl":      detailed.BannerURL,
		"bannerBlurhash": detailed.BannerBlurhash,
		"isLocked":       detailed.IsLocked,
		"isSilenced":     false,
		"isSuspended":    detailed.IsSuspended,
		"description":    detailed.Description,
		"location":       detailed.Location,
		"birthday":       detailed.Birthday,
		"lang":           detailed.Lang,
		"fields":         detailed.Fields,
		"verifiedLinks":  []string{},
		"followersCount": detailed.FollowersCount,
		"followingCount": detailed.FollowingCount,
		"notesCount":     detailed.NotesCount,
		"pinnedNoteIds":  detailed.PinnedNoteIDs,
		"pinnedNotes":    detailed.PinnedNotes,
		"roles":          detailed.Roles,
		"uri":            detailed.URI,
		"url":            detailed.URL,
		"movedTo":        nil,
		"alsoKnownAs":    nil,
		"createdAt":      detailed.CreatedAt,
		"updatedAt":      detailed.UpdatedAt,
		"lastFetchedAt":  nil,
		// MeDetailed (新規ユーザーのデフォルト値)
		"avatarId":                 nil,
		"bannerId":                 nil,
		"followersVisibility":      "public",
		"followingVisibility":      "public",
		"chatScope":                "mutual",
		"canChat":                  true,
		"followedMessage":          nil,
		"memo":                     nil,
		"moderationNote":           nil,
		"isAdmin":                  false,
		"isModerator":              false,
		"hideOnlineStatus":         u.HideOnlineStatus,
		"email":                    nil,
		"emailVerified":            false,
		"autoAcceptFollowed":       true,
		"noCrawle":                 false,
		"preventAiLearning":        true,
		"alwaysMarkNsfw":           false,
		"autoSensitive":            false,
		"carefulBot":               false,
		"injectFeaturedNote":       true,
		"receiveAnnouncementEmail": true,
		"twoFactorEnabled":         false,
		"usePasswordLessLogin":     false,
		"publicReactions":          true,
		"mutedWords":               []any{},
		"hardMutedWords":           []any{},
		"mutedInstances":           []any{},
		"policies":                 role.DefaultPolicies(),
		"token":                    token,
	}
}

// defaultPolicies returns the Misskey default policies for a new user.
var errInvalidCode = errors.New("invalid invitation code")
