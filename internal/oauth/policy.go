package oauth

import (
	"net/url"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// officialClientDomains 是官方 ChatGPT / Claude 连接器会用到的域名：批准页对来自这些域名（及其子域）的客户端
// 不显示「不在默认信任列表中」的提示。
var officialClientDomains = []string{"chatgpt.com", "claude.ai", "claude.com"}

// isOfficialClientHost 要求主机就是这些域名本身或它们的子域，而不是「名字里带着这几个字」——
// 否则 chatgpt-fake.example.com 这样的陌生域名也会被当成官方。
func isOfficialClientHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, d := range officialClientDomains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// WarnUntrustedClient 报告批准页要不要对这个客户端显示黄色提示「此客户端不在默认信任列表中」。
// 这只是提醒你看清楚是谁在申请，不会拒绝任何授权——真正的闸门是总开关与 DCR 开关。规则固定，没有开关：
//   - registered 为真（本服务登记的 DCR 客户端）：看重定向地址的主机，官方域名不提示；
//   - registered 为假、client_id 是 https 地址（CIMD 客户端）：看这个地址的主机，官方域名不提示；
//   - 本机回环重定向一律提示（Claude Code 等本地工具；批准页另有一条专门的回环提示，见 IsLoopbackRedirect）；
//   - 地址解析不了、或不是官方域名：提示。
//
// 为什么要传 registered：本服务给 DCR 客户端签发的 client_id 就是「<对外地址>/oauth/clients/<随机串>」，
// 对外地址是 https 时它也以 https:// 开头——只看前缀会把真正的 ChatGPT / Claude 登记当成 CIMD、去比对本服务自己的域名，
// 于是永远被当成陌生来源。是不是 DCR 要由「这个 client_id 是不是在本服务登记过」来决定，调用方（授权入口）知道。
func WarnUntrustedClient(clientID string, redirectURI string, registered bool) bool {
	// CIMD: client_id 是 https URL
	if !registered && strings.HasPrefix(strings.ToLower(clientID), "https://") {
		u, err := url.Parse(clientID)
		if err != nil {
			return true
		}
		return !isOfficialClientHost(u.Hostname())
	}
	// DCR 客户端：看 redirect 主机
	if IsLoopbackRedirect(redirectURI) {
		return true
	}
	ru, err := url.Parse(redirectURI)
	if err != nil {
		return true
	}
	return !isOfficialClientHost(ru.Hostname())
}

// RedirectSummary 是一条 DCR 登记的回调地址概况，给设置页「刚登记、还没授权的客户端」清单用：
// 去重后的回调主机，以及按批准页同一套规则得出的「陌生来源 / 本机回环」标记。
type RedirectSummary struct {
	Hosts     []string
	Untrusted bool // 有任何一个回调地址会让批准页显示黄色提示（含回环）
	Loopback  bool // 有任何一个回调地址是本机回环
}

// SummarizeRedirects 汇总一条 DCR 登记的回调地址（登记的客户端一定是 registered）。没有任何回调地址按陌生来源处理。
func SummarizeRedirects(redirectURIs []string) RedirectSummary {
	var out RedirectSummary
	seen := map[string]bool{}
	for _, u := range redirectURIs {
		if h := RedirectHost(u); h != "" && !seen[h] {
			seen[h] = true
			out.Hosts = append(out.Hosts, h)
		}
		if WarnUntrustedClient("", u, true) {
			out.Untrusted = true
		}
		if IsLoopbackRedirect(u) {
			out.Loopback = true
		}
	}
	if len(redirectURIs) == 0 {
		out.Untrusted = true
	}
	return out
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
