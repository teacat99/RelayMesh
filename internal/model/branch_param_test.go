package model

import (
	"reflect"
	"testing"
)

func TestExposeParamsRank_FailClosed(t *testing.T) {
	cases := map[string]int{
		ExposeParamsOff: 0, ExposeParamsResident: 1, ExposeParamsResidentPhase: 2, ExposeParamsAll: 3,
		"": 0, "ALL": 0, "admin": 0, " all": 0,
	}
	for in, want := range cases {
		if got := ExposeParamsRank(in); got != want {
			t.Errorf("ExposeParamsRank(%q) = %d，期望 %d（不认识的取值必须按 off 处理）", in, got, want)
		}
	}
}

func TestParamFieldAllowed_Matrix(t *testing.T) {
	// 行：开放档位；列：BranchParamFields 的顺序（resident、phase、wake_resume、wake_new、title、async）。
	want := map[string][]bool{
		ExposeParamsOff:           {false, false, false, false, false, false},
		ExposeParamsResident:      {true, false, false, false, false, false},
		ExposeParamsResidentPhase: {true, true, false, false, false, false},
		ExposeParamsAll:           {true, true, true, true, true, true},
		"bogus":                   {false, false, false, false, false, false},
	}
	for level, row := range want {
		for i, f := range BranchParamFields {
			if got := ParamFieldAllowed(level, f); got != row[i] {
				t.Errorf("ParamFieldAllowed(%q, %q) = %v，期望 %v", level, f, got, row[i])
			}
		}
	}
	if ParamFieldAllowed(ExposeParamsAll, "profile_id") || ParamFieldAllowed(ExposeParamsAll, "wait") || ParamFieldAllowed(ExposeParamsAll, "") {
		t.Fatalf("不是可写字段的名字，任何档位都不能放行")
	}
}

func TestParamFieldsEditable_OrderAndLevels(t *testing.T) {
	if got := ParamFieldsEditable(ExposeParamsOff); len(got) != 0 {
		t.Fatalf("off 档不能改任何字段: %v", got)
	}
	if got, want := ParamFieldsEditable(ExposeParamsResident), []string{ParamFieldResident}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resident 档: %v，期望 %v", got, want)
	}
	if got, want := ParamFieldsEditable(ExposeParamsResidentPhase), []string{ParamFieldResident, ParamFieldPhase}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resident_phase 档: %v，期望 %v", got, want)
	}
	if got := ParamFieldsEditable(ExposeParamsAll); !reflect.DeepEqual(got, BranchParamFields) {
		t.Fatalf("all 档应是全部字段且顺序固定: %v", got)
	}
}

func TestExposeWidens(t *testing.T) {
	collab, _ := BranchExposePreset(ExposePresetCollab)
	visible, _ := BranchExposePreset(ExposePresetVisible)
	managed, _ := BranchExposePreset(ExposePresetManaged)
	with := func(e BranchExpose, mutate func(*BranchExpose)) BranchExpose {
		mutate(&e)
		return e
	}
	cases := []struct {
		name     string
		from, to BranchExpose
		want     bool
	}{
		{"仅可见 → 协作（params off → resident）", visible, collab, true},
		{"协作 → 托管（params 上调且 launch 打开）", collab, managed, true},
		{"托管 → 协作（收紧）", managed, collab, false},
		{"协作 → 仅可见（收紧）", collab, visible, false},
		{"没变", collab, collab, false},
		{"resident → resident_phase", collab, with(collab, func(e *BranchExpose) { e.Params = ExposeParamsResidentPhase }), true},
		{"resident_phase → resident", with(collab, func(e *BranchExpose) { e.Params = ExposeParamsResidentPhase }), collab, false},
		{"resident → all 但 launch 不变", collab, with(collab, func(e *BranchExpose) { e.Params = ExposeParamsAll }), true},
		{"只打开 launch", collab, with(collab, func(e *BranchExpose) { e.Launch = true }), true},
		{"只关闭 launch", managed, with(managed, func(e *BranchExpose) { e.Launch = false }), false},
		{"读历史加深不算放宽", visible, with(visible, func(e *BranchExpose) { e.History = ExposeHistoryFull }), false},
		{"打开发消息与定夺不算放宽", visible, with(visible, func(e *BranchExpose) { e.Say, e.Decide = true, true }), false},
		{"params 从 off 调到 all", visible, with(visible, func(e *BranchExpose) { e.Params = ExposeParamsAll }), true},
		{"原值不认识 → 任何已知档位都算放宽（宁紧勿松）", with(collab, func(e *BranchExpose) { e.Params = "bogus" }), collab, true},
	}
	for _, c := range cases {
		if got := ExposeWidens(c.from, c.to); got != c.want {
			t.Errorf("%s: ExposeWidens = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// 终端不可用时把「放宽」的部分压回当前取值，其余照请求；压回之后永远不再算放宽，也永远是合法的开放范围。
func TestClampExposeWiden(t *testing.T) {
	collab, _ := BranchExposePreset(ExposePresetCollab)
	visible, _ := BranchExposePreset(ExposePresetVisible)
	managed, _ := BranchExposePreset(ExposePresetManaged)
	with := func(e BranchExpose, mutate func(*BranchExpose)) BranchExpose {
		mutate(&e)
		return e
	}
	cases := []struct {
		name        string
		from, to    BranchExpose
		want        BranchExpose
		wantClamped []string
	}{
		{"仅可见 → 协作：改参数压回 off，其余照协作", visible, collab,
			with(collab, func(e *BranchExpose) { e.Params = ExposeParamsOff }), []string{"params"}},
		{"协作 → 托管：改参数压回 resident、启动压回关", collab, managed,
			with(managed, func(e *BranchExpose) { e.Params, e.Launch = ExposeParamsResident, false }), []string{"params", "launch"}},
		{"只打开 launch", collab, with(collab, func(e *BranchExpose) { e.Launch = true }), collab, []string{"launch"}},
		{"resident → resident_phase 压回 resident", collab, with(collab, func(e *BranchExpose) { e.Params = ExposeParamsResidentPhase }), collab, []string{"params"}},
		{"收紧原样放行", managed, visible, visible, nil},
		{"没变原样放行", collab, collab, collab, nil},
		{"只改非放宽项原样放行", visible, with(visible, func(e *BranchExpose) { e.History, e.Say = ExposeHistoryFull, true }),
			with(visible, func(e *BranchExpose) { e.History, e.Say = ExposeHistoryFull, true }), nil},
		{"原值不认识：压回成 off（宁紧勿松）", with(collab, func(e *BranchExpose) { e.Params = "bogus" }), collab,
			with(collab, func(e *BranchExpose) { e.Params = ExposeParamsOff }), []string{"params"}},
		{"原值为空：压回成 off", with(collab, func(e *BranchExpose) { e.Params = "" }), collab,
			with(collab, func(e *BranchExpose) { e.Params = ExposeParamsOff }), []string{"params"}},
	}
	for _, c := range cases {
		got, clamped := ClampExposeWiden(c.from, c.to)
		if got != c.want || !reflect.DeepEqual(clamped, c.wantClamped) {
			t.Errorf("%s: ClampExposeWiden = %+v %v，期望 %+v %v", c.name, got, clamped, c.want, c.wantClamped)
		}
		if err := got.Validate(); err != nil {
			t.Errorf("%s: 压回之后必须是合法的开放范围: %v", c.name, err)
		}
		if ExposeWidens(c.from, got) {
			t.Errorf("%s: 压回之后不该再算放宽: %+v → %+v", c.name, c.from, got)
		}
		if again, againClamped := ClampExposeWiden(c.from, got); again != got || len(againClamped) != 0 {
			t.Errorf("%s: 压回是幂等的: %+v %v", c.name, again, againClamped)
		}
	}
}

func TestBranchParamEvent_Flags(t *testing.T) {
	if !(BranchParamEvent{By: "agent:wf-main"}).ByAgent() {
		t.Fatal("agent: 前缀应判为主会话写入")
	}
	if (BranchParamEvent{By: BranchParamByHuman}).ByAgent() || (BranchParamEvent{}).ByAgent() {
		t.Fatal("human / 空不是主会话写入")
	}
	if (BranchParamEvent{}).IsRevert() || !(BranchParamEvent{RevertOf: 3}).IsRevert() {
		t.Fatal("IsRevert 应只看 RevertOf")
	}
}
