// Package captcha provides pluggable CAPTCHA verification for signup and
// signin flows. Each provider (hCaptcha, reCAPTCHA, Turnstile, mCaptcha,
// testcaptcha) implements the Verifier interface. Service reads the active
// Meta config and delegates to the first enabled provider.
package captcha

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/shiroha-a/mk/internal/model"
)

// Errors returned by captcha verification.
var (
	ErrNoResponse       = errors.New("captcha: no response provided")
	ErrVerificationFail = errors.New("captcha: verification failed")
	ErrRequestFailed    = errors.New("captcha: request to provider failed")
)

// Verifier validates a captcha response token against its provider.
type Verifier interface {
	Verify(ctx context.Context, token string) error
}

// CaptchaTokens bundles all possible captcha response tokens sent by the
// frontend. 有効な provider に対応するものが**すべて**検証される (#3037)。
type CaptchaTokens struct {
	Hcaptcha    string
	Recaptcha   string
	Turnstile   string
	Mcaptcha    string
	Testcaptcha string
}

// Service selects the active captcha provider from meta config and verifies
// the corresponding token. If no provider is enabled, verification succeeds
// unconditionally (captcha is optional).
type Service struct {
	// **provider は meta の更新で差し替わる (`Reload`)。** 起動時のスナップ
	// ショットのままだと、運営者が管理画面で captcha を有効にしても再起動まで
	// 一切検証されない。`/api/meta` は DB を読むのでフロントは captcha を
	// 描画し、**運営者からは ON に見える**という最悪の形になっていた。
	//
	// upstream は `GlobalModule` が `metaUpdated` を受けて `meta` オブジェクトを
	// その場で書き換え、`SignupApiService` がリクエストごとに読む。
	mu        sync.RWMutex
	client    *http.Client
	hcaptcha  Verifier
	recaptcha Verifier
	turnstile Verifier
	mcaptcha  Verifier
	testcap   Verifier
}

// Reload swaps the provider set to match meta.
//
// **起動時のスナップショットを更新する唯一の経路。** `metaUpdated` の
// subscriber から呼ぶ (自 worker の更新も他 worker からの受信も同じ channel を
// 通る)。nil meta は無視する — 読めなかったことを理由に検証を落とすと、
// captcha を有効にしている運営者の設定が DB の瞬断で消える。
func (s *Service) Reload(meta *model.Meta) {
	if s == nil || meta == nil {
		return
	}
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	next := buildProviders(meta, client)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.hcaptcha = next.hcaptcha
	s.recaptcha = next.recaptcha
	s.turnstile = next.turnstile
	s.mcaptcha = next.mcaptcha
	s.testcap = next.testcap
}

// NewService builds a Service from the given meta. Equivalent to
// NewServiceWithClient(meta, nil): uses http.DefaultClient for the siteverify
// calls. Production paths should prefer NewServiceWithClient with an
// SSRF-safe transport (#638).
func NewService(meta *model.Meta) *Service {
	return NewServiceWithClient(meta, nil)
}

// NewServiceWithClient builds a Service that uses client for every provider's
// siteverify call. Pass nil to fall back to http.DefaultClient.
//
// production の router.go では SSRF-safe transport + forward proxy 経由の
// client を渡し、operator が outbound 経路を集約 / origin IP を隠せるように
// する (#638)。
func NewServiceWithClient(meta *model.Meta, client *http.Client) *Service {
	s := buildProviders(meta, client)
	s.client = client
	return s
}

// buildProviders constructs the provider set for meta. Reload と共有する。
func buildProviders(meta *model.Meta, client *http.Client) *Service {
	s := &Service{}

	if meta.EnableHcaptcha && meta.HcaptchaSecretKey != nil {
		s.hcaptcha = NewHcaptchaWithClient(*meta.HcaptchaSecretKey, client)
	}
	if meta.EnableRecaptcha && meta.RecaptchaSecretKey != nil {
		s.recaptcha = NewRecaptchaWithClient(*meta.RecaptchaSecretKey, client)
	}
	if meta.EnableTurnstile && meta.TurnstileSecretKey != nil {
		s.turnstile = NewTurnstileWithClient(*meta.TurnstileSecretKey, client)
	}
	if meta.EnableMcaptcha && meta.McaptchaSecretKey != nil && meta.McaptchaInstanceURL != nil {
		siteKey := ""
		if meta.McaptchaSiteKey != nil {
			siteKey = *meta.McaptchaSiteKey
		}
		s.mcaptcha = NewMcaptchaWithClient(*meta.McaptchaInstanceURL, siteKey, *meta.McaptchaSecretKey, client)
	}
	if meta.EnableTestcaptcha {
		s.testcap = NewTestcaptcha()
	}

	return s
}

// IsEnabled reports whether any captcha provider is configured.
func (s *Service) IsEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hcaptcha != nil || s.recaptcha != nil || s.turnstile != nil || s.mcaptcha != nil || s.testcap != nil
}

// HasRealProvider reports whether a provider other than testcaptcha is
// configured.
//
// **testcaptcha は実 provider として数えない (#2806)。** 中身は
// `"testcaptcha-passed"` との文字列一致で、開発 / E2E 専用でセキュリティは無い。
// 数えてしまうと「マジック文字列一致だけが効いていて、フォームトークンは要求
// されない」という最悪の組み合わせになる。
func (s *Service) HasRealProvider() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hcaptcha != nil || s.recaptcha != nil || s.turnstile != nil || s.mcaptcha != nil
}

// Verify checks the tokens of **every** enabled provider. Returns nil if no
// provider is enabled (captcha disabled).
func (s *Service) Verify(ctx context.Context, tokens CaptchaTokens) error {
	// **provider は Reload で差し替わりうる**ので、判定と呼び出しの間で
	// 読み直さないよう局所変数に写してからロックを外す。
	s.mu.RLock()
	hcap, recap, turn, mcap, testc := s.hcaptcha, s.recaptcha, s.turnstile, s.mcaptcha, s.testcap
	s.mu.RUnlock()
	// **有効な provider は全部検証する (#3037)。** 以前は最初の 1 つで
	// return していたので、運営者が 2 つ有効にすると**後ろの 1 つは素通り**
	// だった。設定画面は両方を「有効」と表示し、フォームも両方のウィジェットを
	// 出すので、効いていない側があることに気付けない。
	//
	// 順序は upstream (`SignupApiService.ts:80-108`) と同じ hcaptcha →
	// mcaptcha → recaptcha → turnstile → testcaptcha。**最初に失敗した
	// provider の error を返す**ので、どれが落ちたかは順序で決まる。
	for _, v := range []struct {
		p     Verifier
		token string
	}{
		{hcap, tokens.Hcaptcha},
		{mcap, tokens.Mcaptcha},
		{recap, tokens.Recaptcha},
		{turn, tokens.Turnstile},
		{testc, tokens.Testcaptcha},
	} {
		if v.p == nil {
			continue
		}
		if err := v.p.Verify(ctx, v.token); err != nil {
			return err
		}
	}
	return nil
}
