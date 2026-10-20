package hookcli

import (
	"strings"
	"testing"
)

func TestGoStatus(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"enabled": true, "hooks_enabled": true, "unread": 2, "delivered": 1})
	bin := fakeBinary(t, s.tmp)
	_, _, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin, McpURL: f.url()})
	if code != 0 {
		t.Fatal("install 失败")
	}
	out, exit := s.runStatus(s.project, tokenEnv())
	if exit != 0 {
		t.Fatalf("status 应成功: %d\n%s", exit, out)
	}
	for _, want := range []string{"wf-1", "环境变量 RELAYMESH_TOKEN", "项目级 hooks.json：已登记 postToolUse、stop", "连通性：正常", "未读 2 条", "已读未确认 1 条"} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少 %q:\n%s", want, out)
		}
	}
	assertNoSecret(t, out, "status", testToken, urlSecret)
	args := f.hits()[0].args()
	if args["source"] != "check" {
		t.Fatalf("source=check: %v", args)
	}
	if _, has := args["reminder"]; has {
		t.Fatal("status 不消耗提醒")
	}

	f.mu.Lock()
	f.status = 401
	f.mu.Unlock()
	out, exit = s.runStatus(s.project, tokenEnv())
	if exit == 0 || !strings.Contains(out, "HTTP 401") || !strings.Contains(out, "令牌不对") {
		t.Fatalf("401: exit=%d\n%s", exit, out)
	}

	out, exit = s.runStatus(s.project, nil)
	if exit == 0 || !strings.Contains(out, "没有找到") {
		t.Fatalf("无令牌: exit=%d\n%s", exit, out)
	}
}

func TestGoStatus_TokenInMcpJSONURL(t *testing.T) {
	for _, tc := range []struct {
		name, suffix, wantSource, wantMasked string
	}{
		{"查询参数", "?token=" + urlSecret, "令牌写在查询参数里", "/mcp"},
		{"路径", "/" + urlSecret, "令牌写在路径里", "/mcp/<令牌已省略>"},
		{"能力网址", "/c/" + capSecret, "令牌写在路径里", "/mcp/c/<令牌已省略>"},
		{"不透明令牌", "/" + opaqueSecret, "令牌写在路径里", "/mcp/<令牌已省略>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newGoSandbox(t)
			f := newFakeMCP(t, map[string]any{"enabled": true, "hooks_enabled": true, "unread": 0})
			s.config(map[string]any{"workflow_id": "wf-1"})
			s.mcpJSON(map[string]any{"relaymesh": map[string]any{"url": f.url() + tc.suffix}})
			out, exit := s.runStatus(s.project, nil)
			if exit != 0 {
				t.Fatalf("status 失败: %d\n%s", exit, out)
			}
			for _, want := range []string{tc.wantSource, "连通性：正常", "MCP 地址：", tc.wantMasked} {
				if !strings.Contains(out, want) {
					t.Fatalf("缺少 %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "没有找到") {
				t.Fatal("不应说没找到令牌")
			}
			assertNoSecret(t, out, "status", urlSecret, capSecret, opaqueSecret)
			if h := f.hits(); len(h) != 1 || h[0].args()["source"] != "check" {
				t.Fatalf("check 查询: %+v", h)
			}
		})
	}
}

func TestGoStatus_WarnsWhenProjectConfigHoldsAToken(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"enabled": true, "hooks_enabled": true, "unread": 0})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url() + "?token=" + urlSecret})
	out, _ := s.runStatus(s.project, tokenEnv())
	if !strings.Contains(out, "令牌不该写进项目文件") {
		t.Fatalf("应提醒项目文件令牌:\n%s", out)
	}
	assertNoSecret(t, out, "status", urlSecret, testToken)

	out, exit := s.runStatus(s.project, nil)
	if exit == 0 || !strings.Contains(out, "没有找到") || !strings.Contains(out, "令牌不该写进项目文件") {
		t.Fatalf("无来源也要提示: exit=%d\n%s", exit, out)
	}
}
