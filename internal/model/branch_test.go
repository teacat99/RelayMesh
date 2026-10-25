package model

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// 开放范围的一行摘要只写当前版本真正生效的几项（读历史、发消息、定夺请示、改参数、启动、派任务——定夺从 #236 P2 起生效、改参数从 P3 起、启动从 P4 起、派任务从 P5 起）：
// 预设里有取值但还没有对应能力的项一旦写进摘要，就等于告诉智能体「你可以做」，它会照着去做然后撞墙（P5 之前的任务就是这样）。
func TestBranchExpose_SummaryOnlyEnforcedItems(t *testing.T) {
	cases := map[string]string{
		ExposePresetVisible: "读历史：摘要，不可发消息，不可定夺请示，不可改参数，不可启动，不可派任务",
		ExposePresetCollab:  "读历史：全文，可发消息，可定夺请示，可改参数：常驻规则，不可启动，可派任务",
		ExposePresetManaged: "读历史：全文，可发消息，可定夺请示，可改参数：常驻规则、阶段、标题、唤醒词、异步开关，可启动，可派任务",
	}
	for name, want := range cases {
		e, ok := BranchExposePreset(name)
		if !ok {
			t.Fatalf("预设 %s 应存在", name)
		}
		if got := e.Summary(); got != want {
			t.Errorf("预设 %s 的摘要 = %q，期望 %q", name, got, want)
		}
	}

	// 六项都已经生效，各自的取值都会反映在摘要里（P5 之前任务这一项是预留的，改它摘要不变；现在必须变）。
	collab, _ := BranchExposePreset(ExposePresetCollab)
	tasksRead := collab
	tasksRead.Tasks = ExposeTasksRead
	if tasksRead.Summary() == collab.Summary() || !strings.HasSuffix(tasksRead.Summary(), "，不可派任务") || !strings.HasSuffix(collab.Summary(), "，可派任务") {
		t.Errorf("派任务已经生效，派单 / 只读应反映在摘要里: %q vs %q", collab.Summary(), tasksRead.Summary())
	}
	tasksBogus := collab
	tasksBogus.Tasks = "bogus"
	if got := tasksBogus.Summary(); !strings.HasSuffix(got, "，不可派任务") {
		t.Errorf("不认识的任务取值按「不可派任务」写（宁紧勿松）: %q", got)
	}
	launchOn := collab
	launchOn.Launch = !collab.Launch
	if launchOn.Summary() == collab.Summary() {
		t.Errorf("启动已经生效，打开 / 关掉后摘要应变化: %q", launchOn.Summary())
	}
	noDecide := collab
	noDecide.Decide = false
	if noDecide.Summary() == collab.Summary() {
		t.Errorf("定夺已经生效，关掉后摘要应变化: %q", noDecide.Summary())
	}
	for _, level := range []string{ExposeParamsOff, ExposeParamsResident, ExposeParamsResidentPhase, ExposeParamsAll} {
		changed := collab
		changed.Params = level
		if level != collab.Params && changed.Summary() == collab.Summary() {
			t.Errorf("改参数已经生效，档位 %s 应反映在摘要里: %q", level, changed.Summary())
		}
	}
	bogus := collab
	bogus.Params = "bogus"
	if got := bogus.Summary(); got != "读历史：全文，可发消息，可定夺请示，不可改参数，不可启动，可派任务" {
		t.Errorf("不认识的档位按「不可改」写（宁紧勿松）: %q", got)
	}

	if got := (BranchExpose{History: ExposeHistoryOff, Say: true}).Summary(); got != "不可读历史，可发消息，不可定夺请示，不可改参数，不可启动，不可派任务" {
		t.Errorf("关掉读历史的摘要 = %q", got)
	}
}

func TestBranchPrefs_ValidateAndEffectiveMinutes(t *testing.T) {
	for _, ok := range []int{0, 5, 30, 1440} {
		if err := (BranchPrefs{EscalateMinutes: ok}).Validate(); err != nil {
			t.Errorf("escalate_minutes=%d 应合法: %v", ok, err)
		}
	}
	for _, bad := range []int{-1, 1, 4, 1441, 100000} {
		if err := (BranchPrefs{EscalateMinutes: bad}).Validate(); err == nil || !errors.Is(err, ErrInvalidBranchPrefs) {
			t.Errorf("escalate_minutes=%d 应不合法: %v", bad, err)
		}
	}
	for in, want := range map[int]int{0: 30, 5: 5, 45: 45, 1440: 1440, 3: 30, 99999: 30, -2: 30} {
		if got := (BranchPrefs{EscalateMinutes: in}).EscalateAfterMinutes(); got != want {
			t.Errorf("EscalateAfterMinutes(%d) = %d，期望 %d", in, got, want)
		}
	}
}

func TestConfirmAddressee_Helpers(t *testing.T) {
	if got := ConfirmAddresseeMain("wf-1"); got != "wf:wf-1" {
		t.Fatalf("ConfirmAddresseeMain = %q", got)
	}
	if got := ConfirmAddresseeMainOf("wf:wf-1"); got != "wf-1" {
		t.Fatalf("ConfirmAddresseeMainOf = %q", got)
	}
	for _, none := range []string{"", "human", "agent:wf-1", "wf:"} {
		if got := ConfirmAddresseeMainOf(none); got != "" {
			t.Errorf("ConfirmAddresseeMainOf(%q) = %q，应为空", none, got)
		}
	}
	if got := ConfirmByAgent("wf-1"); got != "agent:wf-1" {
		t.Fatalf("ConfirmByAgent = %q", got)
	}
	pending := ConfirmItem{Status: ConfirmStatusPending, Addressee: "wf:wf-1"}
	if !pending.RoutedToMain() {
		t.Error("待处理且 addressee 非空应算在等主会话")
	}
	for _, no := range []ConfirmItem{
		{Status: ConfirmStatusAnswered, Addressee: "wf:wf-1"},
		{Status: ConfirmStatusPending, Addressee: ""},
		{Status: ConfirmStatusPending, Addressee: "wf:"},
	} {
		if no.RoutedToMain() {
			t.Errorf("不应算在等主会话: %+v", no)
		}
	}
	// 视图：老记录（没有路由字段）和带转交信息的记录都能解出来；坏 JSON 不拖垮整行
	now := time.Now()
	v := NewConfirmItemView(&ConfirmItem{Seq: 3, Status: ConfirmStatusPending, EscalatedAt: &now, EscalationJSON: `{"by":"timeout","note":"超时"}`})
	if v.Escalation == nil || v.Escalation.By != "timeout" || v.Escalation.Note != "超时" || v.EscalatedAt == nil {
		t.Fatalf("转交信息没解出来: %+v", v)
	}
	if bad := NewConfirmItemView(&ConfirmItem{Seq: 4, EscalationJSON: "{坏的"}); bad.Escalation != nil {
		t.Fatalf("坏 JSON 应忽略: %+v", bad.Escalation)
	}
	if old := NewConfirmItemView(&ConfirmItem{Seq: 5}); old.Addressee != "" || old.Escalation != nil || old.EscalatedAt != nil {
		t.Fatalf("老记录不应有路由字段: %+v", old)
	}
}
