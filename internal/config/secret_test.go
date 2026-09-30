package config

import (
	"strings"
	"testing"
)

func TestJWTSecretWeakness(t *testing.T) {
	const strong32 = "Xk9#mP2vL7qR4tWz8Yb3NcF6hJd5GsA1"

	cases := []struct {
		name       string
		secret     string
		wantWeak   bool
		wantReason string
	}{
		{name: "空值", secret: "", wantWeak: true, wantReason: "为空"},
		{name: "内置默认值", secret: DefaultJWTSecret, wantWeak: true, wantReason: "内置默认值"},
		{name: "31 字符不足", secret: strong32[:31], wantWeak: true, wantReason: "长度不足"},
		{name: "README 示例占位符", secret: "YourRandomJWTSecretKeyAtLeast32CharsLong!", wantWeak: true, wantReason: "占位符"},
		{name: "含默认值特征片段", secret: "prefix-relaymesh-secret-" + strong32, wantWeak: true, wantReason: "占位符"},
		{name: "含 change-me 类占位写法", secret: "Please-ChangeMe-" + strong32, wantWeak: true, wantReason: "占位符"},
		{name: "测试用弱密钥", secret: "test-jwt-secret-" + strong32, wantWeak: true, wantReason: "占位符"},
		{name: "单一字符重复", secret: strings.Repeat("a", 64), wantWeak: true, wantReason: "字符种类"},
		{name: "短周期重复", secret: strings.Repeat("abcd", 16), wantWeak: true, wantReason: "字符种类"},
		{name: "恰好 32 字符的随机串合格", secret: strong32},
		{name: "十六进制 64 字符合格", secret: "9f2c6a1e4b7d30581a0c9e7f2b4d60831c5e8a7b3d9f04628e1b5c7a9d3f2e60"},
		{name: "base64url 风格随机串合格", secret: "q3Vt8n-Zk2Lw_7YxP0aRb5HcJ9dFgS1uEo6NiTmXyAvBzCe4WhKjDlQrMpU2fGs"},
		{name: "长口令式密钥合格", secret: "correct-horse-battery-staple-9271-violet-anchor"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := JWTSecretWeakness(tc.secret)
			if strong := IsStrongJWTSecret(tc.secret); strong == tc.wantWeak {
				t.Fatalf("IsStrongJWTSecret = %v，期望 %v（原因: %q）", strong, !tc.wantWeak, reason)
			}
			if tc.wantWeak {
				if reason == "" {
					t.Fatalf("期望判定为弱密钥，实际合格")
				}
				if !strings.Contains(reason, tc.wantReason) {
					t.Fatalf("原因 %q 不包含 %q", reason, tc.wantReason)
				}
				if strings.Contains(reason, tc.secret) && tc.secret != "" {
					t.Fatalf("原因中不得包含密钥内容: %q", reason)
				}
				return
			}
			if reason != "" {
				t.Fatalf("期望合格，实际原因: %q", reason)
			}
		})
	}
}

func TestLoadWithMode_DefaultJWTSecretIsWeakSentinel(t *testing.T) {
	t.Setenv("RELAYMESH_JWT_SECRET", "")
	cfg := LoadWithMode("serve")
	if cfg.JWTSecret != DefaultJWTSecret {
		t.Fatalf("未配置时 JWTSecret 应为哨兵默认值以便启动阶段替换")
	}
	if IsStrongJWTSecret(cfg.JWTSecret) {
		t.Fatalf("内置默认值必须被判定为弱密钥")
	}
}
