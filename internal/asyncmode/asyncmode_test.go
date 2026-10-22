package asyncmode

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		in         Input
		effective  bool
		ever       bool
		reason     Reason
		wantedMode string
	}{
		{"从未开启", Input{Now: now}, false, false, ReasonNone, Sync},
		{"从未开启但 hook 在线：没有降级可言", Input{HookSeen: now.Add(-time.Minute), Now: now}, false, false, ReasonNone, Sync},
		{"开启且 hook 刚出现", Input{Enabled: true, HookSeen: now.Add(-time.Second), Now: now}, true, true, ReasonNone, Async},
		{"开启且 hook 恰好 60 分钟前：仍有效", Input{Enabled: true, HookSeen: now.Add(-HookFreshness), Now: now}, true, true, ReasonNone, Async},
		{"开启但 hook 超过 60 分钟", Input{Enabled: true, HookSeen: now.Add(-HookFreshness - time.Second), Now: now}, false, true, ReasonHookStale, Sync},
		{"开启但从没见过 hook", Input{Enabled: true, Now: now}, false, true, ReasonNoHook, Sync},
		{"开启过现已关闭（hook 在线）", Input{Ever: true, HookSeen: now, Now: now}, false, true, ReasonDisabled, Sync},
		{"开启过现已关闭（hook 过期）", Input{Ever: true, HookSeen: now.Add(-5 * time.Hour), Now: now}, false, true, ReasonDisabled, Sync},
		{"未标记 Ever 但开着：一定算开启过", Input{Enabled: true, HookSeen: now, Now: now}, true, true, ReasonNone, Async},
		{"hook 时间在未来（时钟漂移）按新鲜处理", Input{Enabled: true, HookSeen: now.Add(2 * time.Minute), Now: now}, true, true, ReasonNone, Async},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.in)
			if got.Effective != tc.effective || got.Ever != tc.ever || got.Reason != tc.reason || got.Mode() != tc.wantedMode {
				t.Fatalf("Evaluate(%+v) = %+v（mode %s），期望 effective=%v ever=%v reason=%q mode=%s",
					tc.in, got, got.Mode(), tc.effective, tc.ever, tc.reason, tc.wantedMode)
			}
		})
	}
}

func TestContractFitsBudgetAndKeepsIrreversibleRule(t *testing.T) {
	// 上限从 600 放宽到 760：写进了「等用户的事一律发确认卡」与「长耗时命令默认后台跑」两条工作方式（#221，Q73 选 a）
	if n := utf8.RuneCountInString(Contract); n > 760 {
		t.Fatalf("async 契约 %d 字，超过 760 字上限", n)
	}
	if !strings.HasPrefix(Contract, "async: ") {
		t.Fatalf("契约必须以 async: 开头")
	}
	// INV-3：不可逆操作（L2）发确认卡、不阻断推进，但批准前绝不执行；这几句话不能在改文案时被删掉
	for _, must := range []string{"wait:false", "async_ask", "inbox", "部署", "推送远端", "删除数据", "改动凭据", "不可逆迁移",
		`risk:"L2"`, "确认卡", "不要停下来等", "批准前绝不执行", "没有答复就是不批准"} {
		if !strings.Contains(Contract, must) {
			t.Errorf("契约缺少关键词 %q", must)
		}
	}
}

// #221（用户 #859 / #834，Q73 选 a）：两条工作方式写进内置契约，改文案时不能被悄悄删掉——
// (1) 等用户答复或拍板的事一律发成确认卡，不只写在汇报里（汇报里的等待项容易被漏看）；
// (7) 长耗时命令默认后台启动、等待时先做不依赖结果的事、等待只用计时器分片并在片间读收件箱。
func TestContractKeepsConfirmCardAndLongCommandPractices(t *testing.T) {
	item := func(from, to string) string {
		i := strings.Index(Contract, from)
		if i < 0 {
			t.Fatalf("契约里找不到 %s 条目", from)
		}
		if to == "" {
			return Contract[i:]
		}
		j := strings.Index(Contract, to)
		if j < i {
			t.Fatalf("契约里 %s 应在 %s 之前", from, to)
		}
		return Contract[i:j]
	}
	item1 := item("(1)", "(2)")
	for _, must := range []string{"汇报只讲进展与结论", "凡是在等用户答复或拍板的事", "一律发成确认卡（async_ask）", "不要只写在汇报里", "避免被漏看"} {
		if !strings.Contains(item1, must) {
			t.Errorf("(1) 缺少 %q：%s", must, item1)
		}
	}
	item7 := item("(7)", "")
	for _, must := range []string{"长耗时命令", "构建、测试、e2e、部署", "默认后台启动", "先做不依赖结果的事", "计时器分片", "每片 ≤2 分钟", "片间读收件箱"} {
		if !strings.Contains(item7, must) {
			t.Errorf("(7) 缺少 %q：%s", must, item7)
		}
	}
	// 新增两条没有挤掉原有的条目：编号 (1)～(7) 各出现一次，且顺序递增
	last := -1
	for _, no := range []string{"(1)", "(2)", "(3)", "(4)", "(5)", "(6)", "(7)"} {
		if strings.Count(Contract, no) != 1 {
			t.Errorf("契约里 %s 应恰好出现一次，实际 %d 次", no, strings.Count(Contract, no))
		}
		at := strings.Index(Contract, no)
		if at <= last {
			t.Errorf("契约条目 %s 的顺序不对（位置 %d，上一条在 %d）", no, at, last)
		}
		last = at
	}
}

// #217：L2 不再「阻塞确认」，契约里不能留下旧的阻塞说法，否则同一段文字里前后矛盾（发卡又要停下来等）。
func TestContractNoLongerTellsAgentToBlockOnL2(t *testing.T) {
	for _, stale := range []string{"阻塞确认", "不能用 async_ask 绕过", "不带 wait:false 的 interactive_feedback"} {
		if strings.Contains(Contract, stale) {
			t.Errorf("契约还留着旧的阻塞说法 %q", stale)
		}
	}
	// L2 的条目要在 (4) 里讲完整：发卡、不执行、没批准置暂缓并写明等的编号
	i, j := strings.Index(Contract, "(4)"), strings.Index(Contract, "(5)")
	if i < 0 || j < i {
		t.Fatalf("契约里找不到 (4)(5) 条目")
	}
	item4 := Contract[i:j]
	for _, must := range []string{"（L2）", `risk:"L2"`, "批准前绝不执行", "没有答复就是不批准", "「暂缓」", "Q<N>"} {
		if !strings.Contains(item4, must) {
			t.Errorf("(4) 缺少 %q：%s", must, item4)
		}
	}
}

// #124：收尾规则「非阻断任务做完才能结束会话」与对档位已授权交付动作的让路，改文案时不能被悄悄删掉。
func TestContractKeepsClosingRuleAndAuthCarveOut(t *testing.T) {
	for _, must := range []string{"收尾：", "不用等用户答复的任务", "暂缓", "不挡收尾", "window: 行", "收件箱为空才收尾"} {
		if !strings.Contains(Contract, must) {
			t.Errorf("契约缺少收尾规则关键词 %q", must)
		}
	}
	// (4) 的让路只给 auth: 行已授权的可回滚交付动作；删数据、改凭据、不可逆迁移仍在必须阻塞的清单里
	i := strings.Index(Contract, "(4)")
	j := strings.Index(Contract, "(5)")
	if i < 0 || j < i {
		t.Fatalf("契约里找不到 (4)(5) 条目")
	}
	item4 := Contract[i:j]
	for _, must := range []string{"auth: 行已授权的推私有库、热更生产除外", "删除数据", "改动凭据", "不可逆迁移"} {
		if !strings.Contains(item4, must) {
			t.Errorf("(4) 缺少 %q：%s", must, item4)
		}
	}
	for _, mustNot := range []string{"除外：删除", "删除数据除外", "改动凭据除外", "不可逆迁移除外"} {
		if strings.Contains(item4, mustNot) {
			t.Errorf("(4) 不该给不可逆操作让路：%s", item4)
		}
	}
}

func TestReasonTextCoversEveryReason(t *testing.T) {
	for _, r := range []Reason{ReasonDisabled, ReasonNoHook, ReasonHookStale} {
		if ReasonText(r) == "" {
			t.Errorf("降级原因 %q 没有文案", r)
		}
	}
	if ReasonText(ReasonNone) != "" {
		t.Errorf("没有降级时不应有原因文案")
	}
}

func TestAuthLine(t *testing.T) {
	p := Profile{Name: "标准授权", Level: "L1", Rules: "读与分析直接做；部署先问。"}
	if got, want := AuthLine(p), "auth: 标准授权 [L1] 读与分析直接做；部署先问。"; got != want {
		t.Fatalf("auth 行 = %q，期望 %q", got, want)
	}
	// 等级为空按 L1 处理
	if got := AuthLine(Profile{Name: "x", Rules: "y"}); !strings.Contains(got, "[L1]") {
		t.Fatalf("空等级应按 L1 展示: %q", got)
	}
	// 过长的规则被截断，整行恰好不超过上限，并以省略号收尾
	long := Profile{Name: "很长的档位", Level: "L0", Rules: strings.Repeat("规", 1000)}
	line := AuthLine(long)
	if n := utf8.RuneCountInString(line); n != AuthLineMax {
		t.Fatalf("截断后应恰好 %d 字，实际 %d", AuthLineMax, n)
	}
	if !strings.HasSuffix(line, "…") {
		t.Fatalf("被截断时应以省略号收尾")
	}
	// 档位名异常长到占满整行时也不能越界或 panic
	huge := Profile{Name: strings.Repeat("名", 400), Level: "L1", Rules: "规则"}
	if n := utf8.RuneCountInString(AuthLine(huge)); n < 1 {
		t.Fatalf("不应为空")
	}
}

func TestReceiptLines(t *testing.T) {
	prof := Profile{Name: "标准授权", Level: "L1", Rules: "规则"}

	// INV-2：从没开启过的工作流什么都不输出
	if mode, auth := ReceiptLines(State{}, prof); mode != nil || auth != "" {
		t.Fatalf("从未开启时回执应为空: %v %q", mode, auth)
	}
	// 从未开启但 hook 在线、没有降级原因：同样不输出
	if mode, auth := ReceiptLines(Evaluate(Input{HookSeen: time.Now(), Now: time.Now()}), prof); mode != nil || auth != "" {
		t.Fatalf("从未开启时回执应为空: %v %q", mode, auth)
	}

	now := time.Now()
	// 生效：mode + 契约 + auth
	mode, auth := ReceiptLines(Evaluate(Input{Enabled: true, HookSeen: now, Now: now}), prof)
	if len(mode) != 2 || mode[0] != "mode: async" || mode[1] != Contract || auth != AuthLine(prof) {
		t.Fatalf("异步生效的回执不对: %v %q", mode, auth)
	}

	// 降级：一行 mode: sync + 原因，没有 auth（阶段提示词照旧）
	for _, tc := range []struct {
		in   Input
		want Reason
	}{
		{Input{Enabled: true, Now: now}, ReasonNoHook},
		{Input{Enabled: true, HookSeen: now.Add(-2 * time.Hour), Now: now}, ReasonHookStale},
		{Input{Ever: true, HookSeen: now, Now: now}, ReasonDisabled},
	} {
		mode, auth := ReceiptLines(Evaluate(tc.in), prof)
		if len(mode) != 1 || !strings.HasPrefix(mode[0], "mode: sync（") || !strings.Contains(mode[0], ReasonText(tc.want)) || auth != "" {
			t.Fatalf("%q 降级的回执不对: %v %q", tc.want, mode, auth)
		}
	}
}

func TestSignatureTracksModeReasonAndProfile(t *testing.T) {
	now := time.Now()
	prof := Profile{Name: "标准授权", Level: "L1", Rules: "可回滚的直接做"}
	if got := Signature(Evaluate(Input{Now: now}), 1, prof); got != "" {
		t.Fatalf("从未开启时没有签名: %q", got)
	}
	async := Evaluate(Input{Enabled: true, HookSeen: now, Now: now})
	stale := Evaluate(Input{Enabled: true, HookSeen: now.Add(-3 * time.Hour), Now: now})
	off := Evaluate(Input{Ever: true, HookSeen: now, Now: now})
	edited := prof
	edited.Rules = "只读，写入先问"
	sigs := map[string]bool{
		Signature(async, 1, prof):   true,
		Signature(async, 2, prof):   true, // 换了档位
		Signature(async, 1, edited): true, // 同一档位被改了规则
		Signature(stale, 1, prof):   true,
		Signature(off, 1, prof):     true,
	}
	if len(sigs) != 5 {
		t.Fatalf("模式 / 原因 / 档位 / 档位内容不同，签名必须不同: %v", sigs)
	}
	if Signature(async, 1, prof) != Signature(Evaluate(Input{Enabled: true, HookSeen: now.Add(-time.Minute), Now: now}), 1, prof) {
		t.Fatalf("同样的状态签名必须相同（hook 时间的细小变化不算变化）")
	}
	if n := len(Signature(Evaluate(Input{Enabled: true, HookSeen: now.Add(-3 * time.Hour), Now: now}), 4294967295, prof)); n > 64 {
		t.Fatalf("签名要能放进 64 字符的列: %d", n)
	}
}

func TestExtendWindow(t *testing.T) {
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	a := ExtendWindow(nil, now, 5*time.Minute)
	if !a.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("无窗口应从 now 起算: %v", a)
	}
	b := ExtendWindow(&a, now, 5*time.Minute)
	if !b.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("应累加: %v", b)
	}
	past := now.Add(-time.Hour)
	c := ExtendWindow(&past, now, 5*time.Minute)
	if !c.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("已过期应从 now 起算: %v", c)
	}
	far := now.Add(29 * time.Minute)
	d := ExtendWindow(&far, now, 5*time.Minute)
	if !d.Equal(now.Add(WindowMax)) {
		t.Fatalf("应封顶 30 分钟: %v", d)
	}
}

func TestWindowLine(t *testing.T) {
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	if WindowLine(WindowState{}, now, 0) != "" {
		t.Fatal("没有窗口不该有 window 行")
	}
	u := now.Add(25 * time.Minute)
	line := WindowLine(WindowState{Until: &u}, now, 10*time.Minute)
	for _, want := range []string{"剩余 25:00", "等待 10:00", "block_until_ms: 600000", "窗口到期仍无消息则走收尾"} {
		if !strings.Contains(line, want) {
			t.Fatalf("缺少 %q: %s", want, line)
		}
	}
	u2 := now.Add(3*time.Minute + 20*time.Second)
	if l := WindowLine(WindowState{Until: &u2}, now, 10*time.Minute); !strings.Contains(l, "block_until_ms: 200000") {
		t.Fatalf("剩余小于单次上限时应等剩余: %s", l)
	}
	exp := now.Add(-time.Second)
	if l := WindowLine(WindowState{Until: &exp}, now, 0); !strings.Contains(l, "已到期") || strings.Contains(l, "AwaitShell") {
		t.Fatalf("到期文案不对: %s", l)
	}
}

func TestWindowLinePausedWhileTasksActive(t *testing.T) {
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	line := WindowLine(WindowState{BankSec: 300, ActiveTasks: 2}, now, 0)
	for _, want := range []string{"已暂停", "2 个进行中", "05:00", "不要为了等回复而停下来", "暂缓", "不挡收尾"} {
		if !strings.Contains(line, want) {
			t.Fatalf("缺少 %q: %s", want, line)
		}
	}
	if strings.Contains(line, "AwaitShell") {
		t.Fatalf("暂停时不该让智能体去等: %s", line)
	}
	if got := WindowLine(WindowState{ActiveTasks: 3}, now, 0); got != "" {
		t.Fatalf("没有窗口时即使有任务也不该有行: %s", got)
	}
}
