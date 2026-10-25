package terminal

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 会话页底部「运行」按钮（#140）：哪些非交互续聊预设「对应」这个会话、此刻能不能运行、没有时怎么建议。

// saveLauncherAt 用指定的主机、目录保存一条预设；run 为 nil 时存成普通的终端预设。
func (f *execFixture) saveLauncherAt(t *testing.T, id, name, host, dir string, run map[string]any) {
	t.Helper()
	body := map[string]any{"name": name, "host": host, "dir": dir}
	if run != nil {
		body["kind"], body["run"] = "headless", run
	}
	if res := f.env.putLauncher("/api/v1/terminal/launchers/"+id, body, f.adm...); res.Status != 200 {
		t.Fatalf("保存预设 %s 失败: %d %s", id, res.Status, res.Raw)
	}
}

func (f *execFixture) sessionLaunchers(t *testing.T, wf string) SessionLaunchersView {
	t.Helper()
	v, err := f.env.hub.guardian.sessionLaunchers(context.Background(), wf, time.Now())
	if err != nil {
		t.Fatalf("读取会话对应的预设: %v", err)
	}
	return v
}

func launcherIDsOf(v SessionLaunchersView) []string {
	out := []string{}
	for _, p := range v.Presets {
		out = append(out, p.ID)
	}
	return out
}

func issueCodesOf(p SessionLauncherView) []string {
	out := []string{}
	for _, i := range p.Issues {
		out = append(out, i.Code)
	}
	return out
}

func wantIDs(t *testing.T, v SessionLaunchersView, want ...string) {
	t.Helper()
	got := launcherIDsOf(v)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("对应的预设应是 %v，得到 %v（%+v）", want, got, v)
	}
}

// 对应 = 主机 + 目录 + 跟的是这个会话；三条缺一不可，类型也要对。
func TestSessionLaunchersCorrespondByHostDirAndSession(t *testing.T) {
	f := sessionFixture(t) // 工作流 wf-session：对话 chat-s1、主机 exec-host、目录 /work/app、已停止
	other := f.env.enrollAgent(testMeta("other-box"), "")
	wf := map[string]any{"workflow_id": sessWF}

	f.saveLauncherAt(t, "p1", "A 工作流", f.a.code, "/work/app", wf)
	f.saveLauncherAt(t, "p2", "B 固定对话", f.a.code, "/work/app", map[string]any{"conversation_id": sessConv})
	f.saveLauncherAt(t, "p7", "C 目录带斜杠", f.a.code, "/work/app/", wf)
	// 以下都不对应：
	f.saveLauncherAt(t, "p3", "D 别的工作流", f.a.code, "/work/app", map[string]any{"workflow_id": "wf-other"})
	f.saveLauncherAt(t, "p3b", "E 别的对话", f.a.code, "/work/app", map[string]any{"conversation_id": "chat-other"})
	f.saveLauncherAt(t, "p4", "别的目录", f.a.code, "/work/app-other", wf)
	f.saveLauncherAt(t, "p4b", "子目录", f.a.code, "/work/app/sub", wf)
	f.saveLauncherAt(t, "p5", "别的主机", other.code, "/work/app", wf)
	f.saveLauncherAt(t, "p6", "终端预设", f.a.code, "/work/app", nil)

	v := f.sessionLaunchers(t, sessWF)
	wantIDs(t, v, "p1", "p2", "p7")
	if v.Host != f.a.code || len(v.Hosts) != 1 || v.Hosts[0] != f.a.code || v.Dir != "/work/app" || v.ConversationID != sessConv || v.SessionHost != "exec-host" {
		t.Fatalf("会话目标不对: %+v", v)
	}
	if v.Suggest != nil || v.Reason != "" {
		t.Fatalf("已有对应预设时不该再建议: %+v", v)
	}
	for _, p := range v.Presets {
		if !p.CanRun || p.Busy || len(p.Issues) != 0 || p.LastRun != nil {
			t.Fatalf("空闲的会话上，对应预设应可运行: %+v", p)
		}
		if p.ConversationID != sessConv || p.Host != f.a.code || p.HostTitle != "build box" || !strings.Contains(p.Command, "--resume chat-s1") {
			t.Fatalf("确认窗要展示的内容不对: %+v", p)
		}
		if p.Posture != store.HeadlessPostureAllowlist || p.MaxRunSeconds != headlessTimeoutSec || !strings.Contains(strings.Join(p.Warnings, "|"), "Mcp(") {
			t.Fatalf("默认姿态与时限、白名单提示不对: %+v", p)
		}
	}

	// 别的会话（另一个工作流）看到的是它自己的预设，而不是同一台主机同一个目录里的所有预设。
	f.sessionEvent(t, "wf-other", "chat-other", "exec-host", "/work/app", "cli", agentstate.EvStop, time.Now())
	vo := f.sessionLaunchers(t, "wf-other")
	wantIDs(t, vo, "p3", "p3b")
}

// 现在不能运行的原因：与提交运行是同一份判断（第一个原因就是点了会被拒绝的原因）；对话还在运行 / 上一次没结束算「忙」。
func TestSessionLaunchersIssuesMatchWhatRunWouldRefuse(t *testing.T) {
	setup := func(t *testing.T) *execFixture {
		f := sessionFixture(t)
		f.saveLauncherAt(t, "p1", "续聊", f.a.code, "/work/app", map[string]any{"workflow_id": sessWF})
		return f
	}
	only := func(t *testing.T, f *execFixture) SessionLauncherView {
		t.Helper()
		v := f.sessionLaunchers(t, sessWF)
		if len(v.Presets) != 1 {
			t.Fatalf("应只有一个对应预设: %+v", v)
		}
		return v.Presets[0]
	}
	// 视图说的原因 == 真的点运行时被拒绝的原因。
	agree := func(t *testing.T, f *execFixture, p SessionLauncherView, code string) {
		t.Helper()
		if p.CanRun || len(p.Issues) == 0 || p.Issues[0].Code != code || strings.TrimSpace(p.Issues[0].Message) == "" {
			t.Fatalf("应因 %s 不能运行: %+v", code, p)
		}
		_, err := f.runLauncher("p1")
		wantWakeCode(t, err, code)
		if f.sim.runCount() != 0 {
			t.Fatal("被拒绝的运行不该到达主机")
		}
	}

	t.Run("主机离线：不是忙，按钮显示成不可点", func(t *testing.T) {
		f := setup(t)
		f.a.close()
		eventually(t, 3*time.Second, "主机离线", func() bool { return f.env.hub.onlineAgent(f.a.code) == nil })
		p := only(t, f)
		if p.Busy {
			t.Fatalf("主机离线不是「忙」: %+v", p)
		}
		agree(t, f, p, "host_offline")
	})

	t.Run("对话还在运行：忙", func(t *testing.T) {
		f := setup(t)
		f.sessionEvent(t, sessWF, sessConv, "exec-host", "/work/app", "cli", agentstate.EvPrompt, time.Now())
		p := only(t, f)
		if !p.Busy || p.Command == "" {
			t.Fatalf("对话还在运行：算忙，命令预览仍在: %+v", p)
		}
		agree(t, f, p, "conversation_live")
	})

	t.Run("上一次运行没结束：忙，现状里是进行中", func(t *testing.T) {
		f := setup(t)
		first, err := f.runLauncher("p1") // 假主机不结束它
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })
		p := only(t, f)
		if !p.Busy || p.LastRun == nil || !p.LastRun.Active || p.LastRun.RunID != first.RunID || p.LastRun.State != model.RunRunning {
			t.Fatalf("进行中的运行应算忙并带现状: %+v", p)
		}
		_, err = f.runLauncher("p1")
		wantWakeCode(t, err, "run_active")
	})

	t.Run("运行结束后：又能运行，带最近一次现状", func(t *testing.T) {
		f := setup(t)
		f.completeWith(launcherResultOut)
		plan, err := f.runLauncher("p1")
		if err != nil {
			t.Fatal(err)
		}
		f.waitRunFinal(t, plan.RunID)
		p := only(t, f)
		if !p.CanRun || p.Busy || p.LastRun == nil || p.LastRun.Active || p.LastRun.State != model.RunSucceeded {
			t.Fatalf("结束后应可再运行并带最近一次现状: %+v", p)
		}
	})

	t.Run("预设固定的对话在这台主机上不存在时也照常解析", func(t *testing.T) {
		f := sessionFixture(t)
		f.saveLauncherAt(t, "p1", "固定对话", f.a.code, "/work/app", map[string]any{"conversation_id": sessConv})
		p := only(t, f)
		if !p.CanRun || p.ConversationID != sessConv {
			t.Fatalf("固定对话应可运行: %+v", p)
		}
	})

	t.Run("需批准的主机：可运行，但提前说明要去批准", func(t *testing.T) {
		f := setup(t)
		f.setModel(t, map[string]any{"allow_model": true, "exec_mode": "approve"})
		p := only(t, f)
		if !p.CanRun || !strings.Contains(strings.Join(p.Warnings, "|"), "需批准") {
			t.Fatalf("需批准的主机应提前提示: %+v", p)
		}
	})

	t.Run("force 姿态没有白名单提示", func(t *testing.T) {
		f := sessionFixture(t)
		f.saveLauncherAt(t, "p1", "放开", f.a.code, "/work/app", map[string]any{"workflow_id": sessWF, "posture": "force", "model": "m1", "max_run_minutes": 30})
		p := only(t, f)
		if p.Posture != store.HeadlessPostureForce || p.Model != "m1" || p.MaxRunSeconds != 1800 || strings.Contains(strings.Join(p.Warnings, "|"), "Mcp(") {
			t.Fatalf("force 姿态、模型与时限应如实展示: %+v", p)
		}
		if !strings.Contains(p.Command, "--force") || !strings.Contains(p.Command, "--model 'm1'") || !strings.Contains(p.Command, "本次运行最长 30 分钟") {
			t.Fatalf("命令预览应带上姿态、模型与时限提醒: %s", p.Command)
		}
	})
}

// 预览里的命令就是点「运行」之后真正下发给主机的那一行。
func TestSessionLaunchersPreviewIsWhatRunSubmits(t *testing.T) {
	f := sessionFixture(t)
	f.saveLauncherAt(t, "p1", "续聊", f.a.code, "/work/app", map[string]any{"workflow_id": sessWF, "model": "m1", "posture": "force"})
	f.completeWith(launcherResultOut)
	v := f.sessionLaunchers(t, sessWF)
	if len(v.Presets) != 1 || !v.Presets[0].CanRun {
		t.Fatalf("应有一个可运行的对应预设: %+v", v)
	}
	plan, err := f.runLauncher("p1")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })
	if got := f.sim.lastRun().Cmd; got != v.Presets[0].Command {
		t.Fatalf("预览与真正下发的命令不一致：\npreview %s\n actual %s", v.Presets[0].Command, got)
	}
	if plan.ConversationID != v.Presets[0].ConversationID || plan.MaxRunSeconds != v.Presets[0].MaxRunSeconds {
		t.Fatalf("对话与时限也应一致: %+v vs %+v", plan, v.Presets[0])
	}
	// 预览本身是只读的：读了好几遍也没有多出运行。
	f.waitRunFinal(t, plan.RunID)
	f.sessionLaunchers(t, sessWF)
	f.sessionLaunchers(t, sessWF)
	if f.sim.runCount() != 1 {
		t.Fatalf("读取视图不该提交运行: %d", f.sim.runCount())
	}
}

// 没有对应预设：给一份预填好的建议（名字不与已有的重复）；建议不出来时说明原因。
func TestSessionLaunchersSuggestsWhenNothingCorresponds(t *testing.T) {
	f := sessionFixture(t)

	v := f.sessionLaunchers(t, sessWF)
	s := v.Suggest
	if len(v.Presets) != 0 || s == nil || v.Reason != "" {
		t.Fatalf("没有对应预设时应给建议: %+v", v)
	}
	if s.Name != "续聊 · app" || s.Host != f.a.code || s.HostTitle != "build box" || s.Dir != "/work/app" || s.WorkflowID != sessWF ||
		s.Posture != store.HeadlessPostureAllowlist || s.NeedsHost {
		t.Fatalf("建议的内容不对: %+v", s)
	}

	// 名字被占了（不分大小写）：加序号；别的工作流的预设不算对应，也不挡建议。
	f.saveLauncherAt(t, "x1", "续聊 · APP", f.a.code, "/work/app", map[string]any{"workflow_id": "wf-other"})
	if v = f.sessionLaunchers(t, sessWF); v.Suggest == nil || v.Suggest.Name != "续聊 · app 2" {
		t.Fatalf("重名时应加序号: %+v", v.Suggest)
	}
	f.saveLauncherAt(t, "x2", "续聊 · app 2", f.a.code, "/work/app", map[string]any{"workflow_id": "wf-other"})
	if v = f.sessionLaunchers(t, sessWF); v.Suggest == nil || v.Suggest.Name != "续聊 · app 3" {
		t.Fatalf("序号应继续往后找: %+v", v.Suggest)
	}

	// 建议的名字过长：截断后仍是合法名字。
	long := "/work/" + strings.Repeat("长", 80)
	f.sessionEvent(t, "wf-long", "chat-long", "exec-host", long, "cli", agentstate.EvStop, time.Now())
	if lv := f.sessionLaunchers(t, "wf-long"); lv.Suggest == nil || len([]rune(lv.Suggest.Name)) > store.MaxLauncherNameRunes {
		t.Fatalf("名字应截到上限内: %+v", lv.Suggest)
	}

	// 有了对应预设，就不再建议。
	f.saveLauncherAt(t, "p1", "续聊", f.a.code, "/work/app", map[string]any{"workflow_id": sessWF})
	if v = f.sessionLaunchers(t, sessWF); v.Suggest != nil || len(v.Presets) != 1 {
		t.Fatalf("已有对应预设时不该再建议: %+v", v)
	}
}

func TestSessionLaunchersNoSuggestionReasons(t *testing.T) {
	t.Run("预设数量到上限", func(t *testing.T) {
		f := sessionFixture(t)
		for i := 0; i < store.MaxTerminalLaunchers; i++ {
			f.saveLauncherAt(t, fmt.Sprintf("q%d", i), fmt.Sprintf("占位 %d", i), f.a.code, "/work/elsewhere", nil)
		}
		v := f.sessionLaunchers(t, sessWF)
		if v.Suggest != nil || !strings.Contains(v.Reason, "上限") {
			t.Fatalf("到上限时不该建议，并说明原因: %+v", v)
		}
	})
	t.Run("会话没有上报目录", func(t *testing.T) {
		f := newExecFixture(t)
		f.sessionEvent(t, "wf-nodir", "chat-d1", "exec-host", "", "cli", agentstate.EvStop, time.Now())
		f.saveLauncherAt(t, "p1", "续聊", f.a.code, "/work/app", map[string]any{"workflow_id": "wf-nodir"})
		v := f.sessionLaunchers(t, "wf-nodir")
		if len(v.Presets) != 0 || v.Suggest != nil || v.Dir != "" || !strings.Contains(v.Reason, "目录") || strings.Contains(v.Reason, "更多选项") {
			t.Fatalf("不知道目录就没法判断对应，也没法建议（原因不能指向已经没有了的「更多选项」）: %+v", v)
		}
	})
	t.Run("会话上报的主机清单里没有", func(t *testing.T) {
		f := newExecFixture(t)
		f.sessionEvent(t, "wf-ghost", "chat-g1", "some-other-box", "/work/app", "cli", agentstate.EvStop, time.Now())
		f.saveLauncherAt(t, "p1", "续聊", f.a.code, "/work/app", map[string]any{"workflow_id": "wf-ghost"})
		v := f.sessionLaunchers(t, "wf-ghost")
		if len(v.Presets) != 0 || v.Suggest != nil || len(v.Hosts) != 0 || !strings.Contains(v.Reason, "some-other-box") || strings.Contains(v.Reason, "更多选项") {
			t.Fatalf("主机对不上：预设在别的机器上，不算对应，也不建议: %+v", v)
		}
	})
	t.Run("工作流没有任何会话", func(t *testing.T) {
		f := newExecFixture(t)
		v := f.sessionLaunchers(t, "wf-unknown")
		if len(v.Presets) != 0 || v.Suggest != nil || v.Reason == "" {
			t.Fatalf("还没有会话：什么都对应不上，并说明原因: %+v", v)
		}
	})
}

func TestSessionLaunchersHostCases(t *testing.T) {
	t.Run("同名多台：钉了其中一台的预设算对应；建议要人选主机", func(t *testing.T) {
		f := sessionFixture(t)
		dup := f.env.enrollAgent(testMeta("exec-host"), "同名机器")
		v := f.sessionLaunchers(t, sessWF)
		if v.Host != "" || len(v.Hosts) != 2 || v.Suggest == nil || !v.Suggest.NeedsHost || v.Suggest.Host != "" {
			t.Fatalf("同名多台时没法唯一确定主机，建议要人选: %+v", v)
		}
		f.saveLauncherAt(t, "p1", "钉在原机", f.a.code, "/work/app", map[string]any{"workflow_id": sessWF})
		f.saveLauncherAt(t, "p2", "钉在同名机", dup.code, "/work/app", map[string]any{"workflow_id": sessWF})
		v = f.sessionLaunchers(t, sessWF)
		wantIDs(t, v, "p1", "p2")
		if v.Suggest != nil {
			t.Fatalf("有对应预设时不该建议: %+v", v.Suggest)
		}
	})
	t.Run("会话没上报主机名：不按主机筛；建议要人选主机", func(t *testing.T) {
		f := newExecFixture(t)
		f.sessionEvent(t, "wf-nohost", "chat-n1", "", "/work/app", "cli", agentstate.EvStop, time.Now())
		v := f.sessionLaunchers(t, "wf-nohost")
		if v.SessionHost != "" || v.Suggest == nil || !v.Suggest.NeedsHost || v.Suggest.Dir != "/work/app" {
			t.Fatalf("没有主机名：建议里主机待选: %+v", v)
		}
		f.saveLauncherAt(t, "p1", "续聊", f.a.code, "/work/app", map[string]any{"workflow_id": "wf-nohost"})
		v = f.sessionLaunchers(t, "wf-nohost")
		wantIDs(t, v, "p1")
	})
	t.Run("占位的主机名 hosts（客户端没报时 store 填的）按没上报处理", func(t *testing.T) {
		f := newExecFixture(t)
		f.sessionEvent(t, "wf-hosts", "chat-h1", "hosts", "/work/app", "cli", agentstate.EvStop, time.Now())
		v := f.sessionLaunchers(t, "wf-hosts")
		if v.SessionHost != "" || v.Suggest == nil || !v.Suggest.NeedsHost || v.Reason != "" {
			t.Fatalf("占位值不是主机名：建议里主机待选，而不是「没有叫 hosts 的主机」: %+v", v)
		}
		f.saveLauncherAt(t, "p1", "续聊", f.a.code, "/work/app", map[string]any{"workflow_id": "wf-hosts"})
		wantIDs(t, f.sessionLaunchers(t, "wf-hosts"), "p1")
	})
	t.Run("按备注对上主机（与 terminal_exec 同口径）", func(t *testing.T) {
		f := newExecFixture(t)
		f.sessionEvent(t, "wf-remark", "chat-r1", "build box", "/work/app", "cli", agentstate.EvStop, time.Now())
		f.saveLauncherAt(t, "p1", "续聊", f.a.code, "/work/app", map[string]any{"workflow_id": "wf-remark"})
		v := f.sessionLaunchers(t, "wf-remark")
		wantIDs(t, v, "p1")
		if v.Host != f.a.code {
			t.Fatalf("备注 build box 应对上这台主机: %+v", v)
		}
	})
}

func TestSessionLaunchersREST(t *testing.T) {
	f := sessionFixture(t)
	f.saveLauncherAt(t, "p1", "续聊", f.a.code, "/work/app", map[string]any{"workflow_id": sessWF})
	const path = "/api/v1/terminal/launchers/for-session"

	if res := f.env.call("GET", path+"?workflow_id="+sessWF, nil); res.Status != 401 {
		t.Fatalf("没登录应 401: %d %s", res.Status, res.Raw)
	}
	// 登录即可读，不需要解锁；只读，不产生运行。
	res := f.env.call("GET", path+"?workflow_id="+sessWF, nil, "Authorization", humanAuth(testUser))
	sess, _ := res.Body["session"].(map[string]any)
	presets, _ := sess["presets"].([]any)
	if res.Status != 200 || len(presets) != 1 {
		t.Fatalf("应返回一个对应预设: %d %s", res.Status, res.Raw)
	}
	p, _ := presets[0].(map[string]any)
	if p["id"] != "p1" || p["can_run"] != true || p["busy"] != false || sess["dir"] != "/work/app" || sess["workflow_id"] != sessWF {
		t.Fatalf("字段不对: %s", res.Raw)
	}
	if f.sim.runCount() != 0 {
		t.Fatal("读取视图不该提交运行")
	}
	if bad := f.env.call("GET", path, nil, "Authorization", humanAuth(testUser)); bad.Status != 400 || bad.Body["code"] != "no_workflow" {
		t.Fatalf("缺工作流应 400: %d %s", bad.Status, bad.Raw)
	}
	// 没有对应预设的工作流：presets 是空数组而不是 null（界面按数组处理）。
	empty := f.env.call("GET", path+"?workflow_id=wf-none", nil, "Authorization", humanAuth(testUser))
	es, _ := empty.Body["session"].(map[string]any)
	if arr, ok := es["presets"].([]any); empty.Status != 200 || !ok || len(arr) != 0 {
		t.Fatalf("没有对应预设时 presets 应是空数组: %d %s", empty.Status, empty.Raw)
	}
}
