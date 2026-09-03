// Package captcha provides pluggable CAPTCHA verification for signup and
// signin flows. Each provider (hCaptcha, reCAPTCHA, Turnstile, mCaptcha,
// testcaptcha) implements the Verifier interface. Service reads the active
// Meta config and delegates to the first enabled provider.
package captcha

import (
	"context"
	"errors"
	"net/http"

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
// frontend. Only the one matching the enabled provider is inspected.
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
	hcaptcha  Verifier
	recaptcha Verifier
	turnstile Verifier
	mcaptcha  Verifier
	testcap   Verifier
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
	return s.hcaptcha != nil || s.recaptcha != nil || s.turnstile != nil || s.mcaptcha != nil
}

// Verify checks the token matching the first enabled provider. Returns nil
// if no provider is enabled (captcha disabled).
func (s *Service) Verify(ctx context.Context, tokens CaptchaTokens) error {
	switch {
	case s.hcaptcha != nil:
		return s.hcaptcha.Verify(ctx, tokens.Hcaptcha)
	case s.recaptcha != nil:
		return s.recaptcha.Verify(ctx, tokens.Recaptcha)
	case s.turnstile != nil:
		return s.turnstile.Verify(ctx, tokens.Turnstile)
	case s.mcaptcha != nil:
		return s.mcaptcha.Verify(ctx, tokens.Mcaptcha)
	case s.testcap != nil:
		return s.testcap.Verify(ctx, tokens.Testcaptcha)
	default:
		return nil
	}
}
