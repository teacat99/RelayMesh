package mcp

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"
)

type clientIPKeyType struct{}

var clientIPKey = clientIPKeyType{}

// WithClientIP 把经可信代理解析后的客户端 IP 放进上下文，由 HTTP 接入层（Gin）在转交给 MCP 服务前调用。
// MCP 服务自身不解析 X-Forwarded-For：信任哪些代理是接入层的职责。
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey, strings.TrimSpace(ip))
}

func clientIPFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(clientIPKey).(string); ok {
		return v
	}
	return ""
}

// requestClientIP 优先取接入层放入上下文的 IP，否则退回直连对端地址。
func requestClientIP(r *http.Request) string {
	if ip := clientIPFromContext(r.Context()); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

// ipAllowedByRules 判断 IP 是否命中白名单（CIDR 或单个 IP）。规则为空表示不限制。
// 无法解析的来源地址按拒绝处理（fail-closed）。
func ipAllowedByRules(ip string, rules []string) bool {
	if len(rules) == 0 {
		return true
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		if _, network, err := net.ParseCIDR(rule); err == nil {
			if network.Contains(parsed) {
				return true
			}
			continue
		}
		if single := net.ParseIP(rule); single != nil && single.Equal(parsed) {
			return true
		}
	}
	return false
}

// credentialUsageInterval 是「最近使用」写库的最小间隔：每个凭据每分钟最多写一次。
const credentialUsageInterval = time.Minute
