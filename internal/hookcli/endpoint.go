package hookcli

import (
	"net/url"
	"os"
	"regexp"
	"strings"
)

var (
	pathTokenRe = regexp.MustCompile(`/mcp/(?:(?:token|c)/)?[^/?#]+/?$`)
	envExpandRe = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}|\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

type endpoint struct {
	URL    string
	Auth   string
	Source string
}

func normURL(u string) string {
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

func expandEnv(s string) string {
	return envExpandRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := envExpandRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return ""
		}
		name := sub[1]
		if name == "" {
			name = sub[2]
		}
		return os.Getenv(name)
	})
}

func readMCPServers() map[string]any {
	path := filepathJoinHome(".cursor", "mcp.json")
	data := loadJSON(path)
	servers, _ := data["mcpServers"].(map[string]any)
	if servers == nil {
		return map[string]any{}
	}
	return servers
}

func filepathJoinHome(parts ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	all := append([]string{home}, parts...)
	return strings.Join(all, string(os.PathSeparator))
}

func resolveEndpoint(cfg map[string]any) endpoint {
	servers := readMCPServers()
	urlStr := normURL(str(cfg["mcp_url"]))
	name := strings.TrimSpace(str(cfg["mcp_server"]))
	if name == "" && urlStr == "" {
		name = "relaymesh"
	}
	var entry map[string]any
	if name != "" {
		if e, ok := servers[name].(map[string]any); ok {
			entry = e
		}
	} else if urlStr != "" {
		base := stripURLToken(urlStr)
		var fallback map[string]any
		for _, e := range servers {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			eu := normURL(expandEnv(str(em["url"])))
			if eu == urlStr {
				entry = em
				break
			}
			if fallback == nil && eu != "" && stripURLToken(eu) == base {
				fallback = em
			}
		}
		if entry == nil {
			entry = fallback
		}
	}
	if urlStr == "" && entry != nil {
		urlStr = normURL(expandEnv(str(entry["url"])))
	}

	tokenEnv := strings.TrimSpace(str(cfg["token_env"]))
	if tokenEnv == "" {
		tokenEnv = defaultTokenEnv
	}
	auth, source := "", ""
	if v := strings.TrimSpace(os.Getenv(tokenEnv)); v != "" {
		if matched, _ := regexp.MatchString(`(?i)^bearer\s`, v); !matched {
			v = "Bearer " + v
		}
		auth, source = v, "环境变量 "+tokenEnv
	} else if entry != nil {
		if headers, ok := entry["headers"].(map[string]any); ok {
			for k, v := range headers {
				if strings.EqualFold(k, "authorization") {
					if s, ok := v.(string); ok && strings.TrimSpace(expandEnv(s)) != "" {
						auth = strings.TrimSpace(expandEnv(s))
						label := name
						if label == "" {
							label = "按地址匹配到的服务器"
						}
						source = "~/.cursor/mcp.json 里的 " + label + " 条目"
						break
					}
				}
			}
		}
	}
	if source == "" && entry != nil {
		entryURL := normURL(expandEnv(str(entry["url"])))
		form := urlTokenForm(entryURL)
		if form != "" && (urlStr == "" || sameOrigin(urlStr, entryURL)) {
			urlStr = entryURL
			label := name
			if label == "" {
				label = "按地址匹配到的服务器"
			}
			where := "路径"
			if form == "query" {
				where = "查询参数"
			}
			source = "~/.cursor/mcp.json 里的 " + label + " 条目的地址（令牌写在" + where + "里）"
		}
	}
	return endpoint{URL: urlStr, Auth: auth, Source: source}
}

func maskURL(u string) string {
	u = regexp.MustCompile(`//[^/@]*@`).ReplaceAllString(u, "//<已省略>@")
	u = strings.SplitN(strings.SplitN(u, "?", 2)[0], "#", 2)[0]
	u = regexp.MustCompile(`(/mcp/(?:(?:token|c)/)?)[^/]+`).ReplaceAllString(u, `${1}<令牌已省略>`)
	return regexp.MustCompile(`/(rmc?-[A-Za-z0-9_\-]{6,})`).ReplaceAllString(u, "/<令牌已省略>")
}

func urlTokenForm(u string) string {
	parts, err := url.Parse(u)
	if err != nil {
		return ""
	}
	for k, vals := range parts.Query() {
		if strings.EqualFold(k, "token") && len(vals) > 0 && strings.TrimSpace(vals[0]) != "" {
			return "query"
		}
	}
	if pathTokenRe.FindStringIndex(parts.Path) != nil {
		return "path"
	}
	return ""
}

func stripURLToken(u string) string {
	parts, err := url.Parse(u)
	if err != nil {
		return normURL(u)
	}
	q := parts.Query()
	q.Del("token")
	path := pathTokenRe.ReplaceAllString(parts.Path, "/mcp")
	parts.RawQuery = q.Encode()
	parts.Path = path
	parts.Fragment = ""
	return normURL(parts.String())
}

func sameOrigin(a, b string) bool {
	x, err1 := url.Parse(a)
	y, err2 := url.Parse(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return x.Scheme != "" && x.Host != "" &&
		strings.EqualFold(x.Scheme, y.Scheme) && strings.EqualFold(x.Host, y.Host)
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
