package ntfy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// legacyConfigJSON 是升级前（没有 priority_by_kind 字段）保存在库里的配置。
const legacyConfigJSON = `{"enabled":true,"server":"https://ntfy.example.com","topic":"t-1","priority":5,
"include_summary":false,"delay_seconds":0,"run_notify":true,"run_min_seconds":60}`

func legacyConfig(t *testing.T, priority int) Config {
	t.Helper()
	var c Config
	if err := json.Unmarshal([]byte(legacyConfigJSON), &c); err != nil {
		t.Fatal(err)
	}
	c.Priority = priority
	return c.Normalize()
}

// 升级不改变任何人的推送：没有覆盖表时，每一类消息生成的优先级与升级前的规则逐项一致。
func TestBuiltinPriorities_MatchLegacyRules(t *testing.T) {
	for p := 1; p <= 5; p++ {
		cfg := legacyConfig(t, p)
		if len(cfg.PriorityByKind) != 0 {
			t.Fatalf("旧配置不该有覆盖表：%v", cfg.PriorityByKind)
		}

		// 老规则 1：等你回复、守护告警、命令出状况、测试推送都用全局优先级。
		if got := BuildRequestMessage(cfg, RequestInfo{SessionID: "s"}, 0).Priority; got != p {
			t.Errorf("p=%d 等你回复：%d", p, got)
		}
		if got := BuildAutoResumeMessage(cfg, AutoResumeInfo{Key: "k", Name: "n"}, "", 0).Priority; got != p {
			t.Errorf("p=%d 守护告警：%d", p, got)
		}
		if got := BuildTestMessage(cfg, time.Now()).Priority; got != p {
			t.Errorf("p=%d 测试推送：%d", p, got)
		}
		for _, st := range []string{model.RunFailed, model.RunTimedOut, model.RunLost, model.RunCancelled} {
			if got := BuildRunMessage(cfg, RunInfo{RunID: "r", State: st, Duration: time.Minute}, "", 0).Priority; got != p {
				t.Errorf("p=%d 命令 %s：%d", p, st, got)
			}
		}

		// 老规则 2：命令顺利完成最多按 3。
		wantOK := p
		if wantOK > 3 {
			wantOK = 3
		}
		if got := BuildRunMessage(cfg, RunInfo{RunID: "r", State: model.RunSucceeded, Duration: time.Minute}, "", 0).Priority; got != wantOK {
			t.Errorf("p=%d 命令成功：%d，期望 %d", p, got, wantOK)
		}

		// 老规则 3：新消息类写死，穿透类 4、其余 3，不跟全局优先级走。
		for _, c := range []struct {
			kind      string
			penetrate bool
			want      int
		}{
			{"mention", true, 4},
			{"approval_waiting", true, 4},
			{"guard_alert", true, 4},
			{"ask_card", false, 3},
			{"agent_text", false, 3},
		} {
			in := EventInfo{ID: "e", Kind: c.kind, Penetrate: c.penetrate}
			if got := BuildEventMessage(cfg, in, 1, 0).Priority; got != c.want {
				t.Errorf("p=%d 新消息类 %s：%d，期望 %d", p, c.kind, got, c.want)
			}
		}

		// 异常类默认全不推送。
		for _, k := range kindTable {
			if k.Group == GroupException {
				if _, on := cfg.PriorityFor(k.ID); on {
					t.Errorf("p=%d 异常类 %s 默认不该推送", p, k.ID)
				}
			}
		}
		if cfg.ExceptionKindEnabled() {
			t.Errorf("p=%d 没有覆盖表时不该有任何异常类被打开", p)
		}
	}
}

func TestPriorityFor_Overrides(t *testing.T) {
	cfg := Config{Enabled: true, Topic: "t", Priority: 4, PriorityByKind: map[string]int{
		KindMention:       2,
		KindRunOK:         5, // 单独改过就不再被「最多 3」限制
		KindAgentText:     0, // 不推送
		KindSecurityAlert: 5, // 打开一个默认关闭的异常类
	}}.Normalize()

	for _, c := range []struct {
		kind string
		want int
		on   bool
	}{
		{KindMention, 2, true},
		{KindRunOK, 5, true},
		{KindAgentText, 0, false},
		{KindSecurityAlert, 5, true},
		{KindAgentError, 0, false}, // 没覆盖：异常类仍关
		{KindApproval, 4, true},    // 没覆盖：内置默认
		{KindRequest, 4, true},     // 没覆盖：跟随默认优先级
		{"no_such_kind", 4, true},  // 没登记的类型不拦，跟随默认优先级
	} {
		got, on := cfg.PriorityFor(c.kind)
		if got != c.want || on != c.on {
			t.Errorf("%s：得到 (%d, %v)，期望 (%d, %v)", c.kind, got, on, c.want, c.on)
		}
	}
	if !cfg.ExceptionKindEnabled() {
		t.Error("打开了安全告警，应报告有异常类被打开")
	}
	cfg.Enabled = false
	if cfg.ExceptionKindEnabled() {
		t.Error("总开关关着时不该报告有异常类被打开")
	}

	// 越界值在取用时被夹住（Validate 会在保存时拒绝它们，这里只是防御库里已经存在的坏值）。
	bad := Config{Priority: 4, PriorityByKind: map[string]int{KindMention: 9, KindAskCard: -3}}
	if p, on := bad.PriorityFor(KindMention); p != 5 || !on {
		t.Errorf("过大的值应夹到 5：(%d, %v)", p, on)
	}
	if _, on := bad.PriorityFor(KindAskCard); on {
		t.Error("负数当作不推送")
	}
}

func TestNormalize_DropsUnknownKinds(t *testing.T) {
	c := Config{Priority: 3, PriorityByKind: map[string]int{" mention ": 2, "ghost": 5}}.Normalize()
	if len(c.PriorityByKind) != 1 || c.PriorityByKind[KindMention] != 2 {
		t.Fatalf("应只留下登记过的类型（并去掉键两端的空白）：%v", c.PriorityByKind)
	}
	if got := (Config{Priority: 3, PriorityByKind: map[string]int{"ghost": 5}}).Normalize().PriorityByKind; got != nil {
		t.Errorf("全是未登记的类型时应归一成 nil：%v", got)
	}
}

func TestValidate_PriorityByKind(t *testing.T) {
	base := Config{Enabled: true, Topic: "t-1", Priority: 4}.Normalize()
	ok := base
	ok.PriorityByKind = map[string]int{KindMention: 0, KindAskCard: 5, KindAgentError: 1}
	if err := ok.Validate(true); err != nil {
		t.Fatalf("0~5 都应合法：%v", err)
	}
	for _, v := range []int{-1, 6, 100} {
		bad := base
		bad.PriorityByKind = map[string]int{KindMention: v}
		err := bad.Validate(true)
		if err == nil || !strings.Contains(err.Error(), "群里 @ 了你") {
			t.Errorf("值 %d 应被拒绝，且提示是哪一类：%v", v, err)
		}
	}
}

func TestUpdateApplyTo_PriorityByKind(t *testing.T) {
	cur := Config{Enabled: true, Topic: "t-1", Priority: 4, PriorityByKind: map[string]int{KindMention: 2}}.Normalize()

	// 没带这个字段（nil）：保持原样，不能因为别的项被改就把表清掉。
	keep := Update{Enabled: true, Topic: "t-1", Priority: 3}.ApplyTo(cur)
	if keep.PriorityByKind[KindMention] != 2 {
		t.Errorf("没带字段应保持原覆盖表：%v", keep.PriorityByKind)
	}
	if keep.Priority != 3 {
		t.Errorf("其他项照常整体替换：%d", keep.Priority)
	}

	// 带了就整张替换。
	repl := Update{Enabled: true, Topic: "t-1", Priority: 4, PriorityByKind: map[string]int{KindAskCard: 0}}.ApplyTo(cur)
	if len(repl.PriorityByKind) != 1 || repl.PriorityByKind[KindAskCard] != 0 {
		t.Errorf("应整张替换：%v", repl.PriorityByKind)
	}

	// 空表 = 清空，全部回到内置默认。
	cleared := Update{Enabled: true, Topic: "t-1", Priority: 4, PriorityByKind: map[string]int{}}.ApplyTo(cur)
	if len(cleared.PriorityByKind) != 0 {
		t.Errorf("空表应清空：%v", cleared.PriorityByKind)
	}

	// 改了表也不能顺带动到原配置（Normalize 复制了一份）。
	repl.PriorityByKind[KindMention] = 5
	if cur.PriorityByKind[KindMention] != 2 {
		t.Error("ApplyTo 的结果不该与原配置共用同一张表")
	}
}

func TestPublic_PriorityByKindIsAlwaysObject(t *testing.T) {
	b, err := json.Marshal(Config{Priority: 4}.Public(""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"priority_by_kind":{}`) {
		t.Errorf("没有覆盖时应序列化成 {} 而不是 null：%s", b)
	}
	b, _ = json.Marshal(Config{Priority: 4, PriorityByKind: map[string]int{KindMention: 0}}.Public(""))
	if !strings.Contains(string(b), `"priority_by_kind":{"mention":0}`) {
		t.Errorf("0（不推送）必须原样给出：%s", b)
	}
}

func TestKindTable_Integrity(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range kindTable {
		if k.ID == "" || k.Label == "" || k.Desc == "" {
			t.Errorf("每一行都要有标识、名字和说明：%+v", k)
		}
		if seen[k.ID] {
			t.Errorf("标识重复：%s", k.ID)
		}
		seen[k.ID] = true
		switch k.Group {
		case GroupNeedYou, GroupRun:
			if !k.DefaultOn {
				t.Errorf("%s 升级前就会推送，内置默认必须是开", k.ID)
			}
		case GroupException:
			if k.DefaultOn {
				t.Errorf("异常类 %s 默认必须不推送", k.ID)
			}
			if k.SuggestedPriority < 1 || k.SuggestedPriority > 5 {
				t.Errorf("异常类 %s 需要一个 1~5 的建议优先级", k.ID)
			}
		default:
			t.Errorf("%s 的分组 %q 不认识", k.ID, k.Group)
		}
		if k.DefaultPriority < 0 || k.DefaultPriority > 5 {
			t.Errorf("%s 的内置优先级越界：%d", k.ID, k.DefaultPriority)
		}
	}

	// Kinds 给的是副本。
	ks := Kinds()
	ks[0].Label = "被改过"
	if Kinds()[0].Label == "被改过" {
		t.Error("Kinds 应返回副本")
	}
	if !KnownKind(KindMention) || KnownKind("nope") {
		t.Error("KnownKind 判断不对")
	}
	if KindLabel("nope") != "nope" || KindLabel(KindMention) == KindMention {
		t.Error("KindLabel：没登记的原样返回，登记了的返回名字")
	}
}

func TestEventPushKind_CoversGateKinds(t *testing.T) {
	for gate, want := range map[string]string{
		"mention":          KindMention,
		"approval_waiting": KindApproval,
		"ask_card":         KindAskCard,
		"agent_text":       KindAgentText,
		"guard_alert":      KindGroupCooling,
	} {
		if got, ok := eventPushKind(gate); !ok || got != want {
			t.Errorf("%s → (%q, %v)，期望 %q", gate, got, ok, want)
		}
	}
	for _, gate := range []string{"", "run_end", "blocking_feedback", "task_update", "system_event"} {
		if _, ok := eventPushKind(gate); ok {
			t.Errorf("%q 不走新消息类入口，不该有对应的类型", gate)
		}
	}
}

func TestBuildKindTestMessage(t *testing.T) {
	now := time.Date(2026, 10, 22, 15, 4, 5, 0, time.Local)
	cfg := Config{Priority: 4, PriorityByKind: map[string]int{KindMention: 2, KindAskCard: 0}}.Normalize()

	m, p := BuildKindTestMessage(cfg, KindMention, now)
	if p != 2 || m.Priority != 2 {
		t.Errorf("应按设好的 2 级发：%d / %d", p, m.Priority)
	}
	if !strings.Contains(m.Title, "群里 @ 了你") || !strings.Contains(m.Body, "2 级（较低）") || !strings.Contains(m.Body, "15:04:05") {
		t.Errorf("标题要写明哪一类，正文要写明几级：%+v", m)
	}

	// 设成不推送的类型：试发用内置默认（让人看到打开后的效果），并明说实际不会收到。
	m, p = BuildKindTestMessage(cfg, KindAskCard, now)
	if p != 3 || !strings.Contains(m.Body, "不推送") {
		t.Errorf("不推送的类型试发应走内置默认 3 并说明：%d %q", p, m.Body)
	}

	// 默认关闭的异常类：用界面建议的优先级。
	m, p = BuildKindTestMessage(cfg, KindSecurityAlert, now)
	if p != 5 || !strings.Contains(m.Body, "不推送") {
		t.Errorf("默认关闭的异常类试发应走建议值 5 并说明：%d %q", p, m.Body)
	}

	// 不认识的类型退回普通测试推送。
	m, p = BuildKindTestMessage(cfg, "", now)
	if p != 4 || m.Title != "RelayMesh 测试推送" {
		t.Errorf("没有类型就是普通测试推送：%d %q", p, m.Title)
	}
}
