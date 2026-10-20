package agent

import "testing"

func TestParseServer(t *testing.T) {
	cases := []struct {
		in         string
		wantURL    string
		wantHost   string
		wantSecure bool
		wantErr    bool
	}{
		{in: "relay.example.com", wantURL: "wss://relay.example.com" + AgentWSPath, wantHost: "relay.example.com:443", wantSecure: true},
		{in: "https://relay.example.com", wantURL: "wss://relay.example.com" + AgentWSPath, wantHost: "relay.example.com:443", wantSecure: true},
		{in: "https://relay.example.com/", wantURL: "wss://relay.example.com" + AgentWSPath, wantHost: "relay.example.com:443", wantSecure: true},
		{in: "wss://relay.example.com:8443", wantURL: "wss://relay.example.com:8443" + AgentWSPath, wantHost: "relay.example.com:8443", wantSecure: true},
		{in: "http://10.0.0.5:18775", wantURL: "ws://10.0.0.5:18775" + AgentWSPath, wantHost: "10.0.0.5:18775"},
		{in: "ws://10.0.0.5", wantURL: "ws://10.0.0.5" + AgentWSPath, wantHost: "10.0.0.5:80"},
		{in: "  https://relay.example.com/base/  ", wantURL: "wss://relay.example.com/base" + AgentWSPath, wantHost: "relay.example.com:443", wantSecure: true},
		{in: "https://relay.example.com" + AgentWSPath, wantURL: "wss://relay.example.com" + AgentWSPath, wantHost: "relay.example.com:443", wantSecure: true},
		{in: "https://relay.example.com?x=1#frag", wantURL: "wss://relay.example.com" + AgentWSPath, wantHost: "relay.example.com:443", wantSecure: true},
		{in: "https://[2001:db8::1]:9000", wantURL: "wss://[2001:db8::1]:9000" + AgentWSPath, wantHost: "[2001:db8::1]:9000", wantSecure: true},
		{in: "https://[2001:db8::1]", wantURL: "wss://[2001:db8::1]" + AgentWSPath, wantHost: "[2001:db8::1]:443", wantSecure: true},

		{in: "", wantErr: true},
		{in: "   ", wantErr: true},
		{in: "ftp://relay.example.com", wantErr: true},
		{in: "https://user:pass@relay.example.com", wantErr: true}, // 禁止内嵌凭据
		{in: "https://", wantErr: true},
		{in: "https://bad host name", wantErr: true},
	}
	for _, c := range cases {
		got, err := ParseServer(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseServer(%q) 应报错，得到 %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseServer(%q) 意外报错: %v", c.in, err)
			continue
		}
		if got.URL != c.wantURL || got.Host != c.wantHost || got.Secure != c.wantSecure {
			t.Errorf("ParseServer(%q) = %+v，期望 URL=%s Host=%s Secure=%v", c.in, got, c.wantURL, c.wantHost, c.wantSecure)
		}
	}
}

func TestServerTargetHTTPBase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://relay.example.com", "https://relay.example.com"},
		{"https://relay.example.com:8443/", "https://relay.example.com:8443"},
		{"https://relay.example.com/base/", "https://relay.example.com/base"},
		{"http://10.0.0.5:18775", "http://10.0.0.5:18775"},
		{"https://[2001:db8::1]:9000", "https://[2001:db8::1]:9000"},
	}
	for _, c := range cases {
		target, err := ParseServer(c.in)
		if err != nil {
			t.Fatalf("ParseServer(%q): %v", c.in, err)
		}
		got, err := target.HTTPBase()
		if err != nil || got != c.want {
			t.Errorf("HTTPBase(%q) = %q, %v，期望 %q", c.in, got, err, c.want)
		}
	}
	if _, err := (ServerTarget{URL: "ftp://x/y"}).HTTPBase(); err == nil {
		t.Error("不认识的协议应报错")
	}
}
