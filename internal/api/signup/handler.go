package signup

import (
	"context"
	"errors"
	"log/slog"
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
	// MarkPending records usedAt + pendingUserId for an email-confirmation
	// signup (確認メール再送防止窓、#2083)。
	MarkPending(ticketID, pendingID string) error
}

// SigninRecorder fires the side-effects of a successful login (signin history,
// login notification, main-stream publish) after account creation, matching
// upstream SignupApiService -> signinService.signin (#1804)。実装は
// internal/api/signin.Handler.RecordSuccessfulSignin。未配線なら副作用を発火しない
// だけ (response shape は不変)。
type SigninRecorder interface {
	RecordSuccessfulSignin(userID, ip string, headers http.Header)
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
	// signinRecorder はアカウント作成成功時に signin 副作用 (履歴 / login 通知 /
	// main publish) を発火する。upstream SignupApiService が signinService.signin を
	// 呼ぶのに相当 (#1804)。未配線なら副作用なし。
	signinRecorder SigninRecorder
}

// SetSigninRecorder wires the recorder used to fire login side-effects after
// account creation (#1804)。
func (h *Handler) SetSigninRecorder(r SigninRecorder) {
	h.signinRecorder = r
}

// fireSigninSideEffects records the successful-login side-effects for a newly
// created account (#1804). headers は Echo が request を再利用する前に Clone する。
func (h *Handler) fireSigninSideEffects(c echo.Context, userID string) {
	if h.signinRecorder == nil || userID == "" {
		return
	}
	h.signinRecorder.RecordSuccessfulSignin(userID, c.RealIP(), c.Request().Header.Clone())
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
// upstream Misskey TS は \`/api/signup\` の username 重複を Fastify-style
// reply error 形式 \`{statusCode:400, error:"Bad Request",
// message:"Error: DUPLICATED_USERNAME"}\` で返す (third_party の
// SignupApiService.ts:174 で \`throw new FastifyReplyError(400,
// 'DUPLICATED_USERNAME')\`)。mk-go も同 status / shape に揃える
// (#798 で status / code、#802 で body shape)。
//
// signup-pending 経路 / 通常 signup / signup-pending promote の 3 箇所から
// 参照されるので helper 化している。
func duplicatedUsernameError(c echo.Context) error {
	return apierr.FastifyReply(c, http.StatusBadRequest, "DUPLICATED_USERNAME")
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
		t, vErr := h.validateInvitationCode(req.InvitationCode, meta.EmailRequiredForSignup)
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
			// upstream は captcha verify 失敗時に Fastify-style reply error
			// (`throw new FastifyReplyError(400, err)`) を返す (SignupApiService.ts:81)。
			// shape を揃えて drop-in 互換を担保する (#802)。
			return apierr.FastifyReply(c, http.StatusBadRequest, "CAPTCHA_FAILED")
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
			// upstream は username 系 error を Fastify-style reply error
			// で投げる (SignupApiService.ts:174-184)。mk-go も同 shape に揃える
			// (#802)。INVALID_PARAM は upstream 上流に対応 code が無いが
			// (= signupService 内部の generic catch で error.toString() を
			// message に詰める形) drop-in でも shape を揃える方が consumer 側
			// で 1 経路に統一できるので Fastify-style にする。
			if errors.Is(perr, coresignup.ErrUsernameAlreadyExists) {
				return duplicatedUsernameError(c)
			}
			if errors.Is(perr, coresignup.ErrInvalidUsername) {
				// upstream SignupService は INVALID_USERNAME を投げる (#2081)。
				return apierr.FastifyReply(c, http.StatusBadRequest, "INVALID_USERNAME")
			}
			if errors.Is(perr, coresignup.ErrUsernameUsed) {
				return apierr.FastifyReply(c, http.StatusBadRequest, "USED_USERNAME")
			}
			if errors.Is(perr, coresignup.ErrUsernameReserved) {
				// upstream SignupApiService の email-required path は preserved を
				// DENIED_USERNAME で返す (非 email path の USED_USERNAME とは異なる、#2080)。
				return apierr.FastifyReply(c, http.StatusBadRequest, "DENIED_USERNAME")
			}
			if errors.Is(perr, coresignup.ErrPasswordTooLong) {
				return apierr.FastifyReply(c, http.StatusBadRequest, "PASSWORD_TOO_LONG")
			}
			return apierr.FastifyReply(c, http.StatusInternalServerError, "INTERNAL_ERROR")
		}
		// 招待制併用時は ticket に usedAt + pendingId をセットし、確認メール送信から
		// 30 分間の再送を validateInvitationCode で弾く (upstream SignupApiService の
		// update({usedAt, pendingUserId})、#2083)。確認完了時に PromotePending が
		// MarkUsed で usedById を立てる。
		if ticket != nil && h.ticketStore != nil {
			if merr := h.ticketStore.MarkPending(ticket.ID, pending.ID); merr != nil {
				// best-effort (pending は作成済) だが、失敗すると再送防止窓が効かないので
				// observability のため warn を残す (#2083)。
				slog.Warn("signup: failed to mark invitation ticket pending", "ticketId", ticket.ID, "err", merr)
			}
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

	// 本家 SignupService は経路を問わず rootUserId 未設定なら、作成した
	// アカウントを root にする (SignupService.ts:160)。mk-go は本番でこれを
	// 認めない。本家は /api/signup 側で setupPassword を検証しないため、
	// disableRegistration を開けた瞬間に誰でも root を取れてしまうからで、
	// mk-go では setupPassword で保護された admin/accounts/create のみを
	// root 生成経路とする (docs/divergence.md)。
	//
	// ただし TestMode では本家に揃える。本家の e2e は
	// `root = await signup({ username: 'root' })` で管理者を作る前提で書かれて
	// おり、揃えないと admin 系がまとめて 403 になる。TestMode は本番で必ず
	// false かつ起動時に警告が出る。
	isInitialSetup := h.testMode && meta.RootUserID == nil
	result, err := h.signupService.Signup(req.Username, req.Password, isInitialSetup)
	if err != nil {
		// upstream の `/api/signup` は username 系 error を Fastify-style
		// reply error で投げる (SignupApiService.ts)。shape を揃える (#802)。
		if errors.Is(err, coresignup.ErrUsernameAlreadyExists) {
			return duplicatedUsernameError(c)
		}
		if errors.Is(err, coresignup.ErrInvalidUsername) {
			// upstream SignupService は INVALID_USERNAME を投げる (#2081)。
			return apierr.FastifyReply(c, http.StatusBadRequest, "INVALID_USERNAME")
		}
		if errors.Is(err, coresignup.ErrUsernameUsed) {
			return apierr.FastifyReply(c, http.StatusBadRequest, "USED_USERNAME")
		}
		if errors.Is(err, coresignup.ErrUsernameReserved) {
			// 非 email path は upstream SignupService と同じく preserved も USED_USERNAME (#2080)。
			return apierr.FastifyReply(c, http.StatusBadRequest, "USED_USERNAME")
		}
		if errors.Is(err, coresignup.ErrPasswordTooLong) {
			return apierr.FastifyReply(c, http.StatusBadRequest, "PASSWORD_TOO_LONG")
		}
		return apierr.FastifyReply(c, http.StatusInternalServerError, "INTERNAL_ERROR")
	}

	// invitation code使用済みにする
	if ticket != nil && h.ticketStore != nil {
		_ = h.ticketStore.MarkUsed(ticket.ID, result.User.ID)
	}

	// アカウント作成も signin 経路を通す (履歴 / login 通知 / main publish、#1804)。
	h.fireSigninSideEffects(c, result.User.ID)
	return c.JSON(http.StatusOK, packSignupResponse(result.User, result.Token, h.idGen))
}

// SignupPending handles POST /api/signup-pending. Misskey TS 互換: code を
// 受け取り、対応する user_pending を本登録に昇格して { id, i: token } を返す。
//
// upstream `SignupApiService.signupPending` は handler 全体を try-catch で
// 囲み catch 節で `throw new FastifyReplyError(400, ...)` を投げるため、
// 既知 error も unknown error も status 400 + Fastify shape で返る。
// mk-go も #809 で status/shape を upstream に揃える (旧 mk-go は
// NO_SUCH_CODE=404 / EXPIRED=410 / INVITATION_ALREADY_USED=409 /
// INVITATION_REVOKED=410 と独自 status を返していて drop-in 互換が崩れて
// いた)。INTERNAL_ERROR (default catch) のみ server-side error として 500
// を維持する (upstream の 400 を再現すると true internal failure が 400 に
// 丸まり sanity check 上望ましくないため意図的 drift)。
func (h *Handler) SignupPending(c echo.Context) error {
	var req struct {
		Code string `json:"code"`
	}
	if err := c.Bind(&req); err != nil || req.Code == "" {
		return apierr.FastifyReply(c, http.StatusBadRequest, "INVALID_PARAM")
	}
	result, err := h.signupService.PromotePending(req.Code)
	if err != nil {
		switch err {
		case coresignup.ErrPendingNotFound:
			return apierr.FastifyReply(c, http.StatusBadRequest, "NO_SUCH_CODE")
		case coresignup.ErrPendingExpired:
			return apierr.FastifyReply(c, http.StatusBadRequest, "EXPIRED")
		case coresignup.ErrUsernameAlreadyExists:
			return duplicatedUsernameError(c)
		case coresignup.ErrInvitationAlreadyUsed:
			return apierr.FastifyReply(c, http.StatusBadRequest, "INVITATION_ALREADY_USED")
		case coresignup.ErrInvitationRevoked:
			// admin が ticket を削除した状態 (#610 item 2)。AlreadyUsed と区別。
			return apierr.FastifyReply(c, http.StatusBadRequest, "INVITATION_REVOKED")
		default:
			return apierr.FastifyReply(c, http.StatusInternalServerError, "INTERNAL_ERROR")
		}
	}
	// 招待コード消費 — Service の tx 経路では tx 内で MarkUsed 済 (#604)。
	// 非 tx 経路 (mock テスト等) のみ handler 側で best-effort consume。
	// Service が consume したかは SignupResult.InvitationTicketConsumed で判別。
	if result.InvitationTicketID != nil && !result.InvitationTicketConsumed && h.ticketStore != nil {
		_ = h.ticketStore.MarkUsed(*result.InvitationTicketID, result.User.ID)
	}
	// signup-pending 完了も signin 経路を通す (履歴 / login 通知 / main publish、#1804)。
	h.fireSigninSideEffects(c, result.User.ID)
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
// emailRequired は emailRequiredForSignup。upstream SignupApiService の usedAt
// gate (#2083) を再現する: email 確認制では確認メール送信から 30 分以内の ticket を
// 弾き (再送防止)、非 email 制では usedAt が立っている ticket を弾く。
func (h *Handler) validateInvitationCode(code string, emailRequired bool) (*model.RegistrationTicket, error) {
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
	if emailRequired {
		// 確認メール送信 (= MarkPending で usedAt セット) から 30 分以内は再送不可。
		if ticket.UsedAt != nil && ticket.UsedAt.Add(30*time.Minute).After(time.Now()) {
			return nil, errInvalidCode
		}
	} else if ticket.UsedAt != nil {
		// 非 email 制では usedAt が立っていれば使用済 (upstream の else if 分岐)。
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
