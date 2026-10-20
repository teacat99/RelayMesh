package docs

import (
	"crypto/sha256"
	"encoding/hex"
)

// BodyHash 计算正文 SHA-256 十六进制摘要。
func BodyHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
