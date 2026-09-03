// Package signupform issues and verifies the signed form tokens that guard the
// approval-based signup endpoints when no captcha provider is configured
// (#2806).
//
// **これは captcha の代替ではない。** 止まるのは「フォームを取得せずに endpoint を
// 直接叩く」bot だけで、mk-go 向けに書かれたスクリプトにはほぼコストを課さない
// (発行 endpoint を並列で叩き、1 回だけ待ち、一斉に送れば、増える負担はリクエスト
// 数が約 2 倍になることだけ)。IP を回す攻撃者にとっては大差ない。
//
// したがってこれは**素朴な層を落とす床上げ**であり、以下は引き続き必要:
//
//   - captcha 未設定時の管理画面の警告
//   - 申請の一括却下 (清掃コストを下げる)
//   - 生きている申請の総数上限 (IP 回転に効く唯一の手段)
//
// この位置づけを消すと「captcha を入れた」と誤解され、上の 3 つが不要だと判断される。
package signupform

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PurposeApply is the token purpose bound to signup-application/apply.
//
// **`:` を含めないこと。** payload の区切りに使っているので、含めると検証側の
// 分解がずれる。用途は定数でしか作らないので、ここを守れば足りる。
const PurposeApply = "signup-application/apply"

// tokenVersion prefixes every payload so the format can change without
// accepting old tokens under the new parser.
const tokenVersion = "1"

const (
	// DefaultMinAge is how long a form must stay open before it can be
	// submitted. **人間の下限ではなく bot の下限**として置く — フォームを読んで
	// 記入する人間はこれより速くならないし、空フォームでも画面側が送信ボタンを
	// 抑えるので、正規の利用者がこの error を見ることは無い。
	DefaultMinAge = 3 * time.Second
	// DefaultTTL bounds how long an issued token stays usable. 申請フォームは
	// 自由記述なので、書くのに時間が掛かる前提で長めに取る。
	DefaultTTL = 30 * time.Minute
)

var (
	// ErrTokenInvalid covers a missing, malformed, or forged token.
	ErrTokenInvalid = errors.New("signup form token is invalid")
	// ErrTokenExpired is returned when the token is older than the TTL.
	ErrTokenExpired = errors.New("signup form token is expired")
	// ErrTokenTooSoon is returned when the form was submitted faster than
	// DefaultMinAge. **nonce は焼かない** ので、待てば同じ token で通る。
	ErrTokenTooSoon = errors.New("signup form token was submitted too soon")
	// ErrTokenUsed is returned when the nonce was already consumed.
	ErrTokenUsed = errors.New("signup form token was already used")
)

// NonceStore records consumed nonces so a token cannot be replayed.
//
// **使い捨てにしないと 1 枚を無限に使い回される。** 署名と滞在時間だけでは、
// 1 回だけ正規にフォームを開いた攻撃者が同じ token で申請を量産できる。
type NonceStore interface {
	// Burn marks nonce as consumed for ttl. Returns true when this call is
	// the one that consumed it, false when it was already consumed.
	Burn(ctx context.Context, nonce string, ttl time.Duration) (bool, error)
}

// Issuer issues and verifies signed form tokens.
type Issuer struct {
	secret []byte
	nonces NonceStore
	minAge time.Duration
	ttl    time.Duration
	// now is swappable for tests.
	now func() time.Time
}

// NewIssuer creates an Issuer. secret と nonces のどちらかが欠けると nil を返す
// ので、呼び出し側は「未配線 = 保護が無い」として扱える (router の
// recordCriticalWiring がそれを起動時に検出する)。
func NewIssuer(secret []byte, nonces NonceStore) *Issuer {
	return NewIssuerWithTimings(secret, nonces, DefaultMinAge, DefaultTTL)
}

// NewIssuerWithTimings is NewIssuer with explicit timings.
//
// 0 以下を渡すと既定に戻す。**minAge だけは 0 を許す** — 滞在時間を課さない
// 構成 (テストなど) を作れるようにするため。
func NewIssuerWithTimings(secret []byte, nonces NonceStore, minAge, ttl time.Duration) *Issuer {
	if len(secret) == 0 || nonces == nil {
		return nil
	}
	if minAge < 0 {
		minAge = DefaultMinAge
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Issuer{secret: secret, nonces: nonces, minAge: minAge, ttl: ttl, now: time.Now}
}

// MinWait returns how long a form must stay open before it can be submitted.
// 画面が送信ボタンを抑える時間として使う。
func (i *Issuer) MinWait() time.Duration {
	if i == nil {
		return 0
	}
	return i.minAge
}

var enc = base64.RawURLEncoding

// Issue returns a fresh token bound to purpose.
func (i *Issuer) Issue(purpose string) (string, error) {
	if i == nil {
		return "", ErrTokenInvalid
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("signupform: generate nonce: %w", err)
	}
	payload := fmt.Sprintf("%s:%s:%d:%s", tokenVersion, purpose, i.now().Unix(), enc.EncodeToString(nonce))
	return enc.EncodeToString([]byte(payload)) + "." + enc.EncodeToString(i.sign([]byte(payload))), nil
}

// Verify checks the token and consumes its nonce.
//
// 検証の順序が要点。**nonce を焼くのは最後**にする — 早すぎる送信 (ErrTokenTooSoon)
// で焼いてしまうと、待ってやり直した正規の利用者が二度と通らなくなる。
func (i *Issuer) Verify(ctx context.Context, purpose, token string) error {
	if i == nil {
		return ErrTokenInvalid
	}
	rawPayload, mac, ok := splitToken(token)
	if !ok {
		return ErrTokenInvalid
	}
	if !hmac.Equal(mac, i.sign(rawPayload)) {
		return ErrTokenInvalid
	}
	issuedAt, nonce, ok := parsePayload(string(rawPayload), purpose)
	if !ok {
		return ErrTokenInvalid
	}
	age := i.now().Sub(issuedAt)
	if age > i.ttl {
		return ErrTokenExpired
	}
	// 負の age は時計が巻き戻ったときに出る。署名は通っているので偽造ではないが、
	// 経過時間を測れない以上は「早すぎる」側に倒す。**minAge は必ず 0 以上**
	// (NewIssuerWithTimings が負を既定へ戻す) なので、この 1 つの比較で
	// 巻き戻りも拾える。
	if age < i.minAge {
		return ErrTokenTooSoon
	}
	fresh, err := i.nonces.Burn(ctx, nonce, i.ttl)
	if err != nil {
		return fmt.Errorf("signupform: burn nonce: %w", err)
	}
	if !fresh {
		return ErrTokenUsed
	}
	return nil
}

func (i *Issuer) sign(payload []byte) []byte {
	m := hmac.New(sha256.New, i.secret)
	m.Write(payload)
	return m.Sum(nil)
}

func splitToken(token string) (payload, mac []byte, ok bool) {
	rawPayload, rawMAC, found := strings.Cut(token, ".")
	if !found {
		return nil, nil, false
	}
	payload, err := enc.DecodeString(rawPayload)
	if err != nil {
		return nil, nil, false
	}
	mac, err = enc.DecodeString(rawMAC)
	if err != nil {
		return nil, nil, false
	}
	return payload, mac, true
}

func parsePayload(payload, purpose string) (issuedAt time.Time, nonce string, ok bool) {
	parts := strings.SplitN(payload, ":", 4)
	if len(parts) != 4 || parts[0] != tokenVersion || parts[1] != purpose {
		return time.Time{}, "", false
	}
	sec, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return time.Time{}, "", false
	}
	if parts[3] == "" {
		return time.Time{}, "", false
	}
	return time.Unix(sec, 0), parts[3], true
}
