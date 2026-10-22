package agent

import (
	"fmt"
	"net/url"
	"strings"
)

// AgentWSPath 是 Hub 上通用 Agent 入口的路径。
const AgentWSPath = "/api/v1/terminal/agent/ws"

// ServerTarget 是规范化之后的服务端地址。
type ServerTarget struct {
	URL    string // 完整的 ws:// 或 wss:// 地址（含入口路径）
	Host   string // host:port，TOFU 记录的键
	Secure bool   // wss
}

// HTTPBase 返回同一台服务端的 HTTP(S) 基地址（不含末尾斜杠，保留部署在子路径下时的路径前缀）。
// Agent 用它拼出升级文件的下载地址：地址只由 Agent 自己配置的服务端推出，不接受 Hub 在帧里指定。
func (t ServerTarget) HTTPBase() (string, error) {
	u, err := url.Parse(t.URL)
	if err != nil {
		return "", fmt.Errorf("服务端地址无法解析: %w", err)
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		u.Scheme = "http"
	default:
		return "", fmt.Errorf("不支持的协议 %q", u.Scheme)
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), AgentWSPath)
	u.RawPath, u.RawQuery, u.Fragment = "", "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

// ParseServer 把用户给的服务端地址规范化：
//   - 允许省略协议（默认 https）；http/https 会转换为 ws/wss；
//   - 自动补全入口路径；不接受内嵌账号密码，丢弃查询串与片段。
func ParseServer(raw string) (ServerTarget, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ServerTarget{}, fmt.Errorf("服务端地址不能为空")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ServerTarget{}, fmt.Errorf("服务端地址无法解析: %w", err)
	}
	if u.User != nil {
		return ServerTarget{}, fmt.Errorf("服务端地址不能内嵌账号或密码")
	}
	if u.Hostname() == "" {
		return ServerTarget{}, fmt.Errorf("服务端地址缺少主机名")
	}
	secure := false
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		secure = true
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return ServerTarget{}, fmt.Errorf("不支持的协议 %q（请使用 https / wss，或在受控网络内显式使用 http / ws 并加 --insecure）", u.Scheme)
	}
	path := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(path, AgentWSPath) {
		path += AgentWSPath
	}
	u.Path = path
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""

	host := u.Host
	if u.Port() == "" {
		port := "80"
		if secure {
			port = "443"
		}
		host = u.Hostname() + ":" + port
		if strings.Contains(u.Hostname(), ":") { // IPv6 字面量
			host = "[" + u.Hostname() + "]:" + port
		}
	}
	return ServerTarget{URL: u.String(), Host: host, Secure: secure}, nil
}
