package oauth

import (
	"reflect"
	"testing"
)

// 批准页的黄色提示规则固定（#238 起不再有开关）：官方域名本身或子域不提示，其余一律提示；回环另有专门的提示。
// registered 是「这个 client_id 是不是本服务登记的 DCR 客户端」：是就看重定向地址，否则（CIMD）看 client_id 地址。
func TestWarnUntrustedClient(t *testing.T) {
	const dcrOnHTTPS = "https://hub.example.test/oauth/clients/0123456789abcdef0123456789abcdef"
	cases := []struct {
		name       string
		clientID   string
		redirect   string
		registered bool
		want       bool
	}{
		// CIMD：看 client_id 地址的主机
		{"CIMD ChatGPT 官方", "https://chatgpt.com/oauth/client.json", "https://chatgpt.com/connector_platform_oauth_redirect", false, false},
		{"CIMD Claude 官方", "https://claude.ai/oauth/mcp-oauth-client-metadata", "https://claude.ai/api/mcp/auth_callback", false, false},
		{"CIMD Claude 官方新域名", "https://claude.com/oauth/client.json", "https://claude.com/api/mcp/auth_callback", false, false},
		{"CIMD 官方子域", "https://app.chatgpt.com/client.json", "https://app.chatgpt.com/cb", false, false},
		{"CIMD 大小写与末尾点不影响", "https://ChatGPT.COM./client.json", "https://chatgpt.com/cb", false, false},
		{"CIMD 陌生域名", "https://tools.example.com/client.json", "https://tools.example.com/cb", false, true},
		{"CIMD 名字里带字样的陌生域名", "https://chatgpt-fake.example.com/client.json", "https://chatgpt-fake.example.com/cb", false, true},
		{"CIMD 官方域名只是别人的子域", "https://chatgpt.com.evil.org/client.json", "https://chatgpt.com.evil.org/cb", false, true},
		{"CIMD 地址解析不了", "https://%zz/client.json", "https://chatgpt.com/cb", false, true},

		// DCR：看重定向地址的主机
		{"DCR ChatGPT 官方回调", "dcr-abc", "https://chatgpt.com/connector_platform_oauth_redirect", true, false},
		{"DCR Claude 官方回调", "dcr-abc", "https://claude.ai/api/mcp/auth_callback", true, false},
		{"DCR Claude 新域名回调", "dcr-abc", "https://claude.com/api/mcp/auth_callback", true, false},
		{"DCR 官方子域回调", "dcr-abc", "https://www.claude.ai/cb", true, false},
		{"DCR 陌生域名", "dcr-abc", "https://tools.example.com/cb", true, true},
		// 过去的判断是「主机名里含 chatgpt / claude」，这些陌生域名都会被当成官方；现在必须提示。
		{"DCR 名字里带 chatgpt 的陌生域名", "dcr-abc", "https://chatgpt-fake.example.com/cb", true, true},
		{"DCR 名字里带 claude 的陌生域名", "dcr-abc", "https://myclaude.example.org/cb", true, true},
		{"DCR 官方域名作为别人的前缀", "dcr-abc", "https://chatgpt.com.evil.org/cb", true, true},
		{"DCR 回环一律提示", "dcr-abc", "http://127.0.0.1:53682/callback", true, true},
		{"DCR localhost 一律提示", "dcr-abc", "http://localhost:8080/cb", true, true},
		{"DCR 地址解析不了", "dcr-abc", "https://%zz/cb", true, true},
		{"DCR 没有重定向地址", "dcr-abc", "", true, true},

		// 回归（#239）：本服务给 DCR 客户端签发的 client_id 是「<对外地址>/oauth/clients/<随机串>」，
		// 对外地址是 https 时它以 https:// 开头。过去只看前缀，会把真正的 ChatGPT / Claude 登记当成 CIMD，
		// 拿本服务自己的域名去比对官方列表，于是永远被当成陌生来源。
		{"DCR 登记在 https 对外地址下：官方回调不提示", dcrOnHTTPS, "https://chatgpt.com/connector_platform_oauth_redirect", true, false},
		{"DCR 登记在 https 对外地址下：Claude 官方回调不提示", dcrOnHTTPS, "https://claude.ai/api/mcp/auth_callback", true, false},
		{"DCR 登记在 https 对外地址下：陌生回调照样提示", dcrOnHTTPS, "https://tools.example.com/cb", true, true},
		{"DCR 登记在 https 对外地址下：回环照样提示", dcrOnHTTPS, "http://127.0.0.1:53682/callback", true, true},
		{"同样形状的地址不是本服务登记的，就按 CIMD 看 client_id 主机", dcrOnHTTPS, "https://chatgpt.com/connector_platform_oauth_redirect", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WarnUntrustedClient(tc.clientID, tc.redirect, tc.registered); got != tc.want {
				t.Fatalf("WarnUntrustedClient(%q, %q, registered=%v) = %v，期望 %v", tc.clientID, tc.redirect, tc.registered, got, tc.want)
			}
		})
	}
}

func TestIsOfficialClientHost(t *testing.T) {
	for host, want := range map[string]bool{
		"chatgpt.com":          true,
		"CHATGPT.com":          true,
		"chatgpt.com.":         true,
		"a.b.chatgpt.com":      true,
		"claude.ai":            true,
		"claude.com":           true,
		"x.claude.com":         true,
		"":                     false,
		"chatgpt":              false,
		"notchatgpt.com":       false,
		"chatgpt.com.evil.org": false,
		"claude.ai.cn":         false,
		"openai.com":           false,
	} {
		if got := isOfficialClientHost(host); got != want {
			t.Errorf("isOfficialClientHost(%q) = %v，期望 %v", host, got, want)
		}
	}
}

// 设置页「刚登记、还没授权」清单用的汇总：回调主机去重，陌生来源与回环标记与批准页同一套规则。
func TestSummarizeRedirects(t *testing.T) {
	cases := []struct {
		name string
		uris []string
		want RedirectSummary
	}{
		{"官方 ChatGPT", []string{"https://chatgpt.com/connector_platform_oauth_redirect"},
			RedirectSummary{Hosts: []string{"chatgpt.com"}}},
		{"官方多个回调、主机去重", []string{"https://claude.ai/api/mcp/auth_callback", "https://claude.ai/other", "https://claude.com/api/mcp/auth_callback"},
			RedirectSummary{Hosts: []string{"claude.ai", "claude.com"}}},
		{"陌生域名", []string{"https://tools.example.com/cb"},
			RedirectSummary{Hosts: []string{"tools.example.com"}, Untrusted: true}},
		{"回环既是陌生来源也是回环", []string{"http://127.0.0.1:53682/callback"},
			RedirectSummary{Hosts: []string{"127.0.0.1"}, Untrusted: true, Loopback: true}},
		{"官方加陌生：整体按陌生来源算", []string{"https://chatgpt.com/cb", "https://tools.example.com/cb"},
			RedirectSummary{Hosts: []string{"chatgpt.com", "tools.example.com"}, Untrusted: true}},
		{"没有回调地址按陌生来源", nil, RedirectSummary{Untrusted: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SummarizeRedirects(tc.uris)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SummarizeRedirects(%v) = %+v，期望 %+v", tc.uris, got, tc.want)
			}
		})
	}
}

// 设置页用 client_id 末尾的随机段指认一条登记，不把完整地址放进 URL。
func TestClientRef(t *testing.T) {
	for in, want := range map[string]string{
		"https://hub.example.test/oauth/clients/0123456789abcdef0123456789abcdef": "0123456789abcdef0123456789abcdef",
		"http://127.0.0.1:18780/oauth/clients/abc":                                "abc",
		"https://tools.example.com/client.json":                                   "",
		"dcr-abc":                                                                 "",
		"":                                                                        "",
	} {
		if got := ClientRef(in); got != want {
			t.Errorf("ClientRef(%q) = %q，期望 %q", in, got, want)
		}
	}
}
