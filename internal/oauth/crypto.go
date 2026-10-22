package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

const (
	AccessTokenPrefix  = "rmo_"
	accessTokenBytes   = 32
	authCodeBytes      = 32
	internalCredPrefix = "rm-oauth-internal-"
)

func GenerateAccessToken() (plain string, hash string, err error) {
	b := make([]byte, accessTokenBytes)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	plain = AccessTokenPrefix + hex.EncodeToString(b)
	hash = HashToken(plain)
	return plain, hash, nil
}

func GenerateAuthCode() (string, error) {
	b := make([]byte, authCodeBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func GenerateRefreshToken() (plain string, hash string, err error) {
	return GenerateAccessToken()
}

func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func TokenEqualHash(plain, hash string) bool {
	if plain == "" || hash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(HashToken(plain)), []byte(hash)) == 1
}

func IsAccessTokenFormat(s string) bool {
	return stringsHasPrefix(s, AccessTokenPrefix)
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// PKCE S256
func VerifyPKCE(challenge, method, verifier string) bool {
	method = stringsTrim(method)
	if method != "S256" {
		return false
	}
	verifier = stringsTrim(verifier)
	if verifier == "" {
		return false
	}
	h := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(h[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

func stringsTrim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func InternalCredentialToken(grantID uint) string {
	return fmt.Sprintf("%s%d", internalCredPrefix, grantID)
}
