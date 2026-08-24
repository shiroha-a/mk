package password

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

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
	if scheme, ok := Verify(hash, "correct horse battery staple"); !ok || scheme != SchemeArgon2id {
		t.Fatalf("Verify(correct)=(%v,%v), want (%v,true)", scheme, ok, SchemeArgon2id)
	}
	if scheme, ok := Verify(hash, "wrong"); ok || scheme != SchemeArgon2id {
		t.Fatalf("Verify(wrong)=(%v,%v), want (%v,false)", scheme, ok, SchemeArgon2id)
	}
}

func TestVerify_Bcrypt(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if scheme, ok := Verify(string(hash), "hunter2"); !ok || scheme != SchemeBcrypt {
		t.Fatalf("Verify(bcrypt)=(%v,%v), want (%v,true)", scheme, ok, SchemeBcrypt)
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
			scheme, ok := Verify(tt.hash, "hunter2")
			if ok || scheme != tt.scheme {
				t.Fatalf("Verify=(%v,%v), want (%v,false)", scheme, ok, tt.scheme)
			}
		})
	}
}
