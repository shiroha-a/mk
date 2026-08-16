package signup_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	apisignup "github.com/shiroha-a/mk/internal/api/signup"
	"github.com/shiroha-a/mk/internal/core/signupapplication"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubApplications is an in-memory stand-in for the state machine.
type stubApplications struct {
	app       *model.SignupApplication
	code      string
	applyErr  error
	lookupErr error
	markErr   error

	appliedReason string
	completedApp  string
	completedUsr  string
	completedTkt  string
	lastCode      string
}

func (s *stubApplications) Apply(reason string) (*model.SignupApplication, string, error) {
	s.appliedReason = reason
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

// stubTicketStore satisfies TicketStore plus the optional issue / discard halves.
type stubTicketStore struct {
	created   []*model.RegistrationTicket
	deleted   []string
	usedTkt   string
	usedUsr   string
	createErr error
	markErr   error
}

func (s *stubTicketStore) FindByCode(string) (*model.RegistrationTicket, error) {
	return nil, errors.New("not used")
}

func (s *stubTicketStore) MarkUsed(ticketID, userID string) error {
	s.usedTkt, s.usedUsr = ticketID, userID
	return s.markErr
}

func (s *stubTicketStore) MarkPending(string, string) error { return nil }

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
	metaRepo.Meta = &model.Meta{ID: "x", ApprovalRequiredForSignup: enabled}
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

	rec := doPost(env.handler.ApplicationApply, `{"reason":"よろしく"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := parseResp(t, rec)

	assert.Equal(t, "claim-code-1", resp["claimCode"])
	assert.Equal(t, "よろしく", env.apps.appliedReason)

	app := resp["application"].(map[string]any)
	assert.Equal(t, "pending", app["status"])
	// **管理者向けの列は出さない。**
	assert.NotContains(t, app, "processedById")
	assert.NotContains(t, app, "id")
}

func TestApplicationApply_Errors(t *testing.T) {
	t.Run("reason too long", func(t *testing.T) {
		env := newApprovalEnv(t, true)
		env.apps.applyErr = signupapplication.ErrReasonTooLong
		rec := doPost(env.handler.ApplicationApply, `{"reason":"x"}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "REASON_TOO_LONG")
	})

	t.Run("unexpected", func(t *testing.T) {
		env := newApprovalEnv(t, true)
		env.apps.applyErr = errors.New("boom")
		rec := doPost(env.handler.ApplicationApply, `{}`)
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
