package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

func ptrInt(v int) *int { return &v }

func TestRunInfoFromEvent_MapsFacts(t *testing.T) {
	end := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	ev := terminal.RunEndEvent{
		Run: model.TerminalRun{
			RunID: "run-1", State: model.RunFailed, Target: "RM-AAAA-BBBB",
			Description: "回归测试", Command: "go test ./...", ExitCode: ptrInt(2), Signal: "",
			DurationMS: 90_500, WorkflowID: "wf-1", ProjectDirectory: "/home/dev/RelayMesh", EndedAt: &end,
			PreviewHead: "不该出现在推送里的输出", PreviewTail: "同上",
		},
		CancelBy: "user", Why: "主机重启，进程已丢失",
	}
	in := runInfoFromEvent(ev)

	if in.RunID != "run-1" || in.State != model.RunFailed || in.Machine != "RM-AAAA-BBBB" {
		t.Errorf("标识 / 状态 / 机器码不对：%+v", in)
	}
	if in.Description != "回归测试" || in.Command != "go test ./..." || in.WorkflowID != "wf-1" || in.ProjectDirectory != "/home/dev/RelayMesh" {
		t.Errorf("说明 / 命令 / 项目不对：%+v", in)
	}
	if in.ExitCode == nil || *in.ExitCode != 2 || in.Duration != 90*time.Second+500*time.Millisecond {
		t.Errorf("退出码 / 时长不对：%+v", in)
	}
	if in.CancelBy != "user" || in.Why != "主机重启，进程已丢失" {
		t.Errorf("取消来源 / 原因说明不对：%+v", in)
	}
	// 推送快照里根本没有放输出的地方——这是「推送不带输出」的结构性保证。
	if strings.Contains(fmt.Sprintf("%+v", in), "不该出现") || strings.Contains(fmt.Sprintf("%+v", in), "同上") {
		t.Errorf("快照里不应带出任何输出预览：%+v", in)
	}
}

func saveRunNtfyConfig(t *testing.T, srv *Server, serverURL string, extra map[string]any) {
	t.Helper()
	body := map[string]any{
		"enabled": true, "server": serverURL, "topic": "rm-run-topic", "priority": 5,
		"run_notify": true, "run_min_seconds": 60,
	}
	for k, v := range extra {
		body[k] = v
	}
	if code, _, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/ntfy", body); code != http.StatusOK {
		t.Fatalf("保存 ntfy 配置失败：%d %s", code, raw)
	}
}

func longRun(id string, mutate func(*terminal.RunEndEvent)) terminal.RunEndEvent {
	ev := terminal.RunEndEvent{Run: model.TerminalRun{
		RunID: id, State: model.RunFailed, Target: "RM-HOST-0001", ExitCode: ptrInt(2),
		Description: "夜间全量回归", Command: "make nightly", DurationMS: 90_000,
		ProjectDirectory: "/home/dev/RelayMesh",
	}}
	if mutate != nil {
		mutate(&ev)
	}
	return ev
}

// 用真实的 NewServer 装配出来的通知器（真实配置读取 + 真实的主机名解析）验证整条链路：
// 保存配置 → 运行结束事件 → 假 ntfy 收到推送；门槛、开关、取消来源都按配置生效。
func TestNtfyRunEnd_ThroughProductionWiring(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)
	fake := newFakeNtfy(t)
	saveRunNtfyConfig(t, srv, fake.URL, nil)
	if err := st.SaveTerminalHost(context.Background(), &model.TerminalHost{
		MachineCode: "RM-HOST-0001", PublicKey: "pk-test", Hostname: "ip-10-0-0-5", Remark: "构建机", Status: "enabled",
	}); err != nil {
		t.Fatal(err)
	}
	hook := srv.notifier.RunEndHook()

	hook(longRun("r-fail", nil))
	posts := fake.waitFor(t, 1)
	got := posts[0].Body
	if got["title"] != "模型命令失败（退出码 2）" || got["priority"] != float64(5) || got["topic"] != "rm-run-topic" {
		t.Fatalf("推送内容不对：%+v", got)
	}
	msg, _ := got["message"].(string)
	for _, want := range []string{"夜间全量回归", "主机：构建机", "项目：RelayMesh", "用时 1 分 30 秒"} {
		if !strings.Contains(msg, want) {
			t.Errorf("正文应含 %q：\n%s", want, msg)
		}
	}
	for _, banned := range []string{"make nightly", "RM-HOST-0001", "ip-10-0-0-5"} {
		if strings.Contains(msg, banned) {
			t.Errorf("正文不应含 %q（命令默认不带、机器码不外露、有备注时用备注）：\n%s", banned, msg)
		}
	}

	// 不该推送的：太短的、你自己取消的、被拒绝的。
	hook(longRun("r-short", func(ev *terminal.RunEndEvent) { ev.Run.DurationMS = 20_000 }))
	hook(longRun("r-user-cancel", func(ev *terminal.RunEndEvent) {
		ev.Run.State, ev.Run.ExitCode, ev.CancelBy = model.RunCancelled, nil, "user"
	}))
	hook(longRun("r-denied", func(ev *terminal.RunEndEvent) { ev.Run.State, ev.Run.ExitCode = model.RunDenied, nil }))
	time.Sleep(300 * time.Millisecond)
	if n := len(fake.snapshot()); n != 1 {
		t.Fatalf("太短 / 用户取消 / 被拒绝都不该推送，实际共 %d 条", n)
	}

	// 对照：没人要求取消、却被中断（守护进程停了）要推送，并写明原因。
	hook(longRun("r-interrupted", func(ev *terminal.RunEndEvent) {
		ev.Run.State, ev.Run.ExitCode, ev.Why = model.RunCancelled, nil, "主机上的会话守护进程已退出，进程已丢失"
	}))
	posts = fake.waitFor(t, 2)
	if posts[1].Body["title"] != "模型命令被中断" || !strings.Contains(posts[1].Body["message"].(string), "原因：主机上的会话守护进程已退出") {
		t.Errorf("被主机侧中断应推送并写明原因：%+v", posts[1].Body)
	}

	// 关掉运行结束推送：同样的事件不再推送（每条事件现读配置，保存后立即生效）。
	saveRunNtfyConfig(t, srv, fake.URL, map[string]any{"run_notify": false})
	hook(longRun("r-after-off", nil))
	time.Sleep(300 * time.Millisecond)
	if n := len(fake.snapshot()); n != 2 {
		t.Fatalf("关闭运行结束推送后不应再推送，实际共 %d 条", n)
	}
}

// 默认配置（没有明确打开）下，运行结束不会推送；「新反馈请求」的推送不受影响。
func TestNtfyRunEnd_OffUntilExplicitlyEnabled(t *testing.T) {
	srv, _ := newNtfyTestServer(t, nil)
	fake := newFakeNtfy(t)
	saveRunNtfyConfig(t, srv, fake.URL, map[string]any{"run_notify": false})

	srv.notifier.RunEndHook()(longRun("r1", nil))
	time.Sleep(300 * time.Millisecond)
	if n := len(fake.snapshot()); n != 0 {
		t.Fatalf("没打开运行结束推送，不应推送：%d", n)
	}
}

func TestNtfyAPI_RunNotifyFieldsRoundTripAndDefaults(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)

	// 从未设置过：关闭、门槛为默认值，界面据此显示。
	_, out, raw := ntfyCall(t, srv, http.MethodGet, "/api/v1/notify/ntfy", nil)
	cfg := out["config"].(map[string]any)
	if cfg["run_notify"] != false || cfg["run_min_seconds"] != float64(60) {
		t.Fatalf("默认应是关闭 + 60 秒：%s", raw)
	}

	saveRunNtfyConfig(t, srv, "https://ntfy.example.com", map[string]any{"run_min_seconds": 300})
	_, out, raw = ntfyCall(t, srv, http.MethodGet, "/api/v1/notify/ntfy", nil)
	cfg = out["config"].(map[string]any)
	if cfg["run_notify"] != true || cfg["run_min_seconds"] != float64(300) {
		t.Fatalf("保存的两项应原样读回：%s", raw)
	}
	saved, _ := st.GetNtfyConfig(context.Background())
	if !saved.RunNotify || saved.RunMinSeconds != 300 {
		t.Fatalf("库里的值不对：%+v", saved)
	}

	// 没带这两项的整张表单（旧版本的页面）= 回到默认，而不是沿用上次的值：表单是整体替换。
	body := map[string]any{"enabled": true, "server": "https://ntfy.example.com", "topic": "rm-run-topic", "priority": 4}
	if code, _, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/ntfy", body); code != http.StatusOK {
		t.Fatalf("PUT 失败：%d %s", code, raw)
	}
	saved, _ = st.GetNtfyConfig(context.Background())
	if saved.RunNotify || saved.RunMinSeconds != 60 {
		t.Fatalf("没带字段应回到默认：%+v", saved)
	}
}

func TestNtfyAPI_RunMinSecondsValidation(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)
	saveRunNtfyConfig(t, srv, "https://ntfy.example.com", map[string]any{"run_min_seconds": 120})

	for name, v := range map[string]int{"为负": -1, "超过上限": 86401, "远超上限": 9_999_999} {
		t.Run(name, func(t *testing.T) {
			body := map[string]any{"enabled": true, "server": "https://ntfy.example.com", "topic": "rm-run-topic", "priority": 4, "run_notify": true, "run_min_seconds": v}
			code, out, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/ntfy", body)
			if code != http.StatusBadRequest {
				t.Fatalf("应返回 400：%d %s", code, raw)
			}
			if msg, _ := out["error"].(string); !strings.Contains(msg, "最短运行时长") {
				t.Fatalf("错误信息应说明是哪一项：%s", raw)
			}
		})
	}
	saved, _ := st.GetNtfyConfig(context.Background())
	if saved.RunMinSeconds != 120 {
		t.Fatalf("校验失败不应改动已保存的配置：%+v", saved)
	}
}
