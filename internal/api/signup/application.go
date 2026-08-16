package signup

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/miauth"
	coresignup "github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/core/signupapplication"
	"github.com/shiroha-a/mk/internal/model"
)

// approvalTicketTTL bounds how long the internally minted invite stays usable.
//
// **短くてよい。** 発行するのは MiAuth を通した直後で、そのまま同じリクエスト
// 内で消費する。長くすると、利用者に渡していない credential が DB に残る時間が
// 伸びるだけ。
const approvalTicketTTL = 5 * time.Minute

// SignupApplications is the applicant-side surface of the state machine.
// 循環依存を避けるため interface で受け取る。実装は
// core/signupapplication.Service。
type SignupApplications interface {
	Apply(contact signupapplication.Contact, reason string) (*model.SignupApplication, error)
	Current(contact signupapplication.Contact) (*model.SignupApplication, error)
	Latest(contact signupapplication.Contact) (*model.SignupApplication, error)
	MarkCompleted(applicationID, userID, ticketID string) error
}

// SetSignupApplications wires the approval-based signup flow (#2556).
// 未配線なら該当 endpoint は 503 を返す。
// publicURL はコールバックの組み立てに使うインスタンスの公開 URL。
// **email 送信用の serverURL を流用しない。** あちらは SetEmailSender でしか
// 設定されないため、メール周りの配線を変えると気づかないままコールバックが
// 空になり、承認フローだけが黙って戻れなくなる。
func (h *Handler) SetSignupApplications(
	apps SignupApplications,
	client *miauth.Client,
	sessions *miauth.SessionStore,
	publicURL string,
) {
	h.applications = apps
	h.miauth = client
	h.miauthSessions = sessions
	h.publicURL = publicURL
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
		"reason":    a.Reason,
		"createdAt": a.CreatedAt,
		"expiresAt": a.ExpiresAt,
	}
}

// contactView is the verified identity echoed back so the page can show
// "you are signed in as @alice@remote.example".
func contactView(c *miauth.Contact) map[string]any {
	return map[string]any{
		"host":      c.Host,
		"username":  c.Username,
		"acct":      "@" + c.Username + "@" + c.Host,
		"name":      c.Name,
		"avatarUrl": c.AvatarURL,
	}
}

// approvalEnabled reports whether the approval flow is configured and turned on.
func (h *Handler) approvalEnabled() (bool, error) {
	if h.applications == nil || h.miauth == nil || h.miauthSessions == nil {
		return false, nil
	}
	meta, err := h.metaRepo.Fetch()
	if err != nil {
		return false, err
	}
	return meta.ApprovalRequiredForSignup, nil
}

// approvalReady writes an error response and returns ok=false when the flow is
// unavailable.
//
// **戻り値を (ok, err) にしているのは、`c.JSON` が成功時に nil を返すため。**
// エラー応答を書いたことを err の非 nil で表そうとすると、書いた直後に呼び出し
// 側が素通りして処理を続けてしまう。
func (h *Handler) approvalReady(c echo.Context) (bool, error) {
	enabled, err := h.approvalEnabled()
	if err != nil {
		return false, c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	if !enabled {
		return false, c.JSON(http.StatusServiceUnavailable,
			apierr.Error("UNAVAILABLE", "Approval-based signup is not enabled.", "7c1c9c2f-1a2b-4c3d-8e5f-6a7b8c9d0e1f"))
	}
	return true, nil
}

// ApplicationMiAuthStart handles POST /api/signup-application/miauth/start.
//
// host を受けて MiAuth の認可 URL を返す。**リダイレクト先として使う前に、
// 相手が Misskey 系であることを確かめる** — これが無いと任意のホストへ利用者を
// 飛ばすオープンリダイレクタになる。
func (h *Handler) ApplicationMiAuthStart(c echo.Context) error {
	if ok, err := h.approvalReady(c); !ok {
		return err
	}
	var req struct {
		Host string `json:"host"`
	}
	if err := c.Bind(&req); err != nil || req.Host == "" {
		return c.JSON(http.StatusBadRequest,
			apierr.Error("INVALID_PARAM", "host is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	host := normalizeContactHost(req.Host)
	if err := miauth.ValidateHost(host); err != nil {
		return c.JSON(http.StatusBadRequest,
			apierr.Error("INVALID_HOST", "That is not a valid host.", "5e6f7a8b-9c0d-4e1f-a2b3-c4d5e6f7a8b9"))
	}

	ctx := c.Request().Context()
	if err := h.miauth.Probe(ctx, host); err != nil {
		// MiAuth を持つのは Misskey 系だけ。認証が失敗する前に案内する。
		return c.JSON(http.StatusBadRequest,
			apierr.Error("NOT_MISSKEY_HOST", "That host does not look like a Misskey server.", "6f7a8b9c-0d1e-4f2a-b3c4-d5e6f7a8b9c0"))
	}

	session, err := miauth.NewSessionID()
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	url, err := miauth.AuthorizeURL(host, session, h.instanceDisplayName(), h.applicationCallbackURL())
	if err != nil {
		return c.JSON(http.StatusBadRequest,
			apierr.Error("INVALID_HOST", "That is not a valid host.", "5e6f7a8b-9c0d-4e1f-a2b3-c4d5e6f7a8b9"))
	}
	token, err := h.miauthSessions.StartPending(ctx, host, session)
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, map[string]any{"token": token, "url": url})
}

// ApplicationMiAuthComplete handles POST /api/signup-application/miauth/complete.
//
// **コールバックの `session` は受け取らない。** どのフローかは呼び出し側が持つ
// トークンで決まる。URL 経由の値を信じると、攻撃者が開始したフローを被害者に
// 承認させ、攻撃者のブラウザで踏む筋が残る。
func (h *Handler) ApplicationMiAuthComplete(c echo.Context) error {
	if ok, err := h.approvalReady(c); !ok {
		return err
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := c.Bind(&req); err != nil || req.Token == "" {
		return c.JSON(http.StatusBadRequest,
			apierr.Error("INVALID_PARAM", "token is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	ctx := c.Request().Context()
	pending, err := h.miauthSessions.TakePending(ctx, req.Token)
	if err != nil {
		if errors.Is(err, miauth.ErrSessionNotFound) {
			return c.JSON(http.StatusBadRequest,
				apierr.Error("SESSION_EXPIRED", "The authentication session has expired. Start again.", "8b9c0d1e-2f3a-4b5c-9d6e-7f8a9b0c1d2e"))
		}
		return c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// **認証先は申請フローの記録から引く。** リクエスト側に選ばせると、攻撃者が
	// 自分のアカウントで認証して他人のフローを乗っ取れる。
	contact, err := h.miauth.Check(ctx, pending.Host, pending.Session)
	if err != nil {
		switch {
		case errors.Is(err, miauth.ErrNotAuthorized):
			return c.JSON(http.StatusBadRequest,
				apierr.Error("NOT_AUTHORIZED", "The authentication was not completed.", "9c0d1e2f-3a4b-4c5d-8e6f-7a8b9c0d1e2f"))
		case errors.Is(err, miauth.ErrNotLocalToHost):
			return c.JSON(http.StatusBadRequest,
				apierr.Error("NOT_LOCAL_ACCOUNT", "That account does not belong to the host you chose.", "0d1e2f3a-4b5c-4d6e-9f7a-8b9c0d1e2f3a"))
		default:
			return c.JSON(http.StatusBadRequest,
				apierr.Error("NOT_MISSKEY_HOST", "That host did not answer as a Misskey server.", "6f7a8b9c-0d1e-4f2a-b3c4-d5e6f7a8b9c0"))
		}
	}

	verified, err := h.miauthSessions.SaveVerified(ctx, contact)
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	app, err := h.currentOrLatest(toServiceContact(contact))
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"token":       verified,
		"contact":     contactView(contact),
		"application": applicationView(app),
	})
}

// ApplicationStatus handles POST /api/signup-application/status.
//
// 検証済みトークンで申請の状態を引き直す。**登録ページに戻ってきた人が続きから
// 進めるための入口**で、DM が届かなくても詰まないのはこれがあるため。
func (h *Handler) ApplicationStatus(c echo.Context) error {
	if ok, err := h.approvalReady(c); !ok {
		return err
	}
	var req struct {
		Token string `json:"token"`
	}
	_ = c.Bind(&req)
	contact, ok, err := h.contactFor(c, req.Token)
	if !ok {
		return err
	}
	app, err := h.currentOrLatest(toServiceContact(contact))
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"contact":     contactView(contact),
		"application": applicationView(app),
	})
}

// ApplicationApply handles POST /api/signup-application/apply.
func (h *Handler) ApplicationApply(c echo.Context) error {
	if ok, err := h.approvalReady(c); !ok {
		return err
	}
	var req struct {
		Token  string `json:"token"`
		Reason string `json:"reason"`
	}
	_ = c.Bind(&req)
	contact, ok, err := h.contactFor(c, req.Token)
	if !ok {
		return err
	}

	app, err := h.applications.Apply(toServiceContact(contact), req.Reason)
	if err != nil {
		switch {
		case errors.Is(err, signupapplication.ErrLiveApplicationExists):
			return c.JSON(http.StatusBadRequest,
				apierr.Error("ALREADY_APPLIED", "There is already an application for this account.", "1e2f3a4b-5c6d-4e7f-8a9b-0c1d2e3f4a5b"))
		case errors.Is(err, signupapplication.ErrReasonTooLong):
			return c.JSON(http.StatusBadRequest,
				apierr.Error("REASON_TOO_LONG", "The reason is too long.", "2f3a4b5c-6d7e-4f8a-9b0c-1d2e3f4a5b6c"))
		case errors.Is(err, signupapplication.ErrInvalidContact):
			return c.JSON(http.StatusBadRequest,
				apierr.Error("INVALID_PARAM", "Invalid contact.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
		default:
			return c.JSON(http.StatusInternalServerError,
				apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"application": applicationView(app)})
}

// currentOrLatest returns the live application, falling back to the most recent
// terminal one so the applicant can see that they were rejected / expired.
func (h *Handler) currentOrLatest(contact signupapplication.Contact) (*model.SignupApplication, error) {
	app, err := h.applications.Current(contact)
	if err != nil {
		return nil, err
	}
	if app != nil {
		return app, nil
	}
	return h.applications.Latest(contact)
}

// contactFor resolves a verified MiAuth token, writing the error response and
// returning ok=false when it cannot.
//
// **トークンは呼び出し側が bind 済みのものを渡す。** ここで再度 c.Bind すると
// 本文を二度読むことになり、handler 側の bind が空になる (register で username /
// password が消えて INVALID_PARAM になった)。
func (h *Handler) contactFor(c echo.Context, token string) (*miauth.Contact, bool, error) {
	if token == "" {
		return nil, false, c.JSON(http.StatusBadRequest,
			apierr.Error("INVALID_PARAM", "token is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	contact, err := h.miauthSessions.Verified(c.Request().Context(), token)
	if err != nil {
		if errors.Is(err, miauth.ErrSessionNotFound) {
			return nil, false, c.JSON(http.StatusBadRequest,
				apierr.Error("SESSION_EXPIRED", "The authentication session has expired. Start again.", "8b9c0d1e-2f3a-4b5c-9d6e-7f8a9b0c1d2e"))
		}
		return nil, false, c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return contact, true, nil
}

func toServiceContact(c *miauth.Contact) signupapplication.Contact {
	return signupapplication.Contact{Host: c.Host, RemoteID: c.RemoteID, Username: c.Username}
}

// normalizeContactHost accepts what a person is likely to type and reduces it
// to a bare host.
//
// `@name@host` / `host` / `https://host/` のいずれで来ても同じ host に落とす。
// **ここで揃えておかないと、表記違いが (host, remoteID) の一致判定に効いて
// 「申請したのに見つからない」になる。**
func normalizeContactHost(input string) string {
	s := strings.TrimSpace(input)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	// `@name@host` は最後の `@` の後ろが host。
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	// 末尾のパスやスラッシュを落とす。
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// instanceDisplayName is the name shown on the remote consent screen.
// 相手には「どこからの要求か」がこれでしか分からないので、インスタンス名を出す。
func (h *Handler) instanceDisplayName() string {
	if m, err := h.metaRepo.Fetch(); err == nil && m != nil && m.Name != nil && *m.Name != "" {
		return *m.Name
	}
	return h.publicURL
}

// applicationCallbackURL is where the remote sends the applicant back to.
func (h *Handler) applicationCallbackURL() string {
	if h.publicURL == "" {
		return ""
	}
	return strings.TrimSuffix(h.publicURL, "/") + "/signup-application/callback"
}

// ApplicationRegister handles POST /api/signup-application/register.
//
// 承認済みの申請に対して、実際にアカウントを作る。**招待コードは利用者に渡さない**
// — ここで発行し、同じ流れで消費する。DM に載せて 7 日間置くと、渡していない
// bearer 相当の credential が DB に残るだけで得るものが無い (#2554)。
func (h *Handler) ApplicationRegister(c echo.Context) error {
	if ok, err := h.approvalReady(c); !ok {
		return err
	}
	var req struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil || req.Username == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest,
			apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	contact, ok, err := h.contactFor(c, req.Token)
	if !ok {
		return err
	}

	// **申請の状態はここで引き直す。** 画面が古い状態を握っていても、承認されて
	// いないものが通ることは無い。
	app, err := h.applications.Current(toServiceContact(contact))
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	if app == nil || app.Status != model.SignupApplicationApproved {
		return c.JSON(http.StatusBadRequest,
			apierr.Error("NOT_APPROVED", "This application is not approved.", "3a4b5c6d-7e8f-4a9b-0c1d-2e3f4a5b6c7d"))
	}

	ticket, err := h.mintApprovalTicket(app)
	if err != nil {
		return c.JSON(http.StatusInternalServerError,
			apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	result, err := h.signupService.Signup(req.Username, req.Password, false)
	if err != nil {
		// 失敗したチケットは残さない。**残すと、次の試行で「使用済み」に
		// 見えないまま浮いた招待が積み上がる。**
		h.discardApprovalTicket(ticket)
		return h.signupServiceError(c, err)
	}

	if ticket != nil && h.ticketStore != nil {
		if merr := h.ticketStore.MarkUsed(ticket.ID, result.User.ID); merr != nil {
			// 消費の記録に失敗してもアカウントは作られている。ここで 500 を
			// 返すと、利用者には「失敗したのに登録されている」状態になる。
			c.Logger().Warnf("signup application: mark ticket used failed: %v", merr)
		}
	}
	ticketID := ""
	if ticket != nil {
		ticketID = ticket.ID
	}
	if cerr := h.applications.MarkCompleted(app.ID, result.User.ID, ticketID); cerr != nil {
		// 同上。申請の完了記録は監査用で、アカウントの成立とは独立。
		c.Logger().Warnf("signup application: mark completed failed: %v", cerr)
	}
	// 登録が済んだら検証済みトークンは用済み。**残すと、同じトークンで再度
	// 登録画面に入れる。**
	_ = h.miauthSessions.DropVerified(c.Request().Context(), req.Token)

	h.fireSigninSideEffects(c, result.User.ID)
	return c.JSON(http.StatusOK, packSignupResponse(result.User, result.Profile, result.Token, h.idGen))
}

// mintApprovalTicket issues the short-lived invite consumed by this signup.
//
// createdById には審査した管理者を入れる。招待一覧から「誰の承認で作られたか」を
// 辿れるようにするため。ticketStore 未配線なら nil を返し、招待を介さずに進む
// (テスト構成)。
func (h *Handler) mintApprovalTicket(app *model.SignupApplication) (*model.RegistrationTicket, error) {
	if h.ticketStore == nil {
		return nil, nil
	}
	creator, ok := h.ticketStore.(approvalTicketCreator)
	if !ok {
		return nil, nil
	}
	code, err := miauth.NewSessionID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	expires := now.Add(approvalTicketTTL)
	ticket := &model.RegistrationTicket{
		ID:          h.idGen.Generate(now),
		Code:        code,
		ExpiresAt:   &expires,
		CreatedByID: app.ProcessedByID,
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
