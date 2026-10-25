package terminal

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 会话页「运行」（#128）：解析目标、预览、提交、闸门、单飞、REST 与审计。

const (
	sessWF   = "wf-session"
	sessConv = "chat-s1"
)

// sessionEvent 以编辑器 hook 的身份给某个工作流里的对话报一个事件（身份 = 主机名 + 项目目录）。
func (f *execFixture) sessionEvent(t *testing.T, wf, conv, host, dir, kind, name string, at time.Time) {
	t.Helper()
	_, err := f.env.store.IngestAgentEvent(context.Background(), store.AgentIngestInput{
		WorkflowID: wf, CredentialID: 1, HostName: host, ProjectDirectory: dir, ConversationID: conv, ClientKind: kind,
		Event: agentstate.Event{Source: agentstate.SourceHook, Name: name, Received: at, At: at, Status: "completed"},
	})
	if err != nil {
		t.Fatalf("hook %s: %v", name, err)
	}
}

// sessionFixture 在执行夹具上让对话 sessConv 有一个 hook 汇报过的会话：主机 exec-host、目录 /work/app、命令行客户端、已停止（空闲）。
func sessionFixture(t *testing.T) *execFixture {
	t.Helper()
	f := newExecFixture(t)
	now := time.Now()
	f.sessionEvent(t, sessWF, sessConv, "exec-host", "/work/app", "cli", agentstate.EvPrompt, now.Add(-2*time.Second))
	f.sessionEvent(t, sessWF, sessConv, "exec-host", "/work/app", "cli", agentstate.EvStop, now.Add(-time.Second))
	return f
}

func (f *execFixture) sessionPreview(req sessionRunRequest) SessionRunPreview {
	return f.env.hub.guardian.sessionRunPreview(context.Background(), req, time.Now())
}

func (f *execFixture) runSession(req sessionRunRequest) (LauncherRunPlan, error) {
	plan, _, err := f.env.hub.guardian.runSession(context.Background(), req, time.Now())
	return plan, err
}

func issueCodes(pv SessionRunPreview) []string {
	var out []string
	for _, i := range pv.Issues {
		out = append(out, i.Code)
	}
	return out
}

func wantIssue(t *testing.T, pv SessionRunPreview, code string) {
	t.Helper()
	if pv.CanRun {
		t.Fatalf("应该不能运行（%s）: %+v", code, pv)
	}
	for _, i := range pv.Issues {
		if i.Code == code {
			if strings.TrimSpace(i.Message) == "" {
				t.Fatalf("原因 %s 没有说明", code)
			}
			return
		}
	}
	t.Fatalf("期望原因 %q，得到 %v", code, issueCodes(pv))
}

func TestSessionThreadLabelIsStableAndShort(t *testing.T) {
	long := strings.Repeat("w", 200)
	a, b := sessionThreadFor(long), sessionThreadFor(long+"x")
	if len(a) > 64 || len(b) > 64 {
		t.Fatalf("线程标签最长 64 个字符：%d %d", len(a), len(b))
	}
	if a == b {
		t.Fatal("不同的工作流应得到不同的标签")
	}
	if a != sessionThreadFor("  "+long+" ") {
		t.Fatal("首尾空白不该改变标签")
	}
	if !strings.HasPrefix(a, sessionThreadPrefix) {
		t.Fatalf("标签前缀不对: %s", a)
	}
}

func TestSessionRunPreviewResolvesTargetFromSession(t *testing.T) {
	f := sessionFixture(t)
	pv := f.sessionPreview(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
	if !pv.CanRun || len(pv.Issues) != 0 {
		t.Fatalf("应该可以运行: %+v", pv)
	}
	if pv.ConversationID != sessConv || pv.Host != f.a.code || pv.Dir != "/work/app" || pv.ClientKind != "cli" || pv.SessionHost != "exec-host" {
		t.Fatalf("目标解析不对: %+v", pv)
	}
	want := "agent -p --resume chat-s1 --approve-mcps --trust --workspace '/work/app' --output-format stream-json '" +
		renderLauncherWake(context.Background(), f.env.store, launcherWake{Kind: sessionprompt.KindResume, WorkflowID: sessWF, Project: "/work/app", NoLimitNote: true}).Text + "'"
	if pv.Command != want {
		t.Fatalf("预览的命令不对：\n got %s\nwant %s", pv.Command, want)
	}
	if pv.Bin != "agent" || pv.Posture != store.HeadlessPostureAllowlist || pv.Model != "" || pv.Prompt != pv.DefaultPrompt || pv.Prompt == "" {
		t.Fatalf("默认选项不对: %+v", pv)
	}
	if len(pv.Hosts) != 1 || !pv.Hosts[0].Matched || !pv.Hosts[0].Online || pv.Hosts[0].MachineCode != f.a.code {
		t.Fatalf("主机候选不对: %+v", pv.Hosts)
	}
	joined := strings.Join(pv.Warnings, "|")
	if !strings.Contains(joined, "空闲") || !strings.Contains(joined, "Mcp(") {
		t.Fatalf("应提示对话空闲、以及白名单的前提: %v", pv.Warnings)
	}
	if pv.LastRun != nil {
		t.Fatalf("从没运行过: %+v", pv.LastRun)
	}

	// 不指定对话：取这个工作流里最近一个有对话编号的会话，结果相同。
	pv2 := f.sessionPreview(sessionRunRequest{WorkflowID: sessWF})
	if !pv2.CanRun || pv2.ConversationID != sessConv || pv2.Host != f.a.code || pv2.Dir != "/work/app" || pv2.Command != want {
		t.Fatalf("不指定对话时的预览不对: %+v", pv2)
	}

	// 预览是只读的：没有运行到达主机，也没有运行记录。
	if f.sim.runCount() != 0 {
		t.Fatal("预览不该提交运行")
	}
	if rows, err := f.env.store.ListTerminalRuns(context.Background(), store.RunFilter{CredentialName: sessionRunCaller, Limit: 5}); err != nil || len(rows) != 0 {
		t.Fatalf("预览不该产生运行记录: %v %v", rows, err)
	}
}

func TestSessionRunSubmitsExactCommandAndLeavesChatLines(t *testing.T) {
	f := sessionFixture(t)
	f.completeWith(launcherResultOut)

	plan, host, err := f.env.hub.guardian.runSession(context.Background(), sessionRunRequest{
		WorkflowID: sessWF, ConversationID: sessConv, Model: "claude-sonnet-5-5-max", Posture: "force", Prompt: "请接着 %workflow 的工作",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if host == nil || host.MachineCode != f.a.code {
		t.Fatalf("应在匹配的主机上运行: %+v", host)
	}
	if plan.RunID == "" || plan.Posture != store.HeadlessPostureForce || plan.Model != "claude-sonnet-5-5-max" || plan.ConversationID != sessConv {
		t.Fatalf("计划不对: %+v", plan)
	}
	for _, w := range plan.Warnings {
		if strings.Contains(w, "Mcp(") {
			t.Fatalf("force 姿态没有白名单提示: %v", plan.Warnings)
		}
	}

	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })
	run := f.sim.lastRun()
	want := "agent -p --resume chat-s1 --approve-mcps --trust --model 'claude-sonnet-5-5-max' --force --workspace '/work/app' --output-format stream-json '请接着 wf-session 的工作'"
	if run.Cmd != want {
		t.Fatalf("命令不对：\n got %s\nwant %s", run.Cmd, want)
	}
	if run.Cwd != "/work/app" {
		t.Fatalf("应在会话的项目目录里运行: %q", run.Cwd)
	}

	rec := f.waitRunFinal(t, plan.RunID)
	if rec.CredentialName != sessionRunCaller || rec.Thread != sessionThreadFor(sessWF) || rec.WorkflowID != sessWF ||
		!strings.Contains(rec.Description, "会话页运行") || rec.State != model.RunSucceeded {
		t.Fatalf("运行记录的归属不对: %+v", rec)
	}

	// 聊天里先后有「发起」与「结束」两条系统行：不含命令与提示词；没有智能体消息，结束行带空转提示。
	var texts []string
	eventually(t, 3*time.Second, "聊天里出现结束行", func() bool {
		texts = f.chatTexts(sessWF)
		return len(texts) == 2
	})
	if !strings.Contains(texts[0], "你在会话页运行了非交互续聊（接着对话 chat-s1 · claude-sonnet-5-5-max · 放开全部命令（--force））") {
		t.Fatalf("发起行不对: %q", texts[0])
	}
	if !strings.Contains(texts[1], "会话页的非交互续聊已结束：成功") || !strings.Contains(texts[1], "空转退出") {
		t.Fatalf("结束行不对: %q", texts[1])
	}
	for _, s := range texts {
		if strings.Contains(s, "agent -p") || strings.Contains(s, "请接着") || strings.Contains(s, "--approve-mcps") {
			t.Fatalf("系统行里不该有命令或提示词: %q", s)
		}
	}

	// 现状：最近一次运行已成功，带回复与空转标记。
	v := f.env.hub.guardian.sessionRunView(context.Background(), sessWF, time.Now())
	if v == nil || v.RunID != plan.RunID || v.State != model.RunSucceeded || v.Active || v.Reply != "已恢复现场：收件箱为空。" || !v.Silent {
		t.Fatalf("现状不对: %+v", v)
	}
	if v.ThreadKey == "" {
		t.Fatalf("应带 ExecMesh 线程键: %+v", v)
	}
	// 别的工作流看不到它。
	if other := f.env.hub.guardian.sessionRunView(context.Background(), "wf-other", time.Now()); other != nil {
		t.Fatalf("别的工作流不该看到这次运行: %+v", other)
	}
}

func TestSessionRunHonoursManualOverrides(t *testing.T) {
	f := newExecFixture(t) // 这个工作流没有任何会话：对话、主机、目录都得手填
	f.completeWith("")

	pv := f.sessionPreview(sessionRunRequest{WorkflowID: "wf-manual"})
	wantIssue(t, pv, "no_conversation")
	if pv.Issues[0].Code != "no_conversation" || !strings.Contains(pv.Issues[0].Message, "conversation_id") {
		t.Fatalf("没有对话编号时应指向请求里的 conversation_id: %+v", pv.Issues)
	}
	if len(pv.Hosts) != 1 {
		t.Fatalf("主机候选应列出已启用的主机: %+v", pv.Hosts)
	}

	req := sessionRunRequest{
		WorkflowID: "wf-manual", ConversationID: "e33e27ef-0fbb-46e4-9446-5dee56ae61fa", Host: strings.ToUpper(f.a.code), Dir: "/srv/proj",
		Bin: "/opt/cursor/bin/agent", Model: "gpt-5.1", Posture: "allowlist", Prompt: "看看收件箱",
	}
	pv = f.sessionPreview(req)
	if !pv.CanRun || pv.Host != f.a.code || pv.Dir != "/srv/proj" || pv.Bin != "/opt/cursor/bin/agent" || pv.Model != "gpt-5.1" {
		t.Fatalf("手填的目标应被采用: %+v", pv)
	}
	if !strings.HasPrefix(pv.Command, "/opt/cursor/bin/agent -p --resume e33e27ef-0fbb-46e4-9446-5dee56ae61fa ") ||
		!strings.Contains(pv.Command, "--workspace '/srv/proj'") || !strings.Contains(pv.Command, "--model 'gpt-5.1'") ||
		!strings.HasSuffix(pv.Command, "'看看收件箱'") {
		t.Fatalf("命令不对: %s", pv.Command)
	}
	plan, err := f.runSession(req)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })
	if got := f.sim.lastRun(); got.Cwd != "/srv/proj" || got.Cmd != pv.Command {
		t.Fatalf("运行与预览不一致:\n run %+v\n预览 %s", got, pv.Command)
	}
	f.waitRunFinal(t, plan.RunID)
}

func TestSessionRunHostChoice(t *testing.T) {
	f := sessionFixture(t)

	// 两台同名主机都在线：没法替人选。
	dup := f.env.enrollAgent(testMeta("exec-host"), "同名机器")
	pv := f.sessionPreview(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
	wantIssue(t, pv, "host_ambiguous")
	if pv.Host != "" || len(pv.Hosts) != 2 || !pv.Hosts[0].Matched || !pv.Hosts[1].Matched {
		t.Fatalf("两台同名主机都应列为候选并标记匹配: %+v", pv)
	}
	if _, err := f.runSession(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv}); err == nil {
		t.Fatal("有歧义时不该运行")
	} else {
		wantWakeCode(t, err, "host_ambiguous")
	}
	if f.sim.runCount() != 0 {
		t.Fatal("不该有运行到达主机")
	}

	// 人指定其中一台：放行。
	pv = f.sessionPreview(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv, Host: dup.code})
	if !pv.CanRun || pv.Host != dup.code {
		t.Fatalf("指定主机后应可运行: %+v", pv)
	}

	// 同名的另一台掉线：只剩一台在线，自动选它。
	dup.close()
	eventually(t, 3*time.Second, "同名主机离线", func() bool { return f.env.hub.onlineAgent(dup.code) == nil })
	pv = f.sessionPreview(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
	if !pv.CanRun || pv.Host != f.a.code {
		t.Fatalf("应自动选在线的那台: %+v", pv)
	}
	// 指定离线的那台：说明主机离线。
	pv = f.sessionPreview(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv, Host: dup.code})
	wantIssue(t, pv, "host_offline")

	// 机器码不存在。
	pv = f.sessionPreview(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv, Host: "ffffffffffffffff"})
	wantIssue(t, pv, "host_not_found")
}

// #139：自动选主机认「备注」，与 terminal_exec 同一口径（机器码 → 显示名 → 主机名别名）。
// 现场：会话上报短名 devhost，ExecMesh 主机自报全名 devhost.example.com、管理员写了备注 devhost——以前只比自报主机名，判成「清单里没有」。
// 边界同样要守住：短名不前缀匹配全名；主机名只是别名，不抢走别人的备注；一致的有多台时不替人选；不区分大小写。
func TestSessionRunHostMatchesRemarkLikeTerminalExec(t *testing.T) {
	f := newExecFixture(t)
	now := time.Now()
	f.sessionEvent(t, sessWF, sessConv, "short-box", "/work/app", "cli", agentstate.EvPrompt, now.Add(-2*time.Second))
	f.sessionEvent(t, sessWF, sessConv, "short-box", "/work/app", "cli", agentstate.EvStop, now.Add(-time.Second))
	req := sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv}
	matchedCodes := func(pv SessionRunPreview) map[string]bool {
		out := map[string]bool{}
		for _, h := range pv.Hosts {
			if h.Matched {
				out[h.MachineCode] = true
			}
		}
		return out
	}
	issueMsg := func(pv SessionRunPreview, code string) string {
		for _, i := range pv.Issues {
			if i.Code == code {
				return i.Message
			}
		}
		return ""
	}

	// 清单里只有 exec-host（备注 build box）：短名既不是备注也不是主机名，不猜，并提示可以写同名备注。
	pv := f.sessionPreview(req)
	wantIssue(t, pv, "host_not_found")
	if pv.Host != "" || len(matchedCodes(pv)) != 0 {
		t.Fatalf("对不上时不该选主机、也不该标记匹配: %+v", pv)
	}
	if msg := issueMsg(pv, "host_not_found"); !strings.Contains(msg, "short-box") || !strings.Contains(msg, "备注") {
		t.Fatalf("应点名对不上的名字，并提示可以写同名备注: %q", msg)
	}

	// 主机自报全名、没有备注：short-box.example.com 只是以短名开头，不做前缀匹配。
	b := f.env.enrollAgent(testMeta("short-box.example.com"), "")
	pv = f.sessionPreview(req)
	wantIssue(t, pv, "host_not_found")
	if len(matchedCodes(pv)) != 0 {
		t.Fatalf("短名不该前缀匹配全名: %+v", pv.Hosts)
	}

	// 管理员给它写了备注 short-box（现场的情形）：自动对上，而且排在候选清单的最前面。
	if r := f.env.call("POST", "/api/v1/terminal/hosts/"+b.code+"/remark", map[string]string{"remark": "short-box"}, f.adm...); r.Status != 200 {
		t.Fatalf("写备注失败: %d %s", r.Status, r.Raw)
	}
	pv = f.sessionPreview(req)
	if m := matchedCodes(pv); !pv.CanRun || pv.Host != b.code || len(m) != 1 || !m[b.code] || pv.Hosts[0].MachineCode != b.code {
		t.Fatalf("按备注应自动对上 %s: %+v", b.code, pv)
	}

	// 主机名只是别名：另一台自报主机名恰好叫 short-box、备注叫别的，不会抢走备注叫 short-box 的那台。
	c := f.env.enrollAgent(testMeta("short-box"), "other")
	pv = f.sessionPreview(req)
	if !pv.CanRun || pv.Host != b.code || matchedCodes(pv)[c.code] {
		t.Fatalf("主机名别名不该抢走备注: %+v", pv)
	}

	// 备注相同（不区分大小写）的两台：不替人选。
	d := f.env.enrollAgent(testMeta("another.example.com"), "SHORT-BOX")
	pv = f.sessionPreview(req)
	wantIssue(t, pv, "host_ambiguous")
	if m := matchedCodes(pv); len(m) != 2 || !m[b.code] || !m[d.code] || pv.Host != "" {
		t.Fatalf("两台备注相同的主机都应标记匹配且不自动选: %+v", pv)
	}
	if msg := issueMsg(pv, "host_ambiguous"); !strings.Contains(msg, "2 台") || !strings.Contains(msg, "short-box") {
		t.Fatalf("应说明有几台同名: %q", msg)
	}

	// 其中一台掉线，只剩一台在线：自动选在线的那台。
	d.close()
	eventually(t, 3*time.Second, "备注相同的另一台离线", func() bool { return f.env.hub.onlineAgent(d.code) == nil })
	pv = f.sessionPreview(req)
	if !pv.CanRun || pv.Host != b.code {
		t.Fatalf("应自动选在线的那台: %+v", pv)
	}
}

func TestSessionRunRefusals(t *testing.T) {
	t.Run("缺工作流", func(t *testing.T) {
		f := sessionFixture(t)
		wantIssue(t, f.sessionPreview(sessionRunRequest{}), "no_workflow")
		_, err := f.runSession(sessionRunRequest{})
		wantWakeCode(t, err, "no_workflow")
	})
	t.Run("选项不合法：命令拼不出来，但目标照样解析", func(t *testing.T) {
		f := sessionFixture(t)
		for name, req := range map[string]sessionRunRequest{
			"模型有空格":    {Model: "a b"},
			"模型注入":     {Model: "x'; touch /tmp/pwn; echo '"},
			"前缀命令":     {Bin: "sudo"},
			"姿态不认识":    {Posture: "yolo"},
			"对话编号带空格":  {ConversationID: "chat s1"},
			"提示词超长":    {Prompt: strings.Repeat("长", store.MaxHeadlessPromptRunes+1)},
			"可执行文件带空格": {Bin: "my agent"},
		} {
			req.WorkflowID = sessWF
			if req.ConversationID == "" {
				req.ConversationID = sessConv
			}
			pv := f.sessionPreview(req)
			wantIssue(t, pv, "bad_options")
			if pv.Command != "" {
				t.Errorf("%s：选项不合法时不该拼出命令: %s", name, pv.Command)
			}
			// 对话编号本身不合法时，按它找不到会话，主机与目录自然解析不出来；其余情况下仍应照常解析。
			if req.ConversationID == sessConv && (pv.Host != f.a.code || pv.Dir != "/work/app") {
				t.Errorf("%s：主机与目录仍应解析出来: %+v", name, pv)
			}
			if _, err := f.runSession(req); err == nil {
				t.Errorf("%s：应拒绝运行", name)
			} else {
				wantWakeCode(t, err, "bad_options")
			}
		}
		if f.sim.runCount() != 0 {
			t.Fatal("被拒绝的请求不该到达主机")
		}
	})
	t.Run("多行提示词：折成一行，不拒绝", func(t *testing.T) {
		f := sessionFixture(t)
		pv := f.sessionPreview(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv, Prompt: "第一行\n第二行\r\n  第三行"})
		if !pv.CanRun || pv.Prompt != "第一行 第二行 第三行" || !strings.HasSuffix(pv.Command, "'第一行 第二行 第三行'") {
			t.Fatalf("多行提示词应折成一行并放行: %+v", pv)
		}
	})
	t.Run("对话还在运行：预览仍显示对话", func(t *testing.T) {
		f := sessionFixture(t)
		f.sessionEvent(t, sessWF, sessConv, "exec-host", "/work/app", "cli", agentstate.EvPrompt, time.Now())
		pv := f.sessionPreview(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
		wantIssue(t, pv, "conversation_live")
		if pv.ConversationID != sessConv || pv.Host != f.a.code {
			t.Fatalf("对话还在运行时预览仍应显示对话与主机: %+v", pv)
		}
		_, err := f.runSession(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
		wantWakeCode(t, err, "conversation_live")
		if f.sim.runCount() != 0 {
			t.Fatal("对话还在运行，不该提交运行")
		}
	})
	t.Run("主机名对不上", func(t *testing.T) {
		f := newExecFixture(t)
		f.sessionEvent(t, "wf-elsewhere", "chat-e1", "some-other-box", "/work/app", "cli", agentstate.EvStop, time.Now())
		pv := f.sessionPreview(sessionRunRequest{WorkflowID: "wf-elsewhere", ConversationID: "chat-e1"})
		wantIssue(t, pv, "host_not_found")
		if !strings.Contains(pv.Issues[0].Message, "some-other-box") || len(pv.Hosts) != 1 || pv.Hosts[0].Matched {
			t.Fatalf("应说明对不上的主机名，并列出可手选的主机: %+v", pv)
		}
		// 手选之后放行（目录与对话仍来自会话）。
		pv = f.sessionPreview(sessionRunRequest{WorkflowID: "wf-elsewhere", ConversationID: "chat-e1", Host: f.a.code})
		if !pv.CanRun || pv.Host != f.a.code || pv.Dir != "/work/app" {
			t.Fatalf("手选主机后应可运行: %+v", pv)
		}
	})
	t.Run("目录：缺失与相对路径", func(t *testing.T) {
		f := newExecFixture(t)
		req := sessionRunRequest{WorkflowID: "wf-nodir", ConversationID: "chat-d1", Host: f.a.code}
		wantIssue(t, f.sessionPreview(req), "no_dir")
		req.Dir = "relative/path"
		wantIssue(t, f.sessionPreview(req), "bad_dir")
		req.Dir = "/ok\n/rm -rf"
		wantIssue(t, f.sessionPreview(req), "bad_dir")
		if _, err := f.runSession(req); err == nil {
			t.Fatal("目录不合法不该运行")
		}
		if f.sim.runCount() != 0 {
			t.Fatal("不该到达主机")
		}
	})
	t.Run("主机离线", func(t *testing.T) {
		f := sessionFixture(t)
		f.a.close()
		eventually(t, 3*time.Second, "主机离线", func() bool { return f.env.hub.onlineAgent(f.a.code) == nil })
		pv := f.sessionPreview(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
		wantIssue(t, pv, "host_offline")
		_, err := f.runSession(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
		wantWakeCode(t, err, "host_offline")
	})
	t.Run("Windows 主机：目录要写成 Windows 路径（#178 起支持 Windows 主机；运行与命令见 headless_winhost_test.go）", func(t *testing.T) {
		f := newExecFixture(t)
		meta := testMeta("win-box")
		meta.OS = "windows"
		win := f.env.enrollAgent(meta, "windows")
		f.sessionEvent(t, "wf-win", "chat-w1", "win-box", "C:/work", "cli", agentstate.EvStop, time.Now())
		wantIssue(t, f.sessionPreview(sessionRunRequest{WorkflowID: "wf-win", ConversationID: "chat-w1", Host: win.code, Dir: "/c/work"}), "bad_dir")
		pv := f.sessionPreview(sessionRunRequest{WorkflowID: "wf-win", ConversationID: "chat-w1", Host: win.code})
		if !pv.CanRun || pv.Dir != "C:/work" || !strings.HasPrefix(pv.Command, "& 'agent' -p --resume chat-w1 ") {
			t.Fatalf("会话上报的是 Windows 路径时，Windows 主机应可运行且命令是 PowerShell 写法: %+v", pv)
		}
	})
	t.Run("执行闸门拒绝：主机没开模型访问", func(t *testing.T) {
		f := sessionFixture(t)
		f.setModel(t, map[string]any{"allow_model": false})
		_, err := f.runSession(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
		if _, ok := err.(*wakeError); !ok {
			t.Fatalf("应转换成面向界面的错误: %T %v", err, err)
		}
		if f.sim.runCount() != 0 {
			t.Fatal("不该有运行到达主机")
		}
		if v := f.env.hub.guardian.sessionRunView(context.Background(), sessWF, time.Now()); v != nil {
			t.Fatalf("被闸门拒绝的运行不属于「最近一次」（拒绝由审计记录）：%+v", v)
		}
	})
	t.Run("需批准模式：运行停在待批准并提示", func(t *testing.T) {
		f := sessionFixture(t)
		f.setModel(t, map[string]any{"allow_model": true, "exec_mode": "approve"})
		pv := f.sessionPreview(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
		if !pv.CanRun || !strings.Contains(strings.Join(pv.Warnings, "|"), "需批准") {
			t.Fatalf("预览就该提示要去批准: %+v", pv)
		}
		plan, err := f.runSession(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
		if err != nil {
			t.Fatal(err)
		}
		if plan.RunState != model.RunAwaitingApproval || f.sim.runCount() != 0 {
			t.Fatalf("应停在待批准且不到达主机: %+v runs=%d", plan, f.sim.runCount())
		}
		notes := 0
		for _, w := range plan.Warnings {
			if strings.Contains(w, "需批准") {
				notes++
			}
		}
		if notes != 1 {
			t.Fatalf("「需批准」的提示应恰好出现一次: %v", plan.Warnings)
		}
		if v := f.env.hub.guardian.sessionRunView(context.Background(), sessWF, time.Now()); v == nil || v.State != model.RunAwaitingApproval || !v.Active {
			t.Fatalf("待批准的现状不对: %+v", v)
		}
		// 待批准的运行还没结束：同一个工作流不能再提交。
		pv = f.sessionPreview(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
		wantIssue(t, pv, "run_active")
	})
	t.Run("IDE 里的对话：放行但提示", func(t *testing.T) {
		f := newExecFixture(t)
		f.sessionEvent(t, "wf-ide", "chat-i1", "exec-host", "/work/app", "ide", agentstate.EvStop, time.Now())
		pv := f.sessionPreview(sessionRunRequest{WorkflowID: "wf-ide", ConversationID: "chat-i1"})
		if !pv.CanRun || pv.ClientKind != "ide" || !strings.Contains(strings.Join(pv.Warnings, "|"), "编辑器") {
			t.Fatalf("IDE 对话应放行并提示: %+v", pv)
		}
	})
}

func TestSessionRunIsSingleFlightPerWorkflow(t *testing.T) {
	f := sessionFixture(t)
	// 没有 completeWith：假主机收到运行后不结束它，运行一直停在 running。
	first, err := f.runSession(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() == 1 })

	_, err = f.runSession(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
	wantWakeCode(t, err, "run_active")
	if !strings.Contains(err.Error(), first.RunID) {
		t.Fatalf("错误信息应带上还在跑的运行编号: %v", err)
	}
	if f.sim.runCount() != 1 {
		t.Fatalf("第二次不该到达主机: %d", f.sim.runCount())
	}
	pv := f.sessionPreview(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
	wantIssue(t, pv, "run_active")
	if pv.Issues[0].Code != "run_active" {
		t.Fatalf("「上一次运行还没结束」应排在最前面: %v", issueCodes(pv))
	}
	if pv.LastRun == nil || pv.LastRun.RunID != first.RunID || !pv.LastRun.Active || pv.LastRun.State != model.RunRunning {
		t.Fatalf("预览应带上进行中的运行现状: %+v", pv.LastRun)
	}

	// 别的工作流不受影响（线程标签按工作流区分）。
	f.completeWith("")
	f.sessionEvent(t, "wf-second", "chat-s2", "exec-host", "/work/app", "cli", agentstate.EvStop, time.Now())
	if _, err := f.runSession(sessionRunRequest{WorkflowID: "wf-second", ConversationID: "chat-s2"}); err != nil {
		t.Fatalf("别的工作流应能同时运行: %v", err)
	}

	// 记录残留超过宽限（异常残留）不再挡路。
	old := time.Now().Add(-launcherRunStale - time.Minute)
	if ok, err := f.env.store.UpdateTerminalRun(context.Background(), first.RunID, map[string]any{"created_at": old}); err != nil || !ok {
		t.Fatalf("改旧运行记录: %v %v", ok, err)
	}
	if _, err := f.runSession(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv}); err != nil {
		t.Fatalf("残留的旧记录不该挡住新的运行: %v", err)
	}
}

func TestSessionRunConcurrentSubmitsOnlyOneWins(t *testing.T) {
	f := sessionFixture(t)
	const n = 6
	var wg sync.WaitGroup
	results := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.runSession(sessionRunRequest{WorkflowID: sessWF, ConversationID: sessConv})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	ok, active := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		default:
			if we, isWake := err.(*wakeError); isWake && we.Code == "run_active" {
				active++
			} else {
				t.Errorf("意外的错误: %v", err)
			}
		}
	}
	if ok != 1 || active != n-1 {
		t.Fatalf("并发提交应恰好成功一次: 成功 %d，run_active %d", ok, active)
	}
	eventually(t, 2*time.Second, "假主机收到运行", func() bool { return f.sim.runCount() >= 1 })
	time.Sleep(100 * time.Millisecond)
	if f.sim.runCount() != 1 {
		t.Fatalf("只该有一次运行到达主机: %d", f.sim.runCount())
	}

	// 提交位置占着时（另一个窗口正在提交）也直接拒绝，而不是排队。
	g := f.env.hub.guardian
	if !g.acquireSessionRun("wf-held") {
		t.Fatal("应能占到提交位置")
	}
	if g.acquireSessionRun("wf-held") {
		t.Fatal("同一个工作流不该占到两次")
	}
	_, _, err := g.runSession(context.Background(), sessionRunRequest{WorkflowID: "wf-held", ConversationID: "chat-h1"}, time.Now())
	wantWakeCode(t, err, "run_active")
	g.releaseSessionRun("wf-held")
	if !g.acquireSessionRun("wf-held") {
		t.Fatal("释放后应能再占")
	}
	g.releaseSessionRun("wf-held")
}

func TestSessionRunRESTNeedsUnlockAndAuditsWithoutPromptOrCommand(t *testing.T) {
	f := sessionFixture(t)
	f.completeWith(launcherResultOut)
	login := []string{"Authorization", humanAuth(testUser)}
	body := map[string]any{"workflow_id": sessWF, "conversation_id": sessConv, "prompt": "接着做 %workflow 的事", "model": "m1"}

	// 预览：登录即可，不需要解锁；只读。
	if res := f.env.call("POST", "/api/v1/terminal/session-run/preview", body); res.Status != 401 {
		t.Fatalf("没登录应 401: %d %s", res.Status, res.Raw)
	}
	res := f.env.call("POST", "/api/v1/terminal/session-run/preview", body, login...)
	pv, _ := res.Body["preview"].(map[string]any)
	if res.Status != 200 || pv["can_run"] != true || pv["host"] != f.a.code || pv["conversation_id"] != sessConv || pv["dir"] != "/work/app" ||
		pv["model"] != "m1" || pv["posture"] != "allowlist" || !strings.Contains(pv["command"].(string), "--model 'm1'") {
		t.Fatalf("预览响应不对: %d %s", res.Status, res.Raw)
	}
	if f.sim.runCount() != 0 {
		t.Fatal("预览不该提交运行")
	}
	if st := f.env.call("GET", "/api/v1/terminal/session-run/status?workflow_id="+sessWF, nil, login...); st.Status != 200 || st.Body["run"] != nil {
		t.Fatalf("从没运行过应是 null: %d %s", st.Status, st.Raw)
	}
	if st := f.env.call("GET", "/api/v1/terminal/session-run/status", nil, login...); st.Status != 400 || st.Body["code"] != "no_workflow" {
		t.Fatalf("缺工作流应 400: %d %s", st.Status, st.Raw)
	}

	// 运行：需要刚刚解锁。
	if res := f.env.call("POST", "/api/v1/terminal/session-run", body); res.Status != 401 {
		t.Fatalf("没登录应 401: %d %s", res.Status, res.Raw)
	}
	if res := f.env.call("POST", "/api/v1/terminal/session-run", body, login...); res.Status == 200 {
		t.Fatalf("只登录没解锁不该能运行（运行 = 让主机上的 agent 动手）: %d %s", res.Status, res.Raw)
	}
	if f.sim.runCount() != 0 {
		t.Fatal("被拒绝的请求不该到达主机")
	}
	if bad := f.env.call("POST", "/api/v1/terminal/session-run", map[string]any{"conversation_id": sessConv}, f.adm...); bad.Status != 400 || bad.Body["code"] != "no_workflow" {
		t.Fatalf("缺工作流应 400: %d %s", bad.Status, bad.Raw)
	}
	if bad := f.env.call("POST", "/api/v1/terminal/session-run", map[string]any{"workflow_id": sessWF, "conversation_id": sessConv, "model": "a b"}, f.adm...); bad.Status != 400 || bad.Body["code"] != "bad_options" {
		t.Fatalf("选项不合法应 400: %d %s", bad.Status, bad.Raw)
	}

	res = f.env.call("POST", "/api/v1/terminal/session-run", body, f.adm...)
	if res.Status != 200 {
		t.Fatalf("运行失败: %d %s", res.Status, res.Raw)
	}
	run, _ := res.Body["run"].(map[string]any)
	if run["run_id"] == "" || run["conversation_id"] != sessConv || run["posture"] != "allowlist" || run["model"] != "m1" {
		t.Fatalf("响应不对: %s", res.Raw)
	}
	runID, _ := run["run_id"].(string)
	// 同一个工作流上一次还没结束（或刚结束）时，REST 的拒绝要带上原因码。
	f.waitRunFinal(t, runID)

	logs := f.env.auditEvents(EvSessionRun)
	if len(logs) != 1 || logs[0].RunID != runID || logs[0].MachineCode != f.a.code ||
		!strings.Contains(logs[0].Detail, "workflow="+sessWF) || !strings.Contains(logs[0].Detail, "posture=allowlist") {
		t.Fatalf("审计应记一条 session_run（工作流、姿态、运行编号）: %+v", logs)
	}
	for _, l := range logs {
		if strings.Contains(l.Detail, "agent") || strings.Contains(l.Detail, "接着做") || strings.Contains(l.Detail, sessConv) || strings.Contains(l.Detail, "m1") {
			t.Fatalf("审计不该含命令、提示词、对话编号或模型: %q", l.Detail)
		}
	}

	// 现状接口：登录即可读，带最近一次运行。
	got := f.env.call("GET", "/api/v1/terminal/session-run/status?workflow_id="+sessWF, nil, login...)
	cur, _ := got.Body["run"].(map[string]any)
	if got.Status != 200 || cur["run_id"] != runID || cur["state"] != model.RunSucceeded || cur["reply"] != "已恢复现场：收件箱为空。" || cur["silent"] != true {
		t.Fatalf("现状接口不对: %d %s", got.Status, got.Raw)
	}
}

// Hub 重启后：会话页运行仍被接回监视（与预设运行一样），状态来源是运行输出。
func TestSessionRunIsRestoredAfterHubRestart(t *testing.T) {
	f := watchFixture(t)
	out := &watchOut{f: f}
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		out.setID(m.ID)
		s.started(m.ID)
		out.add(jsonLine("system", "init"), jsonLine("thinking", "delta"), toolStarted("c1", "shellToolCall"))
	}
	cmd, _ := headlessCmd{Bin: "agent", ChatID: watchConv, Dir: "/work/app", Posture: store.HeadlessPostureForce, Prompt: "x"}.build()
	res, err := f.svc.submitHeadless(context.Background(), headlessSubmit{
		Caller: sessionRunCaller, UserAgent: "test", Machine: f.a.code, Workflow: watchWF,
		Cwd: "/work/app", Command: cmd, Description: "restored", Thread: sessionThreadFor(watchWF),
	})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, "假主机有了输出", func() bool { return out.ident() != "" && out.size() > 0 })
	rec := f.dbRun(t, res.RunID)
	f.svc.watch.restore(*rec)
	f.waitView(t, "还原成最后一个工具", func(v store.AgentSessionView) bool { return v.Sub == "tool:Shell" && v.StateSource == "run" })
}
