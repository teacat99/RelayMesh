package config

import (
	"fmt"
	"strings"
)

// DefaultJWTSecret 是历史版本内置的 JWT 签名密钥默认值。
//
// 它随开源代码公开，任何人都能用它伪造管理员令牌，因此现在只作为「未配置」的哨兵值：
// 启动时若环境变量缺省或强度不足，会被自动生成并持久化的随机密钥取代，永远不会被用于签名。
const DefaultJWTSecret = "relaymesh-secret-key-change-in-production"

const (
	// MinJWTSecretLength 是 JWT 签名密钥的最小字节数（HS256 至少应有 256 bit 熵的量级，README 亦要求不少于 32 字符）。
	MinJWTSecretLength = 32
	// minJWTSecretDistinct 用于拦截 "aaaa…" / "abababab…" 这类长度够但毫无熵的值。
	// 32 位随机十六进制串的不同字符数期望约 14，低于 8 的概率可以忽略。
	minJWTSecretDistinct = 8
)

// weakJWTSecretMarkers 收录默认值、文档示例与常见占位写法中的特征片段（小写比较）。
// 片段都足够长，随机生成的密钥不可能误命中。
var weakJWTSecretMarkers = []string{
	"change-in-production",
	"relaymesh-secret",
	"yourrandomjwtsecret",
	"changeme",
	"change_me",
	"change-me",
	"your-secret",
	"your_secret",
	"yoursecret",
	"test-secret",
	"test-jwt-secret",
	"secret-key",
}

// JWTSecretWeakness 判断 JWT 签名密钥是否足够强。返回空字符串表示合格，否则返回不合格的原因（不含密钥内容，可安全写入日志）。
func JWTSecretWeakness(secret string) string {
	if secret == "" {
		return "为空"
	}
	if secret == DefaultJWTSecret {
		return "为内置默认值"
	}
	if len(secret) < MinJWTSecretLength {
		return fmt.Sprintf("长度不足 %d 字符", MinJWTSecretLength)
	}

	lowered := strings.ToLower(secret)
	for _, marker := range weakJWTSecretMarkers {
		if strings.Contains(lowered, marker) {
			return "疑似默认值或文档示例占位符"
		}
	}

	distinct := make(map[rune]struct{}, len(secret))
	for _, r := range secret {
		distinct[r] = struct{}{}
	}
	if len(distinct) < minJWTSecretDistinct {
		return "字符种类过少，缺乏随机性"
	}
	return ""
}

// IsStrongJWTSecret 是 JWTSecretWeakness 的布尔形式，供终端硬门禁等需要「是/否」判断的场景使用。
func IsStrongJWTSecret(secret string) bool {
	return JWTSecretWeakness(secret) == ""
}
