package signupform

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeNonces struct {
	burnt map[string]bool
	err   error
	calls int
}

func newFakeNonces() *fakeNonces { return &fakeNonces{burnt: map[string]bool{}} }

func (f *fakeNonces) Burn(_ context.Context, nonce string, _ time.Duration) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	if f.burnt[nonce] {
		return false, nil
	}
	f.burnt[nonce] = true
	return true, nil
}

// newTestIssuer returns an issuer whose clock the caller drives.
func newTestIssuer(t *testing.T) (*Issuer, *fakeNonces, *time.Time) {
	t.Helper()
	nonces := newFakeNonces()
	i := NewIssuer([]byte("secret-key"), nonces)
	require.NotNil(t, i)
	clock := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	i.now = func() time.Time { return clock }
	return i, nonces, &clock
}

func TestIssuer_HappyPath(t *testing.T) {
	i, nonces, clock := newTestIssuer(t)
	token, err := i.Issue(PurposeApply)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	*clock = clock.Add(DefaultMinAge)
	require.NoError(t, i.Verify(context.Background(), PurposeApply, token))
	assert.Equal(t, 1, nonces.calls, "nonce を焼くこと")
}

// **署名を検証しないと、誰でも payload を組み立てて通せる。**
func TestIssuer_RejectsTamperedToken(t *testing.T) {
	i, nonces, clock := newTestIssuer(t)
	token, err := i.Issue(PurposeApply)
	require.NoError(t, err)
	*clock = clock.Add(DefaultMinAge)

	payload, mac, ok := strings.Cut(token, ".")
	require.True(t, ok)

	tests := []struct {
		name  string
		token string
	}{
		{name: "payload を差し替える", token: enc.EncodeToString([]byte("1:"+PurposeApply+":1:zz")) + "." + mac},
		{name: "MAC を差し替える", token: payload + "." + enc.EncodeToString([]byte("not-a-mac"))},
		{name: "区切りが無い", token: payload},
		{name: "空", token: ""},
		{name: "base64 でない", token: "!!!.???"},
		{name: "MAC だけ base64 でない", token: payload + ".???"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ErrorIs(t, i.Verify(context.Background(), PurposeApply, tt.token), ErrTokenInvalid)
		})
	}
	assert.Equal(t, 0, nonces.calls, "署名が通らないものは nonce を焼かない")
}

// 用途を跨いだ使い回しを弾く。将来 apply 以外に発行しても混ざらない。
func TestIssuer_RejectsPurposeMismatch(t *testing.T) {
	i, _, clock := newTestIssuer(t)
	token, err := i.Issue("other-purpose")
	require.NoError(t, err)
	*clock = clock.Add(DefaultMinAge)
	assert.ErrorIs(t, i.Verify(context.Background(), PurposeApply, token), ErrTokenInvalid)
}

// **早すぎる送信では nonce を焼かない。** 焼くと、待ってやり直した正規の
// 利用者が二度と通らなくなる。
func TestIssuer_TooSoonDoesNotBurnNonce(t *testing.T) {
	i, nonces, clock := newTestIssuer(t)
	token, err := i.Issue(PurposeApply)
	require.NoError(t, err)

	*clock = clock.Add(DefaultMinAge - time.Millisecond)
	assert.ErrorIs(t, i.Verify(context.Background(), PurposeApply, token), ErrTokenTooSoon)
	assert.Equal(t, 0, nonces.calls)

	// 待てば同じ token で通る。
	*clock = clock.Add(time.Second)
	require.NoError(t, i.Verify(context.Background(), PurposeApply, token))
}

// 時計が巻き戻った場合も「早すぎる」側に倒す (経過時間を測れないため)。
func TestIssuer_ClockGoingBackwardsIsTooSoon(t *testing.T) {
	i, _, clock := newTestIssuer(t)
	token, err := i.Issue(PurposeApply)
	require.NoError(t, err)
	*clock = clock.Add(-time.Hour)
	assert.ErrorIs(t, i.Verify(context.Background(), PurposeApply, token), ErrTokenTooSoon)
}

func TestIssuer_Expired(t *testing.T) {
	i, nonces, clock := newTestIssuer(t)
	token, err := i.Issue(PurposeApply)
	require.NoError(t, err)
	*clock = clock.Add(DefaultTTL + time.Second)
	assert.ErrorIs(t, i.Verify(context.Background(), PurposeApply, token), ErrTokenExpired)
	assert.Equal(t, 0, nonces.calls)
}

// **使い捨てにしないと 1 枚を無限に使い回される。**
func TestIssuer_NonceIsSingleUse(t *testing.T) {
	i, _, clock := newTestIssuer(t)
	token, err := i.Issue(PurposeApply)
	require.NoError(t, err)
	*clock = clock.Add(DefaultMinAge)

	require.NoError(t, i.Verify(context.Background(), PurposeApply, token))
	assert.ErrorIs(t, i.Verify(context.Background(), PurposeApply, token), ErrTokenUsed)
}

// 発行のたびに nonce が変わること (同じ payload なら 1 枚しか通らなくなる)。
func TestIssuer_NoncesDiffer(t *testing.T) {
	i, _, clock := newTestIssuer(t)
	a, err := i.Issue(PurposeApply)
	require.NoError(t, err)
	b, err := i.Issue(PurposeApply)
	require.NoError(t, err)
	require.NotEqual(t, a, b)

	*clock = clock.Add(DefaultMinAge)
	require.NoError(t, i.Verify(context.Background(), PurposeApply, a))
	require.NoError(t, i.Verify(context.Background(), PurposeApply, b))
}

// **nonce store の障害は素通しにしない。** captcha 未設定のインスタンスで
// 防波堤が 1 つも無い状態に戻る。
func TestIssuer_NonceStoreErrorIsNotFailOpen(t *testing.T) {
	i, nonces, clock := newTestIssuer(t)
	token, err := i.Issue(PurposeApply)
	require.NoError(t, err)
	*clock = clock.Add(DefaultMinAge)

	nonces.err = errors.New("redis down")
	verr := i.Verify(context.Background(), PurposeApply, token)
	require.Error(t, verr)
	assert.NotErrorIs(t, verr, ErrTokenInvalid)
	assert.NotErrorIs(t, verr, ErrTokenUsed)
}

// 鍵が違えば通らない (署名鍵がインスタンス固有であることの担保)。
func TestIssuer_OtherInstanceKeyDoesNotVerify(t *testing.T) {
	i, _, clock := newTestIssuer(t)
	token, err := i.Issue(PurposeApply)
	require.NoError(t, err)

	other := NewIssuer([]byte("another-key"), newFakeNonces())
	require.NotNil(t, other)
	other.now = func() time.Time { return clock.Add(DefaultMinAge) }
	assert.ErrorIs(t, other.Verify(context.Background(), PurposeApply, token), ErrTokenInvalid)
}

// 未配線 (鍵か nonce store が欠けている) は nil を返し、呼び出し側が
// 「保護が無い」として扱えるようにする。
func TestNewIssuer_RequiresSecretAndStore(t *testing.T) {
	assert.Nil(t, NewIssuer(nil, newFakeNonces()))
	assert.Nil(t, NewIssuer([]byte("k"), nil))

	var nilIssuer *Issuer
	assert.Equal(t, time.Duration(0), nilIssuer.MinWait())
	_, err := nilIssuer.Issue(PurposeApply)
	assert.ErrorIs(t, err, ErrTokenInvalid)
	assert.ErrorIs(t, nilIssuer.Verify(context.Background(), PurposeApply, "x"), ErrTokenInvalid)
}

func TestIssuer_MinWait(t *testing.T) {
	i, _, _ := newTestIssuer(t)
	assert.Equal(t, DefaultMinAge, i.MinWait())
}

// payload の形が壊れているものは全て invalid。**署名は通るが中身が壊れている**
// ケースを直接組み立てて確かめる (発行経路では作れない形)。
func TestIssuer_RejectsMalformedPayload(t *testing.T) {
	i, _, _ := newTestIssuer(t)
	forge := func(payload string) string {
		return enc.EncodeToString([]byte(payload)) + "." + enc.EncodeToString(i.sign([]byte(payload)))
	}
	tests := []struct {
		name    string
		payload string
	}{
		{name: "フィールドが足りない", payload: "1:" + PurposeApply + ":123"},
		{name: "version が違う", payload: "2:" + PurposeApply + ":123:nonce"},
		{name: "発行時刻が数値でない", payload: "1:" + PurposeApply + ":abc:nonce"},
		{name: "nonce が空", payload: "1:" + PurposeApply + ":123:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ErrorIs(t, i.Verify(context.Background(), PurposeApply, forge(tt.payload)), ErrTokenInvalid)
		})
	}
}

// 明示した timings が効くこと。0 以下は既定に戻す (minAge の 0 は許す)。
func TestNewIssuerWithTimings(t *testing.T) {
	nonces := newFakeNonces()
	assert.Equal(t, time.Second, NewIssuerWithTimings([]byte("k"), nonces, time.Second, time.Minute).MinWait())
	assert.Equal(t, time.Duration(0), NewIssuerWithTimings([]byte("k"), nonces, 0, time.Minute).MinWait())
	assert.Equal(t, DefaultMinAge, NewIssuerWithTimings([]byte("k"), nonces, -1, time.Minute).MinWait())
	assert.Equal(t, DefaultTTL, NewIssuerWithTimings([]byte("k"), nonces, 0, 0).ttl)
	assert.Nil(t, NewIssuerWithTimings(nil, nonces, 0, 0))
}

// minAge が 0 でも、時計が巻き戻った token は通さない。
// **負の age は `age < minAge` が拾う** (minAge は必ず 0 以上) ので、判定を
// 消したときにだけ落ちる。
func TestIssuer_NegativeAgeRejectedEvenWithZeroMinAge(t *testing.T) {
	nonces := newFakeNonces()
	i := NewIssuerWithTimings([]byte("k"), nonces, 0, time.Minute)
	require.NotNil(t, i)
	clock := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	i.now = func() time.Time { return clock }
	token, err := i.Issue(PurposeApply)
	require.NoError(t, err)

	clock = clock.Add(-time.Minute)
	assert.ErrorIs(t, i.Verify(context.Background(), PurposeApply, token), ErrTokenTooSoon)
	assert.Equal(t, 0, nonces.calls)
}
