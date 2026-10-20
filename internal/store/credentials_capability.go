package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

const (
	// CapabilityTokenPrefix 是能力 URL 凭据令牌的专用前缀：只有这一类凭据使用它，普通凭据不得使用。
	// 固定前缀与固定长度也让服务端能在查库之前就丢弃形状不对的路径，随手乱填的网址不会产生数据库访问。
	CapabilityTokenPrefix = "rmc-"
	capabilityTokenBytes  = 32
	capabilityTokenLength = len(CapabilityTokenPrefix) + capabilityTokenBytes*2
)

// GenerateCapabilityToken 生成能力 URL 凭据的令牌：专用前缀 + 32 字节随机数的十六进制。
func GenerateCapabilityToken() (string, error) {
	b := make([]byte, capabilityTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return CapabilityTokenPrefix + hex.EncodeToString(b), nil
}

// IsCapabilityTokenShape 判断字符串是否具有能力 URL 令牌的形状（前缀 + 64 位小写十六进制）。
// 它只是廉价的形状检查，不代表令牌有效。
func IsCapabilityTokenShape(s string) bool {
	if len(s) != capabilityTokenLength || !strings.HasPrefix(s, CapabilityTokenPrefix) {
		return false
	}
	for _, r := range s[len(CapabilityTokenPrefix):] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// validateCapabilityOnCreate 校验新建的凭据：能力 URL 凭据必须满足约束、必须已逐台授权主机、令牌只能由服务端生成；
// 普通凭据不得占用能力 URL 的令牌前缀。
func validateCapabilityOnCreate(c *model.MCPCredential) error {
	if !c.Capability {
		if strings.HasPrefix(c.Token, CapabilityTokenPrefix) {
			return NewInvalidInputError(fmt.Sprintf("令牌前缀 %s 保留给能力 URL 凭据", CapabilityTokenPrefix))
		}
		return nil
	}
	if strings.TrimSpace(c.Token) != "" {
		return NewInvalidInputError("能力 URL 凭据的令牌只能由服务端生成")
	}
	if problem := c.CapabilityProblem(); problem != "" {
		return NewInvalidInputError(problem)
	}
	if len(c.TerminalHosts) == 0 {
		return NewInvalidInputError("能力 URL 凭据必须逐台授权主机")
	}
	return nil
}

// validateCapabilityOnUpdate 校验对能力 URL 凭据的更新：权限与主机授权改动后仍须满足约束（收紧总是允许的）。
func validateCapabilityOnUpdate(cur *model.MCPCredential, updates map[string]any) error {
	if !cur.Capability {
		return nil
	}
	next := *cur
	if v, ok := updates["permissions"]; ok {
		if perms, isPerms := v.(model.Permissions); isPerms {
			next.Permissions = perms
			// 取消终端权限是收紧：凭据变成什么都做不了，允许；其余约束照常检查。
			if !perms.Terminal {
				next.Permissions.Terminal = true
			}
		}
	}
	if v, ok := updates["terminal_hosts"]; ok {
		if hosts, isHosts := v.(model.StringList); isHosts {
			next.TerminalHosts = hosts
		}
	}
	if problem := next.CapabilityProblem(); problem != "" {
		return NewInvalidInputError(problem)
	}
	return nil
}
