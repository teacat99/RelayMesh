package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }

// setParamsTier 把分支向主会话开放的「改参数」档位设成 level，其余各项取「协作」的值。
func setParamsTier(t *testing.T, st *Store, branch, level string) {
	t.Helper()
	exp := model.BranchExpose{History: model.ExposeHistoryFull, Decide: true, Say: true, Params: level, Tasks: model.ExposeTasksAssign}
	if _, err := st.UpdateBranchSettings(context.Background(), branch, UpdateBranchInput{Expose: &exp}); err != nil {
		t.Fatalf("设置开放范围 %s: %v", level, err)
	}
}

// paramsFixture 建好主会话 wf-main、分支 wf-b1、无关会话 wf-other，并把分支的「改参数」档位设成 level。
func paramsFixture(t *testing.T, level string) *Store {
	t.Helper()
	st := newBranchStore(t)
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mkWF(t, st, "wf-other", "无关会话")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)
	setParamsTier(t, st, "wf-b1", level)
	return st
}

// validPatch 返回只改 field 一项的合法 patch（async 取 false：默认就是关，所以是「没变」）。
func validPatch(field string) SessionParamsPatch {
	switch field {
	case model.ParamFieldResident:
		return SessionParamsPatch{Resident: strp("规则：先写测试再改代码")}
	case model.ParamFieldPhase:
		return SessionParamsPatch{Phase: strp("dev")}
	case model.ParamFieldWakeResume:
		return SessionParamsPatch{WakeResume: strp("请继续手上的任务")}
	case model.ParamFieldWakeNew:
		return SessionParamsPatch{WakeNew: strp("接入工作流 %workflow 后先读收件箱")}
	case model.ParamFieldTitle:
		return SessionParamsPatch{Title: strp("新标题")}
	case model.ParamFieldAsync:
		return SessionParamsPatch{Async: boolp(false)}
	}
	panic("未知字段 " + field)
}

func countParamEvents(t *testing.T, st *Store, branch string) int64 {
	t.Helper()
	var n int64
	if err := st.DB().Model(&model.BranchParamEvent{}).Where("branch_workflow_id = ?", branch).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// paramState 读出分支此刻的全部参数值（去掉每次都不同的时间戳），用来断言「没有任何写入」。
func paramState(t *testing.T, st *Store, branch string) SessionParamValues {
	t.Helper()
	cur, err := st.readParamCurrent(context.Background(), branch, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	v := cur.values()
	v.Resident.UpdatedAt = nil
	return *v
}

func TestSessionParams_PermissionMatrix_Main(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	for _, level := range []string{model.ExposeParamsOff, model.ExposeParamsResident, model.ExposeParamsResidentPhase, model.ExposeParamsAll} {
		for _, field := range model.BranchParamFields {
			level, field := level, field
			t.Run(level+"/"+field, func(t *testing.T) {
				st := paramsFixture(t, level)
				before := paramState(t, st, "wf-b1")
				res, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", validPatch(field), now)
				if !model.ParamFieldAllowed(level, field) {
					want := model.ParamFieldMinLevel(field)
					if level == model.ExposeParamsOff {
						want = "没有向主会话开放"
					}
					wantBranchErr(t, err, ErrForbidden, want)
					if res != nil {
						t.Fatalf("被拒绝时不应返回结果: %+v", res)
					}
					if n := countParamEvents(t, st, "wf-b1"); n != 0 {
						t.Fatalf("被拒绝时不应有事件，得到 %d", n)
					}
					if after := paramState(t, st, "wf-b1"); !reflect.DeepEqual(before, after) {
						t.Fatalf("被拒绝时不应写入任何字段:\n前 %+v\n后 %+v", before, after)
					}
					return
				}
				if err != nil {
					t.Fatalf("档位 %s 应能改 %s: %v", level, field, err)
				}
				if field == model.ParamFieldAsync {
					if len(res.Applied) != 0 || !reflect.DeepEqual(res.Unchanged, []string{model.ParamFieldAsync}) {
						t.Fatalf("async 默认就是关，写 false 应是「没变」: %+v", res)
					}
					return
				}
				if len(res.Applied) != 1 || res.Applied[0].Field != field || res.Applied[0].By != "agent:wf-main" {
					t.Fatalf("应写入 1 项并记在主会话名下: %+v", res.Applied)
				}
				if n := countParamEvents(t, st, "wf-b1"); n != 1 {
					t.Fatalf("应有 1 条事件，得到 %d", n)
				}
			})
		}
	}
}

func TestSessionParams_CallerMatrix(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := paramsFixture(t, model.ExposeParamsAll)
	mkWF(t, st, "wf-main2", "另一个主会话")
	mkWF(t, st, "wf-b2", "另一个分支")
	mustLink(t, st, "wf-main2", "wf-b2", model.BranchCreatedByHuman)
	setParamsTier(t, st, "wf-b2", model.ExposeParamsAll)

	cases := []struct {
		name, caller, target string
		kind                 ErrorKind
		want                 string
	}{
		{"分支本人不能改自己的", "wf-b1", "wf-b1", ErrForbidden, "分支不能改自己的参数"},
		{"无关会话", "wf-other", "wf-b1", ErrForbidden, "不是你的分支"},
		{"别人的主会话（它自己的分支开到 all 也没用）", "wf-main2", "wf-b1", ErrForbidden, "不是你的分支"},
		{"反过来也一样", "wf-main", "wf-b2", ErrForbidden, "不是你的分支"},
		{"空调用者", "", "wf-b1", ErrForbidden, "不是你的分支"},
		{"目标不是分支", "wf-main", "wf-other", ErrForbidden, "不是任何主会话的分支"},
		{"主会话改自己", "wf-main", "wf-main", ErrForbidden, "不是任何主会话的分支"},
		{"目标不存在", "wf-main", "wf-ghost", ErrForbidden, "不是任何主会话的分支"},
		{"目标为空", "wf-main", "", ErrInvalidInput, "缺少要操作的分支"},
	}
	for _, c := range cases {
		_, err := st.ApplySessionParams(ctx, c.caller, c.target, SessionParamsPatch{Resident: strp("x")}, now)
		t.Run("set/"+c.name, func(t *testing.T) { wantBranchErr(t, err, c.kind, c.want) })
		if c.name == "分支本人不能改自己的" {
			continue // 分支本人可以读自己的（见 GetView 测试）
		}
		_, err = st.GetSessionParams(ctx, c.caller, c.target, now)
		t.Run("get/"+c.name, func(t *testing.T) { wantBranchErr(t, err, c.kind, c.want) })
	}
	for _, b := range []string{"wf-b1", "wf-b2"} {
		if n := countParamEvents(t, st, b); n != 0 {
			t.Fatalf("所有被拒绝的调用都不应留下事件，%s 有 %d 条", b, n)
		}
	}
}

func TestSessionParams_EndedRelation(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	t.Run("closed", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		if _, err := st.CloseBranch(ctx, "wf-b1", model.BranchCreatedByHuman); err != nil {
			t.Fatal(err)
		}
		_, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", validPatch(model.ParamFieldResident), now)
		wantBranchErr(t, err, ErrForbidden, "已经结束")
		_, err = st.GetSessionParams(ctx, "wf-main", "wf-b1", now)
		wantBranchErr(t, err, ErrForbidden, "已经结束")
		_, err = st.GetSessionParams(ctx, "wf-b1", "wf-b1", now)
		wantBranchErr(t, err, ErrForbidden, "已经结束")
	})
	t.Run("unlinked", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		if _, err := st.UnlinkBranch(ctx, "wf-b1", false, "human"); err != nil {
			t.Fatal(err)
		}
		_, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", validPatch(model.ParamFieldResident), now)
		wantBranchErr(t, err, ErrForbidden, "不是任何主会话的分支")
	})
	t.Run("history_survives_close", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		if _, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", validPatch(model.ParamFieldResident), now); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CloseBranch(ctx, "wf-b1", model.BranchCreatedByHuman); err != nil {
			t.Fatal(err)
		}
		evs, err := st.ListBranchParamEvents(ctx, "wf-b1", 0)
		if err != nil || len(evs) != 1 {
			t.Fatalf("关系结束后历史事件应保留: %v %v", evs, err)
		}
	})
	t.Run("tier_lowered_takes_effect_immediately", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		if _, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", validPatch(model.ParamFieldTitle), now); err != nil {
			t.Fatal(err)
		}
		setParamsTier(t, st, "wf-b1", model.ExposeParamsOff) // 用户刹车
		_, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", SessionParamsPatch{Title: strp("再改一次")}, now)
		wantBranchErr(t, err, ErrForbidden, "没有向主会话开放")
		if got := st.WorkflowTitleOf(ctx, "wf-b1"); got != "新标题" {
			t.Fatalf("刹车之后不能再写入，标题应保持 %q，得到 %q", "新标题", got)
		}
	})
}

func TestSessionParams_AllOrNothing(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	cases := []struct {
		name  string
		level string
		patch SessionParamsPatch
		kind  ErrorKind
		want  string
	}{
		{"一项越权整条拒绝", model.ExposeParamsResidentPhase,
			SessionParamsPatch{Resident: strp("规则甲"), Phase: strp("plan"), Title: strp("越权标题")}, ErrForbidden, "title（至少需要 all 档）"},
		{"多项越权都要点名", model.ExposeParamsResident,
			SessionParamsPatch{Resident: strp("规则甲"), Phase: strp("plan"), WakeNew: strp("%workflow")}, ErrForbidden, "phase（至少需要 resident_phase 档）、wake_new（至少需要 all 档）"},
		{"取值不合法整条拒绝", model.ExposeParamsAll,
			SessionParamsPatch{Resident: strp("规则甲"), Phase: strp("bogus")}, ErrInvalidInput, "phase"},
		{"常驻规则超长整条拒绝", model.ExposeParamsAll,
			SessionParamsPatch{Title: strp("好标题"), Resident: strp(strings.Repeat("长", model.ResidentPromptMax+1))}, ErrInvalidInput, "resident"},
		{"异步被拒整条拒绝", model.ExposeParamsAll,
			SessionParamsPatch{Resident: strp("规则甲"), Async: boolp(true)}, ErrForbidden, "async"},
		{"空 patch", model.ExposeParamsAll, SessionParamsPatch{}, ErrInvalidInput, "没有任何要改的字段"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := paramsFixture(t, c.level)
			before := paramState(t, st, "wf-b1")
			res, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", c.patch, now)
			wantBranchErr(t, err, c.kind, c.want)
			if res != nil {
				t.Fatalf("整条拒绝时不应返回结果: %+v", res)
			}
			if after := paramState(t, st, "wf-b1"); !reflect.DeepEqual(before, after) {
				t.Fatalf("整条拒绝时一个字段也不能写:\n前 %+v\n后 %+v", before, after)
			}
			if n := countParamEvents(t, st, "wf-b1"); n != 0 {
				t.Fatalf("整条拒绝时不应有事件，得到 %d", n)
			}
		})
	}
}

func TestSessionParams_ValueValidation(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	long := strings.Repeat("字", model.BranchParamTitleMax+1)
	cases := []struct {
		name  string
		patch SessionParamsPatch
		want  string
	}{
		{"常驻规则超长", SessionParamsPatch{Resident: strp(strings.Repeat("长", model.ResidentPromptMax+1))}, "resident"},
		{"新开唤醒词缺 %workflow", SessionParamsPatch{WakeNew: strp("接入吧")}, "%workflow"},
		{"阶段不在三个合法值里", SessionParamsPatch{Phase: strp("review")}, "phase"},
		{"阶段为空", SessionParamsPatch{Phase: strp("  ")}, "phase"},
		{"标题为空", SessionParamsPatch{Title: strp("   ")}, "title"},
		{"标题多行", SessionParamsPatch{Title: strp("第一行\n第二行")}, "一行"},
		{"标题超长", SessionParamsPatch{Title: strp(long)}, "title"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := paramsFixture(t, model.ExposeParamsAll)
			before := paramState(t, st, "wf-b1")
			_, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", c.patch, now)
			wantBranchErr(t, err, ErrInvalidInput, c.want)
			if after := paramState(t, st, "wf-b1"); !reflect.DeepEqual(before, after) {
				t.Fatalf("取值不合法时不应写入: %+v → %+v", before, after)
			}
		})
	}
	t.Run("旧阶段编号会换算", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		res, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", SessionParamsPatch{Phase: strp("assess")}, now)
		if err != nil {
			t.Fatal(err)
		}
		if got := paramState(t, st, "wf-b1").Phase; got != model.PhaseDiscuss {
			t.Fatalf("assess 应换算成 discuss，得到 %q", got)
		}
		if len(res.Applied) != 0 || len(res.Unchanged) != 1 { // 默认阶段本来就是 discuss
			t.Fatalf("换算后与现状一致，应是「没变」: %+v", res)
		}
	})
}

func TestParseSessionParamsPatch(t *testing.T) {
	t.Run("空", func(t *testing.T) {
		_, err := ParseSessionParamsPatch(nil)
		wantBranchErr(t, err, ErrInvalidInput, "patch 不能为空")
		_, err = ParseSessionParamsPatch(map[string]any{})
		wantBranchErr(t, err, ErrInvalidInput, "patch 不能为空")
	})
	t.Run("合法的六个键", func(t *testing.T) {
		p, err := ParseSessionParamsPatch(map[string]any{
			"resident": "r", "phase": "dev", "wake_resume": "wr", "wake_new": "wn %workflow", "title": "t", "async": false,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := p.Fields(), model.BranchParamFields; !reflect.DeepEqual(got, want) {
			t.Fatalf("Fields 顺序应固定: %v", got)
		}
		if *p.Resident != "r" || *p.Phase != "dev" || *p.WakeResume != "wr" || *p.WakeNew != "wn %workflow" || *p.Title != "t" || *p.Async {
			t.Fatalf("取值没带对: %+v", p)
		}
	})
	t.Run("键名宽容大小写、连字符与空格", func(t *testing.T) {
		p, err := ParseSessionParamsPatch(map[string]any{"Wake-Resume": "x", " TITLE ": "y"})
		if err != nil || p.WakeResume == nil || p.Title == nil {
			t.Fatalf("应识别: %+v %v", p, err)
		}
	})
	t.Run("async 的各种写法", func(t *testing.T) {
		for in, want := range map[any]bool{true: true, false: false, "on": true, "OFF": false, "true": true, " false ": false} {
			p, err := ParseSessionParamsPatch(map[string]any{"async": in})
			if err != nil || p.Async == nil || *p.Async != want {
				t.Fatalf("async=%v 应解析成 %v: %+v %v", in, want, p, err)
			}
		}
		for _, bad := range []any{"maybe", 1, nil, []string{"on"}} {
			_, err := ParseSessionParamsPatch(map[string]any{"async": bad})
			wantBranchErr(t, err, ErrInvalidInput, "async")
		}
	})
	t.Run("类型错", func(t *testing.T) {
		for _, key := range []string{"resident", "phase", "wake_resume", "wake_new", "title"} {
			for _, bad := range []any{1, true, nil, map[string]any{}} {
				_, err := ParseSessionParamsPatch(map[string]any{key: bad})
				wantBranchErr(t, err, ErrInvalidInput, key+" 必须是字符串")
			}
		}
	})
	t.Run("永不开放的键被点名拒绝", func(t *testing.T) {
		for _, key := range []string{
			"profile", "profile_id", "Profile-ID", "auth_profile", "authorization", "credential", "credentials", "token", "secret", "password",
			"directory", "project_directory", "dir", "host", "host_id", "host_name", "terminal", "terminal_exec",
			"launch", "expose", "expose_preset", "params", "parent", "parent_workflow_id", "workflow_id", "strict_wrapup",
		} {
			_, err := ParseSessionParamsPatch(map[string]any{"title": "好标题", key: "x"})
			wantBranchErr(t, err, ErrForbidden, "永不对主会话开放")
		}
	})
	t.Run("不认识的键列出可用的", func(t *testing.T) {
		for _, key := range []string{"wait", "wait_minutes", "foo", "status"} {
			_, err := ParseSessionParamsPatch(map[string]any{key: "x"})
			wantBranchErr(t, err, ErrInvalidInput, "session_params 只认")
		}
	})
}

func TestSessionParams_WritesEventsAndAudit(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := paramsFixture(t, model.ExposeParamsAll)

	patch := SessionParamsPatch{
		Resident: strp("规则：先写测试"), Phase: strp("dev"), WakeResume: strp("请继续"), WakeNew: strp("接入 %workflow"),
		Title: strp("新标题"),
	}
	res, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", patch, now)
	if err != nil {
		t.Fatal(err)
	}
	// 阶段默认就是 discuss，改成 dev 是真变化；五项都应写入。
	if got := len(res.Applied); got != 5 {
		t.Fatalf("应写入 5 项，得到 %d: %+v", got, res.Applied)
	}
	order := []string{model.ParamFieldResident, model.ParamFieldPhase, model.ParamFieldWakeResume, model.ParamFieldWakeNew, model.ParamFieldTitle}
	for i, ev := range res.Applied {
		if ev.Field != order[i] || ev.ID == 0 || ev.By != "agent:wf-main" || ev.Summary == "" ||
			ev.ParentWorkflowID != "wf-main" || ev.BranchWorkflowID != "wf-b1" || ev.ToldAt != nil || ev.RevertedAt != nil {
			t.Fatalf("事件 %d 不对: %+v", i, ev)
		}
	}
	// 底层各字段都真写进去了。
	v := paramState(t, st, "wf-b1")
	if v.Resident.Text != "规则：先写测试" || v.Resident.Rev != 1 || !strings.HasPrefix(v.Resident.UpdatedBy, "agent:wf-main") ||
		v.Phase != model.PhaseDev || v.WakeResume != "请继续" || v.WakeNew != "接入 %workflow" || v.Title != "新标题" {
		t.Fatalf("字段没写对: %+v", v)
	}
	if res.Applied[0].Rev != 1 || res.Applied[0].OldValue != "" || res.Applied[0].NewValue != "规则：先写测试" {
		t.Fatalf("常驻规则事件要带版本号与旧 / 新值: %+v", res.Applied[0])
	}
	if res.Applied[1].OldValue != model.PhaseDiscuss || res.Applied[1].NewValue != model.PhaseDev || !strings.Contains(res.Applied[1].Summary, "讨论 → 开发") {
		t.Fatalf("阶段事件不对: %+v", res.Applied[1])
	}
	if res.Applied[4].OldValue != "分支一" || res.Applied[4].NewValue != "新标题" {
		t.Fatalf("标题事件不对: %+v", res.Applied[4])
	}
	// 摘要不含常驻规则与唤醒词全文。
	for _, ev := range res.Applied[:4] {
		if strings.Contains(ev.Summary, "先写测试") || strings.Contains(ev.Summary, "请继续") || strings.Contains(ev.Summary, "接入") {
			t.Fatalf("摘要不能含正文: %q", ev.Summary)
		}
	}
	// 审计：每个字段一条，主体是主会话，细节不含正文。
	audits, err := st.ListAuditEvents(ctx, AuditFilter{Action: AuditBranchParamsSet, Limit: 50})
	if err != nil || len(audits) != 5 {
		t.Fatalf("应有 5 条 branch.params_set 审计: %d %v", len(audits), err)
	}
	for _, a := range audits {
		if a.ActorRef != "workflow:wf-main" || a.ScopeRef != "wf-main" || a.Target != "wf-b1" {
			t.Fatalf("审计的主体 / 范围 / 目标不对: %+v", a)
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(a.Detail), &d); err != nil || d["field"] == nil || d["event_id"] == nil {
			t.Fatalf("审计细节要有字段与事件编号: %q %v", a.Detail, err)
		}
		if strings.Contains(a.Detail, "先写测试") {
			t.Fatalf("审计不能含常驻规则正文: %s", a.Detail)
		}
	}

	// 同样的 patch 再来一遍：全是「没变」，不写事件、不涨版本、不占额度。
	again, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", patch, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Applied) != 0 || len(again.Unchanged) != 5 {
		t.Fatalf("重复写入应全是「没变」: %+v", again)
	}
	if n := countParamEvents(t, st, "wf-b1"); n != 5 {
		t.Fatalf("没变的字段不应再有事件，得到 %d", n)
	}
	if got := paramState(t, st, "wf-b1").Resident.Rev; got != 1 {
		t.Fatalf("没变不应涨版本，得到 rev %d", got)
	}
}

func TestSessionParams_Async(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	modeRow := func(t *testing.T, st *Store) model.WorkflowMode {
		t.Helper()
		var row model.WorkflowMode
		if err := st.DB().Where("workflow_id = ?", "wf-b1").Take(&row).Error; err != nil {
			return model.WorkflowMode{WorkflowID: "wf-b1"}
		}
		return row
	}

	t.Run("从没被用户开启过：主会话开不了，关是没变", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		st.RecordHookSeen("wf-b1", now) // 即使 hook 在，第一次开启也只有用户能做
		_, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", SessionParamsPatch{Async: boolp(true)}, now)
		wantBranchErr(t, err, ErrForbidden, "只有用户本人能做")
		if row := modeRow(t, st); row.AsyncEnabled || row.EverEnabled {
			t.Fatalf("被拒绝时不应改动: %+v", row)
		}
		res, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", SessionParamsPatch{Async: boolp(false)}, now)
		if err != nil || len(res.Applied) != 0 || !reflect.DeepEqual(res.Unchanged, []string{model.ParamFieldAsync}) {
			t.Fatalf("本来就是关，写 false 应是没变: %+v %v", res, err)
		}
	})

	t.Run("用户开启过：主会话能关，也能恢复；授权档位不动", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		st.RecordHookSeen("wf-b1", now)
		if _, err := st.UpdateWorkflowMode(ctx, "wf-b1", ModePatch{Enabled: boolp(true)}, now); err != nil {
			t.Fatal(err)
		}
		profile := modeRow(t, st).ProfileID

		res, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", SessionParamsPatch{Async: boolp(false)}, now)
		if err != nil || len(res.Applied) != 1 {
			t.Fatalf("主会话应能关掉: %+v %v", res, err)
		}
		if ev := res.Applied[0]; ev.OldValue != "on" || ev.NewValue != "off" || !strings.Contains(ev.Summary, "已关闭") {
			t.Fatalf("事件不对: %+v", ev)
		}
		if row := modeRow(t, st); row.AsyncEnabled || !row.EverEnabled {
			t.Fatalf("应是关闭但仍记着开启过: %+v", row)
		}

		res, err = st.ApplySessionParams(ctx, "wf-main", "wf-b1", SessionParamsPatch{Async: boolp(true)}, now)
		if err != nil || len(res.Applied) != 1 {
			t.Fatalf("开启过的应能恢复: %+v %v", res, err)
		}
		if ev := res.Applied[0]; ev.OldValue != "off" || ev.NewValue != "on" || !strings.Contains(ev.Summary, "已恢复开启") {
			t.Fatalf("事件不对: %+v", ev)
		}
		if row := modeRow(t, st); !row.AsyncEnabled || row.ProfileID != profile {
			t.Fatalf("应恢复开启且授权档位不变: %+v（档位原为 %d）", row, profile)
		}
	})

	t.Run("开启过但没见过 hook：恢复被拒（冲突）", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		if err := st.DB().Create(&model.WorkflowMode{WorkflowID: "wf-b1", EverEnabled: true}).Error; err != nil {
			t.Fatal(err)
		}
		_, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", SessionParamsPatch{Async: boolp(true)}, now)
		wantBranchErr(t, err, ErrConflict, "hook")
		if row := modeRow(t, st); row.AsyncEnabled {
			t.Fatalf("被拒绝时不应开启: %+v", row)
		}
	})
}

func TestSessionParams_RateLimit(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	limit := model.BranchParamRateLimit
	st := paramsFixture(t, model.ExposeParamsAll)
	set := func(i int, at time.Time) error {
		_, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", SessionParamsPatch{Resident: strp(fmt.Sprintf("规则 %d", i))}, at)
		return err
	}
	for i := 0; i < limit-1; i++ {
		if err := set(i, now); err != nil {
			t.Fatalf("第 %d 次应通过: %v", i+1, err)
		}
	}
	// 只剩 1 个额度，一次写 2 项：整条拒绝，一个字段也不写。
	before := paramState(t, st, "wf-b1")
	_, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", SessionParamsPatch{Resident: strp("超额甲"), Title: strp("超额标题")}, now)
	wantBranchErr(t, err, ErrConflict, "上限")
	if after := paramState(t, st, "wf-b1"); !reflect.DeepEqual(before, after) {
		t.Fatalf("超额整条拒绝，不能写入一半: %+v → %+v", before, after)
	}
	// 没变的写入不占额度，也不会被限频挡住。
	if _, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", SessionParamsPatch{Resident: strp(before.Resident.Text)}, now); err != nil {
		t.Fatalf("没变的写入不应被限频挡住: %v", err)
	}
	if err := set(limit-1, now); err != nil { // 第 limit 次
		t.Fatalf("最后一个额度应能用: %v", err)
	}
	wantBranchErr(t, set(limit, now), ErrConflict, "上限")
	// 读取不受影响。
	if _, err := st.GetSessionParams(ctx, "wf-main", "wf-b1", now); err != nil {
		t.Fatalf("限频不影响读取: %v", err)
	}
	// 用户撤销（by=human）不占额度：撤销最新的一条，之后窗口里主会话写入数没变，仍然超额。
	evs, _ := st.ListBranchParamEvents(ctx, "wf-b1", 1)
	if _, err := st.RevertBranchParamEvent(ctx, "wf-b1", evs[0].ID, "alice", now); err != nil {
		t.Fatalf("撤销: %v", err)
	}
	wantBranchErr(t, set(limit+1, now), ErrConflict, "上限")
	// 一小时之后窗口滑走，恢复正常。
	if err := set(limit+2, now.Add(61*time.Minute)); err != nil {
		t.Fatalf("一小时后应恢复: %v", err)
	}
	// 限额按分支分开算。
	mkWF(t, st, "wf-b2", "分支二")
	mustLink(t, st, "wf-main", "wf-b2", model.BranchCreatedByHuman)
	setParamsTier(t, st, "wf-b2", model.ExposeParamsAll)
	if _, err := st.ApplySessionParams(ctx, "wf-main", "wf-b2", SessionParamsPatch{Resident: strp("别的分支")}, now); err != nil {
		t.Fatalf("另一个分支有自己的额度: %v", err)
	}
}

func TestSessionParams_GetView(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	t.Run("off 档：主会话只拿到开放范围，没有参数值", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsOff)
		if _, _, err := st.SaveResidentPrompt(ctx, "wf-b1", "用户写给分支的私货", "alice", now); err != nil {
			t.Fatal(err)
		}
		v, err := st.GetSessionParams(ctx, "wf-main", "wf-b1", now)
		if err != nil {
			t.Fatal(err)
		}
		if v.Level != model.ExposeParamsOff || v.Role != BranchRoleMain || v.Values != nil || len(v.Editable) != 0 || v.Branch != "wf-b1" || v.Parent != "wf-main" {
			t.Fatalf("off 档不能返回值: %+v", v)
		}
		if b, _ := json.Marshal(v); strings.Contains(string(b), "私货") {
			t.Fatalf("off 档的序列化结果也不能含常驻规则: %s", b)
		}
	})
	t.Run("各档的可改字段与值", func(t *testing.T) {
		want := map[string][]string{
			model.ExposeParamsResident:      {model.ParamFieldResident},
			model.ExposeParamsResidentPhase: {model.ParamFieldResident, model.ParamFieldPhase},
			model.ExposeParamsAll:           model.BranchParamFields,
		}
		for level, editable := range want {
			st := paramsFixture(t, level)
			if _, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", SessionParamsPatch{Resident: strp("规则甲")}, now); err != nil {
				t.Fatal(err)
			}
			v, err := st.GetSessionParams(ctx, "wf-main", "wf-b1", now)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(v.Editable, editable) || v.Values == nil || v.Values.Resident.Text != "规则甲" || v.Values.Resident.Rev != 1 ||
				v.Values.Phase != model.PhaseDiscuss || v.Values.Title != "分支一" || v.Values.Async.Enabled || v.Values.Async.EverEnabled {
				t.Fatalf("%s 档: %+v / %+v", level, v, v.Values)
			}
		}
	})
	t.Run("分支本人读自己的：恒有值、没有可改字段", func(t *testing.T) {
		for _, level := range []string{model.ExposeParamsOff, model.ExposeParamsAll} {
			st := paramsFixture(t, level)
			v, err := st.GetSessionParams(ctx, "wf-b1", "wf-b1", now)
			if err != nil {
				t.Fatal(err)
			}
			if v.Role != BranchRoleBranch || v.Values == nil || len(v.Editable) != 0 || v.Level != level {
				t.Fatalf("%s 档，分支本人: %+v", level, v)
			}
		}
	})
	t.Run("序列化里没有授权档位与凭据", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		v, _ := st.GetSessionParams(ctx, "wf-main", "wf-b1", now)
		b, _ := json.Marshal(v)
		for _, bad := range []string{"profile", "credential", "token", "directory", "host"} {
			if strings.Contains(strings.ToLower(string(b)), bad) {
				t.Fatalf("get 的结果里不该出现 %q: %s", bad, b)
			}
		}
	})
}

func TestResidentWriter_FollowsTheResidentRevisionNotUpdatedBy(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := paramsFixture(t, model.ExposeParamsAll)
	if got := st.ResidentWriter(ctx, "wf-b1"); got != "" {
		t.Fatalf("没配置过应为空: %q", got)
	}
	if _, _, err := st.SaveResidentPrompt(ctx, "wf-b1", "用户写的规则", "alice", now); err != nil {
		t.Fatal(err)
	}
	if got := st.ResidentWriter(ctx, "wf-b1"); got != "alice" {
		t.Fatalf("应是 alice: %q", got)
	}
	// 主会话之后只改了唤醒词：常驻规则仍是用户写的，不能被误标成主会话写的。
	if _, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", SessionParamsPatch{WakeNew: strp("接入 %workflow")}, now); err != nil {
		t.Fatal(err)
	}
	if got := st.ResidentWriter(ctx, "wf-b1"); got != "alice" {
		t.Fatalf("改唤醒词不应改变常驻规则的写入者: %q", got)
	}
	pr, _ := st.GetWorkflowPrompt(ctx, "wf-b1")
	if !strings.HasPrefix(pr.UpdatedBy, "agent:") {
		t.Fatalf("前提：updated_by 确实被唤醒词的保存覆盖成了主会话（这正是不用它的原因）: %q", pr.UpdatedBy)
	}
	// 主会话改常驻规则：写入者变成主会话；用户再改：又变回用户。
	if _, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", SessionParamsPatch{Resident: strp("主会话写的规则")}, now); err != nil {
		t.Fatal(err)
	}
	if got := st.ResidentWriter(ctx, "wf-b1"); got != "agent:wf-main" {
		t.Fatalf("应是主会话: %q", got)
	}
	if _, _, err := st.SaveResidentPrompt(ctx, "wf-b1", "用户又改了", "alice", now); err != nil {
		t.Fatal(err)
	}
	if got := st.ResidentWriter(ctx, "wf-b1"); got != "alice" {
		t.Fatalf("用户再改应变回用户: %q", got)
	}
}

func TestRevertBranchParamEvent(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	apply := func(t *testing.T, st *Store, p SessionParamsPatch) []model.BranchParamEvent {
		t.Helper()
		res, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", p, now)
		if err != nil {
			t.Fatal(err)
		}
		return res.Applied
	}

	t.Run("常驻规则：存成新版本，写入者变回撤销的人", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		if _, _, err := st.SaveResidentPrompt(ctx, "wf-b1", "规则一", "alice", now); err != nil {
			t.Fatal(err)
		}
		evs := apply(t, st, SessionParamsPatch{Resident: strp("规则二")})
		out, err := st.RevertBranchParamEvent(ctx, "wf-b1", evs[0].ID, "alice", now)
		if err != nil {
			t.Fatal(err)
		}
		v := paramState(t, st, "wf-b1")
		if v.Resident.Text != "规则一" || v.Resident.Rev != 3 {
			t.Fatalf("撤销后应回到规则一并存成第 3 版（历史不改写）: %+v", v.Resident)
		}
		if got := st.ResidentWriter(ctx, "wf-b1"); got != "alice" {
			t.Fatalf("撤销后常驻规则是用户（撤销者）写的: %q", got)
		}
		if out.Reverted.RevertedAt == nil || out.Reverted.RevertedBy != "alice" || out.Reverted.ID != evs[0].ID {
			t.Fatalf("原事件应带撤销标记: %+v", out.Reverted)
		}
		ev := out.Event
		if ev.By != model.BranchParamByHuman || ev.RevertOf != evs[0].ID || ev.Field != model.ParamFieldResident || ev.Rev != 3 ||
			ev.OldValue != "规则二" || ev.NewValue != "规则一" || !strings.Contains(ev.Summary, "用户撤销了主会话对「常驻规则」的修改") || ev.ToldAt != nil {
			t.Fatalf("撤销事件不对: %+v", ev)
		}
		audits, _ := st.ListAuditEvents(ctx, AuditFilter{Action: AuditBranchParamsRevert})
		if len(audits) != 1 || audits[0].ActorKind != "human" || audits[0].Target != "wf-b1" {
			t.Fatalf("应有一条 human 的撤销审计: %+v", audits)
		}
		// 之后分支下一次回执要告诉它：未告知里同时有主会话的写入与用户的撤销。
		untold, _ := st.UntoldBranchParamEvents(ctx, "wf-b1")
		if len(untold) != 2 || untold[0].ID != evs[0].ID || untold[1].RevertOf != evs[0].ID {
			t.Fatalf("未告知应是 [写入, 撤销]: %+v", untold)
		}
	})

	t.Run("标题、阶段、唤醒词", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		evs := apply(t, st, SessionParamsPatch{Phase: strp("plan"), WakeResume: strp("续一下"), WakeNew: strp("接入 %workflow"), Title: strp("新标题")})
		for _, ev := range evs {
			if _, err := st.RevertBranchParamEvent(ctx, "wf-b1", ev.ID, "alice", now); err != nil {
				t.Fatalf("撤销 %s: %v", ev.Field, err)
			}
		}
		v := paramState(t, st, "wf-b1")
		if v.Phase != model.PhaseDiscuss || v.WakeResume != "" || v.WakeNew != "" || v.Title != "分支一" {
			t.Fatalf("应全部回到原值: %+v", v)
		}
	})

	t.Run("异步：关掉的恢复、恢复的再关", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		st.RecordHookSeen("wf-b1", now)
		if _, err := st.UpdateWorkflowMode(ctx, "wf-b1", ModePatch{Enabled: boolp(true)}, now); err != nil {
			t.Fatal(err)
		}
		evs := apply(t, st, SessionParamsPatch{Async: boolp(false)})
		if _, err := st.RevertBranchParamEvent(ctx, "wf-b1", evs[0].ID, "alice", now); err != nil {
			t.Fatal(err)
		}
		if !paramState(t, st, "wf-b1").Async.Enabled {
			t.Fatal("撤销「关闭」应恢复开启")
		}
	})

	t.Run("冲突：已经撤销过 / 撤销记录 / 不是主会话写的 / 不存在 / 别的分支", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		mkWF(t, st, "wf-b2", "分支二")
		mustLink(t, st, "wf-main", "wf-b2", model.BranchCreatedByHuman)
		evs := apply(t, st, SessionParamsPatch{Title: strp("甲")})
		out, err := st.RevertBranchParamEvent(ctx, "wf-b1", evs[0].ID, "alice", now)
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.RevertBranchParamEvent(ctx, "wf-b1", evs[0].ID, "alice", now)
		wantBranchErr(t, err, ErrConflict, "已经撤销过")
		_, err = st.RevertBranchParamEvent(ctx, "wf-b1", out.Event.ID, "alice", now)
		wantBranchErr(t, err, ErrInvalidInput, "撤销记录")
		human := model.BranchParamEvent{CreatedAt: now, ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-b1", Field: model.ParamFieldTitle,
			By: model.BranchParamByHuman, Summary: "手写的", OldValue: "a", NewValue: "b"}
		if err := st.DB().Create(&human).Error; err != nil {
			t.Fatal(err)
		}
		_, err = st.RevertBranchParamEvent(ctx, "wf-b1", human.ID, "alice", now)
		wantBranchErr(t, err, ErrInvalidInput, "只能撤销主会话写入")
		_, err = st.RevertBranchParamEvent(ctx, "wf-b1", 99999, "alice", now)
		wantBranchErr(t, err, ErrNotFound, "")
		other := apply(t, st, SessionParamsPatch{Resident: strp("规则")})
		_, err = st.RevertBranchParamEvent(ctx, "wf-b2", other[0].ID, "alice", now)
		wantBranchErr(t, err, ErrNotFound, "")
	})

	t.Run("冲突：用户在网页上又改过", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		evs := apply(t, st, SessionParamsPatch{Resident: strp("主会话写的")})
		if _, _, err := st.SaveResidentPrompt(ctx, "wf-b1", "用户随后又改了", "alice", now); err != nil {
			t.Fatal(err)
		}
		_, err := st.RevertBranchParamEvent(ctx, "wf-b1", evs[0].ID, "alice", now)
		wantBranchErr(t, err, ErrConflict, "之后又被改过")
		if got := paramState(t, st, "wf-b1").Resident.Text; got != "用户随后又改了" {
			t.Fatalf("冲突时不能覆盖用户的修改: %q", got)
		}
		// 标题同理（用户重命名）。
		tev := apply(t, st, SessionParamsPatch{Title: strp("主会话改的标题")})
		if _, err := st.RenameFeedbackSession(ctx, "wf-b1", "用户改的标题"); err != nil {
			t.Fatal(err)
		}
		_, err = st.RevertBranchParamEvent(ctx, "wf-b1", tev[0].ID, "alice", now)
		wantBranchErr(t, err, ErrConflict, "之后又被改过")
	})

	t.Run("冲突：主会话之后又改过同一字段，只能从最新的撤起", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		first := apply(t, st, SessionParamsPatch{Resident: strp("规则甲")})
		second := apply(t, st, SessionParamsPatch{Resident: strp("规则乙")})
		_, err := st.RevertBranchParamEvent(ctx, "wf-b1", first[0].ID, "alice", now)
		wantBranchErr(t, err, ErrConflict, "不是最新的修改")
		if _, err := st.RevertBranchParamEvent(ctx, "wf-b1", second[0].ID, "alice", now); err != nil {
			t.Fatalf("最新的应能撤销: %v", err)
		}
		if got := paramState(t, st, "wf-b1").Resident.Text; got != "规则甲" {
			t.Fatalf("撤销最新的之后应回到规则甲: %q", got)
		}
		// 最新的撤销之后，前一条成为最新一条未撤销的修改，当前值也正好是它写入的值，可以继续撤。
		if _, err := st.RevertBranchParamEvent(ctx, "wf-b1", first[0].ID, "alice", now); err != nil {
			t.Fatalf("撤销链应能继续: %v", err)
		}
		if got := paramState(t, st, "wf-b1").Resident.Text; got != "" {
			t.Fatalf("两条都撤销后应回到最初（空）: %q", got)
		}
		// 不同字段互不影响。
		a := apply(t, st, SessionParamsPatch{Title: strp("标题甲")})
		apply(t, st, SessionParamsPatch{Resident: strp("规则丙")})
		if _, err := st.RevertBranchParamEvent(ctx, "wf-b1", a[0].ID, "alice", now); err != nil {
			t.Fatalf("别的字段的新修改不应挡住这一条: %v", err)
		}
	})

	t.Run("关系结束后仍可撤销", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		evs := apply(t, st, SessionParamsPatch{Resident: strp("规则甲")})
		if _, err := st.CloseBranch(ctx, "wf-b1", model.BranchCreatedByHuman); err != nil {
			t.Fatal(err)
		}
		if _, err := st.RevertBranchParamEvent(ctx, "wf-b1", evs[0].ID, "alice", now); err != nil {
			t.Fatalf("撤销只看当前值，与关系状态无关: %v", err)
		}
	})

	t.Run("原先没有标题：无法撤销成无标题", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		mkWF(t, st, "wf-b3", "")
		mustLink(t, st, "wf-main", "wf-b3", model.BranchCreatedByHuman)
		setParamsTier(t, st, "wf-b3", model.ExposeParamsAll)
		res, err := st.ApplySessionParams(ctx, "wf-main", "wf-b3", SessionParamsPatch{Title: strp("起个名")}, now)
		if err != nil || len(res.Applied) != 1 || res.Applied[0].OldValue != "" {
			t.Fatalf("前提: %+v %v", res, err)
		}
		_, err = st.RevertBranchParamEvent(ctx, "wf-b3", res.Applied[0].ID, "alice", now)
		wantBranchErr(t, err, ErrInvalidInput, "无标题")
		if got := st.WorkflowTitleOf(ctx, "wf-b3"); got != "起个名" {
			t.Fatalf("失败时不应改动: %q", got)
		}
	})

	t.Run("没带操作者按 human 记", func(t *testing.T) {
		st := paramsFixture(t, model.ExposeParamsAll)
		evs := apply(t, st, SessionParamsPatch{Phase: strp("dev")})
		out, err := st.RevertBranchParamEvent(ctx, "wf-b1", evs[0].ID, "  ", now)
		if err != nil || out.Reverted.RevertedBy != model.BranchParamByHuman {
			t.Fatalf("%+v %v", out, err)
		}
	})
}

func TestBranchParamEvents_ToldAndPrune(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := paramsFixture(t, model.ExposeParamsAll)
	var ids []uint
	for _, p := range []SessionParamsPatch{{Resident: strp("甲")}, {Phase: strp("dev")}, {Title: strp("乙")}} {
		res, err := st.ApplySessionParams(ctx, "wf-main", "wf-b1", p, now)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.Applied[0].ID)
	}
	untold, err := st.UntoldBranchParamEvents(ctx, "wf-b1")
	if err != nil || len(untold) != 3 || untold[0].ID != ids[0] || untold[2].ID != ids[2] {
		t.Fatalf("三条都未告知，旧的在前: %+v %v", untold, err)
	}
	if err := st.MarkBranchParamEventsTold(ctx, ids[:2], now); err != nil {
		t.Fatal(err)
	}
	untold, _ = st.UntoldBranchParamEvents(ctx, "wf-b1")
	if len(untold) != 1 || untold[0].ID != ids[2] {
		t.Fatalf("标记两条之后只剩最后一条: %+v", untold)
	}
	// 再标记一次：已告知的时间不改。
	later := now.Add(time.Hour)
	if err := st.MarkBranchParamEventsTold(ctx, ids, later); err != nil {
		t.Fatal(err)
	}
	first, _ := st.GetBranchParamEvent(ctx, "wf-b1", ids[0])
	last, _ := st.GetBranchParamEvent(ctx, "wf-b1", ids[2])
	if first.ToldAt == nil || first.ToldAt.After(now.Add(time.Minute)) {
		t.Fatalf("已告知的不应被改写时间: %+v", first.ToldAt)
	}
	if last.ToldAt == nil || last.ToldAt.Before(later.Add(-time.Minute)) {
		t.Fatalf("新标记的应是 later: %+v", last.ToldAt)
	}
	if err := st.MarkBranchParamEventsTold(ctx, nil, now); err != nil {
		t.Fatalf("空列表应是空操作: %v", err)
	}
	if untold, _ = st.UntoldBranchParamEvents(ctx, "wf-b1"); len(untold) != 0 {
		t.Fatalf("应全部已告知: %+v", untold)
	}

	// 列表：新的在前；limit 生效；别的分支的事件不混进来。
	list, _ := st.ListBranchParamEvents(ctx, "wf-b1", 2)
	if len(list) != 2 || list[0].ID != ids[2] || list[1].ID != ids[1] {
		t.Fatalf("新的在前、limit=2: %+v", list)
	}
	mkWF(t, st, "wf-b2", "分支二")
	for i := 0; i < model.BranchParamEventKeep+5; i++ {
		ev := model.BranchParamEvent{CreatedAt: now, ParentWorkflowID: "wf-main", BranchWorkflowID: "wf-b2", Field: model.ParamFieldResident,
			By: "agent:wf-main", Summary: fmt.Sprintf("第 %d 条", i)}
		if err := st.recordParamEvent(ctx, &ev); err != nil {
			t.Fatal(err)
		}
	}
	if n := countParamEvents(t, st, "wf-b2"); n != int64(model.BranchParamEventKeep) {
		t.Fatalf("每个分支只保留最近 %d 条，得到 %d", model.BranchParamEventKeep, n)
	}
	kept, _ := st.ListBranchParamEvents(ctx, "wf-b2", 0)
	if len(kept) != 50 || kept[0].Summary != fmt.Sprintf("第 %d 条", model.BranchParamEventKeep+4) {
		t.Fatalf("留下的应是最新的: 共 %d 条，首条 %q", len(kept), kept[0].Summary)
	}
	var oldest model.BranchParamEvent
	if err := st.DB().Where("branch_workflow_id = ?", "wf-b2").Order("id ASC").First(&oldest).Error; err != nil || oldest.Summary != "第 5 条" {
		t.Fatalf("最旧的 5 条应被删掉: %+v %v", oldest, err)
	}
	if n := countParamEvents(t, st, "wf-b1"); n != 3 {
		t.Fatalf("别的分支的裁剪不能碰 wf-b1 的事件: %d", n)
	}
	if got, _ := st.ListBranchParamEvents(ctx, "wf-b2", 100000); len(got) != model.BranchParamEventKeep {
		t.Fatalf("limit 上限应是 %d: %d", model.BranchParamEventKeep, len(got))
	}
}

func TestBranchAudit_RecordsFullExposeValues(t *testing.T) {
	ctx := context.Background()
	st := newBranchStore(t)
	mkWF(t, st, "wf-main", "主会话")
	mkWF(t, st, "wf-b1", "分支一")
	mustLink(t, st, "wf-main", "wf-b1", model.BranchCreatedByHuman)

	detail := func(action string) map[string]any {
		t.Helper()
		audits, err := st.ListAuditEvents(ctx, AuditFilter{Action: action, Limit: 1})
		if err != nil || len(audits) != 1 {
			t.Fatalf("%s 审计: %v %v", action, audits, err)
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(audits[0].Detail), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	link := detail(AuditBranchLink)
	if vals, _ := link["expose_values"].(map[string]any); vals["params"] != model.ExposeParamsResident || vals["launch"] != false {
		t.Fatalf("建立关系的审计应带完整开放范围（协作预设）: %v", link)
	}

	custom := model.BranchExpose{History: model.ExposeHistorySummary, Params: model.ExposeParamsResidentPhase, Tasks: model.ExposeTasksRead}
	if _, err := st.UpdateBranchSettings(ctx, "wf-b1", UpdateBranchInput{Expose: &custom}); err != nil {
		t.Fatal(err)
	}
	d := detail(AuditBranchSettings)
	if d["expose"] != "" {
		t.Fatalf("自定义取值没有预设名: %v", d["expose"])
	}
	vals, _ := d["expose_values"].(map[string]any)
	before, _ := d["expose_before"].(map[string]any)
	if vals["params"] != model.ExposeParamsResidentPhase || vals["history"] != model.ExposeHistorySummary ||
		before["params"] != model.ExposeParamsResident || before["history"] != model.ExposeHistoryFull {
		t.Fatalf("改设置的审计应带改前与改后的完整取值: %v", d)
	}
}
