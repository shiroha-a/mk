package password

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

func cherryPickFixture(plain string) string {
	salt := []byte("0123456789abcdef")
	digest := argon2.IDKey([]byte(plain), salt, 3, 64*1024, 4, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=4$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest))
}

func TestVerify_CherryPickArgon2id(t *testing.T) {
	hash := cherryPickFixture("correct horse battery staple")
	if scheme, out := Verify(context.Background(), hash, "correct horse battery staple"); out != OutcomeMatch || scheme != SchemeArgon2id {
		t.Fatalf("Verify(correct)=(%v,%v), want (%v,%v)", scheme, out, SchemeArgon2id, OutcomeMatch)
	}
	// **平文違いは Mismatch であって Unsupported ではない** (#2849)。ここを
	// 取り違えると、日常的な打ち間違いが警告ログを埋める。
	if scheme, out := Verify(context.Background(), hash, "wrong"); out != OutcomeMismatch || scheme != SchemeArgon2id {
		t.Fatalf("Verify(wrong)=(%v,%v), want (%v,%v)", scheme, out, SchemeArgon2id, OutcomeMismatch)
	}
}

func TestVerify_Bcrypt(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if scheme, out := Verify(context.Background(), string(hash), "hunter2"); out != OutcomeMatch || scheme != SchemeBcrypt {
		t.Fatalf("Verify(bcrypt)=(%v,%v), want (%v,%v)", scheme, out, SchemeBcrypt, OutcomeMatch)
	}
	if scheme, out := Verify(context.Background(), string(hash), "wrong"); out != OutcomeMismatch || scheme != SchemeBcrypt {
		t.Fatalf("Verify(bcrypt wrong)=(%v,%v), want (%v,%v)", scheme, out, SchemeBcrypt, OutcomeMismatch)
	}
	// 壊れた bcrypt hash は「平文違い」ではなく**保存データの異常**なので
	// Unsupported に倒す (#2849)。
	if scheme, out := Verify(context.Background(), "$2a$broken", "hunter2"); out != OutcomeUnsupported || scheme != SchemeBcrypt {
		t.Fatalf("Verify(bcrypt broken)=(%v,%v), want (%v,%v)", scheme, out, SchemeBcrypt, OutcomeUnsupported)
	}
}

func TestVerify_RejectsMalformedOrUnsupportedHashes(t *testing.T) {
	valid := cherryPickFixture("hunter2")
	parts := strings.Split(valid, "$")
	shortSalt := []byte("short")
	shortSaltDigest := argon2.IDKey([]byte("hunter2"), shortSalt, 3, 64*1024, 4, 32)
	shortSaltHash := strings.Join([]string{
		"", parts[1], parts[2], parts[3],
		base64.RawStdEncoding.EncodeToString(shortSalt),
		base64.RawStdEncoding.EncodeToString(shortSaltDigest),
	}, "$")
	tests := []struct {
		name   string
		hash   string
		scheme Scheme
	}{
		{"unknown", "$scrypt$bad", SchemeUnknown},
		{"short", "$argon2id$v=19", SchemeArgon2id},
		{"argon2i", strings.Replace(valid, "$argon2id$", "$argon2i$", 1), SchemeUnknown},
		{"version", strings.Replace(valid, "v=19", "v=16", 1), SchemeArgon2id},
		{"memory", strings.Replace(valid, "m=65536", "m=4294967295", 1), SchemeArgon2id},
		{"iterations", strings.Replace(valid, "t=3", "t=4", 1), SchemeArgon2id},
		{"parallelism", strings.Replace(valid, "p=4", "p=255", 1), SchemeArgon2id},
		{"missing parameter", strings.Replace(valid, "m=65536,t=3,p=4", "m=65536,t=3", 1), SchemeArgon2id},
		{"duplicate parameter", strings.Replace(valid, "m=65536,t=3,p=4", "m=65536,t=3,p=4,p=4", 1), SchemeArgon2id},
		{"parameter order", strings.Replace(valid, "m=65536,t=3,p=4", "t=3,m=65536,p=4", 1), SchemeArgon2id},
		{"bad salt base64", strings.Join([]string{"", parts[1], parts[2], parts[3], "***", parts[5]}, "$"), SchemeArgon2id},
		{"short salt", shortSaltHash, SchemeArgon2id},
		{"bad digest base64", strings.Join([]string{"", parts[1], parts[2], parts[3], parts[4], "***"}, "$"), SchemeArgon2id},
		{"short digest", strings.Join([]string{"", parts[1], parts[2], parts[3], parts[4], base64.RawStdEncoding.EncodeToString(make([]byte, 16))}, "$"), SchemeArgon2id},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// **Unsupported であることまで見る。** 単に「false」だと平文違いと
			// 区別が付かず、呼び出し側が warn を出す判断ができない (#2849)。
			scheme, out := Verify(context.Background(), tt.hash, "hunter2")
			if out != OutcomeUnsupported || scheme != tt.scheme {
				t.Fatalf("Verify=(%v,%v), want (%v,%v)", scheme, out, tt.scheme, OutcomeUnsupported)
			}
		})
	}
}

func TestVerify_Argon2idHonorsContextWhileWaitingForSlot(t *testing.T) {
	hash := cherryPickFixture("hunter2")
	const expectedMaxConcurrency = 4
	if err := argon2VerifySlots.Acquire(context.Background(), expectedMaxConcurrency); err != nil {
		t.Fatal(err)
	}
	defer argon2VerifySlots.Release(expectedMaxConcurrency)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	// **Unavailable であることまで見る。** Mismatch に倒すと呼び出し側が 403 を
	// 返してしまい、正しいパスワードの利用者に不要な reset をさせる (#2849)。
	if scheme, out := Verify(ctx, hash, "hunter2"); out != OutcomeUnavailable || scheme != SchemeArgon2id {
		t.Fatalf("Verify(canceled)=(%v,%v), want (%v,%v)", scheme, out, SchemeArgon2id, OutcomeUnavailable)
	}
}

// ProfileForLog は診断に要る version / parameter だけを返し、**salt と digest を
// 返さない** (#2849)。ここが漏れると、未対応 profile の警告を出すたびに hash 本体が
// ログへ流れる。
func TestProfileForLog_OmitsSaltAndDigest(t *testing.T) {
	hash := cherryPickFixture("hunter2")
	parts := strings.Split(hash, "$")
	got := ProfileForLog(hash)

	if got != "argon2id v=19 m=65536,t=3,p=4" {
		t.Fatalf("ProfileForLog(argon2id)=%q", got)
	}
	if strings.Contains(got, parts[4]) {
		t.Fatal("salt がログ用文字列に含まれている")
	}
	if strings.Contains(got, parts[5]) {
		t.Fatal("digest がログ用文字列に含まれている")
	}

	bc, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if got := ProfileForLog(string(bc)); got != "bcrypt" {
		t.Fatalf("ProfileForLog(bcrypt)=%q", got)
	}
	if strings.Contains(ProfileForLog(string(bc)), string(bc)[7:]) {
		t.Fatal("bcrypt hash がログ用文字列に含まれている")
	}
	if got := ProfileForLog("$scrypt$bad"); got != "unrecognized" {
		t.Fatalf("ProfileForLog(unknown)=%q", got)
	}
}

// OK は OutcomeMatch だけを真にする (#2849)。
//
// **ここが緩むと認証が破れる。** 例えば OutcomeUnavailable を OK に含めると、
// 検証枠を取れなかっただけのリクエストがログイン成功になる。呼び出し側は
// `outcome.OK()` を素通しで passwordOK に使うので、この 1 関数が最後の砦。
func TestOutcome_OKOnlyForMatch(t *testing.T) {
	for _, tc := range []struct {
		out  Outcome
		want bool
	}{
		{OutcomeMatch, true},
		{OutcomeMismatch, false},
		{OutcomeUnsupported, false},
		{OutcomeUnavailable, false},
	} {
		if got := tc.out.OK(); got != tc.want {
			t.Fatalf("Outcome(%v).OK()=%v, want %v", tc.out, got, tc.want)
		}
	}
}
