package password

import (
	"crypto/subtle"
	"encoding/base64"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// Scheme identifies the password hash format selected by Verify.
type Scheme uint8

const (
	SchemeUnknown Scheme = iota
	SchemeBcrypt
	SchemeArgon2id
)

const cherryPickArgon2Params = "m=65536,t=3,p=4"

// Verify compares plain with stored using the hash format declared by stored.
// Malformed and unsupported hashes fail closed.
func Verify(stored, plain string) (Scheme, bool) {
	if strings.HasPrefix(stored, "$2") {
		return SchemeBcrypt, bcrypt.CompareHashAndPassword([]byte(stored), []byte(plain)) == nil
	}
	if !strings.HasPrefix(stored, "$argon2id$") {
		return SchemeUnknown, false
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != cherryPickArgon2Params {
		return SchemeArgon2id, false
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return SchemeArgon2id, false
	}
	want, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(want) != 32 {
		return SchemeArgon2id, false
	}
	got := argon2.IDKey([]byte(plain), salt, 3, 64*1024, 4, 32)
	return SchemeArgon2id, subtle.ConstantTimeCompare(got, want) == 1
}
