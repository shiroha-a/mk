package misc

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestSecureRandomHex_Length(t *testing.T) {
	for _, n := range []int{8, 16, 32, 64} {
		s := SecureRandomHex(n)
		if len(s) != n {
			t.Errorf("SecureRandomHex(%d) returned length %d", n, len(s))
		}
	}
}

func TestSecureRandomHex_Unique(t *testing.T) {
	a := SecureRandomHex(32)
	b := SecureRandomHex(32)
	if a == b {
		t.Error("two calls returned the same value")
	}
}

func TestSecureRandomHex_OddLength(t *testing.T) {
	s := SecureRandomHex(7)
	if len(s) != 7 {
		t.Errorf("expected length 7, got %d", len(s))
	}
}

// failingReader always returns an error.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("simulated rand failure")
}

func TestSecureRandomHex_PanicsOnRandFailure(t *testing.T) {
	orig := randReader
	randReader = failingReader{}
	defer func() { randReader = orig }()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic, got none")
		}
		msg, ok := r.(string)
		if !ok || len(msg) == 0 {
			t.Fatalf("expected string panic message, got %v", r)
		}
	}()
	_ = SecureRandomHex(16)
}

func TestSecureRandomString_LengthAndCharset(t *testing.T) {
	for _, n := range []int{1, 8, 16, 64} {
		s := SecureRandomString(n, AlphanumericChars)
		if len(s) != n {
			t.Errorf("SecureRandomString(%d) returned length %d", n, len(s))
		}
		for _, c := range s {
			if !strings.ContainsRune(AlphanumericChars, c) {
				t.Errorf("char %q not in AlphanumericChars", c)
			}
		}
	}
}

func TestSecureRandomString_EmptyInputs(t *testing.T) {
	if got := SecureRandomString(0, AlphanumericChars); got != "" {
		t.Errorf("length 0 should return empty, got %q", got)
	}
	if got := SecureRandomString(-1, AlphanumericChars); got != "" {
		t.Errorf("negative length should return empty, got %q", got)
	}
	if got := SecureRandomString(8, ""); got != "" {
		t.Errorf("empty charset should return empty, got %q", got)
	}
}

func TestSecureRandomString_SingleCharCharset(t *testing.T) {
	// 単一文字の charset では全桁がその文字になる (modulo が常に 0)。
	if got := SecureRandomString(5, "x"); got != "xxxxx" {
		t.Errorf("expected xxxxx, got %q", got)
	}
}

func TestSecureRandomString_PanicsOnRandFailure(t *testing.T) {
	orig := randReader
	randReader = failingReader{}
	defer func() { randReader = orig }()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	_ = SecureRandomString(8, AlphanumericChars)
}

// limitedReader returns exactly n bytes then io.EOF, useful for verifying
// that SecureRandomHex requests the right amount of bytes.
type limitedReader struct {
	remaining int
}

func (r *limitedReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > r.remaining {
		n = r.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = 0xAB
	}
	r.remaining -= n
	return n, nil
}

// native token は「16 文字」と「62 文字集合」の両方が要件。
//
// 長さは drop-in の制約 (TS の isNativeUserToken が長さだけで判別する)、
// 文字集合は強度。**16 進にすると見た目は同じ 16 文字でも 64 bit しかなく、
// upstream の secureRndstr(16) (約 95 bit) より弱くなる。**
func TestNewNativeToken(t *testing.T) {
	seen := map[string]bool{}
	var outside []string
	hexOnly := true
	for range 200 {
		tok := NewNativeToken()
		if len(tok) != NativeTokenLength {
			t.Fatalf("長さ %d, want %d", len(tok), NativeTokenLength)
		}
		for _, r := range tok {
			if !strings.ContainsRune(AlphanumericChars, r) {
				outside = append(outside, string(r))
			}
			if !strings.ContainsRune("0123456789abcdef", r) {
				hexOnly = false
			}
		}
		if seen[tok] {
			t.Fatalf("同じ token が 2 度出た: %q", tok)
		}
		seen[tok] = true
	}
	if len(outside) > 0 {
		t.Errorf("英数字の外の文字が出た: %v", outside)
	}
	// 200 本 * 16 文字を引いて一度も 16 進の外が出ないのは確率的にありえない。
	if hexOnly {
		t.Error("16 進の範囲しか出ていない = 文字集合が狭まっている")
	}
}
