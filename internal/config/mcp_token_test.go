package config

import (
	"strings"
	"testing"
)

func TestMCPTokenWeakness(t *testing.T) {
	cases := []struct {
		name       string
		token      string
		wantWeak   bool
		wantReason string
	}{
		{"服务端生成的令牌", "rm-3f9a1c7e5b2d48a6c0e1f7b9d3a5c8e2f4b6d8a0c2e4f6a8b0c2d4e6f8a0b2c4", false, ""},
		{"随机 base64 风格", "q8Zr2VxN5mLk7TpW3yHc9BfD1gJs6AaE", false, ""},
		{"空", "", true, "为空"},
		{"太短", "abc123XYZ", true, "长度不足"},
		{"23 位仍不足", "aB3dE5gH7jK9mN1pQ3sT5vX", true, "长度不足"},
		{"占位符 changeme", "changeme-changeme-changeme-x1", true, "占位符"},
		{"文档示例", "your-token-goes-here-1234567890", true, "占位符"},
		{"含 password", "my-super-password-9f8e7d6c5b4a", true, "占位符"},
		{"重复字符", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true, "字符种类"},
		{"两种字符交替", "abababababababababababababababab", true, "字符种类"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := MCPTokenWeakness(tc.token)
			if tc.wantWeak {
				if reason == "" {
					t.Fatalf("期望判定为弱令牌，实际合格")
				}
				if !strings.Contains(reason, tc.wantReason) {
					t.Fatalf("原因 %q 不包含 %q", reason, tc.wantReason)
				}
				if tc.token != "" && strings.Contains(reason, tc.token) {
					t.Fatalf("原因中不得包含令牌内容: %q", reason)
				}
				if IsStrongMCPToken(tc.token) {
					t.Fatal("IsStrongMCPToken 应与 MCPTokenWeakness 一致")
				}
				return
			}
			if reason != "" {
				t.Fatalf("期望合格，实际原因: %q", reason)
			}
			if !IsStrongMCPToken(tc.token) {
				t.Fatal("IsStrongMCPToken 应与 MCPTokenWeakness 一致")
			}
		})
	}
}
