package signup

import (
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/captcha"
	coresignup "github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/core/signupapplication"
	"github.com/shiroha-a/mk/internal/core/signupform"
	"github.com/shiroha-a/mk/internal/model"
)

// approvalTicketTTL bounds how long the internally minted invite stays usable.
//
// **短くてよい。** 発行するのは登録の直前で、そのまま同じリクエスト内で消費する。
// 長くすると、利用者に渡していない credential が DB に残る時間が伸びるだけ。
const approvalTicketTTL = 5 * time.Minute

// SignupApplications is the applicant-side surface of the state machine.
// 循環依存を避けるため interface で受け取る。実装は
// core/signupapplication.Service。
type SignupApplications interface {
	Apply(answers []signupapplication.Answer) (*model.SignupApplication, string, error)
	ByClaimCode(code string) (*model.SignupApplication, error)
	MarkCompleted(applicationID, userID, ticketID string) error
	// MarkTicket records the ticket minted for an in-flight email-confirmation
	// signup, leaving the application approved (#2571). 置き換えた前の ticket ID
	// を返す (#2576)。
	MarkTicket(applicationID, ticketID string) (string, error)
}

// SetSignupApplications wires the approval-based signup flow (#2569).
// 未配線なら該当 endpoint は 503 を返す。
func (h *Handler) SetSignupApplications(apps SignupApplications) {
	h.applications = apps
}

// applicationView is what the applicant is allowed to see about their own
// application.
//
// **管理者向けの列は出さない。** 誰が審査したか (processedById) は申請者に
// 見せる必要が無いうえ、モデレーターの特定につながる。
func applicationView(a *model.SignupApplication) map[string]any {
	if a == nil {
		return nil
	}
	return map[string]any{
		"status":    a.Status,
		"createdAt": a.CreatedAt,
		"expiresAt": a.ExpiresAt,
	}
}

// approvalReady writes an error response and returns ok=false when the flow is
// unavailable. 通ったときは fetch 済みの meta を返す (呼び出し側が引き直さない
// ようにするため)。
//
// **戻り値に ok を持たせているのは、`c.JSON` が成功時に nil を返すため。**
// エラー応答を書いたことを err の非 nil で表そうとすると、書いた直後に呼び出し
// 側が素通りして処理を続けてしまう。
func (h *Handler) approvalReady(c echo.Context) (*model.Meta, bool, error) {
	if h.applications == nil {
		return nil, false, c.JSON(http.StatusServiceUnavailable,
			apierr.Error("UNAVAILABLE", "Approval-based signup is not enabled.", "7c1c9c2f-1a2b-4c3d-8e5f-6a7b8c9d0e1f"))
	}
	meta, err := h.metaRepo.Fetch()
	if err != nil {
		return nil, false, c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	if !meta.ApprovalRequiredForSignup {
		return nil, false, c.JSON(http.StatusServiceUnavailable,
			apierr.Error("UNAVAILABLE", "Approval-based signup is not enabled.", "7c1c9c2f-1a2b-4c3d-8e5f-6a7b8c9d0e1f"))
	}
	return meta, true, nil
}

// approvalOpen is approvalReady plus the registration switch.
//
// **承認制それ自体がゲートでも、登録の停止は別に効かせる。** 承認制を入れる更新は
// disableRegistration を false に正規化する (normalizeSignupConditions) が、その後に
// 運営者が登録を閉じても承認経路は開いたままだった。荒らし対応で登録を止めたのに
// 止まっていない、という形で表面化する。
//
// 状態の照会 (ApplicationStatus) には掛けない。読むだけで何も作らないので、
// 既に申請した人が結果を見られなくなるほうが害が大きい。
func (h *Handler) approvalOpen(c echo.Context) (*model.Meta, bool, error) {
	meta, ok, err := h.approvalReady(c)
	if !ok {
		return nil, false, err
	}
	if meta.DisableRegistration {
		return nil, false, c.JSON(http.StatusServiceUnavailable,
			apierr.Error("UNAVAILABLE", "Registration is closed.", "7c1c9c2f-1a2b-4c3d-8e5f-6a7b8c9d0e1f"))
	}
	return meta, true, nil
}

// ApplicationApply handles POST /api/signup-application/apply.
//
// **クレームコードを返すのはここだけ。** 保存しているのは hash なので、以後
// サーバー側から平文を出す手段は無い。失くしたら再申請してもらうしかない。
func (h *Handler) ApplicationApply(c echo.Context) error {
	meta, ok, err := h.approvalOpen(c)
	if !ok {
		return err
	}
	var req struct {
		// Answers は現在のフォーム定義と**同じ順序**の値の配列。ラベルは
		// サーバーが定義から埋める — クライアントに送らせると、申請者が審査
		// 画面に偽のラベルを流し込める (#2570)。
		Answers []string `json:"answers"`
		// captcha の応答。**申請フォームは誰でも叩けるので、ここが唯一の
		// 防波堤になる** — 連絡先という自然キーが無くなり、重複申請を DB で
		// 抑止できなくなったため (#2569)。
		HcaptchaResponse    string `json:"hcaptcha-response"`
		RecaptchaResponse   string `json:"g-recaptcha-response"`
		TurnstileResponse   string `json:"turnstile-response"`
		McaptchaResponse    string `json:"m-captcha-response"`
		TestcaptchaResponse string `json:"testcaptcha-response"`
		// FormToken は captcha の実 provider が 1 つも無いときだけ要求する
		// 署名付きトークン (#2806)。signup-application/form-token で発行する。
		FormToken string `json:"formToken"`
	}
	_ = c.Bind(&req)

	if !h.testMode && h.captchaSvc != nil {
		tokens := captcha.CaptchaTokens{
			Hcaptcha:    req.HcaptchaResponse,
			Recaptcha:   req.RecaptchaResponse,
			Turnstile:   req.TurnstileResponse,
			Mcaptcha:    req.McaptchaResponse,
			Testcaptcha: req.TestcaptchaResponse,
		}
		if err := h.captchaSvc.Verify(c.Request().Context(), tokens); err != nil {
			return apierr.FastifyReply(c, http.StatusBadRequest, "CAPTCHA_FAILED")
		}
	}

	answers, err := signupapplication.BuildAnswers(
		signupapplication.ParseForm(meta.SignupApplicationForm), req.Answers)
	if err != nil {
		switch {
		case errors.Is(err, signupapplication.ErrAnswerRequired):
			return c.JSON(http.StatusBadRequest,
				apierr.Error("ANSWER_REQUIRED", err.Error(), "4b5c6d7e-8f9a-4b0c-9d1e-2f3a4b5c6d7e"))
		case errors.Is(err, signupapplication.ErrAnswerTooLong):
			return c.JSON(http.StatusBadRequest,
				apierr.Error("ANSWER_TOO_LONG", err.Error(), "2f3a4b5c-6d7e-4f8a-9b0c-1d2e3f4a5b6c"))
		default:
			// フォーム定義が申請の途中で変わった。**黙って詰め合わせると答えと
			// 設問がずれる**ので、やり直してもらう。
			return c.JSON(http.StatusBadRequest,
				apierr.Error("FORM_CHANGED", "The application form has changed. Reload and try again.", "5c6d7e8f-9a0b-4c1d-8e2f-3a4b5c6d7e8f"))
		}
	}

	// **フォームトークンの検証は行を作る直前に置く (#2806)。** captcha の隣に
	// 置くと、回答の検証で落ちたときにも nonce を焼いてしまい、直して送り直した
	// 利用者が「フォームを開き直せ」と言われる。回答の検証は DB を触らないので、
	// 先に通しても bot に与える余地は増えない。
	if ok, err := h.verifyFormToken(c, req.FormToken); !ok {
		return err
	}

	app, code, err := h.applications.Apply(answers)
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"claimCode":   code,
		"application": applicationView(app),
	})
}

// ApplicationStatus handles POST /api/signup-application/status.
//
// 申請者がコードを持って戻ってくる入口。**これが唯一の導線**なので、コードを
// 失くした場合は再申請しかない。
func (h *Handler) ApplicationStatus(c echo.Context) error {
	app, ok, err := h.applicationForClaimCode(c)
	if !ok {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"application": applicationView(app)})
}

// ApplicationRegister handles POST /api/signup-application/register.
//
// 承認済みの申請に対して、実際にアカウントを作る。**招待コードは利用者に渡さない**
// — ここで発行し、同じ流れで消費する。
func (h *Handler) ApplicationRegister(c echo.Context) error {
	meta, ok, err := h.approvalOpen(c)
	if !ok {
		return err
	}
	var req struct {
		ClaimCode string `json:"claimCode"`
		Username  string `json:"username"`
		Password  string `json:"password"`
		// EmailAddress は emailRequiredForSignup が有効なときだけ要る (#2571)。
		EmailAddress string `json:"emailAddress"`
	}
	if err := c.Bind(&req); err != nil || req.Username == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest,
			apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	// testMode は通常の signup と同じくメール確認を丸ごと迂回する。フロントの
	// test が確認リンクを踏めないため。
	emailRequired := !h.testMode && meta.EmailRequiredForSignup
	if emailRequired {
		if req.EmailAddress == "" {
			return c.JSON(http.StatusBadRequest,
				apierr.Error("INVALID_PARAM", "emailAddress is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
		}
		if verr := validateEmailWithMeta(c.Request().Context(), meta, req.EmailAddress, h.emailValidationClient); verr != nil {
			// `/api/signup` はここで UNAVAILABLE を返すが、この endpoint では
			// UNAVAILABLE が既に「承認制が無効」の意味を持つ。**同じ code で
			// 別のことを言うと、client がどちらか一方の文言しか出せない。**
			return c.JSON(http.StatusBadRequest,
				apierr.Error("EMAIL_UNAVAILABLE", "Email is not available.", "a25440a9-451e-41de-b291-00a8f29fbca6"))
		}
	}

	// **申請の状態はここで引き直す。** 画面が古い状態を握っていても、承認されて
	// いないものが通ることは無い。
	app, err := h.applications.ByClaimCode(req.ClaimCode)
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	if app == nil {
		return c.JSON(http.StatusBadRequest,
			apierr.Error("NO_SUCH_APPLICATION", "No such application.", "1f2c0f7a-9b0e-4d5e-8a3f-4b1f5c210c5b"))
	}
	if app.Status != model.SignupApplicationApproved {
		return c.JSON(http.StatusBadRequest,
			apierr.Error("NOT_APPROVED", "This application is not approved.", "3a4b5c6d-7e8f-4a9b-0c1d-2e3f4a5b6c7d"))
	}

	ticket, err := h.mintApprovalTicket()
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	if emailRequired {
		return h.registerViaEmailConfirmation(c, meta, app, ticket, req.Username, req.EmailAddress, req.Password)
	}

	ticketID := ""
	if ticket != nil {
		ticketID = ticket.ID
	}
	// **申請の確定とアカウント作成を同じ tx に入れる (#2580)。** 分けると、承認済みの
	// 申請者が別々の username で同時に登録したときに両方が「承認済み」を読んで
	// 両方アカウントを作れる。負けた側はユーザー作成ごと巻き戻る。
	result, err := h.signupService.SignupForApplication(req.Username, req.Password, app.ID, ticketID)
	if err != nil {
		// 失敗したチケットは残さない。**残すと、次の試行で「使用済み」に
		// 見えないまま浮いた招待が積み上がる。**
		h.discardApprovalTicket(ticket)
		if errors.Is(err, coresignup.ErrApplicationNotApproved) {
			return c.JSON(http.StatusBadRequest,
				apierr.Error("NOT_APPROVED", "This application is not approved.", "3a4b5c6d-7e8f-4a9b-0c1d-2e3f4a5b6c7d"))
		}
		return h.signupServiceError(c, err)
	}

	if ticket != nil && h.ticketStore != nil {
		if merr := h.ticketStore.MarkUsed(ticket.ID, result.User.ID); merr != nil {
			// 消費の記録に失敗してもアカウントは作られている。ここで 500 を
			// 返すと、利用者には「失敗したのに登録されている」状態になる。
			c.Logger().Warnf("signup application: mark ticket used failed: %v", merr)
		}
	}
	completed := result.SignupApplicationCompleted
	if !completed {
		// db / 申請 repo が未配線の構成 (テスト等)。従来どおり後追いで記録する。
		if cerr := h.applications.MarkCompleted(app.ID, result.User.ID, ticketID); cerr != nil {
			c.Logger().Warnf("signup application: mark completed failed: %v", cerr)
		} else {
			completed = true
		}
	}
	if completed && app.TicketID != nil && *app.TicketID != "" && *app.TicketID != ticketID {
		// メール必須を切る前に始まっていた確認待ちの残骸。**完了が通ってから
		// 破棄する** — 通ったということは、確認が先に走って ticket を消費した
		// わけではない (その場合 MarkCompleted が ErrNotApproved で落ちる)。
		h.discardApprovalTicket(&model.RegistrationTicket{ID: *app.TicketID})
	}

	h.fireSigninSideEffects(c, result.User.ID)
	return c.JSON(http.StatusOK, packSignupResponse(result.User, result.Profile, result.Token, h.idGen, h.signupPolicies(result.User.ID)))
}

// registerViaEmailConfirmation stages the approved signup as a pending row and
// mails the confirmation link (#2571).
//
// **この時点ではまだアカウントが無いので、申請を completed にできない。** pending
// row に申請 ID を持たせ、確認が終わった `/api/signup-pending` 側で completed に
// する。申請は approved のまま残るが、発行した ticket を申請に覚えさせておくので、
// やり直しは前回を破棄してから進む。
func (h *Handler) registerViaEmailConfirmation(
	c echo.Context,
	meta *model.Meta,
	app *model.SignupApplication,
	ticket *model.RegistrationTicket,
	username, email, password string,
) error {
	var ticketID *string
	if ticket != nil {
		ticketID = &ticket.ID
		// **pending を作る前に、行ロック付きで申請の状態を見直す。** ここまでの
		// 状態確認はロック無しの読みで、その後に申請が期限切れになったり別の
		// 確認が完了したりしうる。素通しすると、期限切れの申請から確認メールが
		// 飛び、リンクを踏んだ時点でアカウントが出来上がる (完了記録だけが
		// 失敗して、承認の記録に残らないアカウントになる)。
		previous, merr := h.applications.MarkTicket(app.ID, ticket.ID)
		if merr != nil {
			h.discardApprovalTicket(ticket)
			return c.JSON(http.StatusBadRequest,
				apierr.Error("NOT_APPROVED", "This application is not approved.", "3a4b5c6d-7e8f-4a9b-0c1d-2e3f4a5b6c7d"))
		}
		// **前回の試行で発行した ticket は、置き換えが通ってから破棄する。**
		// 届かなかった / 打ち間違えたときのやり直しで積み上げると、届いた分を
		// すべて確認できてしまう。破棄すれば古いリンクは INVITATION_REVOKED に
		// なり、最新の試行だけが通る。
		//
		// 破棄を先にすると、確認が一足先に通っていた場合に**消費済みの ticket を
		// 消して usedById の記録を失う**。行ロックの中で読んだ値をここで消すので、
		// その場合は上の MarkTicket が ErrNotApproved で落ちて到達しない。
		if previous != "" && previous != ticket.ID {
			h.discardApprovalTicket(&model.RegistrationTicket{ID: previous})
		}
	}
	appID := app.ID
	pending, err := h.signupService.CreatePendingForApplication(username, email, password, ticketID, &appID)
	if err != nil {
		// 使われないまま残った ticket は消す。**残すと、次の試行で「使用済み」に
		// 見えないまま浮いた招待が積み上がる。**
		h.discardApprovalTicket(ticket)
		// 承認制の登録は mk-go 独自の endpoint なので、即時作成の分岐と同じ
		// エラー集合に揃える。`/api/signup` のメール経路は予約 username を
		// DENIED_USERNAME で返す upstream 互換の分岐を持つが (#2080)、ここで
		// 分けると同じ画面の同じ操作で code が変わってしまう。
		return h.signupServiceError(c, err)
	}
	if ticket != nil && h.ticketStore != nil {
		if merr := h.ticketStore.MarkPending(ticket.ID, pending.ID); merr != nil {
			// 再送防止窓が効かなくなるだけで pending は成立している。
			c.Logger().Warnf("signup application: mark ticket pending failed: %v", merr)
		}
	}
	h.sendSignupConfirmation(meta, email, pending.Code)
	// TS の signup と同じく本体は返さない (frontend は確認メールを待つ)。
	return c.NoContent(http.StatusNoContent)
}

// ApplicationFormToken handles POST /api/signup-application/form-token.
//
// captcha の実 provider が 1 つも無いときに `signup-application/apply` を守る
// 署名付きトークンを発行する (#2806)。**captcha の代替ではない** — 位置づけは
// core/signupform の doc を見ること。
//
// 実 provider が有効なときも 200 で返す。**二重の負担は課さない** (apply 側が
// 要求しない) が、endpoint 自体を 404 / 503 にすると、画面がどちらの構成かを
// 別経路で判定しないといけなくなる。
func (h *Handler) ApplicationFormToken(c echo.Context) error {
	if _, ok, err := h.approvalOpen(c); !ok {
		return err
	}
	if !h.formTokenRequired() {
		// 要求しない構成では発行もしない。**空文字を返して 200 にする** —
		// 画面は「トークンが空なら送信を抑えない」だけで済む。
		return c.JSON(http.StatusOK, map[string]any{"token": "", "minWaitSeconds": 0})
	}
	token, err := h.formTokens.Issue(signupform.PurposeApply)
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"token": token,
		// **切り上げる。** 切り捨てにすると、非整数秒にした瞬間に画面のほうが
		// 短く待ち、正規の利用者が FORM_TOKEN_TOO_SOON を見る。
		"minWaitSeconds": int(math.Ceil(h.formTokens.MinWait().Seconds())),
	})
}

// formTokenRequired reports whether apply must carry a signed form token.
//
// **発動条件は「実 provider が 1 つも有効でないとき」で、新しい meta 列は作らない。**
// 既存の meta フラグから両側 (サーバー / 画面) が導出できるので、drop-in の復路で
// fail-open する列が増えない。testcaptcha は実 provider として数えない
// (captcha.Service.HasRealProvider)。
func (h *Handler) formTokenRequired() bool {
	if h.testMode || h.formTokens == nil {
		// testMode は既存 captcha と同じ扱い。**捨てる根拠は「captcha と扱いを
		// 分ける理由が無い」で足りる** — この endpoint を叩く e2e は Playwright にも
		// 本家 backend e2e にも無い (mk-go 独自なので upstream のテストは持たない)。
		// **未配線で素通しになるのは router の recordCriticalWiring が起動時に
		// 止める** (signup.formTokens)。
		return false
	}
	return !h.captchaSvc.HasRealProvider()
}

// verifyFormToken enforces the signed form token when it is required.
//
// **戻り値に ok を持たせるのは、`c.JSON` が成功時に nil を返すため。** error の
// 非 nil だけで「応答を書いた」を表そうとすると、書いた直後に呼び出し側が素通り
// して申請行を作ってしまう (approvalReady と同じ規約に揃えてある)。
func (h *Handler) verifyFormToken(c echo.Context, token string) (bool, error) {
	if !h.formTokenRequired() {
		return true, nil
	}
	err := h.formTokens.Verify(c.Request().Context(), signupform.PurposeApply, token)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, signupform.ErrTokenTooSoon):
		// **nonce は焼かれていない**ので、待ってやり直せば同じトークンで通る。
		return false, c.JSON(http.StatusBadRequest,
			apierr.Error("FORM_TOKEN_TOO_SOON", "The form was submitted too quickly.", "04354c61-0b35-466f-a7b0-83ff9a5f32df"))
	case errors.Is(err, signupform.ErrTokenInvalid),
		errors.Is(err, signupform.ErrTokenExpired),
		errors.Is(err, signupform.ErrTokenUsed):
		// 3 つを 1 つのコードに畳む。**どれだったかを教えても利用者の行動は
		// 同じ (フォームを開き直す) で、区別できると総当たりの手掛かりになる。**
		return false, c.JSON(http.StatusBadRequest,
			apierr.Error("FORM_TOKEN_INVALID", "Reload the form and try again.", "a2262c21-9681-4ec9-8c4b-110c51eb9252"))
	default:
		// Redis 障害などはここに来る。**素通しにしない** — captcha 未設定の
		// インスタンスで防波堤が 1 つも無い状態に戻る。
		return false, c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
}

// applicationForClaimCode resolves the caller's claim code, writing the error
// response and returning ok=false when it cannot.
func (h *Handler) applicationForClaimCode(c echo.Context) (*model.SignupApplication, bool, error) {
	if _, ok, err := h.approvalReady(c); !ok {
		return nil, false, err
	}
	var req struct {
		ClaimCode string `json:"claimCode"`
	}
	if err := c.Bind(&req); err != nil || req.ClaimCode == "" {
		return nil, false, c.JSON(http.StatusBadRequest,
			apierr.Error("INVALID_PARAM", "claimCode is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	app, err := h.applications.ByClaimCode(req.ClaimCode)
	if err != nil {
		return nil, false, c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	if app == nil {
		// **存在しないコードと期限切れのコードを区別しない。** 区別できると、
		// 総当たりで「そのコードは実在する」ことだけ漏れる。
		return nil, false, c.JSON(http.StatusBadRequest,
			apierr.Error("NO_SUCH_APPLICATION", "No such application.", "1f2c0f7a-9b0e-4d5e-8a3f-4b1f5c210c5b"))
	}
	return app, true, nil
}

// mintApprovalTicket issues the short-lived invite consumed by this signup.
//
// **createdById は入れない (#2805)。** 審査した管理者を入れると、承認された申請から
// 登録が行われるたびに 1 枚その管理者名義の招待が増え、`invite/create` と
// `invite/limit` の上限 (`CountByCreatorSince`) を食い、利用者の `invite/list`
// (`ListByCreator`) にも出る。どちらの query も `WHERE "createdById" = ?` なので、
// NULL なら両方から外れる。`inviteLimit` が効いている構成では **承認するほど自分の
// 招待枠が減る**という副作用になる (この policy はロール由来とは限らず、
// `meta.policies` のベースポリシーからも来る)。
//
// **監査は失われない。** 審査した管理者は `signup_application.processedById`、
// 登録者は `signup_application.usedById` で辿れる。**`signup_application` には FK が
// 1 つも無い** ので (migration 000075 の判断)、user を消してもこの 2 つは残る。
// `createdById` はこの記録と重複していただけで、片方が上限カウントという副作用を
// 持っていた。
//
// ticket 行そのものは `createdById` / `usedById` の FK がどちらも
// `ON DELETE CASCADE` なので、**審査した管理者か登録者のどちらかを消すと消える**
// (`signup_application.ticketId` は dangling になる)。`createdById` を入れないと、
// その消え方の引き金が 1 つ減る。
//
// `admin/invite/list` は `createdById` で絞らないので従来どおり出る (`createdBy` は
// null で pack される)。**利用者向けの `invite/delete` は `createdById == nil` を
// 拒否するので、この ticket は API からは消せない** (upstream にあるモデレーターの
// bypass を mk-go は持たない)。詳細は docs/divergence.md。
//
// ticketStore 未配線なら nil を返し、招待を介さずに進む (テスト構成)。
func (h *Handler) mintApprovalTicket() (*model.RegistrationTicket, error) {
	if h.ticketStore == nil {
		return nil, nil
	}
	creator, ok := h.ticketStore.(approvalTicketCreator)
	if !ok {
		return nil, nil
	}
	code, err := signupapplication.NewClaimCode()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	expires := now.Add(approvalTicketTTL)
	ticket := &model.RegistrationTicket{
		ID:        h.idGen.Generate(now),
		Code:      code,
		ExpiresAt: &expires,
		// CreatedByID は入れない。上の doc コメント参照 (#2805)。
	}
	if err := creator.Create(ticket); err != nil {
		return nil, err
	}
	return ticket, nil
}

// discardApprovalTicket best-effort removes a ticket that was never consumed.
func (h *Handler) discardApprovalTicket(ticket *model.RegistrationTicket) {
	if ticket == nil || h.ticketStore == nil {
		return
	}
	if deleter, ok := h.ticketStore.(approvalTicketDeleter); ok {
		_ = deleter.Delete(ticket.ID)
	}
}

// approvalTicketCreator / approvalTicketDeleter are the optional halves of the
// ticket store used by the approval flow.
//
// TicketStore は既存経路 (コード検証と消費) だけを要求する最小の interface な
// ので、発行と取り消しは**満たしていれば使う**形で足す。テストの手書き fake を
// 一斉に壊さないための措置。
type approvalTicketCreator interface {
	Create(t *model.RegistrationTicket) error
}

type approvalTicketDeleter interface {
	Delete(id string) error
}

// signupServiceError maps SignupService failures to the same shapes the normal
// `/api/signup` returns, so a client can handle one set of errors.
func (h *Handler) signupServiceError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, coresignup.ErrUsernameAlreadyExists):
		return duplicatedUsernameError(c)
	case errors.Is(err, coresignup.ErrInvalidUsername):
		return apierr.FastifyReply(c, http.StatusBadRequest, "INVALID_USERNAME")
	case errors.Is(err, coresignup.ErrUsernameUsed), errors.Is(err, coresignup.ErrUsernameReserved):
		return apierr.FastifyReply(c, http.StatusBadRequest, "USED_USERNAME")
	case errors.Is(err, coresignup.ErrPasswordTooLong):
		return apierr.FastifyReply(c, http.StatusBadRequest, "PASSWORD_TOO_LONG")
	default:
		return apierr.FastifyReply(c, http.StatusInternalServerError, "INTERNAL_ERROR")
	}
}
