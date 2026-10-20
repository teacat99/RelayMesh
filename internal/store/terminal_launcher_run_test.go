package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// okHeadless 返回一条合法的「非交互续聊」预设。
func okHeadless(id, name string) TerminalLauncher {
	return TerminalLauncher{
		ID: id, Name: name, Host: launcherHost, Dir: "/home/user/RelayMesh", Kind: LauncherKindHeadless,
		Run: &HeadlessRun{WorkflowID: "relaymesh-continuation", Model: "claude-sonnet-5-5-max", Posture: HeadlessPostureForce},
	}
}

func TestHeadlessLauncherRoundTripAndKindNormalization(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	in := okHeadless("h-1", "  续聊  ")
	in.Run.Prompt = "  请继续   处理 %workflow  "
	in.Run.Bin = " agent "
	list, created, err := st.UpsertTerminalLauncher(ctx, in)
	if err != nil || !created || len(list) != 1 {
		t.Fatalf("新增非交互续聊预设: %+v created=%v err=%v", list, created, err)
	}
	got := list[0]
	if got.Kind != LauncherKindHeadless || !got.IsHeadless() || got.Run == nil {
		t.Fatalf("类型与运行配置应保留: %+v", got)
	}
	if got.Name != "续聊" || got.Run.Bin != "agent" || got.Run.Prompt != "请继续 处理 %workflow" {
		t.Fatalf("规整不对（名字、可执行文件首尾空白去掉，提示词空白折叠）: %+v %+v", got, got.Run)
	}
	if got.Run.EffectiveBin() != "agent" || got.Run.EffectivePosture() != HeadlessPostureForce {
		t.Fatalf("生效值不对: %q %q", got.Run.EffectiveBin(), got.Run.EffectivePosture())
	}
	// 调用方的数据不能被规整顺手改掉。
	if in.Run.Bin != " agent " || in.Run.Prompt != "  请继续   处理 %workflow  " {
		t.Fatalf("规整改到了调用方的运行配置: %+v", in.Run)
	}

	// 读回来一致；默认值：没填可执行文件 = agent、没填姿态 = allowlist。
	again, err := st.ListTerminalLaunchers(ctx)
	if err != nil || len(again) != 1 || again[0].Run == nil || again[0].Run.Model != "claude-sonnet-5-5-max" {
		t.Fatalf("读回: %+v err=%v", again, err)
	}
	bare := HeadlessRun{ConversationID: "chat-1"}
	if bare.EffectiveBin() != DefaultHeadlessBin || bare.EffectivePosture() != HeadlessPostureAllowlist {
		t.Fatalf("默认值不对: %q %q", bare.EffectiveBin(), bare.EffectivePosture())
	}

	// 默认类型写成空串（"terminal" 与空串等价），旧存档与新存档一致。
	term := okLauncher("t-1", "终端")
	term.Kind = " Terminal "
	list, _, err = st.UpsertTerminalLauncher(ctx, term)
	if err != nil {
		t.Fatal(err)
	}
	if list[1].Kind != "" || list[1].IsHeadless() || list[1].Run != nil {
		t.Fatalf("terminal 类型应规整为空串: %+v", list[1])
	}
}

func TestHeadlessLauncherValidation(t *testing.T) {
	mutate := func(f func(l *TerminalLauncher)) TerminalLauncher {
		l := okHeadless("h-1", "续聊")
		run := *l.Run
		l.Run = &run
		f(&l)
		return l
	}
	cases := []struct {
		name string
		l    TerminalLauncher
		want string // 错误信息里应出现的片段；空 = 应合法
	}{
		{"合法", mutate(func(l *TerminalLauncher) {}), ""},
		{"只固定对话编号也合法", mutate(func(l *TerminalLauncher) {
			l.Run.WorkflowID, l.Run.ConversationID = "", "e33e27ef-0fbb-46e4-9446-5dee56ae61fa"
		}), ""},
		{"模型带斜杠冒号合法", mutate(func(l *TerminalLauncher) { l.Run.Model = "vendor/model:v1.5+x" }), ""},
		{"可执行文件带路径合法", mutate(func(l *TerminalLauncher) { l.Run.Bin = "~/.local/bin/agent" }), ""},
		{"最长运行时间 1 分钟合法", mutate(func(l *TerminalLauncher) { l.Run.MaxRunMinutes = 1 }), ""},
		{"最长运行时间 7 天合法", mutate(func(l *TerminalLauncher) { l.Run.MaxRunMinutes = MaxHeadlessRunMinutes }), ""},
		{"最长运行时间超过上限会被收进上限（不报错）", mutate(func(l *TerminalLauncher) { l.Run.MaxRunMinutes = MaxHeadlessRunMinutes * 3 }), ""},
		{"最长运行时间是负数", mutate(func(l *TerminalLauncher) { l.Run.MaxRunMinutes = -5 }), "最长运行时间"},
		{"续跑成功判定 1 分钟合法", mutate(func(l *TerminalLauncher) { l.Run.AutoResume, l.Run.AutoResumeOKMinutes = true, 1 }), ""},
		{"续跑成功判定 1 天合法", mutate(func(l *TerminalLauncher) { l.Run.AutoResume, l.Run.AutoResumeOKMinutes = true, MaxAutoResumeOKMinutes }), ""},
		{"续跑成功判定超过上限会被收进上限（不报错）", mutate(func(l *TerminalLauncher) { l.Run.AutoResumeOKMinutes = MaxAutoResumeOKMinutes * 3 }), ""},
		{"续跑成功判定是负数", mutate(func(l *TerminalLauncher) { l.Run.AutoResumeOKMinutes = -1 }), "续跑成功的判定时长"},

		{"不认识的类型", mutate(func(l *TerminalLauncher) { l.Kind = "cron" }), "不认识的预设类型"},
		{"终端预设不能带运行配置", mutate(func(l *TerminalLauncher) { l.Kind = "" }), "只有「非交互续聊」预设才有运行配置"},
		{"不能填命令", mutate(func(l *TerminalLauncher) { l.Command = "agent" }), "不用填命令"},
		{"不支持守护", mutate(func(l *TerminalLauncher) { l.Guard = &LauncherGuard{Enabled: true} }), "不支持会话守护"},
		{"目录必填", mutate(func(l *TerminalLauncher) { l.Dir = "" }), "请填写目录"},
		{"目录必须绝对路径", mutate(func(l *TerminalLauncher) { l.Dir = "~/work" }), "绝对路径"},
		{"缺运行配置", mutate(func(l *TerminalLauncher) { l.Run = nil }), "缺少运行配置"},

		{"可执行文件有空格", mutate(func(l *TerminalLauncher) { l.Run.Bin = "agent --x" }), "可执行文件"},
		{"可执行文件有分号", mutate(func(l *TerminalLauncher) { l.Run.Bin = "agent;rm" }), "可执行文件"},
		{"可执行文件是前缀命令", mutate(func(l *TerminalLauncher) { l.Run.Bin = "sudo" }), "前缀命令"},
		{"可执行文件是 shell", mutate(func(l *TerminalLauncher) { l.Run.Bin = "bash" }), "前缀命令"},
		{"模型有空格", mutate(func(l *TerminalLauncher) { l.Run.Model = "a b" }), "模型名"},
		{"模型注入", mutate(func(l *TerminalLauncher) { l.Run.Model = "x'; touch /tmp/pwn; echo '" }), "模型名"},
		{"模型以符号开头", mutate(func(l *TerminalLauncher) { l.Run.Model = "-x" }), "模型名"},
		{"权限姿态不对", mutate(func(l *TerminalLauncher) { l.Run.Posture = "yolo" }), "权限姿态"},
		{"工作流编号有空格", mutate(func(l *TerminalLauncher) { l.Run.WorkflowID = "a b" }), "工作流编号"},
		{"对话编号注入", mutate(func(l *TerminalLauncher) { l.Run.ConversationID = "c1; rm -rf /" }), "对话编号"},
		{"工作流与对话编号都没有", mutate(func(l *TerminalLauncher) { l.Run.WorkflowID = "" }), "请填工作流编号"},
		{"提示词太长", mutate(func(l *TerminalLauncher) { l.Run.Prompt = strings.Repeat("字", MaxHeadlessPromptRunes+1) }), "提示词最长"},
		{"提示词有控制字符", mutate(func(l *TerminalLauncher) { l.Run.Prompt = "a\x00b" }), "控制字符"},
	}
	for _, c := range cases {
		err := c.l.Normalize().Validate()
		if c.want == "" {
			if err != nil {
				t.Errorf("%s：应合法，得到 %v", c.name, err)
			}
			continue
		}
		if err == nil || !errors.Is(err, ErrInvalidLauncher) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：应拒绝且信息含 %q，得到 %v", c.name, c.want, err)
		}
	}
}

// 预设的「最长运行时间」（#141）：没设 = 平台上限 7 天；设了的收进 [1 分钟, 7 天]；负数拒绝；原样存取，旧存档没有这个字段读出来是 0。
func TestHeadlessRunMaxRunMinutes(t *testing.T) {
	for _, c := range []struct {
		name string
		in   int
		mins int
		secs int
	}{
		{"没设", 0, MaxHeadlessRunMinutes, 7 * 24 * 3600},
		{"30 分钟", 30, 30, 1800},
		{"1 分钟（下限）", 1, 1, 60},
		{"正好 7 天", MaxHeadlessRunMinutes, MaxHeadlessRunMinutes, 7 * 24 * 3600},
		{"超过上限收进上限", MaxHeadlessRunMinutes + 1, MaxHeadlessRunMinutes, 7 * 24 * 3600},
		{"极大的值也收进上限", 1 << 30, MaxHeadlessRunMinutes, 7 * 24 * 3600},
	} {
		r := HeadlessRun{WorkflowID: "wf", MaxRunMinutes: c.in}
		if got := r.EffectiveMaxRunMinutes(); got != c.mins {
			t.Errorf("%s：生效分钟 = %d，期望 %d", c.name, got, c.mins)
		}
		if got := r.EffectiveMaxRunSeconds(); got != c.secs {
			t.Errorf("%s：生效秒数 = %d，期望 %d", c.name, got, c.secs)
		}
		norm, err := NormalizeHeadlessRun(r)
		if err != nil {
			t.Errorf("%s：不该报错: %v", c.name, err)
		} else if norm.MaxRunMinutes > MaxHeadlessRunMinutes {
			t.Errorf("%s：规整后仍超过上限: %d", c.name, norm.MaxRunMinutes)
		}
	}
	// 没设的就是没设：规整不把 0 改成 7 天（界面据此区分「默认」与「显式写了 7 天」）。
	if norm, err := NormalizeHeadlessRun(HeadlessRun{WorkflowID: "wf"}); err != nil || norm.MaxRunMinutes != 0 {
		t.Fatalf("没设的应保持 0: %+v %v", norm, err)
	}
	if _, err := NormalizeHeadlessRun(HeadlessRun{WorkflowID: "wf", MaxRunMinutes: -1}); err == nil ||
		strings.Contains(err.Error(), ErrInvalidLauncher.Error()) || !strings.Contains(err.Error(), "最长运行时间不能是负数") {
		t.Fatalf("负数应被拒绝，且信息面向用户（不带内部前缀）: %v", err)
	}

	// 经预设存取：设了的原样回来，超过上限的存成上限；旧存档（没有这个字段）读出来是 0。
	st := newMemStore(t)
	ctx := context.Background()
	in := okHeadless("h-1", "续聊")
	in.Run.MaxRunMinutes = 45
	if _, _, err := st.UpsertTerminalLauncher(ctx, in); err != nil {
		t.Fatal(err)
	}
	big := okHeadless("h-2", "续聊二")
	big.Run.MaxRunMinutes = MaxHeadlessRunMinutes * 2
	if _, _, err := st.UpsertTerminalLauncher(ctx, big); err != nil {
		t.Fatal(err)
	}
	plain := okHeadless("h-3", "续聊三")
	list, _, err := st.UpsertTerminalLauncher(ctx, plain)
	if err != nil || len(list) != 3 {
		t.Fatalf("存三条: %+v %v", list, err)
	}
	if list[0].Run.MaxRunMinutes != 45 || list[1].Run.MaxRunMinutes != MaxHeadlessRunMinutes || list[2].Run.MaxRunMinutes != 0 {
		t.Fatalf("存取不对: %d %d %d", list[0].Run.MaxRunMinutes, list[1].Run.MaxRunMinutes, list[2].Run.MaxRunMinutes)
	}
	bad := okHeadless("h-4", "负数")
	bad.Run.MaxRunMinutes = -3
	if _, _, err := st.UpsertTerminalLauncher(ctx, bad); err == nil || !errors.Is(err, ErrInvalidLauncher) {
		t.Fatalf("负数应被拒绝: %v", err)
	}
}

// 存档里不合法的非交互续聊预设（被手改过、或来自别的版本）整条丢掉，不连累其余预设；旧存档（没有 kind）照常读。

// 预设的「续跑成功判定时长」（#172）：没设 = 默认 10 分钟；设了的收进 [1 分钟, 1 天]；负数拒绝；规整不把 0 改成 10。
func TestHeadlessRunAutoResumeOKMinutes(t *testing.T) {
	for _, c := range []struct {
		name string
		in   int
		want time.Duration
	}{
		{"没设", 0, 10 * time.Minute},
		{"负数按没设算（保存时已被拒绝，旧数据不会有）", -4, 10 * time.Minute},
		{"30 分钟", 30, 30 * time.Minute},
		{"1 分钟（下限）", 1, time.Minute},
		{"正好 1 天", MaxAutoResumeOKMinutes, 24 * time.Hour},
		{"超过上限收进上限", MaxAutoResumeOKMinutes + 1, 24 * time.Hour},
		{"极大的值也收进上限", 1 << 30, 24 * time.Hour},
	} {
		r := HeadlessRun{WorkflowID: "wf", AutoResumeOKMinutes: c.in}
		if got := r.EffectiveAutoResumeOK(); got != c.want {
			t.Errorf("%s：生效时长 = %v，期望 %v", c.name, got, c.want)
		}
	}
	if DefaultAutoResumeOKMinutes != 10 {
		t.Fatalf("默认判定时长是 10 分钟（与设计、文档一致）: %d", DefaultAutoResumeOKMinutes)
	}
	if norm, err := NormalizeHeadlessRun(HeadlessRun{WorkflowID: "wf", AutoResume: true}); err != nil || norm.AutoResumeOKMinutes != 0 {
		t.Fatalf("没设的应保持 0: %+v %v", norm, err)
	}
	if norm, err := NormalizeHeadlessRun(HeadlessRun{WorkflowID: "wf", AutoResume: true, AutoResumeOKMinutes: 1 << 20}); err != nil || norm.AutoResumeOKMinutes != MaxAutoResumeOKMinutes {
		t.Fatalf("超过上限的收进上限: %+v %v", norm, err)
	}
	if _, err := NormalizeHeadlessRun(HeadlessRun{WorkflowID: "wf", AutoResume: true, AutoResumeOKMinutes: -1}); err == nil ||
		strings.Contains(err.Error(), ErrInvalidLauncher.Error()) || !strings.Contains(err.Error(), "续跑成功的判定时长不能是负数") {
		t.Fatalf("负数应被拒绝，且信息面向用户（不带内部前缀）: %v", err)
	}

	// 经预设存取：设了的原样回来；旧存档（没有这个字段）读出来是 0。
	st := newMemStore(t)
	ctx := context.Background()
	in := okHeadless("h-1", "续聊")
	in.Run.AutoResume, in.Run.AutoResumeOKMinutes = true, 25
	if _, _, err := st.UpsertTerminalLauncher(ctx, in); err != nil {
		t.Fatal(err)
	}
	plain := okHeadless("h-2", "续聊二")
	plain.Run.AutoResume = true
	if _, _, err := st.UpsertTerminalLauncher(ctx, plain); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListTerminalLaunchers(ctx)
	if err != nil || len(list) != 2 || list[0].Run == nil || list[0].Run.AutoResumeOKMinutes != 25 || list[1].Run.AutoResumeOKMinutes != 0 {
		t.Fatalf("应原样存取（没设的还是 0）: %+v %v", list, err)
	}
	bad := okHeadless("h-3", "负数")
	bad.Run.AutoResume, bad.Run.AutoResumeOKMinutes = true, -3
	if _, _, err := st.UpsertTerminalLauncher(ctx, bad); err == nil || !errors.Is(err, ErrInvalidLauncher) {
		t.Fatalf("负数应被拒绝: %v", err)
	}
}

func TestDecodeLaunchersDropsBadHeadlessAndReadsOldDocs(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	put := func(raw string) {
		t.Helper()
		if err := st.db.WithContext(ctx).Save(&model.SystemSetting{Key: TerminalLaunchersKey, Value: raw}).Error; err != nil {
			t.Fatal(err)
		}
	}
	put(`{"launchers":[
		{"id":"old","name":"旧预设","host":"` + launcherHost + `","dir":"/srv","command":"agent"},
		{"id":"ok","name":"续聊","host":"` + launcherHost + `","dir":"/srv","kind":"headless","run":{"workflow_id":"wf","model":"m1"}},
		{"id":"bad1","name":"坏的一","host":"` + launcherHost + `","dir":"/srv","kind":"headless","run":{"workflow_id":"wf","bin":"sudo"}},
		{"id":"bad2","name":"坏的二","host":"` + launcherHost + `","dir":"/srv","kind":"headless"},
		{"id":"bad3","name":"坏的三","host":"` + launcherHost + `","dir":"/srv","kind":"nope"}
	]}`)
	got, err := st.ListTerminalLaunchers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "old" || got[0].IsHeadless() || got[1].ID != "ok" || !got[1].IsHeadless() || got[1].Run.Model != "m1" {
		t.Fatalf("应只留下旧预设与合法的续聊预设: %+v", got)
	}
}

// 「新开对话」（#176 第三期）：预设打开 NewConversation 后，没有对话编号时不再报错而是新开；它与「固定对话编号」互斥，
// 必须有工作流编号（新对话靠它接入、之后的运行靠它找到这个对话）；来源标记只认「启用 Agent」。
func TestHeadlessRunNewConversationRules(t *testing.T) {
	ok := HeadlessRun{WorkflowID: "wf-web-1", NewConversation: true, Origin: " Enable_Agent "}
	norm, err := NormalizeHeadlessRun(ok)
	if err != nil {
		t.Fatalf("新开对话 + 工作流编号应合法: %v", err)
	}
	if !norm.NewConversation || norm.Origin != HeadlessOriginEnableAgent {
		t.Fatalf("新开标记保留、来源规整成小写: %+v", norm)
	}

	for name, c := range map[string]struct {
		run  HeadlessRun
		want string
	}{
		"固定了对话编号又要新开": {HeadlessRun{WorkflowID: "wf", ConversationID: "chat-1", NewConversation: true}, "不能再选「新开对话」"},
		"新开却没有工作流编号":  {HeadlessRun{NewConversation: true}, "请填工作流编号"},
		"不认识的来源":      {HeadlessRun{WorkflowID: "wf", Origin: "somewhere"}, "不认识的预设来源"},
	} {
		if _, err := NormalizeHeadlessRun(c.run); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：应被拒绝并说明原因（含 %q）: %v", name, c.want, err)
		}
	}

	// 默认关：旧预设与没有勾选的预设不新开；存取原样。
	if plain, err := NormalizeHeadlessRun(HeadlessRun{WorkflowID: "wf"}); err != nil || plain.NewConversation || plain.Origin != "" {
		t.Fatalf("没勾选就是续聊预设: %+v %v", plain, err)
	}
	st := newMemStore(t)
	ctx := context.Background()
	in := okHeadless("h-new", "启用 Agent")
	in.Run.NewConversation, in.Run.Origin = true, HeadlessOriginEnableAgent
	if _, _, err := st.UpsertTerminalLauncher(ctx, in); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListTerminalLaunchers(ctx)
	if err != nil || len(list) != 1 || list[0].Run == nil || !list[0].Run.NewConversation || list[0].Run.Origin != HeadlessOriginEnableAgent {
		t.Fatalf("应原样存取: %+v %v", list, err)
	}
	bad := okHeadless("h-bad", "冲突")
	bad.Run.ConversationID, bad.Run.NewConversation = "chat-1", true
	if _, _, err := st.UpsertTerminalLauncher(ctx, bad); err == nil || !errors.Is(err, ErrInvalidLauncher) {
		t.Fatalf("固定对话与新开互斥，保存应被拒绝: %v", err)
	}
}
