// Package oauth implements the OAuth2 / IndieAuth authorization-code provider
// (authorize / decision / token) advertised by the RFC8414 discovery document,
// mirroring upstream Misskey's OAuth2ProviderService (#1899). PKCE (S256) is
// mandatory and client identification follows the IndieAuth client-id-as-URL
// model with remote client metadata discovery.
package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// verifyS256 reports whether a PKCE code_verifier matches the stored
// code_challenge using the S256 method: BASE64URL(SHA256(verifier)) == challenge.
// Comparison is constant-time. Empty inputs never match.
func verifyS256(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}
