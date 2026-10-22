package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 能力 URL：/mcp/c/<令牌>。给无法发送请求头的客户端（例如只能填一个网址的自定义连接器）使用。
//
// 设计要点（与普通令牌刻意分开）：
//   - 能力令牌只在这条路径上有效：请求头、?token=、旧式 /mcp/<令牌> 路径一律不认它；
//     反过来，普通令牌也不能放进这条路径。同一把钥匙不会有两种出示方式，泄露的网址不能当请求头令牌用。
//   - 路径是否属于能力 URL 路由，只看路径本身；一旦进入这条路由，请求头与查询串里的任何凭据都被忽略，
//     环境变量令牌与「开放访问」永远不会兜底。
//   - 令牌形状（专用前缀 + 64 位十六进制）在查库之前检查，乱填的网址不会产生数据库访问。
//   - 过期 / 停用 / 来源 IP 白名单 / 吊销与普通凭据共用同一套检查，吊销即时生效（下一次请求就查不到）。
//   - 未知网址与形状错误的网址返回同一条泛化错误，不透露任何凭据是否存在。

const capabilityPathPrefix = "/mcp/c"

// errCapabilityDenied 是未知 / 形状错误 / 不是能力凭据时统一返回的错误。
var errCapabilityDenied = errors.New("Unauthorized: invalid capability URL")

// capabilitySecretFromPath 识别能力 URL 路由：/mcp/c/<令牌>（允许结尾斜杠）。
// 第二个返回值表示路径属于能力路由（即使令牌缺失或路径多出层级，此时 secret 为空）。
func capabilitySecretFromPath(path string) (secret string, isCapabilityRoute bool) {
	if path != capabilityPathPrefix && !strings.HasPrefix(path, capabilityPathPrefix+"/") {
		return "", false
	}
	rest := strings.Trim(strings.TrimPrefix(path, capabilityPathPrefix), "/")
	if rest == "" || strings.Contains(rest, "/") {
		return "", true
	}
	return rest, true
}

// resolveCapability 解析能力 URL 凭据。任何失败都不会回退到环境变量令牌或开放访问。
func (s *Server) resolveCapability(ctx context.Context, secret string) (*CredentialContext, error) {
	if !store.IsCapabilityTokenShape(secret) {
		return nil, errCapabilityDenied
	}
	cred, err := s.store.FindCredentialByToken(ctx, secret)
	if err != nil {
		return nil, fmt.Errorf("internal error checking credentials: %w", err)
	}
	if cred == nil || !cred.Capability {
		return nil, errCapabilityDenied
	}
	return s.admitCredential(ctx, cred, true)
}

// admitCredential 对数据库凭据做停用 / 过期 / 来源 / 约束检查，通过后登记使用并返回调用上下文。
// 普通通道与能力 URL 通道共用，保证两条通道的限制不会出现差异。
func (s *Server) admitCredential(ctx context.Context, cred *model.MCPCredential, viaCapability bool) (*CredentialContext, error) {
	return s.admitCredentialInner(ctx, cred, viaCapability, false)
}

// admitOAuthGrantCredential 仅用于 rmo_ 访问令牌解析出的隐藏 OAuth 凭据（禁止直接用内部 Token 字段作 Bearer）。
func (s *Server) admitOAuthGrantCredential(ctx context.Context, cred *model.MCPCredential) (*CredentialContext, error) {
	return s.admitCredentialInner(ctx, cred, false, true)
}

func (s *Server) admitCredentialInner(ctx context.Context, cred *model.MCPCredential, viaCapability, oauthGrant bool) (*CredentialContext, error) {
	if cred.Hidden && !oauthGrant {
		return nil, fmt.Errorf("Unauthorized: invalid or missing MCP token")
	}
	if !cred.IsActive {
		return nil, fmt.Errorf("Unauthorized: credential %q is disabled", cred.Name)
	}
	if cred.ExpiresAt != nil && !cred.ExpiresAt.After(time.Now()) {
		return nil, fmt.Errorf("Unauthorized: credential %q has expired", cred.Name)
	}
	remoteIP := clientIPFromContext(ctx)
	if !ipAllowedByRules(remoteIP, cred.AllowedCIDRs) {
		// 不回显白名单内容，也不透露来源地址是否格式有效。
		return nil, fmt.Errorf("Unauthorized: source address is not allowed for credential %q", cred.Name)
	}
	if problem := cred.CapabilityProblem(); problem != "" {
		return nil, fmt.Errorf("Unauthorized: credential %q is misconfigured: %s", cred.Name, problem)
	}
	s.touchCredentialUsage(ctx, cred.ID, remoteIP)
	return &CredentialContext{
		CredentialID:   cred.ID,
		CredentialName: cred.Name,
		HostName:       cred.HostName,
		Permissions:    cred.Permissions,
		Source:         "db_credential",
		Credential:     cred,
		ViaCapability:  viaCapability,
	}, nil
}
