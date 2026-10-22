package store

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/asyncmode"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
func uintPtr(u uint) *uint    { return &u }

func TestDefaultAuthProfileIsSeededOnceAndSurvivesReopen(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	list, err := st.ListAuthProfiles(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("新库应恰有一档默认档位: %v %v", list, err)
	}
	p := list[0]
	if p.Name != "标准授权" || !p.IsDefault || p.Level != model.AuthLevelL1 || p.Rules != defaultAuthProfileRules {
		t.Fatalf("默认档位内容不对: %+v", p)
	}
	// #217：出厂规则与回执契约一致——不可逆操作发 L2 确认卡、批准前不执行，不再写「阻塞确认」
	for _, must := range []string{"L2 确认卡", `risk:"L2"`, "批准前不执行", `kind:"plan"`} {
		if !strings.Contains(p.Rules, must) {
			t.Errorf("出厂规则缺少 %q: %s", must, p.Rules)
		}
	}
	if strings.Contains(p.Rules, "阻塞确认") || strings.Contains(p.Rules, "interactive_feedback") {
		t.Errorf("出厂规则不该再让智能体阻塞确认: %s", p.Rules)
	}
	if n := utf8.RuneCountInString(p.Rules); n > model.AuthProfileRulesMax {
		t.Errorf("出厂规则 %d 字，超过档位规则上限 %d", n, model.AuthProfileRulesMax)
	}
	if line := asyncmode.AuthLine(asyncmode.Profile{Name: p.Name, Level: p.Level, Rules: p.Rules}); strings.HasSuffix(line, "…") {
		t.Errorf("出厂规则放进 auth: 行会被截断: %s", line)
	}
	// 幂等：再种一次不产生第二档
	if err := st.SeedDefaultAuthProfile(ctx); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListAuthProfiles(ctx); len(list) != 1 {
		t.Fatalf("重复播种不应新增: %d", len(list))
	}
}

// #217：升级前建的库里，默认档位还是旧出厂文案（「先用 interactive_feedback 阻塞确认」），启动时要刷新成当前写法，
// 否则 auth: 行与回执契约里「L2 发确认卡」互相矛盾；用户自己写的规则（哪怕也提到阻塞确认）不动；刷新是幂等的。
func TestLegacyDefaultAuthProfileRulesAreRefreshedButCustomOnesAreKept(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	list, err := st.ListAuthProfiles(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("新库应恰有一档默认档位: %v %v", list, err)
	}
	def := list[0]
	if err := st.DB().Model(&model.AuthProfile{}).Where("id = ?", def.ID).Update("rules", legacyDefaultAuthProfileRules).Error; err != nil {
		t.Fatal(err)
	}
	const mine = "部署先用 interactive_feedback 阻塞确认，别的随意"
	custom, err := st.CreateAuthProfile(ctx, AuthProfileInput{Name: strPtr("我的档位"), Rules: strPtr(mine)})
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.refreshLegacyAuthProfileRules(ctx)
	if err != nil || n != 1 {
		t.Fatalf("应刷新恰好 1 档旧出厂文案: n=%d err=%v", n, err)
	}
	if got, _ := st.GetAuthProfile(ctx, def.ID); got.Rules != defaultAuthProfileRules {
		t.Fatalf("默认档位应刷新成当前出厂文案: %q", got.Rules)
	}
	if kept, _ := st.GetAuthProfile(ctx, custom.ID); kept.Rules != mine {
		t.Fatalf("用户自己写的规则不应被刷新: %q", kept.Rules)
	}
	if n, err := st.refreshLegacyAuthProfileRules(ctx); err != nil || n != 0 {
		t.Fatalf("刷新应是幂等的: n=%d err=%v", n, err)
	}
}

func TestAuthProfileCreateValidation(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	long := strings.Repeat("长", model.AuthProfileRulesMax+1)
	cases := []struct {
		name string
		in   AuthProfileInput
		kind ErrorKind
	}{
		{"名称为空", AuthProfileInput{Name: strPtr("  "), Rules: strPtr("规则")}, ErrInvalidInput},
		{"名称换行", AuthProfileInput{Name: strPtr("a\nb"), Rules: strPtr("规则")}, ErrInvalidInput},
		{"名称过长", AuthProfileInput{Name: strPtr(strings.Repeat("名", model.AuthProfileNameMax+1)), Rules: strPtr("规则")}, ErrInvalidInput},
		{"等级非法（没有 L2 档位）", AuthProfileInput{Name: strPtr("全权"), Level: strPtr("L2"), Rules: strPtr("规则")}, ErrInvalidInput},
		{"规则为空", AuthProfileInput{Name: strPtr("空规则")}, ErrInvalidInput},
		{"规则过长", AuthProfileInput{Name: strPtr("长规则"), Rules: strPtr(long)}, ErrInvalidInput},
		{"与默认档位重名", AuthProfileInput{Name: strPtr("标准授权"), Rules: strPtr("规则")}, ErrConflict},
	}
	for _, tc := range cases {
		_, err := st.CreateAuthProfile(ctx, tc.in)
		wantKind(t, err, tc.kind, tc.name)
	}
	// 合法：等级大小写不敏感、名称与规则会去首尾空白，默认等级 L1
	p, err := st.CreateAuthProfile(ctx, AuthProfileInput{Name: strPtr("  谨慎  "), Level: strPtr("l0"), Rules: strPtr("  所有写入先问  ")})
	if err != nil || p.Name != "谨慎" || p.Level != "L0" || p.Rules != "所有写入先问" || p.IsDefault {
		t.Fatalf("新建结果不对: %+v %v", p, err)
	}
	q, err := st.CreateAuthProfile(ctx, AuthProfileInput{Name: strPtr("宽松"), Rules: strPtr("多数事直接做")})
	if err != nil || q.Level != "L1" {
		t.Fatalf("默认等级应为 L1: %+v %v", q, err)
	}
}

func TestAuthProfileLimitUpdateDefaultAndDelete(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	// 总数上限
	for i := 1; i < model.AuthProfileMax; i++ {
		if _, err := st.CreateAuthProfile(ctx, AuthProfileInput{Name: strPtr(strings.Repeat("档", 1) + string(rune('A'+i))), Rules: strPtr("规则")}); err != nil {
			t.Fatalf("第 %d 档应能建成: %v", i+1, err)
		}
	}
	_, err := st.CreateAuthProfile(ctx, AuthProfileInput{Name: strPtr("超限"), Rules: strPtr("规则")})
	wantKind(t, err, ErrConflict, "超过档位上限")

	list, _ := st.ListAuthProfiles(ctx)
	def, other := list[0], list[1]
	if !def.IsDefault {
		t.Fatalf("第一档应是默认: %+v", def)
	}

	// 部分更新：只改给了的字段；改名后重名要冲突
	updated, err := st.UpdateAuthProfile(ctx, other.ID, AuthProfileInput{Rules: strPtr("新规则")})
	if err != nil || updated.Rules != "新规则" || updated.Name != other.Name || updated.Level != other.Level {
		t.Fatalf("部分更新不对: %+v %v", updated, err)
	}
	_, err = st.UpdateAuthProfile(ctx, other.ID, AuthProfileInput{Name: strPtr(def.Name)})
	wantKind(t, err, ErrConflict, "改成已有名称")
	_, err = st.UpdateAuthProfile(ctx, other.ID, AuthProfileInput{Level: strPtr("L9")})
	wantKind(t, err, ErrInvalidInput, "非法等级")
	_, err = st.UpdateAuthProfile(ctx, 9999, AuthProfileInput{Rules: strPtr("x")})
	wantKind(t, err, ErrNotFound, "更新不存在的档位")

	// 默认档位不能删；换了默认之后旧默认可以删
	wantKind(t, st.DeleteAuthProfile(ctx, def.ID), ErrConflict, "删除默认档位")
	if _, err := st.SetDefaultAuthProfile(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	defNow, _ := st.DefaultAuthProfile(ctx)
	if defNow.ID != other.ID {
		t.Fatalf("默认档位应已换成 %d，实际 %d", other.ID, defNow.ID)
	}
	var defaults int64
	st.db.Model(&model.AuthProfile{}).Where("is_default = ?", true).Count(&defaults)
	if defaults != 1 {
		t.Fatalf("有且只有一档默认，实际 %d", defaults)
	}
	if err := st.DeleteAuthProfile(ctx, def.ID); err != nil {
		t.Fatalf("旧默认档位现在应能删: %v", err)
	}
	wantKind(t, st.DeleteAuthProfile(ctx, def.ID), ErrNotFound, "重复删除")
	_, err = st.SetDefaultAuthProfile(ctx, 9999)
	wantKind(t, err, ErrNotFound, "把不存在的档位设为默认")
}

func TestDeletingAProfileFallsWorkflowsBackToDefault(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	now := time.Now()
	prof, err := st.CreateAuthProfile(ctx, AuthProfileInput{Name: strPtr("谨慎"), Level: strPtr("L0"), Rules: strPtr("写入先问")})
	if err != nil {
		t.Fatal(err)
	}
	st.RecordHookSeen("wf-del", now)
	if _, err := st.UpdateWorkflowMode(ctx, "wf-del", ModePatch{Enabled: boolPtr(true), ProfileID: uintPtr(prof.ID)}, now); err != nil {
		t.Fatal(err)
	}
	info, _ := st.WorkflowModeInfo(ctx, "wf-del", now)
	if info.Profile == nil || info.Profile.ID != prof.ID {
		t.Fatalf("应选用刚建的档位: %+v", info.Profile)
	}
	if err := st.DeleteAuthProfile(ctx, prof.ID); err != nil {
		t.Fatal(err)
	}
	info, _ = st.WorkflowModeInfo(ctx, "wf-del", now)
	def, _ := st.DefaultAuthProfile(ctx)
	if info.Row.ProfileID != 0 || info.Profile == nil || info.Profile.ID != def.ID {
		t.Fatalf("档位被删后工作流应回到默认档位: row=%+v profile=%+v", info.Row, info.Profile)
	}
}

func TestResolveAuthProfileFallsBackToDefault(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	def, _ := st.DefaultAuthProfile(ctx)
	for _, id := range []uint{0, 424242} {
		p, err := st.ResolveAuthProfile(ctx, id)
		if err != nil || p.ID != def.ID {
			t.Fatalf("档位 %d 应回退默认: %+v %v", id, p, err)
		}
	}
}

func TestNeverEnabledWorkflowCostsNoProfileLookupAndIsSync(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	now := time.Now()
	st.RecordHookSeen("wf-plain", now) // hook 在线，但没人开过异步
	info, err := st.WorkflowModeInfo(ctx, "wf-plain", now)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Ever || info.State.Effective || info.State.Reason != asyncmode.ReasonNone || info.Profile != nil {
		t.Fatalf("从未开启的工作流应是纯同步、无档位查询: %+v", info)
	}
	if st.IsAsyncEffective(ctx, "wf-plain", now) {
		t.Fatalf("不应有效")
	}
	// 空工作流 id 不查库也不出错
	if info, err := st.WorkflowModeInfo(ctx, "  ", now); err != nil || info.State.Ever {
		t.Fatalf("空 id: %+v %v", info, err)
	}
}

func TestEnableRequiresHookThenFollowsHookFreshness(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	now := time.Now()

	// 没见过 hook：不允许开启（环境不允许），且不留下任何行
	_, err := st.UpdateWorkflowMode(ctx, "wf-a", ModePatch{Enabled: boolPtr(true)}, now)
	wantKind(t, err, ErrConflict, "没装 hook 时开启")
	var rows int64
	st.db.Model(&model.WorkflowMode{}).Count(&rows)
	if rows != 0 {
		t.Fatalf("被拒绝的开启不应落库: %d", rows)
	}

	// 见过 hook：开启成功，生效；EnabledAt 与 EverEnabled 落库
	st.RecordHookSeen("wf-a", now)
	info, err := st.UpdateWorkflowMode(ctx, "wf-a", ModePatch{Enabled: boolPtr(true)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !info.State.Effective || !info.Row.AsyncEnabled || !info.Row.EverEnabled || info.Row.EnabledAt == nil || info.Profile == nil {
		t.Fatalf("开启后应生效: %+v", info)
	}
	firstEnabled := *info.Row.EnabledAt

	// 重复开启是无操作：EnabledAt 不变
	later := now.Add(time.Minute)
	info, _ = st.UpdateWorkflowMode(ctx, "wf-a", ModePatch{Enabled: boolPtr(true)}, later)
	if !info.Row.EnabledAt.Equal(firstEnabled) {
		t.Fatalf("重复开启不应刷新开启时间: %v vs %v", info.Row.EnabledAt, firstEnabled)
	}

	// hook 过期 → 降级，但用户开关仍开着
	stale := now.Add(asyncmode.HookFreshness + time.Minute)
	info, _ = st.WorkflowModeInfo(ctx, "wf-a", stale)
	if info.State.Effective || !info.State.Enabled || info.State.Reason != asyncmode.ReasonHookStale {
		t.Fatalf("hook 过期应降级: %+v", info.State)
	}
	if st.IsAsyncEffective(ctx, "wf-a", stale) {
		t.Fatalf("降级后不应有效")
	}
	// hook 回来 → 自动恢复
	st.RecordHookSeen("wf-a", stale)
	if !st.IsAsyncEffective(ctx, "wf-a", stale) {
		t.Fatalf("hook 回来后应自动恢复")
	}

	// 一键关闭：回到同步，原因 disabled，EverEnabled 保留（回执仍会有 mode: sync 一行）
	info, err = st.UpdateWorkflowMode(ctx, "wf-a", ModePatch{Enabled: boolPtr(false)}, stale)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Effective || info.State.Enabled || !info.State.Ever || info.State.Reason != asyncmode.ReasonDisabled || info.Profile == nil {
		t.Fatalf("关闭后的状态不对: %+v", info.State)
	}
	// 关闭永远允许，哪怕 hook 早就没了
	if _, err := st.UpdateWorkflowMode(ctx, "wf-never-seen", ModePatch{Enabled: boolPtr(false)}, now); err != nil {
		t.Fatalf("关闭不应要求 hook: %v", err)
	}

	// 重新开启：EnabledAt 更新为这一次
	reopen := stale.Add(time.Minute)
	st.RecordHookSeen("wf-a", reopen)
	info, err = st.UpdateWorkflowMode(ctx, "wf-a", ModePatch{Enabled: boolPtr(true)}, reopen)
	if err != nil || !info.Row.EnabledAt.Equal(reopen) {
		t.Fatalf("重新开启应刷新 EnabledAt: %+v %v", info.Row.EnabledAt, err)
	}
}

func TestUpdateWorkflowModeProfileSelection(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	now := time.Now()
	prof, _ := st.CreateAuthProfile(ctx, AuthProfileInput{Name: strPtr("谨慎"), Level: strPtr("L0"), Rules: strPtr("写入先问")})

	// 先选档位、还没开启：允许（不需要 hook），也不算开启过
	info, err := st.UpdateWorkflowMode(ctx, "wf-p", ModePatch{ProfileID: uintPtr(prof.ID)}, now)
	if err != nil || info.Row.ProfileID != prof.ID || info.State.Ever {
		t.Fatalf("只选档位不应算开启: %+v %v", info, err)
	}
	// 不存在的档位
	_, err = st.UpdateWorkflowMode(ctx, "wf-p", ModePatch{ProfileID: uintPtr(9999)}, now)
	wantKind(t, err, ErrNotFound, "选择不存在的档位")
	// 0 = 跟随默认
	info, _ = st.UpdateWorkflowMode(ctx, "wf-p", ModePatch{ProfileID: uintPtr(0)}, now)
	if info.Row.ProfileID != 0 {
		t.Fatalf("0 应表示跟随默认: %+v", info.Row)
	}
	// 空 workflow_id
	_, err = st.UpdateWorkflowMode(ctx, " ", ModePatch{}, now)
	wantKind(t, err, ErrInvalidInput, "空 workflow_id")
}

func TestMarkModeToldRoundTrip(t *testing.T) {
	st := newConfirmStore(t)
	ctx := context.Background()
	now := time.Now()
	st.RecordHookSeen("wf-t", now)
	if _, err := st.UpdateWorkflowMode(ctx, "wf-t", ModePatch{Enabled: boolPtr(true)}, now); err != nil {
		t.Fatal(err)
	}
	info, _ := st.WorkflowModeInfo(ctx, "wf-t", now)
	if info.Row.ToldSig != "" {
		t.Fatalf("刚开启时还没告诉过智能体: %q", info.Row.ToldSig)
	}
	sig := info.Signature()
	if err := st.MarkModeTold(ctx, "wf-t", sig); err != nil {
		t.Fatal(err)
	}
	info, _ = st.WorkflowModeInfo(ctx, "wf-t", now)
	if info.Row.ToldSig != sig {
		t.Fatalf("告知签名应落库: %q vs %q", info.Row.ToldSig, sig)
	}
	// 没有行的工作流：无操作，不报错、不建行
	if err := st.MarkModeTold(ctx, "wf-nope", "x"); err != nil {
		t.Fatal(err)
	}
	var n int64
	st.db.Model(&model.WorkflowMode{}).Where("workflow_id = ?", "wf-nope").Count(&n)
	if n != 0 {
		t.Fatalf("不应为没开启过的工作流建行")
	}
}
