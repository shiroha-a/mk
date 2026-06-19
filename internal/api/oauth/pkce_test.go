package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
)

func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestVerifyS256(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge := challengeFor(verifier)

	assert.True(t, verifyS256(verifier, challenge), "matching verifier")
	assert.False(t, verifyS256("wrong-verifier", challenge), "non-matching verifier")
	assert.False(t, verifyS256("", challenge), "empty verifier")
	assert.False(t, verifyS256(verifier, ""), "empty challenge")
	assert.False(t, verifyS256("", ""), "both empty")
}
