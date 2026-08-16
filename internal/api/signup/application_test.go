package signup_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	apisignup "github.com/shiroha-a/mk/internal/api/signup"
	coresignup "github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/core/signupapplication"
	"github.com/shiroha-a/mk/internal/misc/id"
	miscsmtp "github.com/shiroha-a/mk/internal/misc/smtp"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// stubApplications is an in-memory stand-in for the state machine.
type stubApplications struct {
	app       *model.SignupApplication
	code      string
	applyErr  error
	lookupErr error
	markErr   error

	appliedAnswers []signupapplication.Answer
	completedApp   string
	completedUsr   string
	completedTkt   string
	lastCode       string
	markedTktApp   string
	markedTkt      string
	markTicketErr  error
}

func (s *stubApplications) Apply(answers []signupapplication.Answer) (*model.SignupApplication, string, error) {
	s.appliedAnswers = answers
	if s.applyErr != nil {
		return nil, "", s.applyErr
	}
	return &model.SignupApplication{ID: "app-1", Status: model.SignupApplicationPending}, "claim-code-1", nil
}

func (s *stubApplications) ByClaimCode(code string) (*model.SignupApplication, error) {
	s.lastCode = code
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	if s.code != "" && code != s.code {
		return nil, nil
	}
	return s.app, nil
}

func (s *stubApplications) MarkCompleted(applicationID, userID, ticketID string) error {
	s.completedApp, s.completedUsr, s.completedTkt = applicationID, userID, ticketID
	return s.markErr
}

func (s *stubApplications) MarkTicket(applicationID, ticketID string) error {
	s.markedTktApp, s.markedTkt = applicationID, ticketID
	return s.markTicketErr
}

// stubTicketStore satisfies TicketStore plus the optional issue / discard halves.
type stubTicketStore struct {
	created   []*model.RegistrationTicket
	deleted   []string
	usedTkt   string
	usedUsr   string
	createErr error
	markErr   error

	pendingTkt string
	pendingRow string
}

func (s *stubTicketStore) FindByCode(string) (*model.RegistrationTicket, error) {
	return nil, errors.New("not used")
}

func (s *stubTicketStore) MarkUsed(ticketID, userID string) error {
	s.usedTkt, s.usedUsr = ticketID, userID
	return s.markErr
}

func (s *stubTicketStore) MarkPending(ticketID, pendingID string) error {
	s.pendingTkt, s.pendingRow = ticketID, pendingID
	return nil
}

func (s *stubTicketStore) Create(t *model.RegistrationTicket) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.created = append(s.created, t)
	return nil
}

func (s *stubTicketStore) Delete(id string) error {
	s.deleted = append(s.deleted, id)
	return nil
}

type approvalEnv struct {
	handler *apisignup.Handler
	apps    *stubApplications
	meta    *model.Meta
}

func newApprovalEnv(t *testing.T, enabled bool) *approvalEnv {
	t.Helper()
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta = &model.Meta{
		ID: "x", ApprovalRequiredForSignup: enabled,
		SignupApplicationForm: datatypes.JSON(`[{"label":"参加の動機","type":"textarea","required":true}]`),
	}
	apps := &stubApplications{}
	h.SetSignupApplications(apps)
	return &approvalEnv{handler: h, apps: apps, meta: metaRepo.Meta}
}

func approvedApplication() *model.SignupApplication {
	return &model.SignupApplication{ID: "app-1", Status: model.SignupApplicationApproved}
}

// 承認制が無効なら、どの endpoint も 503。**500 にすると、機能を使っていない
// インスタンスで壊れているように見える。**
func TestApplication_DisabledReturns503(t *testing.T) {
	env := newApprovalEnv(t, false)

	assert.Equal(t, http.StatusServiceUnavailable,
		doPost(env.handler.ApplicationApply, `{}`).Code)
	assert.Equal(t, http.StatusServiceUnavailable,
		doPost(env.handler.ApplicationStatus, `{"claimCode":"x"}`).Code)
	assert.Equal(t, http.StatusServiceUnavailable,
		doPost(env.handler.ApplicationRegister, `{"claimCode":"x","username":"a","password":"b"}`).Code)
}

func TestApplication_NotWiredReturns503(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta = &model.Meta{ID: "x", ApprovalRequiredForSignup: true}

	assert.Equal(t, http.StatusServiceUnavailable, doPost(h.ApplicationApply, `{}`).Code)
}

// **クレームコードを返すのはここだけ。** 保存しているのは hash なので、以後
// サーバー側から平文を出す手段は無い。
func TestApplicationApply(t *testing.T) {
	env := newApprovalEnv(t, true)

	rec := doPost(env.handler.ApplicationApply, `{"answers":["よろしく"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := parseResp(t, rec)

	assert.Equal(t, "claim-code-1", resp["claimCode"])
	// **ラベルはサーバーが定義から埋める。** クライアントに送らせると、申請者が
	// 審査画面に偽のラベルを流し込める (#2570)。
	require.Len(t, env.apps.appliedAnswers, 1)
	assert.Equal(t, "参加の動機", env.apps.appliedAnswers[0].Label)
	assert.Equal(t, "よろしく", env.apps.appliedAnswers[0].Value)

	app := resp["application"].(map[string]any)
	assert.Equal(t, "pending", app["status"])
	// **管理者向けの列は出さない。**
	assert.NotContains(t, app, "processedById")
	assert.NotContains(t, app, "id")
}

func TestApplicationApply_Errors(t *testing.T) {
	t.Run("required answer missing", func(t *testing.T) {
		env := newApprovalEnv(t, true)
		rec := doPost(env.handler.ApplicationApply, `{"answers":["  "]}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "ANSWER_REQUIRED")
	})

	t.Run("answer too long", func(t *testing.T) {
		env := newApprovalEnv(t, true)
		env.meta.SignupApplicationForm = datatypes.JSON(`[{"label":"x","type":"text","maxLength":3}]`)
		rec := doPost(env.handler.ApplicationApply, `{"answers":["toolong"]}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "ANSWER_TOO_LONG")
	})

	// フォーム定義が申請の途中で変わった。**黙って詰め合わせると答えと設問が
	// ずれる**ので、やり直してもらう。
	t.Run("form changed under the applicant", func(t *testing.T) {
		env := newApprovalEnv(t, true)
		rec := doPost(env.handler.ApplicationApply, `{"answers":["a","b"]}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "FORM_CHANGED")
	})

	t.Run("unexpected", func(t *testing.T) {
		env := newApprovalEnv(t, true)
		env.apps.applyErr = errors.New("boom")
		rec := doPost(env.handler.ApplicationApply, `{"answers":["x"]}`)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

// 申請者がコードを持って戻ってくる入口。**これが唯一の導線。**
func TestApplicationStatus(t *testing.T) {
	env := newApprovalEnv(t, true)
	env.apps.app = &model.SignupApplication{
		ID: "app-1", Status: model.SignupApplicationApproved,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}

	rec := doPost(env.handler.ApplicationStatus, `{"claimCode":"code-1"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "code-1", env.apps.lastCode)

	app := parseResp(t, rec)["application"].(map[string]any)
	assert.Equal(t, "approved", app["status"])
}

func TestApplicationStatus_Errors(t *testing.T) {
	t.Run("missing code", func(t *testing.T) {
		env := newApprovalEnv(t, true)
		rec := doPost(env.handler.ApplicationStatus, `{}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "INVALID_PARAM")
	})

	// **存在しないコードと期限切れのコードを区別しない。** 区別できると、
	// 総当たりで「そのコードは実在する」ことだけ漏れる。
	t.Run("unknown code", func(t *testing.T) {
		env := newApprovalEnv(t, true)
		rec := doPost(env.handler.ApplicationStatus, `{"claimCode":"nope"}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "NO_SUCH_APPLICATION")
	})

	t.Run("lookup failure", func(t *testing.T) {
		env := newApprovalEnv(t, true)
		env.apps.lookupErr = errors.New("boom")
		rec := doPost(env.handler.ApplicationStatus, `{"claimCode":"x"}`)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

// **申請の状態はサーバー側で引き直す。** 画面が古い状態を握っていても、承認
// されていないものが通ることは無い。
func TestApplicationRegister_RequiresApproval(t *testing.T) {
	for _, status := range []string{
		model.SignupApplicationPending,
		model.SignupApplicationRejected,
		model.SignupApplicationExpired,
		model.SignupApplicationCompleted,
	} {
		t.Run(status, func(t *testing.T) {
			env := newApprovalEnv(t, true)
			env.apps.app = &model.SignupApplication{ID: "app-1", Status: status}

			rec := doPost(env.handler.ApplicationRegister,
				`{"claimCode":"c","username":"newbie","password":"hunter22"}`)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "NOT_APPROVED")
		})
	}
}

func TestApplicationRegister_Success(t *testing.T) {
	env := newApprovalEnv(t, true)
	env.apps.app = approvedApplication()

	rec := doPost(env.handler.ApplicationRegister,
		`{"claimCode":"c","username":"newbie","password":"hunter22"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := parseResp(t, rec)
	assert.Equal(t, "newbie", resp["username"])
	assert.NotEmpty(t, resp["token"])

	assert.Equal(t, "app-1", env.apps.completedApp)
	assert.NotEmpty(t, env.apps.completedUsr)
}

func TestApplicationRegister_InvalidParams(t *testing.T) {
	env := newApprovalEnv(t, true)
	rec := doPost(env.handler.ApplicationRegister, `{"claimCode":"c"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "INVALID_PARAM")
}

func TestApplicationRegister_UnknownCode(t *testing.T) {
	env := newApprovalEnv(t, true)
	env.apps.code = "right"

	rec := doPost(env.handler.ApplicationRegister,
		`{"claimCode":"wrong","username":"newbie","password":"hunter22"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_APPLICATION")
}

func TestApplicationRegister_LookupFailure(t *testing.T) {
	env := newApprovalEnv(t, true)
	env.apps.lookupErr = errors.New("boom")

	rec := doPost(env.handler.ApplicationRegister,
		`{"claimCode":"c","username":"newbie","password":"hunter22"}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// **招待コードは利用者に渡さない。** ここで発行し、同じ流れで消費する。
func TestApplicationRegister_MintsAndConsumesTicket(t *testing.T) {
	env := newApprovalEnv(t, true)
	tickets := &stubTicketStore{}
	env.handler.SetTicketStore(tickets)
	moderator := "mod-1"
	app := approvedApplication()
	app.ProcessedByID = &moderator
	env.apps.app = app

	rec := doPost(env.handler.ApplicationRegister,
		`{"claimCode":"c","username":"newbie","password":"hunter22"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Len(t, tickets.created, 1)
	issued := tickets.created[0]
	assert.NotEmpty(t, issued.Code)
	require.NotNil(t, issued.ExpiresAt, "期限を切ること")
	assert.True(t, issued.ExpiresAt.Before(time.Now().Add(time.Hour)),
		"渡していない credential を長く残さない")
	// 招待一覧から「誰の承認で作られたか」を辿れるようにする。
	require.NotNil(t, issued.CreatedByID)
	assert.Equal(t, moderator, *issued.CreatedByID)

	assert.Equal(t, issued.ID, tickets.usedTkt, "同じ流れで消費すること")
	assert.Equal(t, issued.ID, env.apps.completedTkt)
	assert.Empty(t, tickets.deleted)
	// レスポンスにコードが漏れていないこと。
	assert.NotContains(t, rec.Body.String(), issued.Code)
}

// **登録に失敗したチケットは残さない。** 残すと、使用済みに見えないまま浮いた
// 招待が積み上がる。
func TestApplicationRegister_DiscardsTicketOnFailure(t *testing.T) {
	env := newApprovalEnv(t, true)
	tickets := &stubTicketStore{}
	env.handler.SetTicketStore(tickets)
	env.apps.app = approvedApplication()

	rec := doPost(env.handler.ApplicationRegister,
		`{"claimCode":"c","username":"!!!invalid!!!","password":"hunter22"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	require.Len(t, tickets.created, 1)
	assert.Equal(t, []string{tickets.created[0].ID}, tickets.deleted)
	assert.Empty(t, env.apps.completedApp, "失敗したら申請は完了扱いにしない")
}

func TestApplicationRegister_TicketIssueFailure(t *testing.T) {
	env := newApprovalEnv(t, true)
	env.handler.SetTicketStore(&stubTicketStore{createErr: errors.New("boom")})
	env.apps.app = approvedApplication()

	rec := doPost(env.handler.ApplicationRegister,
		`{"claimCode":"c","username":"newbie","password":"hunter22"}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// 消費や完了記録に失敗してもアカウントは作られている。**ここで 500 を返すと、
// 利用者には「失敗したのに登録されている」状態になる。**
func TestApplicationRegister_BookkeepingFailuresDoNotFailSignup(t *testing.T) {
	env := newApprovalEnv(t, true)
	env.handler.SetTicketStore(&stubTicketStore{markErr: errors.New("boom")})
	env.apps.app = approvedApplication()
	env.apps.markErr = errors.New("boom")

	rec := doPost(env.handler.ApplicationRegister,
		`{"claimCode":"c","username":"newbie","password":"hunter22"}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// 通常の /api/signup と同じ形のエラーを返すこと。**クライアントが 1 組の
// エラー処理で済むように。**
func TestApplicationRegister_SignupErrorShapes(t *testing.T) {
	tests := []struct {
		name     string
		username string
		password string
		wantBody string
	}{
		{name: "invalid username", username: "!!!", password: "hunter22", wantBody: "INVALID_USERNAME"},
		{name: "password too long", username: "newbie", password: string(make([]byte, 2000)), wantBody: "PASSWORD_TOO_LONG"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newApprovalEnv(t, true)
			env.apps.app = approvedApplication()

			body, err := json.Marshal(map[string]string{
				"claimCode": "c", "username": tt.username, "password": tt.password,
			})
			require.NoError(t, err)
			rec := doPost(env.handler.ApplicationRegister, string(body))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantBody)
		})
	}
}

func TestApplicationRegister_DuplicateUsername(t *testing.T) {
	env := newApprovalEnv(t, true)
	env.apps.app = approvedApplication()

	body := `{"claimCode":"c","username":"newbie","password":"hunter22"}`
	require.Equal(t, http.StatusOK, doPost(env.handler.ApplicationRegister, body).Code)

	rec := doPost(env.handler.ApplicationRegister, body)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// 承認制のときは通常の登録経路を閉じる (#2557)。承認フローは signupService を
// 直接呼ぶのでこのゲートを通らない。
func TestSignup_BlockedWhileApprovalRequired(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta = &model.Meta{ID: "x", ApprovalRequiredForSignup: true}

	rec := doPost(h.Signup, `{"username":"newbie","password":"hunter22"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "APPROVAL_REQUIRED")
}

func TestSignup_AllowedWhenApprovalIsOff(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta = &model.Meta{ID: "x", ApprovalRequiredForSignup: false}

	rec := doPost(h.Signup, `{"username":"newbie","password":"hunter22"}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// --- メール必須との併用 (#2571) ---

// newApprovalEnvWithEmail wires the pending repo and the mail sender so the
// approval flow can go through email confirmation.
func newApprovalEnvWithEmail(t *testing.T) (*approvalEnv, *testutil.MockUserPendingRepository, *stubTicketStore, chan string) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{
		ID: "x", ApprovalRequiredForSignup: true, EmailRequiredForSignup: true,
	}
	pendingRepo := testutil.NewMockUserPendingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coresignup.NewService(userRepo, metaRepo, idGen)
	svc.SetUserPendingRepo(pendingRepo)
	h := apisignup.NewHandler(svc, metaRepo, idGen)

	apps := &stubApplications{app: approvedApplication()}
	h.SetSignupApplications(apps)
	tickets := &stubTicketStore{}
	h.SetTicketStore(tickets)

	// 送信は goroutine なので、宛先を channel で受ける。
	sent := make(chan string, 1)
	h.SetEmailSender("https://example.test", func(to string, _ miscsmtp.Message) {
		sent <- to
	})
	return &approvalEnv{handler: h, apps: apps, meta: metaRepo.Meta}, pendingRepo, tickets, sent
}

// メール必須のときは即時作成せず、確認メールの経路に乗せる。**乗せないと、設定
// しているのに実際にはメールを要求しない状態になる。**
func TestApplicationRegister_EmailRequired_CreatesPendingAndSendsEmail(t *testing.T) {
	env, pendingRepo, tickets, sent := newApprovalEnvWithEmail(t)

	rec := doPost(env.handler.ApplicationRegister,
		`{"claimCode":"c","username":"newbie","password":"hunter22","emailAddress":"newbie@example.com"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	require.Len(t, pendingRepo.Rows, 1)
	row := onlyPending(t, pendingRepo)
	assert.Equal(t, "newbie", row.Username)
	assert.Equal(t, "newbie@example.com", row.Email)

	// **申請 ID を持たせないと、確認完了時に申請を completed にできない。**
	require.NotNil(t, row.SignupApplicationID)
	assert.Equal(t, "app-1", *row.SignupApplicationID)

	// 発行した ticket は pending に結び付き、まだ消費されていない。
	require.Len(t, tickets.created, 1)
	issued := tickets.created[0]
	require.NotNil(t, row.InvitationTicketID)
	assert.Equal(t, issued.ID, *row.InvitationTicketID)
	assert.Equal(t, issued.ID, tickets.pendingTkt)
	assert.Equal(t, row.ID, tickets.pendingRow)
	assert.Empty(t, tickets.usedTkt, "確認が終わるまで消費しない")

	// 次の試行で破棄できるよう、申請が ticket を覚えている。
	assert.Equal(t, "app-1", env.apps.markedTktApp)
	assert.Equal(t, issued.ID, env.apps.markedTkt)

	// この時点ではまだアカウントが無いので完了扱いにしない。
	assert.Empty(t, env.apps.completedApp)

	select {
	case to := <-sent:
		assert.Equal(t, "newbie@example.com", to)
	case <-time.After(2 * time.Second):
		t.Fatal("確認メールが送られていない")
	}
}

func TestApplicationRegister_EmailRequired_RejectsMissingOrBadAddress(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		env, pendingRepo, _, _ := newApprovalEnvWithEmail(t)
		rec := doPost(env.handler.ApplicationRegister,
			`{"claimCode":"c","username":"newbie","password":"hunter22"}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Empty(t, pendingRepo.Rows)
	})

	t.Run("banned domain", func(t *testing.T) {
		env, pendingRepo, tickets, _ := newApprovalEnvWithEmail(t)
		env.meta.BannedEmailDomains = []string{"bad.example"}
		rec := doPost(env.handler.ApplicationRegister,
			`{"claimCode":"c","username":"newbie","password":"hunter22","emailAddress":"x@bad.example"}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		// **UNAVAILABLE は使わない。** この endpoint では既に「承認制が無効」の
		// 意味を持っており、同じ code だと client がどちらかしか出せない。
		assert.Contains(t, rec.Body.String(), "EMAIL_UNAVAILABLE")
		assert.Empty(t, pendingRepo.Rows)
		// **アドレスを見る前に ticket を発行しない。** 弾いた分だけ浮いた招待が残る。
		assert.Empty(t, tickets.created)
	})
}

// メール必須が無効なら従来どおり即時作成する。
func TestApplicationRegister_EmailNotRequired_CreatesImmediately(t *testing.T) {
	env := newApprovalEnv(t, true)
	env.apps.app = approvedApplication()

	rec := doPost(env.handler.ApplicationRegister,
		`{"claimCode":"c","username":"newbie","password":"hunter22"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "app-1", env.apps.completedApp, "その場で完了扱いになる")
	assert.Empty(t, env.apps.markedTkt, "確認待ちの記録は要らない")
}

// **やり直しは前回の ticket を破棄してから進む。** 積み上げると、届いたメールを
// すべて確認して 1 つの承認から複数アカウントを作れる。
func TestApplicationRegister_EmailRequired_RetryDiscardsPreviousTicket(t *testing.T) {
	env, pendingRepo, tickets, sent := newApprovalEnvWithEmail(t)

	rec := doPost(env.handler.ApplicationRegister,
		`{"claimCode":"c","username":"newbie","password":"hunter22","emailAddress":"typo@example.com"}`)
	require.Equal(t, http.StatusNoContent, rec.Code)
	<-sent
	first := tickets.created[0].ID
	assert.Empty(t, tickets.deleted, "1 回目は破棄しない")

	// 申請が 1 回目の ticket を覚えている状態を再現する。
	env.apps.app.TicketID = &first

	rec = doPost(env.handler.ApplicationRegister,
		`{"claimCode":"c","username":"newbie","password":"hunter22","emailAddress":"right@example.com"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	<-sent

	assert.Equal(t, []string{first}, tickets.deleted, "前回の ticket を破棄する")
	require.Len(t, tickets.created, 2)
	assert.NotEqual(t, first, tickets.created[1].ID)
	require.Len(t, pendingRepo.Rows, 2)
	second := pendingByEmail(t, pendingRepo, "right@example.com")
	require.NotNil(t, second.InvitationTicketID)
	assert.Equal(t, tickets.created[1].ID, *second.InvitationTicketID)
}

// pending 作成に失敗したら ticket を残さない。
func TestApplicationRegister_EmailRequired_DiscardsTicketOnPendingFailure(t *testing.T) {
	env, _, tickets, _ := newApprovalEnvWithEmail(t)

	rec := doPost(env.handler.ApplicationRegister,
		`{"claimCode":"c","username":"!!!invalid!!!","password":"hunter22","emailAddress":"x@example.com"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	require.Len(t, tickets.created, 1)
	assert.Equal(t, []string{tickets.created[0].ID}, tickets.deleted)
	assert.Empty(t, env.apps.completedApp)
}

// 記録の失敗はアカウント作成を妨げない (pending は成立している)。
func TestApplicationRegister_EmailRequired_BookkeepingFailureStillSucceeds(t *testing.T) {
	env, pendingRepo, _, sent := newApprovalEnvWithEmail(t)
	env.apps.markTicketErr = errors.New("boom")

	rec := doPost(env.handler.ApplicationRegister,
		`{"claimCode":"c","username":"newbie","password":"hunter22","emailAddress":"x@example.com"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Len(t, pendingRepo.Rows, 1)
	<-sent
}

// onlyPending returns the single pending row, failing when there is not exactly one.
func onlyPending(t *testing.T, repo *testutil.MockUserPendingRepository) *model.UserPending {
	t.Helper()
	require.Len(t, repo.Rows, 1)
	for _, r := range repo.Rows {
		return r
	}
	return nil
}

func pendingByEmail(t *testing.T, repo *testutil.MockUserPendingRepository, email string) *model.UserPending {
	t.Helper()
	for _, r := range repo.Rows {
		if r.Email == email {
			return r
		}
	}
	t.Fatalf("pending row for %s not found", email)
	return nil
}

// 確認が終わって初めてアカウントになり、そこで申請が完了扱いになる。
// **completed にしないと approved のまま残り、同じクレームコードで何度でも
// 登録を始められる。**
func TestSignupPending_CompletesApplication(t *testing.T) {
	env, pendingRepo, tickets, sent := newApprovalEnvWithEmail(t)

	rec := doPost(env.handler.ApplicationRegister,
		`{"claimCode":"c","username":"newbie","password":"hunter22","emailAddress":"x@example.com"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	<-sent
	row := onlyPending(t, pendingRepo)

	rec = doPost(env.handler.SignupPending, `{"code":"`+row.Code+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := parseResp(t, rec)

	assert.Equal(t, "app-1", env.apps.completedApp)
	assert.Equal(t, resp["id"], env.apps.completedUsr)
	assert.Equal(t, tickets.created[0].ID, env.apps.completedTkt)
}

// 申請と無関係な pending の確認では申請に触らない。
func TestSignupPending_WithoutApplicationLeavesApplicationsAlone(t *testing.T) {
	env, pendingRepo, _, _ := newApprovalEnvWithEmail(t)
	env.meta.ApprovalRequiredForSignup = false

	// 通常の /api/signup 経路で積まれた pending を模す。
	rec := doPost(env.handler.Signup,
		`{"username":"other","password":"hunter22","emailAddress":"other@example.com"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	row := onlyPending(t, pendingRepo)
	require.Nil(t, row.SignupApplicationID)

	rec = doPost(env.handler.SignupPending, `{"code":"`+row.Code+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, env.apps.completedApp)
}
