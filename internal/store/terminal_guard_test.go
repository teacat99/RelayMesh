package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func guardedLauncher(id, name string) TerminalLauncher {
	l := okLauncher(id, name)
	l.Guard = &LauncherGuard{
		Enabled:        true,
		WorkflowID:     "relaymesh-continuation",
		SilenceMinutes: 45,
		ResumeCommand:  "agent --resume {chat_id} --force",
		WakePrompt:     "第 %n 次唤醒（上限 %max），原因 %reason，请继续",
	}
	return l
}

func TestLauncherGuardRoundTripAndOmittedWhenAbsent(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	// 没配置守护的预设：存档里不出现 guard 字段（旧版本、旧界面完全不受影响）。
	plain := okLauncher("l-1", "plain")
	list, _, err := st.UpsertTerminalLauncher(ctx, plain)
	if err != nil || list[0].Guard != nil {
		t.Fatalf("不带守护的预设应保持 nil: %+v err=%v", list, err)
	}
	raw, _ := json.Marshal(list[0])
	if strings.Contains(string(raw), "guard") {
		t.Fatalf("没有守护配置时序列化里不应出现 guard: %s", raw)
	}

	// 带守护的预设：每个字段原样读回。
	list, _, err = st.UpsertTerminalLauncher(ctx, guardedLauncher("l-2", "guarded"))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := st.ListTerminalLaunchers(ctx)
	if len(got) != 2 || got[1].Guard == nil {
		t.Fatalf("守护配置应被保存: %+v", got)
	}
	g := got[1].Guard
	if !g.Enabled || g.WorkflowID != "relaymesh-continuation" || g.SilenceMinutes != 45 ||
		g.ResumeCommand != "agent --resume {chat_id} --force" || g.WakePrompt != "第 %n 次唤醒（上限 %max），原因 %reason，请继续" {
		t.Fatalf("守护配置读回不一致: %+v", g)
	}

	// 覆盖：把守护整个去掉（界面把它清空）后读回应为 nil。
	cleared := guardedLauncher("l-2", "guarded")
	cleared.Guard = &LauncherGuard{}
	if _, _, err = st.UpsertTerminalLauncher(ctx, cleared); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListTerminalLaunchers(ctx)
	if got[1].Guard != nil {
		t.Fatalf("什么都没填也没打开的守护配置不应落盘: %+v", got[1].Guard)
	}
}

func TestLauncherGuardKeepsFieldsWhenDisabled(t *testing.T) {
	st := newMemStore(t)
	l := guardedLauncher("l-1", "off")
	l.Guard.Enabled = false
	list, _, err := st.UpsertTerminalLauncher(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if g := list[0].Guard; g == nil || g.Enabled || g.WorkflowID != "relaymesh-continuation" || g.ResumeCommand == "" {
		t.Fatalf("关闭监测应保留其余配置（下次打开不用重填）: %+v", g)
	}
}

func TestLauncherGuardNormalizeDoesNotMutateCaller(t *testing.T) {
	orig := &LauncherGuard{Enabled: true, WorkflowID: "  wf-1  ", WakePrompt: "  a \n b  "}
	l := TerminalLauncher{ID: "l-1", Name: "x", Host: launcherHost, Guard: orig}
	n := l.Normalize()
	if n.Guard == orig {
		t.Fatal("规整应换成新指针，不能原地改调用方的配置")
	}
	if orig.WorkflowID != "  wf-1  " || orig.WakePrompt != "  a \n b  " {
		t.Fatalf("调用方的守护配置被改动了: %+v", orig)
	}
	if n.Guard.WorkflowID != "wf-1" || n.Guard.WakePrompt != "a b" {
		t.Fatalf("规整结果不对: %+v", n.Guard)
	}
}

func TestLauncherGuardValidationRejectsBadFields(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	cases := []struct {
		name string
		mut  func(*LauncherGuard)
		want string
	}{
		{"停摆时长太短", func(g *LauncherGuard) { g.SilenceMinutes = MinGuardSilenceMinutes - 1 }, "停摆判定时长"},
		{"停摆时长为负", func(g *LauncherGuard) { g.SilenceMinutes = -3 }, "停摆判定时长"},
		{"停摆时长太长", func(g *LauncherGuard) { g.SilenceMinutes = MaxGuardSilenceMinutes + 1 }, "停摆判定时长"},
		{"工作流含空格", func(g *LauncherGuard) { g.WorkflowID = "a b" }, "工作流编号"},
		{"工作流含制表符", func(g *LauncherGuard) { g.WorkflowID = "a\tb" }, "工作流编号"},
		{"工作流含控制字符", func(g *LauncherGuard) { g.WorkflowID = "a\x1bb" }, "工作流编号"},
		{"工作流太长", func(g *LauncherGuard) { g.WorkflowID = strings.Repeat("w", MaxGuardWorkflowRunes+1) }, "工作流编号最长"},
		{"拉起命令带换行", func(g *LauncherGuard) { g.ResumeCommand = "agent\nrm -rf /" }, "重新拉起命令只能是一行"},
		{"拉起命令带 ESC", func(g *LauncherGuard) { g.ResumeCommand = "a\x1bb" }, "重新拉起命令只能是一行"},
		{"拉起命令太长", func(g *LauncherGuard) { g.ResumeCommand = strings.Repeat("x", MaxLauncherCommandRunes+1) }, "重新拉起命令最长"},
		{"提示词带控制字符", func(g *LauncherGuard) { g.WakePrompt = "a\x07b" }, "唤醒提示词只能是一行"},
		{"提示词太长", func(g *LauncherGuard) { g.WakePrompt = strings.Repeat("字", MaxGuardPromptRunes+1) }, "唤醒提示词最长"},
		{"非交互姿态不认识", func(g *LauncherGuard) { g.Headless = &HeadlessWake{Enabled: true, Posture: "yolo"} }, "权限姿态"},
	}
	// 提示词里的 shell 特殊字符：每一个都要被拒绝（会话停在命令行时它们会被当成命令）。
	for _, ch := range GuardPromptUnsafe {
		ch := string(ch)
		cases = append(cases, struct {
			name string
			mut  func(*LauncherGuard)
			want string
		}{"提示词含 " + ch, func(g *LauncherGuard) { g.WakePrompt = "继续 " + ch + " 工作" }, "不能有"})
	}
	for i, c := range cases {
		l := guardedLauncher(fmt.Sprintf("g-%d", i), fmt.Sprintf("守护%d", i))
		c.mut(l.Guard)
		_, _, err := st.UpsertTerminalLauncher(ctx, l)
		if !errors.Is(err, ErrInvalidLauncher) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s：应被拒绝并说明「%s」，得到 %v", c.name, c.want, err)
		}
	}
	if list, _ := st.ListTerminalLaunchers(ctx); len(list) != 0 {
		t.Fatalf("被拒绝的预设不能被保存: %+v", list)
	}

	// 边界上的合法值：上下限、恰好等于长度上限、全留空只开监测。
	ok := guardedLauncher("ok-1", "边界")
	ok.Guard.SilenceMinutes = MinGuardSilenceMinutes
	ok.Guard.WorkflowID = strings.Repeat("字", MaxGuardWorkflowRunes)
	ok.Guard.WakePrompt = strings.Repeat("字", MaxGuardPromptRunes)
	ok.Guard.ResumeCommand = strings.Repeat("字", MaxLauncherCommandRunes)
	if _, _, err := st.UpsertTerminalLauncher(ctx, ok); err != nil {
		t.Fatalf("恰好等于上下限应合法: %v", err)
	}
	ok2 := guardedLauncher("ok-2", "上限")
	ok2.Guard.SilenceMinutes = MaxGuardSilenceMinutes
	ok2.Guard.WakePrompt = "请继续：第 %n/%max 次 - 原因 %reason, 谢谢. 全角标点；！？（）都没问题"
	if _, _, err := st.UpsertTerminalLauncher(ctx, ok2); err != nil {
		t.Fatalf("停摆时长上限应合法: %v", err)
	}
	only := okLauncher("ok-3", "只开监测")
	only.Guard = &LauncherGuard{Enabled: true}
	if list, _, err := st.UpsertTerminalLauncher(ctx, only); err != nil || list[2].Guard == nil || !list[2].Guard.Enabled {
		t.Fatalf("只打开监测、别的留空应合法: %+v err=%v", list, err)
	}
}

func TestLauncherGuardEffectiveSilence(t *testing.T) {
	if got := (LauncherGuard{}).EffectiveSilence(); got != DefaultGuardSilenceMinutes*time.Minute {
		t.Fatalf("默认应为 %d 分钟，得到 %v", DefaultGuardSilenceMinutes, got)
	}
	if got := (LauncherGuard{SilenceMinutes: 90}).EffectiveSilence(); got != 90*time.Minute {
		t.Fatalf("自定义 90 分钟，得到 %v", got)
	}
}

// 旧存档没有 guard：读出来是 nil；存档里的 guard 坏了只丢守护，预设本身保留。
func TestLauncherGuardDecodeKeepsPresetWhenGuardIsBroken(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	put := func(v string) {
		t.Helper()
		if err := st.WithTx(ctx, func(tx *gorm.DB) error {
			return tx.Save(&model.SystemSetting{Key: TerminalLaunchersKey, Value: v}).Error
		}); err != nil {
			t.Fatal(err)
		}
	}
	put(fmt.Sprintf(`{"launchers":[
		{"id":"old","name":"旧的","host":%q,"command":"agent"},
		{"id":"bad-guard","name":"守护坏了","host":%q,"guard":{"enabled":true,"silence_minutes":1,"workflow_id":"wf"}},
		{"id":"bad-cmd","name":"拉起命令带换行","host":%q,"guard":{"enabled":true,"resume_command":"a\nb"}},
		{"id":"good","name":"好的","host":%q,"guard":{"enabled":true,"workflow_id":"wf"}}
	]}`, launcherHost, launcherHost, launcherHost, launcherHost))
	got, err := st.ListTerminalLaunchers(ctx)
	if err != nil || len(got) != 4 {
		t.Fatalf("预设本身都应保留: %+v err=%v", got, err)
	}
	if got[0].Guard != nil {
		t.Fatalf("旧存档没有 guard 应为 nil: %+v", got[0].Guard)
	}
	if got[1].Guard != nil || got[2].Guard != nil {
		t.Fatalf("坏掉的守护配置应只被丢弃: %+v %+v", got[1].Guard, got[2].Guard)
	}
	if got[3].Guard == nil || !got[3].Guard.Enabled || got[3].Guard.WorkflowID != "wf" {
		t.Fatalf("好的守护配置应保留: %+v", got[3].Guard)
	}
}

// 非交互唤醒的配置：默认没有（不落盘）；姿态大小写与空白被规整；规整不改调用方的数据；全空的配置不留痕迹。
func TestLauncherGuardHeadlessNormalizeAndPosture(t *testing.T) {
	var none LauncherGuard
	if !none.isZero() {
		t.Fatal("默认配置应是全空")
	}
	g := LauncherGuard{WorkflowID: "wf", Headless: &HeadlessWake{Enabled: true, Posture: "  FORCE "}}
	n := g.normalize()
	if n.Headless == nil || n.Headless.Posture != "force" || n.Headless.EffectivePosture() != HeadlessPostureForce {
		t.Fatalf("姿态应被规整为 force: %+v", n.Headless)
	}
	if g.Headless.Posture != "  FORCE " {
		t.Fatalf("规整不该改到调用方的数据: %+v", g.Headless)
	}
	if err := n.validate(); err != nil {
		t.Fatal(err)
	}
	if (HeadlessWake{Enabled: true}).EffectivePosture() != HeadlessPostureAllowlist {
		t.Fatal("没写姿态应是 allowlist（不带 --force）")
	}
	empty := LauncherGuard{Headless: &HeadlessWake{}}.normalize()
	if empty.Headless != nil || !empty.isZero() {
		t.Fatalf("全空的非交互配置应被丢掉: %+v", empty)
	}
	off := LauncherGuard{Headless: &HeadlessWake{Enabled: false, Posture: "force"}}.normalize()
	if off.Headless == nil || off.Headless.Enabled || off.isZero() {
		t.Fatalf("关闭时保留姿态，下次打开不用重填: %+v", off.Headless)
	}
}
