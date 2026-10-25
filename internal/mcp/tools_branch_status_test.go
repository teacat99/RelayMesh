package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 主会话的 branch list 带分支的状态（#236 P4）：Agent 状态、没做完的任务数、启动方式 + 下一步怎么做；
// 开放范围里多一项 launch，摘要也写明「可启动 / 不可启动」。

const statusTestHost = "RM-7K2F-9XQ4-M3WD-8ABC"

// branchItem 取 wf-main 的 branch list 里某个分支那一项。
func branchItem(t *testing.T, srv *Server, branch string) map[string]any {
	t.Helper()
	out := branchDo(t, srv, "wf-main", map[string]any{"action": "list"})
	items, _ := out["branches"].([]any)
	for _, it := range items {
		if m, _ := it.(map[string]any); m != nil && m["branch"] == branch {
			return m
		}
	}
	t.Fatalf("branch list 里没有 %s: %v", branch, out)
	return nil
}

func TestBranchList_StatusFieldsForMain(t *testing.T) {
	srv, _ := branchFixture(t)
	ctx := context.Background()
	bwf := paramsBranch(t, srv, "状态分支")

	// 什么数据都没有：状态 unknown（不是缺省）、没有任务字段、启动方式写明「没打开、没有预设」并给出下一步。
	item := branchItem(t, srv, bwf)
	if item["agent_state"] != "unknown" {
		t.Fatalf("没有任何状态上报应是 unknown: %v", item["agent_state"])
	}
	if _, has := item["open_tasks"]; has {
		t.Fatalf("没有没做完的任务就不该带 open_tasks: %v", item["open_tasks"])
	}
	launch, _ := item["launch"].(map[string]any)
	if launch == nil || launch["open"] != false || launch["presets"] != float64(0) {
		t.Fatalf("启动方式应写明没打开、没有预设: %v", item["launch"])
	}
	if how, _ := launch["how"].(string); !strings.Contains(how, "还没有打开") || !strings.Contains(how, "「分支」面板") {
		t.Fatalf("没打开时要告诉主会话请用户去哪里打开: %v", launch["how"])
	}
	expose, _ := item["expose"].(map[string]any)
	if expose["launch"] != false || !strings.Contains(expose["summary"].(string), "不可启动") {
		t.Fatalf("协作预设的开放范围里启动是关的，摘要写明不可启动: %v", expose)
	}

	// 分支的智能体上报了状态：list 里是对应状态与时间。
	at := time.Now().Add(-time.Second).UTC()
	if _, err := srv.store.IngestAgentEvent(ctx, store.AgentIngestInput{
		WorkflowID: bwf, CredentialID: 7, HostName: "devhost", ProjectDirectory: "/code/a", ConversationID: "conv-b",
		Event: agentstate.Event{Source: agentstate.SourceHook, Name: agentstate.EvPrompt, At: at, Received: at},
	}); err != nil {
		t.Fatal(err)
	}
	item = branchItem(t, srv, bwf)
	if item["agent_state"] != "working" {
		t.Fatalf("刚提交过提示词的智能体应是 working: %v", item["agent_state"])
	}
	if s, _ := item["agent_state_at"].(string); s == "" {
		t.Fatalf("有状态就该带这个状态起的时间: %v", item)
	} else if _, err := time.Parse(time.RFC3339, s); err != nil {
		t.Fatalf("agent_state_at 应是 RFC3339: %q", s)
	}

	// 分支名下有没做完的任务：按状态分开数，值为 0 的状态不出现。
	for i, state := range []string{"todo", "doing", "doing"} {
		if _, err := srv.store.CreateWorkItem(ctx, store.WorkItemInput{
			WorkflowID: bwf, Title: "分支的任务" + string(rune('甲'+i)), State: state,
		}, store.WorkActor{Kind: model.WorkActorAgent}); err != nil {
			t.Fatalf("建任务: %v", err)
		}
	}
	item = branchItem(t, srv, bwf)
	tasks, _ := item["open_tasks"].(map[string]any)
	if tasks["todo"] != float64(1) || tasks["doing"] != float64(2) {
		t.Fatalf("应按状态数出没做完的任务: %v", item["open_tasks"])
	}
	if _, has := tasks["review"]; has {
		t.Fatalf("数量为 0 的状态不该出现: %v", tasks)
	}

	// 用户打开「启动」、也有指向它的续聊预设：launch 写预设并给出 preset_run 的用法；摘要变成「可启动」。
	e, _ := model.BranchExposePreset(model.ExposePresetCollab)
	e.Launch = true
	if _, err := srv.store.UpdateBranchSettings(ctx, bwf, store.UpdateBranchInput{Expose: &e}); err != nil {
		t.Fatal(err)
	}
	item = branchItem(t, srv, bwf)
	launch, _ = item["launch"].(map[string]any)
	if launch["open"] != true || launch["presets"] != float64(0) {
		t.Fatalf("打开了但还没有预设: %v", launch)
	}
	if how, _ := launch["how"].(string); !strings.Contains(how, "还没有指向这个分支") {
		t.Fatalf("打开了但没有预设时要告诉主会话请用户去创建: %v", launch["how"])
	}
	if _, _, err := srv.store.UpsertTerminalLauncher(ctx, store.TerminalLauncher{
		ID: "p-branch", Name: "续聊分支", Host: statusTestHost, Dir: "/code/a", Kind: store.LauncherKindHeadless,
		Run: &store.HeadlessRun{WorkflowID: bwf, Posture: store.HeadlessPostureForce},
	}); err != nil {
		t.Fatalf("存预设: %v", err)
	}
	item = branchItem(t, srv, bwf)
	launch, _ = item["launch"].(map[string]any)
	if launch["open"] != true || launch["presets"] != float64(1) || launch["preset"] != "p-branch" || launch["preset_name"] != "续聊分支" {
		t.Fatalf("应带出预设与数量: %v", launch)
	}
	if how, _ := launch["how"].(string); !strings.Contains(how, `preset_run(action: "run", preset: "p-branch")`) {
		t.Fatalf("how 应给出可直接照着调的 preset_run 用法: %v", launch["how"])
	}
	expose, _ = item["expose"].(map[string]any)
	if expose["launch"] != true || !strings.Contains(expose["summary"].(string), "可启动") || strings.Contains(expose["summary"].(string), "不可启动") {
		t.Fatalf("打开后开放范围里 launch 为 true、摘要写明可启动: %v", expose)
	}
}

// 分支自己看到的是「向主会话开放什么」：同样多一项 launch（它能知道主会话能不能启动自己，但看不到主会话的状态面）。
func TestBranchList_BranchSeesItsOwnLaunchExposure(t *testing.T) {
	srv, _ := branchFixture(t)
	bwf := paramsBranch(t, srv, "自己看自己")
	out := branchDo(t, srv, bwf, map[string]any{"action": "list"})
	if out["role"] != "branch" {
		t.Fatalf("分支的角色: %v", out)
	}
	expose, _ := out["expose_to_main"].(map[string]any)
	if expose == nil || expose["launch"] != false {
		t.Fatalf("expose_to_main 应带 launch 项（协作预设默认不开）: %v", out["expose_to_main"])
	}
	for _, k := range []string{"agent_state", "open_tasks", "launch"} {
		if _, has := out[k]; has {
			t.Fatalf("分支自己的视图不该带主会话才看的状态面 %s: %v", k, out[k])
		}
	}
}
