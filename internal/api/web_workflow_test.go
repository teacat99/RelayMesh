package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 网页新建会话（#174）+ 高级选项（#184）的接口层测试。store 层的规整与绑定规则见 internal/store/web_workflow_test.go，
// 这里只验接口：状态码、请求 / 响应形状、各个选项真的落到现有机制上（阶段 / 收件箱 / 会话行）、失败只进 warnings。

func createWorkflowCall(t *testing.T, srv *Server, body map[string]any) (int, map[string]any, string) {
	t.Helper()
	return ntfyCall(t, srv, http.MethodPost, "/api/v1/workflows", body)
}

// 只填名字：与改动前相同——201、占位会话、启动提示词；响应里没有 applied / warnings。
func TestCreateWorkflow_NameOnlyUnchanged(t *testing.T) {
	srv, _ := setupTestAPIServer(t)

	code, out, raw := createWorkflowCall(t, srv, map[string]any{"title": "支付重构"})
	if code != http.StatusCreated {
		t.Fatalf("只填名字应返回 201: %d %s", code, raw)
	}
	wf, _ := out["workflow_id"].(string)
	if !strings.HasPrefix(wf, "wf-web-") {
		t.Errorf("自动生成的编号应以 wf-web- 开头: %q", wf)
	}
	for _, k := range []string{"status", "workflow_id", "session", "start_prompt"} {
		if _, ok := out[k]; !ok {
			t.Errorf("响应缺少 %s: %s", k, raw)
		}
	}
	for _, k := range []string{"applied", "warnings"} {
		if _, ok := out[k]; ok {
			t.Errorf("没填高级选项时响应里不该有 %s（逐字节兼容）: %s", k, raw)
		}
	}
	sess, _ := out["session"].(map[string]any)
	if d, _ := sess["project_directory"].(string); d != "" {
		t.Errorf("没绑定目录时占位会话的目录应为空: %q", d)
	}
	if prompt, _ := out["start_prompt"].(string); !strings.Contains(prompt, "project_directory 填你当前工作区的绝对路径") {
		t.Errorf("没绑定目录时启动提示词应与以前一样:\n%s", prompt)
	}
}

// 四项高级选项全填：编号采用、目录与主机落到占位会话、阶段生效（同时是人工偏好阶段）、首条指令在收件箱里；响应的 applied 写明实际生效的值。
func TestCreateWorkflow_AllAdvancedOptions(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()
	sse := listenSSE(t, srv)

	code, out, raw := createWorkflowCall(t, srv, map[string]any{
		"title":             "高级会话",
		"workflow_id":       "wf-adv-1",
		"first_message":     "  先读 README，再列出改动计划\n不要动测试  ",
		"phase":             "plan",
		"project_directory": " /srv/adv-proj/ ",
		"host_name":         "dev-box",
	})
	if code != http.StatusCreated {
		t.Fatalf("应返回 201: %d %s", code, raw)
	}
	if out["workflow_id"] != "wf-adv-1" {
		t.Fatalf("应采用指定的编号: %v", out["workflow_id"])
	}
	if _, ok := out["warnings"]; ok {
		t.Fatalf("全部成功时不该有 warnings: %s", raw)
	}
	applied, _ := out["applied"].(map[string]any)
	if applied["phase"] != "plan" || applied["first_message"] != true || applied["project_directory"] != "/srv/adv-proj" || applied["host_name"] != "dev-box" {
		t.Errorf("applied 应写明实际生效的值: %v", applied)
	}
	sess, _ := out["session"].(map[string]any)
	if sess["project_directory"] != "/srv/adv-proj" || sess["host_name"] != "dev-box" {
		t.Errorf("占位会话应带规整后的目录与指定的主机: %v", sess)
	}
	if prompt, _ := out["start_prompt"].(string); !strings.Contains(prompt, `project_directory 必须原样填 "/srv/adv-proj"`) {
		t.Errorf("绑定目录后启动提示词应要求原样回填:\n%s", prompt)
	}

	// 阶段：当前阶段与人工偏好阶段都是 plan
	state, err := st.GetWorkflowPhase(ctx, "wf-adv-1")
	if err != nil || state == nil || state.CurrentPhaseID != model.PhasePlan || state.HumanPreferredPhaseID != model.PhasePlan {
		t.Fatalf("起始阶段应按人点的阶段写入: %+v err=%v", state, err)
	}
	// 首条指令：排进收件箱（占位会话已被消费，不并入它的回复），正文去掉首尾空白、保留内部换行，带着绑定的目录与主机
	q, err := st.ListPendingQueuedFeedbacks(ctx, "wf-adv-1")
	if err != nil || len(q) != 1 {
		t.Fatalf("收件箱里应正好有一条首条指令: %v err=%v", q, err)
	}
	if q[0].ResponseText != "先读 README，再列出改动计划\n不要动测试" || q[0].ProjectDirectory != "/srv/adv-proj" || q[0].HostName != "dev-box" {
		t.Errorf("收件箱里的首条指令不对: %+v", q[0])
	}
	// 广播：阶段变化、收件箱更新、新会话三类事件都发了
	joined := strings.Join(drainSSE(sse), "\n")
	for _, ev := range []string{"phase_changed", "queued_feedback_updated", "session_updated"} {
		if !strings.Contains(joined, ev) {
			t.Errorf("应广播 %s 事件:\n%s", ev, joined)
		}
	}
	// 占位会话本身没有被「回复」：仍是已完成且已被消费的 notice
	got, err := st.GetFeedbackSession(ctx, sess["session_id"].(string))
	if err != nil || got.Status != "completed" || !got.ConsumedByAI || got.ResponseText != "" {
		t.Errorf("首条指令不该并入占位会话的回复: %+v err=%v", got, err)
	}
}

// 只选阶段、只填指令、只填目录、只填主机：各自独立可选；旧五段式的阶段编号也接受。
func TestCreateWorkflow_EachOptionIndependent(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()

	code, out, raw := createWorkflowCall(t, srv, map[string]any{"title": "只选阶段", "phase": "assess"})
	if code != http.StatusCreated {
		t.Fatalf("只选阶段: %d %s", code, raw)
	}
	wf := out["workflow_id"].(string)
	if state, _ := st.GetWorkflowPhase(ctx, wf); state == nil || state.CurrentPhaseID != model.PhaseDiscuss {
		t.Errorf("旧编号 assess 应换算成 discuss: %+v", state)
	}
	if q, _ := st.ListPendingQueuedFeedbacks(ctx, wf); len(q) != 0 {
		t.Errorf("没写首条指令就不该有收件箱条目: %v", q)
	}

	code, out, raw = createWorkflowCall(t, srv, map[string]any{"title": "只填指令", "first_message": "开工"})
	if code != http.StatusCreated {
		t.Fatalf("只填指令: %d %s", code, raw)
	}
	wf = out["workflow_id"].(string)
	if q, _ := st.ListPendingQueuedFeedbacks(ctx, wf); len(q) != 1 {
		t.Errorf("应有一条首条指令: %v", q)
	}
	if state, _ := st.GetWorkflowPhase(ctx, wf); state != nil {
		t.Errorf("没选阶段就不该写阶段记录（沿用默认）: %+v", state)
	}

	code, out, raw = createWorkflowCall(t, srv, map[string]any{"title": "只填目录", "project_directory": `C:\Users\me\proj\`})
	if code != http.StatusCreated {
		t.Fatalf("只填目录: %d %s", code, raw)
	}
	sess, _ := out["session"].(map[string]any)
	if sess["project_directory"] != `C:\Users\me\proj` {
		t.Errorf("Windows 路径只去末尾分隔符: %v", sess["project_directory"])
	}
	applied, _ := out["applied"].(map[string]any)
	if _, has := applied["host_name"]; has || applied["project_directory"] != `C:\Users\me\proj` {
		t.Errorf("applied 只写填了的那些项: %v", applied)
	}

	code, out, raw = createWorkflowCall(t, srv, map[string]any{"title": "只填主机", "host_name": "laptop"})
	if code != http.StatusCreated {
		t.Fatalf("只填主机: %d %s", code, raw)
	}
	sess, _ = out["session"].(map[string]any)
	if sess["host_name"] != "laptop" || sess["project_directory"] != "" {
		t.Errorf("只填主机时目录应为空、主机是指定的: %v", sess)
	}
}

// 校验类错误一律 400，且发生在落库之前：不留半个会话，也不留阶段 / 收件箱。
func TestCreateWorkflow_BadAdvancedInputIs400AndLeavesNothing(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()

	bad := []map[string]any{
		{"title": "坏阶段", "workflow_id": "wf-bad-1", "phase": "review", "first_message": "x"},
		{"title": "坏指令", "workflow_id": "wf-bad-2", "first_message": strings.Repeat("字", 20001), "phase": "dev"},
		{"title": "坏目录", "workflow_id": "wf-bad-3", "project_directory": "relative/dir", "first_message": "x", "phase": "dev"},
		{"title": "坏主机", "workflow_id": "wf-bad-4", "host_name": strings.Repeat("h", 121), "first_message": "x", "phase": "dev"},
		{"title": "坏编号", "workflow_id": "ab", "first_message": "x", "phase": "dev"},
	}
	for _, body := range bad {
		code, out, raw := createWorkflowCall(t, srv, body)
		if code != http.StatusBadRequest {
			t.Errorf("%v 应返回 400: %d %s", body["title"], code, raw)
			continue
		}
		if msg, _ := out["error"].(string); msg == "" {
			t.Errorf("400 应带错误原因: %s", raw)
		}
	}
	for _, wf := range []string{"wf-bad-1", "wf-bad-2", "wf-bad-3", "wf-bad-4", "ab"} {
		if rows, _ := st.GetSessionsByWorkflow(ctx, wf, 5); len(rows) != 0 {
			t.Errorf("被拒绝的请求不该落会话: %s %d 行", wf, len(rows))
		}
		if state, _ := st.GetWorkflowPhase(ctx, wf); state != nil {
			t.Errorf("被拒绝的请求不该写阶段: %s %+v", wf, state)
		}
		if q, _ := st.ListPendingQueuedFeedbacks(ctx, wf); len(q) != 0 {
			t.Errorf("被拒绝的请求不该排收件箱: %s %v", wf, q)
		}
	}
}

// 编号撞了：409，且不动原来的会话、阶段与收件箱。
func TestCreateWorkflow_DuplicateIDIs409AndKeepsTheOriginal(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()

	if code, _, raw := createWorkflowCall(t, srv, map[string]any{"title": "原来的", "workflow_id": "wf-dup-1", "phase": "dev"}); code != http.StatusCreated {
		t.Fatalf("先建一个: %d %s", code, raw)
	}
	code, out, raw := createWorkflowCall(t, srv, map[string]any{
		"title": "撞号的", "workflow_id": "wf-dup-1", "phase": "plan", "first_message": "不该进收件箱", "project_directory": "/srv/x", "host_name": "h",
	})
	if code != http.StatusConflict {
		t.Fatalf("撞号应返回 409: %d %s", code, raw)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "wf-dup-1") {
		t.Errorf("409 的原因应写明撞的是哪个编号: %q", msg)
	}
	rows, _ := st.GetSessionsByWorkflow(ctx, "wf-dup-1", 5)
	if len(rows) != 1 || rows[0].Title != "原来的" || rows[0].ProjectDirectory != "" {
		t.Errorf("撞号不该改动原来的会话: %+v", rows)
	}
	if state, _ := st.GetWorkflowPhase(ctx, "wf-dup-1"); state == nil || state.CurrentPhaseID != model.PhaseDev {
		t.Errorf("撞号不该改动原来的阶段: %+v", state)
	}
	if q, _ := st.ListPendingQueuedFeedbacks(ctx, "wf-dup-1"); len(q) != 0 {
		t.Errorf("撞号不该排收件箱: %v", q)
	}
}

// 落库之后首条指令投递失败时，只进 warnings，会话与阶段仍然成功：用「收件箱已满（store.InboxMaxPending）」构造这种失败。
func TestCreateWorkflow_FirstMessageFailureOnlyWarns(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()

	// 给一个还不存在的工作流预先塞满收件箱，再用同一个编号去建：编号不撞（还没有会话行），收件箱却已经满了。
	for i := 0; i < store.InboxMaxPending; i++ {
		if _, err := st.QueueWorkflowFeedback(ctx, store.QueueFeedbackInput{WorkflowID: "wf-warn-1", ResponseText: fmt.Sprintf("占位追加 %d", i)}); err != nil {
			t.Fatalf("预先塞满收件箱 %d: %v", i, err)
		}
	}
	code, out, raw := createWorkflowCall(t, srv, map[string]any{"title": "收件箱满", "workflow_id": "wf-warn-1", "phase": "plan", "first_message": "排不进去"})
	if code != http.StatusCreated {
		t.Fatalf("首条指令失败不该让整个请求失败: %d %s", code, raw)
	}
	warnings, _ := out["warnings"].([]any)
	if len(warnings) != 1 {
		t.Fatalf("应有一条 warnings: %s", raw)
	}
	w0, _ := warnings[0].(map[string]any)
	if w0["field"] != "first_message" || w0["error"] == "" {
		t.Errorf("warning 应指明字段与原因: %v", w0)
	}
	applied, _ := out["applied"].(map[string]any)
	if applied["phase"] != "plan" {
		t.Errorf("阶段仍应成功: %v", applied)
	}
	if _, has := applied["first_message"]; has {
		t.Errorf("首条指令没成功就不该出现在 applied 里: %v", applied)
	}
	if rows, _ := st.GetSessionsByWorkflow(ctx, "wf-warn-1", 5); len(rows) != 1 {
		t.Errorf("会话必须保留: %d 行", len(rows))
	}
}
