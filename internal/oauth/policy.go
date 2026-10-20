package oauth

import (
	"net/url"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func ClientHostAllowed(policy model.OAuthPolicy, clientID string, redirectURI string) (allowed bool, warn bool) {
	// CIMD: client_id 是 https URL
	if strings.HasPrefix(strings.ToLower(clientID), "https://") {
		u, err := url.Parse(clientID)
		if err != nil {
			return false, true
		}
		host := strings.ToLower(u.Hostname())
		if policy.AllowChatGPT && (host == "chatgpt.com" || strings.HasSuffix(host, ".chatgpt.com")) {
			return true, false
		}
		if policy.AllowClaude && (host == "claude.ai" || strings.HasSuffix(host, ".claude.ai")) {
			return true, false
		}
		for _, h := range policy.ClientHostAllowlist {
			h = strings.ToLower(strings.TrimSpace(h))
			if h != "" && host == h {
				return true, false
			}
		}
		return false, true
	}
	// DCR 客户端：看 redirect 主机
	if IsLoopbackRedirect(redirectURI) {
		if policy.AllowLoopback {
			return true, true
		}
		return false, true
	}
	ru, err := url.Parse(redirectURI)
	if err != nil {
		return false, true
	}
	host := strings.ToLower(ru.Hostname())
	if policy.AllowChatGPT && strings.Contains(host, "chatgpt") {
		return true, false
	}
	if policy.AllowClaude && strings.Contains(host, "claude") {
		return true, false
	}
	for _, h := range policy.ClientHostAllowlist {
		if strings.EqualFold(strings.TrimSpace(h), host) {
			return true, false
		}
	}
	return false, true
}

func ScopesToPermissions(scopes []string) (model.Permissions, bool) {
	var p model.Permissions
	ok := false
	for _, sc := range scopes {
		switch strings.TrimSpace(sc) {
		case "exec":
			p.Terminal = true
			ok = true
		case "exec:files":
			p.TerminalFiles = true
			ok = true
		case "relay":
			// 首发不开放
		}
	}
	if !p.Terminal && p.TerminalFiles {
		// 文件权限通常也需要 exec 总闸里的终端执行开关；仍允许只有 files
		ok = true
	}
	return p, ok
}

func ParseScopeString(scope string) []string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return nil
	}
	return strings.Fields(scope)
}
