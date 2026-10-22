package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 同步模式回执金样（INV-2）：
//
// 「异步对话模式」是用户在 hook 芯片里主动开启的升级路径；没开启的工作流（也就是默认）里，
// 智能体看到的每一条回执都必须与升级之前逐字一致——任何新增的回执行都只能在功能被启用 / 命中时才出现。
// 这个测试把一条覆盖面足够广的同步会话（keepalive、秒回、阶段、任务清单、检查点、交付锚点、
// 会话列表与历史……）的全部回执原样写进 testdata/sync_receipts.golden，之后每次运行逐字比对。
//
// 金样是在引入异步模式之前、由当时的 master 代码生成的；它不是「快照一下就完事」的测试：
// 谁要改动同步回执的任何一个字，都得在这里明说（RM_UPDATE_GOLDEN=1 重新生成并在提交里解释原因）。
//
// 回执里会随运行变化的东西（会话编号、时间戳、「N 分钟前」）先规整成占位符，
// 其余内容保持原样——占位符之外的任何差异都是真实差异。

var (
	goldenSessionID = regexp.MustCompile(`sess-[0-9a-f]{8}`)
	goldenRFC3339   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
	goldenGoTime    = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}(:\d{2}(\.\d+)?([+-]\d{2}:\d{2})?)?`)
	goldenAnchorAt  = regexp.MustCompile(`@ \d{2}-\d{2} \d{2}:\d{2}（[^）]*）`)
	goldenClock     = regexp.MustCompile(`, \d{2}:\d{2}\)`)
	goldenAge       = regexp.MustCompile(`\d+ ?(秒|分钟|小时|天)前`)
)

// goldenNormalizer 把会话编号按出现顺序编号（同一个会话在各处映射到同一个占位符），再规整时间。
type goldenNormalizer struct {
	ids map[string]string
}

func newGoldenNormalizer() *goldenNormalizer {
	return &goldenNormalizer{ids: map[string]string{}}
}

func (n *goldenNormalizer) apply(s string) string {
	s = goldenSessionID.ReplaceAllStringFunc(s, func(id string) string {
		if p, ok := n.ids[id]; ok {
			return p
		}
		p := fmt.Sprintf("sess-<%d>", len(n.ids)+1)
		n.ids[id] = p
		return p
	})
	s = goldenAnchorAt.ReplaceAllString(s, "@ <time>（<age>）")
	s = goldenRFC3339.ReplaceAllString(s, "<ts>")
	s = goldenGoTime.ReplaceAllString(s, "<ts>")
	s = goldenClock.ReplaceAllString(s, ", <hh:mm>)")
	s = goldenAge.ReplaceAllString(s, "<age>前")
	return s
}

// callToolAll 调用工具并返回回执里的全部文本块（decorateToolResult 追加的提示行是独立的块，不能漏）。
func callToolAll(t *testing.T, srv *Server, name string, args map[string]any) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var resp jsonRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON: %v\n%s", err, w.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("%s 调用失败: %#v", name, resp.Error)
	}
	result, _ := resp.Result.(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("%s 没有返回 content: %#v", name, resp.Result)
	}
	parts := make([]string, 0, len(content))
	for _, c := range content {
		item, _ := c.(map[string]any)
		text, _ := item["text"].(string)
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n--- 下一个文本块 ---\n")
}

// goldenScenario 跑完整条同步会话，返回规整之后的全部回执。
func goldenScenario(t *testing.T) string {
	t.Helper()
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	const wf = "wf-golden"
	const proj = "/tmp/golden-proj"

	norm := newGoldenNormalizer()
	var out strings.Builder
	rec := func(step, text string) {
		out.WriteString("##### " + step + "\n")
		out.WriteString(norm.apply(text))
		out.WriteString("\n\n")
	}
	pendingID := func() string {
		t.Helper()
		sessions, err := srv.store.GetSessionsByWorkflow(ctx, wf, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range sessions {
			if s.Status == "pending" {
				return s.ID
			}
		}
		t.Fatalf("工作流 %s 没有 pending 会话", wf)
		return ""
	}
	reply := func(text string) {
		t.Helper()
		if _, err := srv.store.SubmitFeedback(ctx, store.SubmitFeedbackInput{SessionID: pendingID(), ResponseText: text}); err != nil {
			t.Fatalf("模拟人类回复失败: %v", err)
		}
	}

	// 1. 第一轮：没人回复，等到超时，拿到 keepalive 回执（带阶段）
	rec("1 interactive_feedback → keepalive（discuss）", callToolAll(t, srv, "interactive_feedback", map[string]any{
		"workflow_id": wf, "project_directory": proj, "title": "golden 第一轮",
		"summary": "第一轮汇报：先看看现状。", "phase": "discuss",
	}))

	// 2. 人类回复后，continue_feedback_session 取到「用户反馈」回执
	reply("确认，继续。")
	rec("2 continue_feedback_session → 用户反馈", callToolAll(t, srv, "continue_feedback_session", map[string]any{
		"workflow_id": wf,
	}))

	// 3. 切阶段、建任务、存检查点：它们的回执，以及之后每条回执头里随之出现的行
	rec("3 workflow_context set_phase → plan", callToolAll(t, srv, "workflow_context", map[string]any{
		"action": "set_phase", "workflow_id": wf, "phase_id": "plan",
	}))
	rec("4 workflow_context task_create", callToolAll(t, srv, "workflow_context", map[string]any{
		"action": "task_create", "workflow_id": wf, "title": "金样任务", "kind": "improve",
		"description": "用来让回执头里出现任务清单概况。", "progress": "刚开始",
	}))
	rec("5 workflow_context checkpoint_save", callToolAll(t, srv, "workflow_context", map[string]any{
		"action": "checkpoint_save", "workflow_id": wf,
		"checkpoint": map[string]any{
			"objective":    "金样：覆盖回执头里的检查点行",
			"next_actions": []string{"继续第二轮"},
			"current_stage": map[string]any{
				"id": "s1", "status": "in_progress",
			},
		},
	}))

	// 4. 第二轮带交付锚点与阶段
	rec("6 interactive_feedback → keepalive（commit + dev）", callToolAll(t, srv, "interactive_feedback", map[string]any{
		"workflow_id": wf, "project_directory": proj, "title": "golden 第二轮",
		"summary": "第二轮汇报：已提交。", "phase": "dev", "commit": "abc1234 golden: 交付锚点",
	}))
	reply("收到，下一步。")
	rec("7 continue_feedback_session → 用户反馈（带锚点行）", callToolAll(t, srv, "continue_feedback_session", map[string]any{
		"workflow_id": wf,
	}))

	// 5. 用户提前追加，下一次 interactive_feedback 秒回
	if _, err := srv.store.QueueWorkflowFeedback(ctx, store.QueueFeedbackInput{
		WorkflowID: wf, ProjectDirectory: proj, ResponseText: "提前追加的一句话。",
	}); err != nil {
		t.Fatalf("追加失败: %v", err)
	}
	rec("8 interactive_feedback → 秒回", callToolAll(t, srv, "interactive_feedback", map[string]any{
		"workflow_id": wf, "project_directory": proj, "title": "golden 第三轮",
		"summary": "第三轮汇报：看到你提前说的话了。",
	}))

	// 6. 查询类工具
	rec("9 list_sessions group_by=workflow", callToolAll(t, srv, "list_sessions", map[string]any{
		"project_directory": proj, "group_by": "workflow",
	}))
	rec("10 list_sessions workflow_id", callToolAll(t, srv, "list_sessions", map[string]any{
		"workflow_id": wf, "status": "all",
	}))
	rec("11 get_session_history", callToolAll(t, srv, "get_session_history", map[string]any{
		"workflow_id": wf,
	}))
	rec("12 workflow_context list_workflows", callToolAll(t, srv, "workflow_context", map[string]any{
		"action": "list_workflows", "project_directory": proj,
	}))
	rec("13 workflow_context inbox（没有新消息）", callToolAll(t, srv, "workflow_context", map[string]any{
		"action": "inbox", "workflow_id": wf,
	}))
	rec("14 task_board overview", callToolAll(t, srv, "task_board", map[string]any{
		"action": "overview", "workflow_id": wf,
	}))

	return out.String()
}

const goldenSyncReceiptsPath = "testdata/sync_receipts.golden"

func TestSyncReceiptsGolden(t *testing.T) {
	got := goldenScenario(t)

	if os.Getenv("RM_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenSyncReceiptsPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenSyncReceiptsPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("已重新生成 %s（%d 字节）", goldenSyncReceiptsPath, len(got))
		return
	}

	wantBytes, err := os.ReadFile(goldenSyncReceiptsPath)
	if err != nil {
		t.Fatalf("读不到金样 %s: %v（首次生成用 RM_UPDATE_GOLDEN=1）", goldenSyncReceiptsPath, err)
	}
	want := string(wantBytes)
	if got == want {
		return
	}
	gotLines := strings.Split(got, "\n")
	wantLines := strings.Split(want, "\n")
	for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
		var g, w string
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			t.Fatalf("同步模式回执与金样不一致（第 %d 行起，INV-2：同步回执必须逐字不变）：\n期望: %s\n实际: %s", i+1, w, g)
		}
	}
	t.Fatalf("同步模式回执与金样不一致（长度 %d vs %d）", len(got), len(want))
}

// 同一个场景连跑两遍必须逐字相同，否则金样本身就不稳定（规整漏了某个随机量）。
func TestSyncReceiptsGoldenIsDeterministic(t *testing.T) {
	a := goldenScenario(t)
	b := goldenScenario(t)
	if a != b {
		al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
		for i := 0; i < len(al) && i < len(bl); i++ {
			if al[i] != bl[i] {
				t.Fatalf("两次运行的回执不一致（第 %d 行），规整漏了随机量：\nA: %s\nB: %s", i+1, al[i], bl[i])
			}
		}
		t.Fatalf("两次运行的回执长度不同：%d vs %d", len(a), len(b))
	}
}
