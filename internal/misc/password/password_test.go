package password

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// withCost sets the work factor for one test and restores it afterwards.
func withCost(t *testing.T, c int) {
	t.Helper()
	prev := Cost()
	if err := SetCost(c); err != nil {
		t.Fatalf("SetCost(%d): %v", c, err)
	}
	t.Cleanup(func() { _ = SetCost(prev) })
}

func TestDefaultCostIsAboveUpstream(t *testing.T) {
	// upstream Misskey は全経路で cost 8。**既定でそこを下回らせない。**
	if DefaultCost <= 8 {
		t.Fatalf("DefaultCost=%d は upstream の 8 以下", DefaultCost)
	}
	if Cost() != DefaultCost {
		t.Fatalf("初期値が %d, want %d", Cost(), DefaultCost)
	}
}

func TestSetCost_RejectsOutOfRange(t *testing.T) {
	before := Cost()
	for _, c := range []int{MinCost - 1, MaxCost + 1, -1, 0, 100} {
		if err := SetCost(c); err == nil {
			t.Errorf("SetCost(%d) がエラーにならない", c)
		}
	}
	// **拒否したときに値を書き換えないこと。** 書き換えると、設定の書き間違いで
	// 弱いハッシュを量産する。
	if Cost() != before {
		t.Fatalf("拒否したのに cost が %d に変わった (was %d)", Cost(), before)
	}
}

func TestSetCost_AcceptsRange(t *testing.T) {
	for _, c := range []int{MinCost, DefaultCost, 12} {
		withCost(t, c)
		if Cost() != c {
			t.Fatalf("Cost()=%d, want %d", Cost(), c)
		}
	}
}

// Hash が実際に設定した cost を使っていること。**ここが効いていないと、
// 設定を足しただけで何も変わらない。**
func TestHash_UsesConfiguredCost(t *testing.T) {
	for _, c := range []int{MinCost, MinCost + 2} {
		withCost(t, c)
		h, err := Hash("hunter2")
		if err != nil {
			t.Fatal(err)
		}
		got, err := bcrypt.Cost([]byte(h))
		if err != nil {
			t.Fatal(err)
		}
		if got != c {
			t.Errorf("hash の cost=%d, want %d", got, c)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(h), []byte("hunter2")); err != nil {
			t.Errorf("自分で作ったハッシュを検証できない: %v", err)
		}
	}
}

// 73 byte 以上は bcrypt のエラーをそのまま返す。呼び出し側が errors.Is で
// 拾って 400 に変換しているので、包んで別物にしない (#1075)。
func TestHash_TooLongPassthrough(t *testing.T) {
	withCost(t, MinCost)
	_, err := Hash(strings.Repeat("a", 73))
	if !errors.Is(err, bcrypt.ErrPasswordTooLong) {
		t.Fatalf("bcrypt.ErrPasswordTooLong のはずが %v", err)
	}
}

func TestNeedsRehash(t *testing.T) {
	withCost(t, MinCost+2)
	weak, err := bcrypt.GenerateFromPassword([]byte("hunter2"), MinCost)
	if err != nil {
		t.Fatal(err)
	}
	current, err := Hash("hunter2")
	if err != nil {
		t.Fatal(err)
	}

	if !NeedsRehash(string(weak)) {
		t.Error("設定より弱いハッシュが焼き直し対象にならない")
	}
	if NeedsRehash(current) {
		t.Error("設定どおりのハッシュが焼き直し対象になっている")
	}

	// 設定より強いものは触らない。運用者が cost を下げたときに、既存の
	// 強いハッシュをわざわざ弱くする必要は無い。
	strong, err := bcrypt.GenerateFromPassword([]byte("hunter2"), MinCost+4)
	if err != nil {
		t.Fatal(err)
	}
	if NeedsRehash(string(strong)) {
		t.Error("設定より強いハッシュを焼き直そうとしている")
	}

	// **読めないものは触らない。** 「弱い」と判断して上書きすると、検証できない
	// まま別形式へ潰しかねない。
	for _, bad := range []string{"", "not-a-hash", "$2a$", "plaintext"} {
		if NeedsRehash(bad) {
			t.Errorf("読めないハッシュ %q を焼き直し対象にしている", bad)
		}
	}
}
