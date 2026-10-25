package oauth

import (
	"fmt"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// NormalizeAndValidatePolicy 校验策略字段并规整对外地址（去掉首尾空白）。
func NormalizeAndValidatePolicy(p model.OAuthPolicy) (model.OAuthPolicy, error) {
	out := p
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
	// 自动注册关闭时「常开」没有意义；存成 false，免得之后把自动注册重新打开时悄悄变成常开。
	if !out.AllowDCR {
		out.DCRAlways = false
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
