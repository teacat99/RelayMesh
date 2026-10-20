package oauth

import (
	"encoding/base64"
	"regexp"
)

var pkceVerifierRe = regexp.MustCompile(`^[A-Za-z0-9\-._~]{43,128}$`)

// ValidateCodeVerifier RFC 7636 code_verifier。
func ValidateCodeVerifier(verifier string) bool {
	verifier = stringsTrim(verifier)
	return pkceVerifierRe.MatchString(verifier)
}

// ValidateCodeChallenge S256 challenge 必须为 43 字符 base64url。
func ValidateCodeChallenge(challenge string) bool {
	challenge = stringsTrim(challenge)
	if len(challenge) != 43 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(challenge)
	if err != nil || len(raw) != 32 {
		return false
	}
	return true
}
