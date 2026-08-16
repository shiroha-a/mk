package signup_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	apisignup "github.com/shiroha-a/mk/internal/api/signup"
	"github.com/shiroha-a/mk/internal/core/miauth"
	"github.com/shiroha-a/mk/internal/core/signupapplication"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubApplications is an in-memory stand-in for the state machine.
type stubApplications struct {
	current   *model.SignupApplication
	latest    *model.SignupApplication
	applyErr  error
	currErr   error
	latestErr error
	markErr   error

	applied      *signupapplication.Contact
	appliedRes   string
	completedApp string
	completedUsr string
	completedTkt string
}

func (s *stubApplications) Apply(contact signupapplication.Contact, reason string) (*model.SignupApplication, error) {
	s.applied, s.appliedRes = &contact, reason
	if s.applyErr != nil {
		return nil, s.applyErr
	}
	return &model.SignupApplication{ID: "app-1", Status: model.SignupApplicationPending}, nil
}

func (s *stubApplications) Current(signupapplication.Contact) (*model.SignupApplication, error) {
	return s.current, s.currErr
}

func (s *stubApplications) Latest(signupapplication.Contact) (*model.SignupApplication, error) {
	return s.latest, s.latestErr
}

func (s *stubApplications) MarkCompleted(applicationID, userID, ticketID string) error {
	s.completedApp, s.completedUsr, s.completedTkt = applicationID, userID, ticketID
	return s.markErr
}

// remoteStub is the fake Misskey server the handler talks to.
type remoteStub struct {
	metaStatus  int
	metaBody    string
	checkBody   string
	checkStatus int
	requests    []string
}

func (r *remoteStub) RoundTrip(req *http.Request) (*http.Response, error) {
	r.requests = append(r.requests, req.URL.String())
	rec := httptest.NewRecorder()
	switch {
	case req.URL.Path == "/api/meta":
		status := r.metaStatus
		if status == 0 {
			status = http.StatusOK
		}
		body := r.metaBody
		if body == "" {
			body = `{"version":"2026.7.0"}`
		}
		rec.WriteHeader(status)
		_, _ = rec.WriteString(body)
	default:
		status := r.checkStatus
		if status == 0 {
			status = http.StatusOK
		}
		body := r.checkBody
		if body == "" {
			body = `{"ok":true,"user":{"id":"9abc","username":"alice","host":null}}`
		}
		rec.WriteHeader(status)
		_, _ = rec.WriteString(body)
	}
	return rec.Result(), nil
}

type approvalEnv struct {
	handler *apisignup.Handler
	apps    *stubApplications
	remote  *remoteStub
	store   *miauth.SessionStore
	meta    *model.Meta
	redis   *miniredis.Miniredis
}

// stopRedis simulates a Redis outage.
func (e *approvalEnv) stopRedis() { e.redis.Close() }

func newApprovalEnv(t *testing.T, enabled bool) *approvalEnv {
	t.Helper()
	h, _, metaRepo := newTestHandler(t)
	metaRepo.Meta = &model.Meta{ID: "x", ApprovalRequiredForSignup: enabled}

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })

	remote := &remoteStub{}
	apps := &stubApplications{}
	store := miauth.NewSessionStore(client)
	h.SetSignupApplications(apps,
		miauth.NewClient(&http.Client{Transport: remote}, "mk-go/test"), store, "https://mk.example")

	return &approvalEnv{handler: h, apps: apps, remote: remote, store: store, meta: metaRepo.Meta, redis: mr}
}

// startFlow runs the MiAuth start step and returns the browser-bound token.
func (e *approvalEnv) startFlow(t *testing.T) string {
	t.Helper()
	rec := doPost(e.handler.ApplicationMiAuthStart, `{"host":"remote.example"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return parseResp(t, rec)["token"].(string)
}

// verifiedToken runs start + complete and returns the verified token.
func (e *approvalEnv) verifiedToken(t *testing.T) string {
	t.Helper()
	pending := e.startFlow(t)
	rec := doPost(e.handler.ApplicationMiAuthComplete, `{"token":"`+pending+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return parseResp(t, rec)["token"].(string)
}

// 承認制が無効なら、どの endpoint も 503。**500 にすると、機能を使っていない
// インスタンスで壊れているように見える。**
func TestApplication_DisabledReturns503(t *testing.T) {
	env := newApprovalEnv(t, false)

	rec := doPost(env.handler.ApplicationMiAuthStart, `{"host":"remote.example"}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	rec = doPost(env.handler.ApplicationMiAuthComplete, `{"token":"x"}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	rec = doPost(env.handler.ApplicationStatus, `{"token":"x"}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	rec = doPost(env.handler.ApplicationApply, `{"token":"x"}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	rec = doPost(env.handler.ApplicationRegister, `{"token":"x","username":"a","password":"b"}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestApplicationMiAuthStart(t *testing.T) {
	env := newApprovalEnv(t, true)

	rec := doPost(env.handler.ApplicationMiAuthStart, `{"host":"remote.example"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := parseResp(t, rec)

	assert.NotEmpty(t, resp["token"])
	u, err := url.Parse(resp["url"].(string))
	require.NoError(t, err)
	assert.Equal(t, "remote.example", u.Host)
	// **permission は要求しない。** 相手には何も許可しない同意画面が出る。
	assert.False(t, u.Query().Has("permission"))
}

// `@name@host` や URL 形式で来ても同じ host に落とすこと。**揃えないと、表記
// 違いが (host, remoteID) の一致判定に効いて「申請したのに見つからない」になる。**
func TestApplicationMiAuthStart_NormalizesHost(t *testing.T) {
	for _, input := range []string{
		`remote.example`,
		`@alice@remote.example`,
		`https://remote.example`,
		`https://remote.example/`,
		`REMOTE.EXAMPLE`,
		`  remote.example  `,
	} {
		t.Run(input, func(t *testing.T) {
			env := newApprovalEnv(t, true)
			rec := doPost(env.handler.ApplicationMiAuthStart, `{"host":"`+input+`"}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			u, err := url.Parse(parseResp(t, rec)["url"].(string))
			require.NoError(t, err)
			assert.Equal(t, "remote.example", u.Host)
		})
	}
}

// **リダイレクト先として使う前に相手が Misskey 系か確かめる。** これが無いと
// 任意のホストへ利用者を飛ばすオープンリダイレクタになる。
func TestApplicationMiAuthStart_RejectsNonMisskeyHost(t *testing.T) {
	env := newApprovalEnv(t, true)
	env.remote.metaStatus = http.StatusNotFound

	rec := doPost(env.handler.ApplicationMiAuthStart, `{"host":"mastodon.example"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "NOT_MISSKEY_HOST")
}

func TestApplicationMiAuthStart_InvalidParams(t *testing.T) {
	env := newApprovalEnv(t, true)

	rec := doPost(env.handler.ApplicationMiAuthStart, `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "INVALID_PARAM")

	rec = doPost(env.handler.ApplicationMiAuthStart, `{"host":"localhost"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "INVALID_HOST")
}

func TestApplicationMiAuthComplete(t *testing.T) {
	env := newApprovalEnv(t, true)
	pending := env.startFlow(t)

	rec := doPost(env.handler.ApplicationMiAuthComplete, `{"token":"`+pending+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := parseResp(t, rec)

	assert.NotEmpty(t, resp["token"])
	contact := resp["contact"].(map[string]any)
	assert.Equal(t, "@alice@remote.example", contact["acct"])
	assert.Nil(t, resp["application"], "まだ申請していない")

	// pending は使い捨て。2 度目は通らない。
	rec = doPost(env.handler.ApplicationMiAuthComplete, `{"token":"`+pending+`"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "SESSION_EXPIRED")
}

func TestApplicationMiAuthComplete_Errors(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*approvalEnv)
		wantBody string
	}{
		{
			name:     "not authorized",
			setup:    func(e *approvalEnv) { e.remote.checkBody = `{"ok":false}` },
			wantBody: "NOT_AUTHORIZED",
		},
		{
			// **相手サーバー自身のユーザーであることを要求する。**
			name: "foreign account",
			setup: func(e *approvalEnv) {
				e.remote.checkBody = `{"ok":true,"user":{"id":"1","username":"a","host":"other.example"}}`
			},
			wantBody: "NOT_LOCAL_ACCOUNT",
		},
		{
			name:     "garbage response",
			setup:    func(e *approvalEnv) { e.remote.checkBody = `<html>` },
			wantBody: "NOT_MISSKEY_HOST",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newApprovalEnv(t, true)
			pending := env.startFlow(t)
			tt.setup(env)

			rec := doPost(env.handler.ApplicationMiAuthComplete, `{"token":"`+pending+`"}`)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantBody)
		})
	}
}

func TestApplicationMiAuthComplete_UnknownToken(t *testing.T) {
	env := newApprovalEnv(t, true)

	rec := doPost(env.handler.ApplicationMiAuthComplete, `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "INVALID_PARAM")

	rec = doPost(env.handler.ApplicationMiAuthComplete, `{"token":"nope"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "SESSION_EXPIRED")
}

func TestApplicationApply(t *testing.T) {
	env := newApprovalEnv(t, true)
	token := env.verifiedToken(t)

	rec := doPost(env.handler.ApplicationApply, `{"token":"`+token+`","reason":"よろしく"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.NotNil(t, env.apps.applied)
	// **一致判定に使うのは (host, remoteID)。**
	assert.Equal(t, "remote.example", env.apps.applied.Host)
	assert.Equal(t, "9abc", env.apps.applied.RemoteID)
	assert.Equal(t, "よろしく", env.apps.appliedRes)
}

func TestApplicationApply_Errors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantBody string
	}{
		{name: "duplicate", err: signupapplication.ErrLiveApplicationExists, wantBody: "ALREADY_APPLIED"},
		{name: "reason too long", err: signupapplication.ErrReasonTooLong, wantBody: "REASON_TOO_LONG"},
		{name: "invalid contact", err: signupapplication.ErrInvalidContact, wantBody: "INVALID_PARAM"},
		{name: "unexpected", err: errors.New("boom"), wantBody: "INTERNAL_ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newApprovalEnv(t, true)
			token := env.verifiedToken(t)
			env.apps.applyErr = tt.err

			rec := doPost(env.handler.ApplicationApply, `{"token":"`+token+`"}`)
			assert.Contains(t, rec.Body.String(), tt.wantBody)
		})
	}
}

// 検証済みトークンで状態を引き直せること。**登録ページに戻ってきた人が続きから
// 進めるための入口**で、DM が届かなくても詰まないのはこれがあるため。
func TestApplicationStatus(t *testing.T) {
	env := newApprovalEnv(t, true)
	token := env.verifiedToken(t)
	env.apps.current = &model.SignupApplication{
		ID: "app-1", Status: model.SignupApplicationApproved,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}

	rec := doPost(env.handler.ApplicationStatus, `{"token":"`+token+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := parseResp(t, rec)

	app := resp["application"].(map[string]any)
	assert.Equal(t, "approved", app["status"])
	// **管理者向けの列は出さない。**
	assert.NotContains(t, app, "processedById")
	assert.NotContains(t, app, "id")
}

// 却下・期限切れの結果も見せる (終端状態は Current では返らないので Latest に落ちる)。
func TestApplicationStatus_FallsBackToLatest(t *testing.T) {
	env := newApprovalEnv(t, true)
	token := env.verifiedToken(t)
	env.apps.latest = &model.SignupApplication{ID: "app-0", Status: model.SignupApplicationRejected}

	rec := doPost(env.handler.ApplicationStatus, `{"token":"`+token+`"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	app := parseResp(t, rec)["application"].(map[string]any)
	assert.Equal(t, "rejected", app["status"])
}

func TestApplicationStatus_ExpiredToken(t *testing.T) {
	env := newApprovalEnv(t, true)

	rec := doPost(env.handler.ApplicationStatus, `{"token":"nope"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "SESSION_EXPIRED")
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
			token := env.verifiedToken(t)
			if status != model.SignupApplicationPending {
				env.apps.current = nil // 終端状態は Current では返らない
			} else {
				env.apps.current = &model.SignupApplication{ID: "app-1", Status: status}
			}

			rec := doPost(env.handler.ApplicationRegister,
				`{"token":"`+token+`","username":"newbie","password":"hunter22"}`)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "NOT_APPROVED")
		})
	}
}

func TestApplicationRegister_Success(t *testing.T) {
	env := newApprovalEnv(t, true)
	token := env.verifiedToken(t)
	env.apps.current = &model.SignupApplication{ID: "app-1", Status: model.SignupApplicationApproved}

	rec := doPost(env.handler.ApplicationRegister,
		`{"token":"`+token+`","username":"newbie","password":"hunter22"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := parseResp(t, rec)
	assert.Equal(t, "newbie", resp["username"])
	assert.NotEmpty(t, resp["token"], "作成したアカウントの token が返る")

	assert.Equal(t, "app-1", env.apps.completedApp)
	assert.NotEmpty(t, env.apps.completedUsr)

	// **登録が済んだら検証済みトークンは用済み。** 残すと同じトークンで再度
	// 登録画面に入れる。
	_, err := env.store.Verified(t.Context(), token)
	assert.ErrorIs(t, err, miauth.ErrSessionNotFound)
}

func TestApplicationRegister_InvalidParams(t *testing.T) {
	env := newApprovalEnv(t, true)
	token := env.verifiedToken(t)

	rec := doPost(env.handler.ApplicationRegister, `{"token":"`+token+`"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "INVALID_PARAM")
}

func TestApplicationRegister_DuplicateUsername(t *testing.T) {
	env := newApprovalEnv(t, true)
	token := env.verifiedToken(t)
	env.apps.current = &model.SignupApplication{ID: "app-1", Status: model.SignupApplicationApproved}

	body := `{"token":"` + token + `","username":"newbie","password":"hunter22"}`
	require.Equal(t, http.StatusOK, doPost(env.handler.ApplicationRegister, body).Code)

	// 2 回目は同じ username なので弾かれる。検証済みトークンは 1 回目で
	// 落ちているため、まず SESSION_EXPIRED になる。
	rec := doPost(env.handler.ApplicationRegister, body)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// stubTicketStore satisfies TicketStore plus the optional issue / discard
// halves the approval flow uses.
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

// **招待コードは利用者に渡さない。** ここで発行し、同じ流れで消費する。
func TestApplicationRegister_MintsAndConsumesTicket(t *testing.T) {
	env := newApprovalEnv(t, true)
	tickets := &stubTicketStore{}
	env.handler.SetTicketStore(tickets)
	token := env.verifiedToken(t)
	moderator := "mod-1"
	env.apps.current = &model.SignupApplication{
		ID: "app-1", Status: model.SignupApplicationApproved, ProcessedByID: &moderator,
	}

	rec := doPost(env.handler.ApplicationRegister,
		`{"token":"`+token+`","username":"newbie","password":"hunter22"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Len(t, tickets.created, 1)
	issued := tickets.created[0]
	assert.NotEmpty(t, issued.Code)
	require.NotNil(t, issued.ExpiresAt, "期限を切ること")
	assert.True(t, issued.ExpiresAt.After(time.Now()))
	assert.True(t, issued.ExpiresAt.Before(time.Now().Add(time.Hour)),
		"渡していない credential を長く残さない")
	// 招待一覧から「誰の承認で作られたか」を辿れるようにする。
	require.NotNil(t, issued.CreatedByID)
	assert.Equal(t, moderator, *issued.CreatedByID)

	assert.Equal(t, issued.ID, tickets.usedTkt, "同じ流れで消費すること")
	assert.Equal(t, issued.ID, env.apps.completedTkt, "申請に消費したチケットを記録すること")
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
	token := env.verifiedToken(t)
	env.apps.current = &model.SignupApplication{ID: "app-1", Status: model.SignupApplicationApproved}

	// username 不正で signup を失敗させる。
	rec := doPost(env.handler.ApplicationRegister,
		`{"token":"`+token+`","username":"!!!invalid!!!","password":"hunter22"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	require.Len(t, tickets.created, 1)
	assert.Equal(t, []string{tickets.created[0].ID}, tickets.deleted)
	assert.Empty(t, env.apps.completedApp, "失敗したら申請は完了扱いにしない")
}

func TestApplicationRegister_TicketIssueFailure(t *testing.T) {
	env := newApprovalEnv(t, true)
	env.handler.SetTicketStore(&stubTicketStore{createErr: errors.New("boom")})
	token := env.verifiedToken(t)
	env.apps.current = &model.SignupApplication{ID: "app-1", Status: model.SignupApplicationApproved}

	rec := doPost(env.handler.ApplicationRegister,
		`{"token":"`+token+`","username":"newbie","password":"hunter22"}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// 消費や完了記録に失敗してもアカウントは作られている。**ここで 500 を返すと、
// 利用者には「失敗したのに登録されている」状態になる。**
func TestApplicationRegister_BookkeepingFailuresDoNotFailSignup(t *testing.T) {
	env := newApprovalEnv(t, true)
	env.handler.SetTicketStore(&stubTicketStore{markErr: errors.New("boom")})
	token := env.verifiedToken(t)
	env.apps.current = &model.SignupApplication{ID: "app-1", Status: model.SignupApplicationApproved}
	env.apps.markErr = errors.New("boom")

	rec := doPost(env.handler.ApplicationRegister,
		`{"token":"`+token+`","username":"newbie","password":"hunter22"}`)
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
			token := env.verifiedToken(t)
			env.apps.current = &model.SignupApplication{ID: "app-1", Status: model.SignupApplicationApproved}

			body, err := json.Marshal(map[string]string{
				"token": token, "username": tt.username, "password": tt.password,
			})
			require.NoError(t, err)
			rec := doPost(env.handler.ApplicationRegister, string(body))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantBody)
		})
	}
}

func TestApplicationRegister_DuplicateUsernameShape(t *testing.T) {
	env := newApprovalEnv(t, true)
	env.apps.current = &model.SignupApplication{ID: "app-1", Status: model.SignupApplicationApproved}

	first := env.verifiedToken(t)
	require.Equal(t, http.StatusOK, doPost(env.handler.ApplicationRegister,
		`{"token":"`+first+`","username":"newbie","password":"hunter22"}`).Code)

	second := env.verifiedToken(t)
	rec := doPost(env.handler.ApplicationRegister,
		`{"token":"`+second+`","username":"newbie","password":"hunter22"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestApplication_StateMachineFailures(t *testing.T) {
	t.Run("current lookup fails on status", func(t *testing.T) {
		env := newApprovalEnv(t, true)
		token := env.verifiedToken(t)
		env.apps.currErr = errors.New("boom")

		rec := doPost(env.handler.ApplicationStatus, `{"token":"`+token+`"}`)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("latest lookup fails on status", func(t *testing.T) {
		env := newApprovalEnv(t, true)
		token := env.verifiedToken(t)
		env.apps.latestErr = errors.New("boom")

		rec := doPost(env.handler.ApplicationStatus, `{"token":"`+token+`"}`)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("current lookup fails on register", func(t *testing.T) {
		env := newApprovalEnv(t, true)
		token := env.verifiedToken(t)
		env.apps.currErr = errors.New("boom")

		rec := doPost(env.handler.ApplicationRegister,
			`{"token":"`+token+`","username":"newbie","password":"hunter22"}`)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("complete surfaces lookup failure", func(t *testing.T) {
		env := newApprovalEnv(t, true)
		pending := env.startFlow(t)
		env.apps.currErr = errors.New("boom")

		rec := doPost(env.handler.ApplicationMiAuthComplete, `{"token":"`+pending+`"}`)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

// meta が引けないときは 503 ではなく 500。**「機能が無い」と「壊れている」を
// 混ぜない。**
func TestApplication_MetaFetchFailure(t *testing.T) {
	h, _, metaRepo := newTestHandler(t)
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	h.SetSignupApplications(&stubApplications{},
		miauth.NewClient(&http.Client{Transport: &remoteStub{}}, ""),
		miauth.NewSessionStore(client), "https://mk.example")
	metaRepo.Meta = nil

	rec := doPost(h.ApplicationMiAuthStart, `{"host":"remote.example"}`)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// Redis が落ちているときは「セッションが無い」ではなく 500。**取り違えると、
// 障害中ずっと「やり直してください」と案内してしまう。**
func TestApplication_RedisFailure(t *testing.T) {
	env := newApprovalEnv(t, true)
	pending := env.startFlow(t)
	token := env.verifiedToken(t)

	// miniredis を止める。
	for _, tc := range []struct {
		name string
		call func() *httptest.ResponseRecorder
	}{
		{name: "start", call: func() *httptest.ResponseRecorder {
			return doPost(env.handler.ApplicationMiAuthStart, `{"host":"remote.example"}`)
		}},
		{name: "complete", call: func() *httptest.ResponseRecorder {
			return doPost(env.handler.ApplicationMiAuthComplete, `{"token":"`+pending+`"}`)
		}},
		{name: "status", call: func() *httptest.ResponseRecorder {
			return doPost(env.handler.ApplicationStatus, `{"token":"`+token+`"}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env.stopRedis()
			assert.Equal(t, http.StatusInternalServerError, tc.call().Code)
		})
	}
}

// 表示名とコールバックは meta / serverURL から作る。片方が空でも壊れないこと。
func TestApplicationCallbackAndName(t *testing.T) {
	env := newApprovalEnv(t, true)
	name := "テスト鯖"
	env.meta.Name = &name

	rec := doPost(env.handler.ApplicationMiAuthStart, `{"host":"remote.example"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	u, err := url.Parse(parseResp(t, rec)["url"].(string))
	require.NoError(t, err)
	assert.Equal(t, name, u.Query().Get("name"))
	assert.Contains(t, u.Query().Get("callback"), "/signup-application/callback")
}
