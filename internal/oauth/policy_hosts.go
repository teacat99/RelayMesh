package oauth

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

const OAuthClientHostAllowlistMax = 32

// NormalizeAndValidatePolicy 规范化 client_host_allowlist 并校验策略字段。
func NormalizeAndValidatePolicy(p model.OAuthPolicy) (model.OAuthPolicy, error) {
	out := p
	hosts, err := NormalizeClientHostAllowlist(p.ClientHostAllowlist)
	if err != nil {
		return model.OAuthPolicy{}, err
	}
	out.ClientHostAllowlist = hosts
	if out.AccessTokenTTLMinutes != 0 && (out.AccessTokenTTLMinutes < 5 || out.AccessTokenTTLMinutes > 1440) {
		return model.OAuthPolicy{}, fmt.Errorf("访问令牌有效期须在 5～1440 分钟之间")
	}
	if out.RefreshIdleDays != 0 && (out.RefreshIdleDays < 1 || out.RefreshIdleDays > 365) {
		return model.OAuthPolicy{}, fmt.Errorf("刷新令牌空闲期须在 1～365 天之间")
	}
	if out.RefreshAbsoluteDays != 0 && (out.RefreshAbsoluteDays < 1 || out.RefreshAbsoluteDays > 730) {
		return model.OAuthPolicy{}, fmt.Errorf("刷新令牌绝对期限须在 1～730 天之间")
	}
	if u := strings.TrimSpace(out.PublicURL); u != "" {
		if err := validateOAuthPublicURL(u); err != nil {
			return model.OAuthPolicy{}, err
		}
		out.PublicURL = strings.TrimRight(u, "/")
	}
	return out, nil
}

func validateOAuthPublicURL(u string) error {
	u = strings.TrimSpace(u)
	if u == "" {
		return nil
	}
	if strings.ContainsAny(u, "?#") || strings.Contains(u, "@") {
		return fmt.Errorf("对外地址不能包含路径、查询或用户信息")
	}
	// 简单校验：必须是 http(s)://host[:port]
	lower := strings.ToLower(u)
	if !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "http://") {
		return fmt.Errorf("对外地址必须以 https:// 或环回 http:// 开头")
	}
	rest := u[strings.Index(u, "://")+3:]
	if rest == "" || strings.Contains(rest, "/") {
		return fmt.Errorf("对外地址必须是纯源地址，不含路径")
	}
	host := rest
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	if strings.HasPrefix(lower, "http://") && !isLoopbackHost(host) {
		return fmt.Errorf("非环回主机须使用 https://")
	}
	if strings.HasPrefix(lower, "https://") && host == "" {
		return fmt.Errorf("对外地址缺少主机名")
	}
	return nil
}

// NormalizeClientHostAllowlist 只保留合法主机名：不含协议、端口、路径、通配符；小写、去重。
func NormalizeClientHostAllowlist(in []string) ([]string, error) {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(in))
	for _, raw := range in {
		h := strings.ToLower(strings.TrimSpace(raw))
		if h == "" {
			continue
		}
		if err := validateOAuthClientHost(h); err != nil {
			return nil, err
		}
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
		if len(out) > OAuthClientHostAllowlistMax {
			return nil, fmt.Errorf("自定义客户端域名最多 %d 条", OAuthClientHostAllowlistMax)
		}
	}
	return out, nil
}

func validateOAuthClientHost(host string) error {
	if host == "" {
		return fmt.Errorf("域名不能为空")
	}
	if strings.ContainsAny(host, ":/\\?#*[]") || strings.Contains(host, "..") {
		return fmt.Errorf("域名「%s」格式无效：只能填写主机名，不要带协议、端口或路径", host)
	}
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return fmt.Errorf("域名「%s」不能以点开头或结尾", host)
	}
	for _, r := range host {
		if r == '.' || r == '-' {
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return fmt.Errorf("域名「%s」含有非法字符", host)
		}
	}
	return nil
}
