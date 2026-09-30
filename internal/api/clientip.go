package api

import (
	"fmt"
	"log"
	"net"
	"strings"

	"github.com/gin-gonic/gin"
)

// 可信代理模式，用于启动日志与测试断言。
const (
	trustedProxyModeDefault = "default" // 未配置：回环 + 私网
	trustedProxyModeCustom  = "custom"  // 管理员显式列出网段
	trustedProxyModeNone    = "none"    // 不信任任何代理
	trustedProxyModeAll     = "all"     // 信任全部（升级前的旧行为，不安全）
)

// defaultTrustedProxies 是未配置 RELAYMESH_TRUSTED_PROXIES 时信任的「直连对端」网段。
// 只有当直连对端落在这些网段内时，才会采信它携带的 X-Forwarded-For / X-Real-IP；
// 覆盖同机反代、Docker 网桥、WSL2 与常见内网反代（1Panel / OpenResty / Nginx / Caddy 等）。
var defaultTrustedProxies = []string{
	"127.0.0.0/8",
	"::1/128",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"fc00::/7",
}

// trustAllProxies 是关键字 "*" 展开后的网段，等价于 Gin 未配置时的默认行为。
var trustAllProxies = []string{"0.0.0.0/0", "::/0"}

// ResolveTrustedProxies 解析 RELAYMESH_TRUSTED_PROXIES，返回可直接传给 gin.Engine.SetTrustedProxies 的列表。
//
//   - 空值：默认网段（回环 + 私网）；
//   - none：不信任任何代理，ClientIP() 恒为直连对端地址（返回 nil 列表）；
//   - *：信任全部代理（升级前的旧行为；X-Forwarded-For 可被任意客户端伪造，仅在明确知道后果时使用）；
//   - 其他：逗号 / 空白分隔的 IP 或 CIDR，关键字 default 展开为默认网段（例如 "default,203.0.113.0/24"）。
//     显式列出即完全以所列为准，不会隐式追加默认网段。
//
// none 与 * 必须单独出现。任何无法识别的条目都会返回错误，由调用方决定回退策略。
func ResolveTrustedProxies(raw string) (proxies []string, mode string, err error) {
	tokens := splitProxyTokens(raw)
	if len(tokens) == 0 {
		return append([]string(nil), defaultTrustedProxies...), trustedProxyModeDefault, nil
	}

	for _, token := range tokens {
		switch strings.ToLower(token) {
		case "none":
			if len(tokens) != 1 {
				return nil, "", fmt.Errorf("关键字 none 不能与其他条目同时使用")
			}
			return nil, trustedProxyModeNone, nil
		case "*":
			if len(tokens) != 1 {
				return nil, "", fmt.Errorf("关键字 * 不能与其他条目同时使用")
			}
			return append([]string(nil), trustAllProxies...), trustedProxyModeAll, nil
		}
	}

	seen := make(map[string]bool, len(tokens))
	add := func(entry string) {
		if !seen[entry] {
			seen[entry] = true
			proxies = append(proxies, entry)
		}
	}
	for _, token := range tokens {
		if strings.EqualFold(token, "default") {
			for _, entry := range defaultTrustedProxies {
				add(entry)
			}
			continue
		}
		if strings.Contains(token, "/") {
			_, network, parseErr := net.ParseCIDR(token)
			if parseErr != nil {
				return nil, "", fmt.Errorf("无效的 CIDR %q: %v", token, parseErr)
			}
			add(network.String())
			continue
		}
		ip := net.ParseIP(token)
		if ip == nil {
			return nil, "", fmt.Errorf("无效的 IP 地址 %q", token)
		}
		add(ip.String())
	}
	return proxies, trustedProxyModeCustom, nil
}

func splitProxyTokens(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
}

// ConfigureTrustedProxies 按 RELAYMESH_TRUSTED_PROXIES 设置 Gin 的可信代理网段。
//
// 背景：Gin 默认信任全部代理，此时 c.ClientIP() 会采信客户端自带的 X-Forwarded-For 最左项，
// 而登录反爆破（RateLimiter）与终端预认证限流都以它为键，攻击者每次换一个头值即可绕过锁定。
// 配置错误时回退为默认网段（回环 + 私网）而不是 Gin 的「信任全部」，宁可少信任也不多信任。
func ConfigureTrustedProxies(engine *gin.Engine, raw string) {
	proxies, mode, err := ResolveTrustedProxies(raw)
	if err != nil {
		log.Printf("[security] RELAYMESH_TRUSTED_PROXIES 配置无效（%v），已回退为默认可信代理（回环 + 私网）", err)
		proxies, mode, _ = ResolveTrustedProxies("")
	}

	if err := engine.SetTrustedProxies(proxies); err != nil {
		// 条目已在解析阶段校验，正常不会走到这里；万一失败按「不信任任何代理」处理，避免落回 Gin 的信任全部默认值。
		log.Printf("[security] 设置可信代理失败（%v），已改为不信任任何代理", err)
		_ = engine.SetTrustedProxies(nil)
		return
	}

	switch mode {
	case trustedProxyModeAll:
		log.Printf("[security] 警告: RELAYMESH_TRUSTED_PROXIES=* 已信任全部代理，X-Forwarded-For 可被任意客户端伪造，登录反爆破可被绕过")
	case trustedProxyModeNone:
		log.Printf("[security] 可信代理: 无（X-Forwarded-For 一律忽略，来源 IP 取直连对端）")
	case trustedProxyModeCustom:
		log.Printf("[security] 可信代理: 自定义 %d 项", len(proxies))
	default:
		log.Printf("[security] 可信代理: 默认（回环 + 私网）；经公网 CDN 或非私网反代访问时请配置 RELAYMESH_TRUSTED_PROXIES")
	}
}
