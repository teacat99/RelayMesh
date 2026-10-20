package oauth

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

const (
	maxRedirectURILen     = 2048
	maxRedirectURIsPerCli = 10
)

var blockedRedirectSchemes = map[string]struct{}{
	"javascript": {}, "data": {}, "vbscript": {}, "file": {}, "blob": {},
	"about": {}, "ftp": {}, "ws": {}, "wss": {}, "chrome": {},
	"chrome-extension": {}, "view-source": {},
}

var customSchemeRe = regexp.MustCompile(`^[a-z][a-z0-9+.-]{0,63}$`)

// ValidateRedirectURI 校验 OAuth redirect_uri（DCR / CIMD / authorize 共用）。
func ValidateRedirectURI(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("redirect_uri required")
	}
	if len(raw) > maxRedirectURILen {
		return fmt.Errorf("redirect_uri too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid redirect_uri")
	}
	if u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid redirect_uri")
	}
	scheme := strings.ToLower(u.Scheme)
	if _, blocked := blockedRedirectSchemes[scheme]; blocked {
		return fmt.Errorf("redirect_uri scheme not allowed")
	}
	switch scheme {
	case "https":
		if u.Hostname() == "" {
			return fmt.Errorf("invalid redirect_uri")
		}
		return nil
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return fmt.Errorf("http redirect_uri only allowed for loopback")
		}
		return nil
	default:
		if !customSchemeRe.MatchString(scheme) {
			return fmt.Errorf("invalid custom scheme")
		}
		if u.Hostname() != "" && u.Port() != "" {
			return fmt.Errorf("invalid redirect_uri")
		}
		return nil
	}
}

// ValidateRedirectURIList 校验列表长度与每一项。
func ValidateRedirectURIList(uris []string) error {
	if len(uris) == 0 {
		return fmt.Errorf("redirect_uris required")
	}
	if len(uris) > maxRedirectURIsPerCli {
		return fmt.Errorf("too many redirect_uris")
	}
	seen := make(map[string]struct{})
	for _, u := range uris {
		if err := ValidateRedirectURI(u); err != nil {
			return err
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
	}
	return nil
}

// RedirectDisplayScheme 供授权页展示。
func RedirectDisplayScheme(redir string) string {
	u, err := url.Parse(redir)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme)
}

// IsCustomSchemeRedirect 是否为 RFC 8252 私有方案（非 http(s)）。
func IsCustomSchemeRedirect(redir string) bool {
	u, err := url.Parse(redir)
	if err != nil {
		return false
	}
	s := strings.ToLower(u.Scheme)
	return s != "http" && s != "https"
}

// ValidateRedirectHostSyntax 校验从请求推导出的 Host 是否合法。
func ValidateRedirectHostSyntax(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, "/\\?#") {
		return false
	}
	h := host
	if strings.HasPrefix(h, "[") {
		if !strings.HasSuffix(h, "]") {
			return false
		}
		ip := net.ParseIP(h[1 : len(h)-1])
		return ip != nil
	}
	if strings.Contains(h, ":") {
		// host:port
		if _, _, err := net.SplitHostPort(h); err != nil {
			return false
		}
		return true
	}
	for _, r := range h {
		if r <= 32 || r == 127 {
			return false
		}
	}
	return true
}
