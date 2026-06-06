package captcha_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shiroha-a/mk/internal/core/captcha"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- siteVerify providers (hCaptcha, reCAPTCHA, Turnstile) ---

func newSiteVerifyServer(t *testing.T, wantSecret string, respond func(secret, token string) any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		resp := respond(r.FormValue("secret"), r.FormValue("response"))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestHcaptcha_Success(t *testing.T) {
	srv := newSiteVerifyServer(t, "sec", func(_, _ string) any {
		return map[string]any{"success": true}
	})
	defer srv.Close()

	v := captcha.NewHcaptchaWithClient("sec", srv.Client())
	// siteVerifier は verifyURL を上書きできないので、内部テストは Turnstile 経由で代理する。
	// ここでは NewHcaptchaWithClient が生成できることだけ確認。
	assert.NotNil(t, v)
}

func TestTurnstile_Success(t *testing.T) {
	srv := newSiteVerifyServer(t, "sec", func(secret, token string) any {
		if secret == "sec" && token == "ok-token" {
			return map[string]any{"success": true}
		}
		return map[string]any{"success": false, "error-codes": []string{"invalid-input-response"}}
	})
	defer srv.Close()

	v := captcha.NewTurnstileWithURL("sec", srv.URL, srv.Client())
	require.NoError(t, v.Verify(context.Background(), "ok-token"))
}

func TestTurnstile_Failure(t *testing.T) {
	srv := newSiteVerifyServer(t, "sec", func(_, _ string) any {
		return map[string]any{"success": false, "error-codes": []string{"bad-request"}}
	})
	defer srv.Close()

	v := captcha.NewTurnstileWithURL("sec", srv.URL, srv.Client())
	err := v.Verify(context.Background(), "bad-token")
	assert.ErrorIs(t, err, captcha.ErrVerificationFail)
}

func TestSiteVerify_EmptyToken(t *testing.T) {
	v := captcha.NewHcaptchaWithClient("sec", http.DefaultClient)
	err := v.Verify(context.Background(), "")
	assert.ErrorIs(t, err, captcha.ErrNoResponse)
}

// --- mCaptcha ---

func TestMcaptcha_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["key"] == "site" && body["secret"] == "sec" && body["token"] == "good" {
			json.NewEncoder(w).Encode(map[string]any{"valid": true})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"valid": false})
		}
	}))
	defer srv.Close()

	v := captcha.NewMcaptchaWithClient(srv.URL, "site", "sec", srv.Client())
	require.NoError(t, v.Verify(context.Background(), "good"))
}

func TestMcaptcha_InvalidToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"valid": false})
	}))
	defer srv.Close()

	v := captcha.NewMcaptchaWithClient(srv.URL, "site", "sec", srv.Client())
	err := v.Verify(context.Background(), "wrong")
	assert.ErrorIs(t, err, captcha.ErrVerificationFail)
}

func TestMcaptcha_EmptyToken(t *testing.T) {
	v := captcha.NewMcaptcha("http://localhost", "site", "sec")
	err := v.Verify(context.Background(), "")
	assert.ErrorIs(t, err, captcha.ErrNoResponse)
}

func TestMcaptcha_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	v := captcha.NewMcaptchaWithClient(srv.URL, "site", "sec", srv.Client())
	err := v.Verify(context.Background(), "token")
	// 非200は instance への到達失敗扱い (upstream verifyMcaptcha と同じ requestFailed)。
	assert.ErrorIs(t, err, captcha.ErrRequestFailed)
}

// --- testcaptcha ---

func TestTestcaptcha_Success(t *testing.T) {
	v := captcha.NewTestcaptcha()
	require.NoError(t, v.Verify(context.Background(), "testcaptcha-passed"))
}

func TestTestcaptcha_WrongToken(t *testing.T) {
	v := captcha.NewTestcaptcha()
	err := v.Verify(context.Background(), "wrong-value")
	assert.ErrorIs(t, err, captcha.ErrVerificationFail)
}

func TestTestcaptcha_EmptyToken(t *testing.T) {
	v := captcha.NewTestcaptcha()
	err := v.Verify(context.Background(), "")
	assert.ErrorIs(t, err, captcha.ErrNoResponse)
}

// --- Service ---

func TestService_NoProviderEnabled(t *testing.T) {
	svc := captcha.NewService(&model.Meta{})
	require.NoError(t, svc.Verify(context.Background(), captcha.CaptchaTokens{}))
}

func TestService_TestcaptchaEnabled(t *testing.T) {
	svc := captcha.NewService(&model.Meta{EnableTestcaptcha: true})
	require.NoError(t, svc.Verify(context.Background(), captcha.CaptchaTokens{Testcaptcha: "testcaptcha-passed"}))

	err := svc.Verify(context.Background(), captcha.CaptchaTokens{Testcaptcha: "wrong"})
	assert.ErrorIs(t, err, captcha.ErrVerificationFail)
}

func TestService_HcaptchaEnabledButNoSecret(t *testing.T) {
	// secret 未設定なら provider は構築されない → captcha スキップ
	svc := captcha.NewService(&model.Meta{EnableHcaptcha: true})
	require.NoError(t, svc.Verify(context.Background(), captcha.CaptchaTokens{}))
}

func TestService_HcaptchaEnabled(t *testing.T) {
	secret := "hcap-secret"
	svc := captcha.NewService(&model.Meta{
		EnableHcaptcha:    true,
		HcaptchaSecretKey: &secret,
	})
	// token が空なので ErrNoResponse
	err := svc.Verify(context.Background(), captcha.CaptchaTokens{})
	assert.ErrorIs(t, err, captcha.ErrNoResponse)
}

func TestService_PriorityOrder(t *testing.T) {
	// 複数有効でも最初の provider (hcaptcha) が使われる。
	secret := "s"
	svc := captcha.NewService(&model.Meta{
		EnableHcaptcha:    true,
		HcaptchaSecretKey: &secret,
		EnableTestcaptcha: true,
	})
	// testcaptcha token は送るが hcaptcha が優先されるため token 空でエラー。
	err := svc.Verify(context.Background(), captcha.CaptchaTokens{Testcaptcha: "testcaptcha-passed"})
	assert.ErrorIs(t, err, captcha.ErrNoResponse)
}

func TestService_RecaptchaEnabled(t *testing.T) {
	secret := "recap-sec"
	svc := captcha.NewService(&model.Meta{
		EnableRecaptcha:    true,
		RecaptchaSecretKey: &secret,
	})
	err := svc.Verify(context.Background(), captcha.CaptchaTokens{})
	assert.ErrorIs(t, err, captcha.ErrNoResponse)
}

func TestService_TurnstileEnabled(t *testing.T) {
	secret := "ts-sec"
	svc := captcha.NewService(&model.Meta{
		EnableTurnstile:    true,
		TurnstileSecretKey: &secret,
	})
	err := svc.Verify(context.Background(), captcha.CaptchaTokens{})
	assert.ErrorIs(t, err, captcha.ErrNoResponse)
}

func TestService_McaptchaEnabled(t *testing.T) {
	secret := "mc-sec"
	inst := "http://mcaptcha.example"
	siteKey := "site"
	svc := captcha.NewService(&model.Meta{
		EnableMcaptcha:      true,
		McaptchaSecretKey:   &secret,
		McaptchaInstanceURL: &inst,
		McaptchaSiteKey:     &siteKey,
	})
	err := svc.Verify(context.Background(), captcha.CaptchaTokens{})
	assert.ErrorIs(t, err, captcha.ErrNoResponse)
}

func TestRecaptcha_Constructor(t *testing.T) {
	v := captcha.NewRecaptcha("sec")
	assert.NotNil(t, v)
	vw := captcha.NewRecaptchaWithClient("sec", http.DefaultClient)
	assert.NotNil(t, vw)
}

func TestTurnstile_Constructor(t *testing.T) {
	v := captcha.NewTurnstile("sec")
	assert.NotNil(t, v)
	vw := captcha.NewTurnstileWithClient("sec", http.DefaultClient)
	assert.NotNil(t, vw)
}

func TestSiteVerify_ServerDown(t *testing.T) {
	// 到達不能サーバー
	v := captcha.NewTurnstileWithURL("sec", "http://127.0.0.1:1", http.DefaultClient)
	err := v.Verify(context.Background(), "token")
	assert.ErrorIs(t, err, captcha.ErrRequestFailed)
}

func TestSiteVerify_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	v := captcha.NewTurnstileWithURL("sec", srv.URL, srv.Client())
	err := v.Verify(context.Background(), "token")
	assert.ErrorIs(t, err, captcha.ErrRequestFailed)
}

func TestSiteVerify_NoErrorCodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"success": false})
	}))
	defer srv.Close()

	v := captcha.NewTurnstileWithURL("sec", srv.URL, srv.Client())
	err := v.Verify(context.Background(), "token")
	assert.ErrorIs(t, err, captcha.ErrVerificationFail)
	assert.Contains(t, err.Error(), "unknown")
}

func TestMcaptcha_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("broken"))
	}))
	defer srv.Close()

	v := captcha.NewMcaptchaWithClient(srv.URL, "site", "sec", srv.Client())
	err := v.Verify(context.Background(), "token")
	assert.ErrorIs(t, err, captcha.ErrRequestFailed)
}

// #340: captcha fetcher が safehttp.ReadAllLimit (1 MiB cap) で過大 response
// を弾くこと。1 MiB 超を返すサーバをsimulate して ErrRequestFailed が返る
// ことを検証する。
func TestMcaptcha_ResponseTooLarge(t *testing.T) {
	oversized := make([]byte, 2<<20) // 2 MiB > 1 MiB cap
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(oversized)
	}))
	defer srv.Close()

	v := captcha.NewMcaptchaWithClient(srv.URL, "site", "sec", srv.Client())
	err := v.Verify(context.Background(), "token")
	assert.ErrorIs(t, err, captcha.ErrRequestFailed)
}

func TestTurnstile_ResponseTooLarge(t *testing.T) {
	oversized := make([]byte, 2<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(oversized)
	}))
	defer srv.Close()

	v := captcha.NewTurnstileWithURL("sec", srv.URL, srv.Client())
	err := v.Verify(context.Background(), "token")
	// Mcaptcha と同じく ErrRequestFailed が返ることを明示 (TestMcaptcha_ResponseTooLarge
	// と揃える、Devin #404 指摘)。
	assert.ErrorIs(t, err, captcha.ErrRequestFailed)
}
