package oauth

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestNormalizeAndValidatePolicy(t *testing.T) {
	t.Run("对外地址去掉首尾空白，其余字段原样保留", func(t *testing.T) {
		out, err := NormalizeAndValidatePolicy(model.OAuthPolicy{
			Enabled: true, AllowDCR: true, PublicURL: " https://relay.example.com ",
			AccessTokenTTLMinutes: 30, RefreshIdleDays: 7, RefreshAbsoluteDays: 60,
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.PublicURL != "https://relay.example.com" || !out.Enabled || !out.AllowDCR ||
			out.AccessTokenTTLMinutes != 30 || out.RefreshIdleDays != 7 || out.RefreshAbsoluteDays != 60 {
			t.Fatalf("规整后的策略不对：%+v", out)
		}
	})

	// 自动注册的三种方式（#239）：「常开」只在自动注册打开时才有意义；关闭自动注册时保存下来的 dcr_always 一并清掉，
	// 免得之后重新打开自动注册时意外变成常开。
	t.Run("关闭自动注册时清掉 dcr_always", func(t *testing.T) {
		out, err := NormalizeAndValidatePolicy(model.OAuthPolicy{AllowDCR: false, DCRAlways: true})
		if err != nil {
			t.Fatal(err)
		}
		if out.DCRAlways || out.AllowDCR {
			t.Fatalf("关闭自动注册后 dcr_always 应当被清掉：%+v", out)
		}
	})
	t.Run("自动注册打开时保留 dcr_always", func(t *testing.T) {
		out, err := NormalizeAndValidatePolicy(model.OAuthPolicy{AllowDCR: true, DCRAlways: true})
		if err != nil {
			t.Fatal(err)
		}
		if !out.DCRAlways || !out.AllowDCR {
			t.Fatalf("常开应当被保留：%+v", out)
		}
	})

	bad := []struct {
		name string
		in   model.OAuthPolicy
		want string
	}{
		{"访问令牌有效期过短", model.OAuthPolicy{AccessTokenTTLMinutes: 1}, "访问令牌有效期"},
		{"访问令牌有效期过长", model.OAuthPolicy{AccessTokenTTLMinutes: 2000}, "访问令牌有效期"},
		{"刷新令牌空闲期过长", model.OAuthPolicy{RefreshIdleDays: 400}, "空闲期"},
		{"刷新令牌绝对期限过长", model.OAuthPolicy{RefreshAbsoluteDays: 800}, "绝对期限"},
		{"对外地址带路径", model.OAuthPolicy{PublicURL: "https://relay.example.com/app"}, "纯源地址"},
		{"对外地址非环回却用 http", model.OAuthPolicy{PublicURL: "http://relay.example.com"}, "https://"},
		{"对外地址不是网址", model.OAuthPolicy{PublicURL: "relay.example.com"}, "https://"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NormalizeAndValidatePolicy(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望报错含 %q，实际：%v", tc.want, err)
			}
		})
	}
}

// 旧版本存进库里（以及旧前端提交）的策略带着已删除的「信任来源」四项：读取与保存都必须照常工作，只是这四项被忽略，
// 用户下次保存策略时它们就从库里消失了（#238）。
func TestPolicyIgnoresRemovedTrustFields(t *testing.T) {
	old := `{"enabled":true,"public_url":"https://relay.example.com","allow_dcr":false,` +
		`"access_token_ttl_minutes":45,"refresh_idle_days":10,"refresh_absolute_days":50,` +
		`"client_host_allowlist":["tools.example.com"],"allow_chatgpt":false,"allow_claude":true,"allow_loopback":false}`
	var p model.OAuthPolicy
	if err := json.Unmarshal([]byte(old), &p); err != nil {
		t.Fatalf("旧策略应能照常解析：%v", err)
	}
	if !p.Enabled || p.AllowDCR || p.PublicURL != "https://relay.example.com" || p.AccessTokenTTLMinutes != 45 {
		t.Fatalf("保留下来的字段不对：%+v", p)
	}
	norm, err := NormalizeAndValidatePolicy(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(norm)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"client_host_allowlist", "allow_chatgpt", "allow_claude", "allow_loopback"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("保存后的策略里不该再有 %s：%s", key, raw)
		}
	}
}
