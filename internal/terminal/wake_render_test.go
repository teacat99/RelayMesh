package terminal

import (
	"context"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 唤醒词渲染的终端侧封装（#232 D3，D-376）：升级前各出口的特殊行为要保留，新增的分层要生效。

func newWakeStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	return st
}

func TestRenderLauncherWake_PresetBodyKeepsLegacyBehaviour(t *testing.T) {
	st := newWakeStore(t)
	ctx := context.Background()
	res := renderLauncherWake(ctx, st, launcherWake{Kind: sessionprompt.KindResume, Custom: "  看看 %workflow 的收件箱  ", WorkflowID: "wf-2", MaxRunSec: 1800})
	if res.Text != "看看 wf-2 的收件箱" || res.Layer != store.WakeLayerPreset {
		t.Fatalf("预设自己的正文原样渲染、不补时限提醒：%q (%s)", res.Text, res.Layer)
	}
	// 新开对话同理
	res = renderLauncherWake(ctx, st, launcherWake{Kind: sessionprompt.KindNew, Custom: "接入 %workflow", WorkflowID: " wf-3 ", MaxRunSec: 1800})
	if res.Text != "接入 wf-3" || res.Layer != store.WakeLayerPreset {
		t.Fatalf("新开对话预设正文：%q (%s)", res.Text, res.Layer)
	}
}

func TestRenderLauncherWake_BuiltinGetsLimitNote(t *testing.T) {
	st := newWakeStore(t)
	ctx := context.Background()
	for _, kind := range []sessionprompt.Kind{sessionprompt.KindResume, sessionprompt.KindNew} {
		base := renderLauncherWake(ctx, st, launcherWake{Kind: kind, WorkflowID: "wf-1"})
		if base.Layer != store.WakeLayerBuiltin || strings.Contains(base.Text, "本次运行最长") || strings.Contains(base.Text, "%") {
			t.Fatalf("%s 没有时限时不补提醒：%q (%s)", kind, base.Text, base.Layer)
		}
		got := renderLauncherWake(ctx, st, launcherWake{Kind: kind, WorkflowID: "wf-1", MaxRunSec: 1800})
		if got.Text != base.Text+" "+launcherRunLimitNote(1800) {
			t.Fatalf("%s 内置默认后面补一句最长运行时间：\n%s", kind, got.Text)
		}
		if no := renderLauncherWake(ctx, st, launcherWake{Kind: kind, WorkflowID: "wf-1", MaxRunSec: 1800, NoLimitNote: true}); no.Text != base.Text {
			t.Fatalf("%s 会话页运行不补时限提醒：\n%s", kind, no.Text)
		}
		if !strings.Contains(base.Text, "wf-1") {
			t.Fatalf("%s 的默认应点名工作流：%q", kind, base.Text)
		}
	}
}

// 没有工作流：续聊的默认改用不点名的写法，不留变量与残句。
func TestRenderLauncherWake_NoWorkflow(t *testing.T) {
	st := newWakeStore(t)
	res := renderLauncherWake(context.Background(), st, launcherWake{Kind: sessionprompt.KindResume, Project: "/srv/app"})
	if strings.Contains(res.Text, "%") || strings.Contains(res.Text, "工作流 （") || !strings.Contains(res.Text, "RelayMesh") {
		t.Fatalf("没有工作流时的默认：%q", res.Text)
	}
}

// 会话模板与全局覆盖里可以用 %limit 放时限提醒；它们是用户自己写的话，Hub 不再另外追加。
func TestRenderLauncherWake_LimitVarInUserTemplates(t *testing.T) {
	st := newWakeStore(t)
	ctx := context.Background()
	if err := st.SaveSettings(ctx, map[string]any{"wakeTemplates": map[string]any{"resume": "全局：继续 %workflow。%limit"}}); err != nil {
		t.Fatal(err)
	}
	res := renderLauncherWake(ctx, st, launcherWake{Kind: sessionprompt.KindResume, WorkflowID: "wf-g", MaxRunSec: 600})
	if res.Layer != store.WakeLayerGlobal || res.Text != "全局：继续 wf-g。"+launcherRunLimitNote(600) {
		t.Fatalf("全局覆盖里的 %%limit：%q (%s)", res.Text, res.Layer)
	}
	if res = renderLauncherWake(ctx, st, launcherWake{Kind: sessionprompt.KindResume, WorkflowID: "wf-g"}); res.Text != "全局：继续 wf-g。" {
		t.Fatalf("没有时限时 %%limit 渲染为空：%q", res.Text)
	}
}
