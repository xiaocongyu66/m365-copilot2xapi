package m365

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
)

// OauthVerifier generates a high-entropy PKCE code verifier (43-128 chars,
// base64url without padding) as required by RFC 7636.
func OauthVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// OauthChallenge derives the S256 code_challenge from a verifier.
func OauthChallenge(v string) string {
	h := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// OauthAuthorizationURL builds the interactive browser authorize URL for the
// PKCE flow. All parameters are required by the Microsoft identity platform.
func OauthAuthorizationURL(endpoint, clientID, redirect, state, challenge, scope string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirect)
	q.Set("response_mode", "query")
	q.Set("scope", scope)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	return fmt.Sprintf("%s?%s", endpoint, q.Encode())
}
