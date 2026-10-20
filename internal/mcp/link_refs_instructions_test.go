package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 正文里的引用链接写进 initialize.instructions 与工具参数说明（#161 步骤 ②，Q25）：
//   - 能写正文的令牌（interactive_feedback / async_ask / task_board）收到「12. 正文里的引用链接」，其余令牌（终端、系统信息）不收；
//   - 它排在任务清单之后、等待流程之前（等待流程固定在最末尾）；
//   - 四种写法（任务 #N、确认项 Q<N>、文档 [[路径]]、会话号 sess-xxxxxxxx）都要写到，识别规则以前端 lib/linkRefs.ts 为准。
func TestInitializeInstructionsDescribeLinkRefs(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	const heading = "12. 正文里的引用链接"

	for name, perms := range map[string]model.Permissions{
		"治理":      {Feedback: true},
		"治理 + 终端": {Feedback: true, Terminal: true},
		"只读会话":    {Sessions: true}, // 有 task_board（任务描述 / 进展里也会写引用）
	} {
		if text := srv.InstructionsForPermissions(ctx, perms); !strings.Contains(text, heading) {
			t.Errorf("%s 令牌的指引应带「正文里的引用链接」一节", name)
		}
	}
	for name, perms := range map[string]model.Permissions{
		"系统信息":    {SystemInfo: true},
		"终端（原生）":  {Terminal: true},
		"终端（兼容）":  {Terminal: true, TerminalCompat: true},
		"终端文件":    {TerminalFiles: true},
		"终端 + 文件": {Terminal: true, TerminalFiles: true},
	} {
		if text := srv.InstructionsForPermissions(ctx, perms); strings.Contains(text, "正文里的引用链接") {
			t.Errorf("%s 令牌不会写汇报 / 提问 / 任务，不该收到引用链接一节", name)
		}
	}

	// 位置：任务清单之后、等待流程之前（等待流程仍是最末尾，见 wait_flows_instructions_test.go）
	full := srv.InstructionsForPermissions(ctx, model.Permissions{Feedback: true})
	at, tasks, wait := strings.Index(full, heading), strings.Index(full, "10. 任务清单"), strings.Index(full, "13. 等待用户回复的两种流程")
	if tasks < 0 || at < tasks || wait < at {
		t.Errorf("顺序应为 10. 任务清单 → 12. 正文里的引用链接 → 13. 等待流程：%d / %d / %d", tasks, at, wait)
	}
	if !strings.HasSuffix(full, waitFlowsInstructionsSection) {
		t.Error("等待流程一节应仍在指引最末尾")
	}
	if n := utf8.RuneCountInString(linkRefsInstructionsSection); n > 900 {
		t.Errorf("这一节每次 initialize 都会下发，要保持精炼：%d 字，上限 900", n)
	}

	// 四种写法都要讲到，并说清「目标存在才成链接」「代码块里不成链接」
	for _, mark := range []string{"#N", "Q<N>", "[[路径]]", "sess-xxxxxxxx", "[[design/x.md#第二节|说明]]", "目标真实存在", "代码块"} {
		if !strings.Contains(linkRefsInstructionsSection, mark) {
			t.Errorf("引用链接一节没有提到 %q", mark)
		}
	}
	// 任务清单一节里「提到任务写成 #N」的那条要指向这一节，不再单独讲一遍链接规则
	if !strings.Contains(taskBoardInstructionsSection, "写法见第 12 条") {
		t.Error("任务清单一节应指向第 12 条的引用写法")
	}
}

// 工具参数说明里的引用写法：模型每次调用工具都会读到参数说明，比 initialize 的指引更不容易被长上下文冲淡。
// 这几处（汇报正文、提问正文、任务描述、任务进展）正是前端会把引用变成链接的地方（任务行摘要、进度条文案不是）。
func TestToolSchemasMentionLinkRefs(t *testing.T) {
	tools := GetToolDefinitionsForPermissions(model.Permissions{Feedback: true, Sessions: true})
	for _, c := range []struct {
		tool  string
		path  []string // 参数在 inputSchema 里的位置（properties 一层层往下）
		marks []string
	}{
		{"interactive_feedback", []string{"summary"}, []string{"#N", "Q<N>", "[[path]]", "sess-xxxxxxxx", "code fences"}},
		{"async_ask", []string{"body"}, []string{"#N", "Q<N>", "[[path]]", "sess-xxxxxxxx"}},
		{"workflow_context", []string{"description"}, []string{"#N", "[[path]]", "task dialog"}},
		{"workflow_context", []string{"note"}, []string{"#N", "[[path]]", "timeline"}},
		{"task_board", []string{"ops", "description"}, []string{"#N", "[[path]]", "task dialog"}},
		{"task_board", []string{"ops", "note"}, []string{"#N", "[[path]]", "timeline"}},
	} {
		desc := schemaPropDescription(t, tools, c.tool, c.path...)
		for _, mark := range c.marks {
			if !strings.Contains(desc, mark) {
				t.Errorf("%s.%s 的参数说明没有提到 %q：%s", c.tool, strings.Join(c.path, "."), mark, desc)
			}
		}
	}
}

// schemaPropDescription 取某个工具某个参数的 description；path 是参数名的路径，数组参数（ops）自动钻进 items。
// 工具或参数不存在直接判失败。
func schemaPropDescription(t *testing.T, tools []ToolDefinition, tool string, path ...string) string {
	t.Helper()
	for _, td := range tools {
		if td.Name != tool {
			continue
		}
		var node map[string]any
		if err := json.Unmarshal(td.InputSchema, &node); err != nil {
			t.Fatalf("%s 的 inputSchema 不是合法 JSON：%v", tool, err)
		}
		for _, name := range path {
			if items, ok := node["items"].(map[string]any); ok {
				node = items
			}
			props, _ := node["properties"].(map[string]any)
			next, ok := props[name].(map[string]any)
			if !ok {
				t.Fatalf("%s 没有参数 %s", tool, strings.Join(path, "."))
			}
			node = next
		}
		desc, _ := node["description"].(string)
		return desc
	}
	t.Fatalf("没有工具 %s", tool)
	return ""
}
