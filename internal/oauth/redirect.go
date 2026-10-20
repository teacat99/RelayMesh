package oauth

import (
	"net"
	"net/url"
	"strings"
)

// RedirectURIMatches 精确匹配 redirect_uri；回环地址按 RFC 8252 忽略端口。
func RedirectURIMatches(registered, requested string) bool {
	registered = strings.TrimSpace(registered)
	requested = strings.TrimSpace(requested)
	if registered == "" || requested == "" {
		return false
	}
	if registered == requested {
		return true
	}
	ru, err1 := url.Parse(registered)
	rq, err2 := url.Parse(requested)
	if err1 != nil || err2 != nil {
		return false
	}
	if !isLoopbackHost(ru.Hostname()) || !isLoopbackHost(rq.Hostname()) {
		return false
	}
	// 回环：scheme + host + path + query 一致，端口可不同
	if strings.ToLower(ru.Scheme) != strings.ToLower(rq.Scheme) {
		return false
	}
	if ru.Path != rq.Path {
		return false
	}
	if ru.RawQuery != rq.RawQuery {
		return false
	}
	return strings.EqualFold(ru.Hostname(), rq.Hostname())
}

func RedirectURIInList(uris []string, requested string) bool {
	for _, u := range uris {
		if RedirectURIMatches(u, requested) {
			return true
		}
	}
	return false
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RedirectHost 用于授权页展示（只显示主机名）。
func RedirectHost(redir string) string {
	u, err := url.Parse(redir)
	if err != nil {
		return redir
	}
	return u.Hostname()
}

func IsLoopbackRedirect(redir string) bool {
	u, err := url.Parse(redir)
	if err != nil {
		return false
	}
	return isLoopbackHost(u.Hostname())
}
