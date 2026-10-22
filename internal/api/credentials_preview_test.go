package api

import "testing"

// 权限页的工具预览由服务端按真实逻辑算出（见 PreviewCredentialTools）：#181 的只读使用手册 usage_guide
// 只对能用治理类工具的令牌出现（沟通反馈、会话查询任一位都够）；ExecMesh 档（只有终端 / 文件 / 预设）
// 与只有系统信息、技能、配置的令牌看不到。前端不需要为它改动，预览自然跟着真实行为走。
func TestCredentialPreviewListsUsageGuideByPermission(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	bearer := []string{"Authorization", "Bearer " + wiredLogin(t, srv)}

	toolsOf := func(perms map[string]any) map[string]string {
		t.Helper()
		res := wiredCall(srv, "POST", "/api/v1/credentials/preview", map[string]any{"permissions": perms}, bearer...)
		if res.Code != 200 {
			t.Fatalf("预览失败: %d %s", res.Code, res.Raw)
		}
		items, _ := res.Body["tools"].([]any)
		out := make(map[string]string, len(items))
		for _, it := range items {
			m, _ := it.(map[string]any)
			name, _ := m["name"].(string)
			summary, _ := m["summary"].(string)
			out[name] = summary
		}
		return out
	}

	cases := []struct {
		name  string
		perms map[string]any
		want  bool
	}{
		{"沟通反馈", map[string]any{"feedback": true}, true},
		{"会话查询", map[string]any{"sessions": true}, true},
		{"治理全家", map[string]any{"feedback": true, "sessions": true, "system_info": true, "skills": true, "configure": true}, true},
		{"治理 + 终端（混合档）", map[string]any{"feedback": true, "terminal": true}, true},
		{"只有系统信息", map[string]any{"system_info": true}, false},
		{"只有技能", map[string]any{"skills": true}, false},
		{"只有配置", map[string]any{"configure": true}, false},
		{"只有终端（ExecMesh 档）", map[string]any{"terminal": true}, false},
		{"只有远程文件", map[string]any{"terminal_files": true}, false},
		{"没有任何权限", map[string]any{}, false},
	}
	for _, c := range cases {
		tools := toolsOf(c.perms)
		summary, has := tools["usage_guide"]
		if has != c.want {
			t.Errorf("%s：usage_guide 出现=%v，应为 %v（工具：%v）", c.name, has, c.want, tools)
		}
		if has && summary == "" {
			t.Errorf("%s：usage_guide 在预览里没有说明", c.name)
		}
	}
}
