package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
)

// 唤醒词模板（#232 D3，D-376）：会话模板的存取、四层分层、「现状」变量现算。

func TestSaveWakeTemplate(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	now := time.Now()

	row, changed, err := st.SaveWakeTemplate(ctx, "wf-w1", sessionprompt.KindResume, "  先读收件箱 %inbox  ", "alice", now)
	if err != nil || !changed || row.WakeResume != "先读收件箱 %inbox" || row.WakeNew != "" {
		t.Fatalf("首次保存：row=%+v changed=%v err=%v", row, changed, err)
	}
	if row.ResidentRev != 0 {
		t.Fatalf("唤醒词模板不该涨常驻提示词的版本：%d", row.ResidentRev)
	}
	if _, changed, _ := st.SaveWakeTemplate(ctx, "wf-w1", sessionprompt.KindResume, "先读收件箱 %inbox", "alice", now); changed {
		t.Fatal("内容没变就什么也不做")
	}
	// 另一类互不影响
	row, changed, err = st.SaveWakeTemplate(ctx, "wf-w1", sessionprompt.KindNew, "接入 %workflow 后汇报", "alice", now)
	if err != nil || !changed || row.WakeResume != "先读收件箱 %inbox" || row.WakeNew != "接入 %workflow 后汇报" {
		t.Fatalf("保存另一类：row=%+v changed=%v err=%v", row, changed, err)
	}
	// 清除
	row, changed, err = st.SaveWakeTemplate(ctx, "wf-w1", sessionprompt.KindResume, "", "alice", now)
	if err != nil || !changed || row.WakeResume != "" || row.WakeNew != "接入 %workflow 后汇报" {
		t.Fatalf("清除：row=%+v changed=%v err=%v", row, changed, err)
	}
	// 版本记录按字段计数
	revs, err := st.ListPromptRevisions(ctx, "wf-w1", model.PromptFieldWakeResume)
	if err != nil || len(revs) != 2 {
		t.Fatalf("续聊模板应有 2 个历史版本（保存、清除）：%d err=%v", len(revs), err)
	}
	if revs, _ := st.ListPromptRevisions(ctx, "wf-w1", model.PromptFieldWakeNew); len(revs) != 1 {
		t.Fatalf("新开对话模板应有 1 个历史版本：%d", len(revs))
	}
	if revs, _ := st.ListPromptRevisions(ctx, "wf-w1", model.PromptFieldResident); len(revs) != 0 {
		t.Fatalf("不该污染常驻提示词的历史：%d", len(revs))
	}

	for _, bad := range []struct {
		wf   string
		kind sessionprompt.Kind
		text string
		why  string
	}{
		{"", sessionprompt.KindResume, "x", "空工作流"},
		{"wf-w1", sessionprompt.Kind("typed"), "x", "键入唤醒已下线（#245），会话也不能再自定义它"},
		{"wf-w1", sessionprompt.KindWebStart, "x", "会话不能自定义网页启动提示词"},
		{"wf-w1", sessionprompt.KindResume, strings.Repeat("字", sessionprompt.TemplateMaxRunes+1), "超长"},
	} {
		if _, _, err := st.SaveWakeTemplate(ctx, bad.wf, bad.kind, bad.text, "a", now); err == nil {
			t.Errorf("%s 应被拒绝", bad.why)
		}
	}
	// 上限本身合法
	if _, _, err := st.SaveWakeTemplate(ctx, "wf-w1", sessionprompt.KindNew, "%workflow"+strings.Repeat("字", sessionprompt.TemplateMaxRunes-len("%workflow")), "a", now); err != nil {
		t.Fatalf("刚好等于上限应合法：%v", err)
	}
}

func TestSaveWakeTemplateKeepsOnlyRecentRevisions(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	now := time.Now()
	for i := 0; i < model.PromptRevisionKeep+4; i++ {
		text := strings.Repeat("甲", i+1)
		if _, _, err := st.SaveWakeTemplate(ctx, "wf-w2", sessionprompt.KindResume, text, "a", now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	revs, err := st.ListPromptRevisions(ctx, "wf-w2", model.PromptFieldWakeResume)
	if err != nil || len(revs) != model.PromptRevisionKeep {
		t.Fatalf("应只保留最近 %d 个版本：%d err=%v", model.PromptRevisionKeep, len(revs), err)
	}
	maxRev := 0
	for _, r := range revs {
		if r.Rev > maxRev {
			maxRev = r.Rev
		}
	}
	if maxRev != model.PromptRevisionKeep+4 {
		t.Fatalf("版本号应持续递增到 %d：%d", model.PromptRevisionKeep+4, maxRev)
	}
}

func saveGlobalWake(t *testing.T, st *Store, w map[string]any) {
	t.Helper()
	if err := st.SaveSettings(context.Background(), map[string]any{"wakeTemplates": w}); err != nil {
		t.Fatalf("写全局设置失败: %v", err)
	}
}

// 分层：预设正文 > 会话模板 > 全局覆盖 > 内置默认；只有空白的不算「写了」。
func TestResolveWakeTemplateLayers(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	now := time.Now()
	check := func(step, presetBody, want string, wantLayer WakeLayer) {
		t.Helper()
		got, layer := st.ResolveWakeTemplate(ctx, sessionprompt.KindResume, "wf-l", presetBody, true)
		if got != want || layer != wantLayer {
			t.Fatalf("%s：got %q (%s)，want %q (%s)", step, got, layer, want, wantLayer)
		}
	}

	check("什么都没配 → 内置", "", sessionprompt.Builtin(sessionprompt.KindResume, true), WakeLayerBuiltin)
	saveGlobalWake(t, st, map[string]any{"resume": "全局续聊"})
	check("全局覆盖", "", "全局续聊", WakeLayerGlobal)
	if _, _, err := st.SaveWakeTemplate(ctx, "wf-l", sessionprompt.KindResume, "会话续聊", "a", now); err != nil {
		t.Fatal(err)
	}
	check("会话模板盖过全局", "", "会话续聊", WakeLayerSession)
	check("预设正文盖过一切", "预设正文 %workflow", "预设正文 %workflow", WakeLayerPreset)
	check("只有空白的预设正文不算写了", "  \n ", "会话续聊", WakeLayerSession)

	// 另一个会话不受影响
	got, layer := st.ResolveWakeTemplate(ctx, sessionprompt.KindResume, "wf-other", "", true)
	if got != "全局续聊" || layer != WakeLayerGlobal {
		t.Fatalf("别的会话应只受全局覆盖影响：%q (%s)", got, layer)
	}
	// 已下线的键入唤醒（#245）：设置里残留的 typed 值只是被忽略——不影响别的类型，也不会被当成任何类型的覆盖
	saveGlobalWake(t, st, map[string]any{"typed": "全局键入", "resume": "全局续聊"})
	if g := st.GlobalWakeTemplates(ctx); g != (WakeTemplates{Resume: "全局续聊"}) {
		t.Fatalf("残留的 typed 应被忽略：%+v", g)
	}
	if tpl, lay := st.ResolveWakeTemplate(ctx, sessionprompt.Kind("typed"), "wf-l", "", true); tpl != "" || lay != WakeLayerBuiltin {
		t.Fatalf("typed 不再是类型，不该解析出任何模板：%q (%s)", tpl, lay)
	}
	// 清除会话模板后回到全局
	saveGlobalWake(t, st, map[string]any{"resume": "全局续聊"})
	if _, _, err := st.SaveWakeTemplate(ctx, "wf-l", sessionprompt.KindResume, "", "a", now); err != nil {
		t.Fatal(err)
	}
	check("清除会话模板后回到全局", "", "全局续聊", WakeLayerGlobal)
}

func TestGlobalWakeTemplatesTolerant(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	if got := st.GlobalWakeTemplates(ctx); got != (WakeTemplates{}) {
		t.Fatalf("没配置过应为空：%+v", got)
	}
	// 格式不对（不是对象）：当作没有，不报错
	if err := st.SaveSettings(ctx, map[string]any{"wakeTemplates": "坏数据"}); err != nil {
		t.Fatal(err)
	}
	if got := st.GlobalWakeTemplates(ctx); got != (WakeTemplates{}) {
		t.Fatalf("格式不对应当作没有：%+v", got)
	}
	// 超长的被截到上限，前后空白去掉
	saveGlobalWake(t, st, map[string]any{"resume": "  " + strings.Repeat("乙", sessionprompt.TemplateMaxRunes+50) + "  ", "webStart": " 网页 "})
	got := st.GlobalWakeTemplates(ctx)
	if n := len([]rune(got.Resume)); n != sessionprompt.TemplateMaxRunes {
		t.Fatalf("超长的覆盖应被截到 %d 字：%d", sessionprompt.TemplateMaxRunes, n)
	}
	if got.WebStart != "网页" || got.For(sessionprompt.KindWebStart) != "网页" || got.For(sessionprompt.Kind("x")) != "" {
		t.Fatalf("For 与去空白不对：%+v", got)
	}
}

// 新开对话与网页启动提示词必须带 %workflow：缺了它智能体接不进来。续聊不要求。
func TestWakeTemplatesRequireWorkflowVarWhereNeeded(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	for _, kind := range []sessionprompt.Kind{sessionprompt.KindNew, sessionprompt.KindWebStart} {
		if _, err := NormalizeWakeTemplate(kind, "接入后汇报"); err == nil || !strings.Contains(err.Error(), "%workflow") {
			t.Errorf("%s 缺 %%workflow 应被拒绝并说明原因：%v", kind, err)
		}
		if v, err := NormalizeWakeTemplate(kind, "接入 %workflow"); err != nil || v != "接入 %workflow" {
			t.Errorf("%s 带 %%workflow 应通过：%q %v", kind, v, err)
		}
		if v, err := NormalizeWakeTemplate(kind, "  "); err != nil || v != "" {
			t.Errorf("%s 空串（清除）应通过：%q %v", kind, v, err)
		}
	}
	for _, kind := range []sessionprompt.Kind{sessionprompt.KindResume} {
		if _, err := NormalizeWakeTemplate(kind, "继续干活"); err != nil {
			t.Errorf("%s 不要求带 %%workflow：%v", kind, err)
		}
	}
	if _, _, err := st.SaveWakeTemplate(ctx, "wf-req", sessionprompt.KindNew, "不带编号", "a", time.Now()); err == nil {
		t.Error("会话的新开对话模板缺 %workflow 应被拒绝")
	}
	if _, err := ValidateWakeTemplates(WakeTemplates{WebStart: "不带编号"}); err == nil {
		t.Error("全局的网页启动提示词缺 %workflow 应被拒绝")
	}
}

// 全局设置里覆盖了网页启动提示词：新建会话时按覆盖渲染；没覆盖时与升级前逐字相同。
func TestCreateWebWorkflowUsesGlobalStartPromptOverride(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	res, err := st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "支付重构", WorkflowID: "wf-web-a1"})
	if err != nil || res.StartPrompt != WebWorkflowStartPrompt("支付重构", "wf-web-a1", "") {
		t.Fatalf("没覆盖时应与升级前相同：err=%v\n%s", err, res.StartPrompt)
	}
	saveGlobalWake(t, st, map[string]any{"webStart": "请接入「%title」，编号 %workflow，目录 %project"})
	res, err = st.CreateWebWorkflow(ctx, CreateWebWorkflowInput{Title: "订单", WorkflowID: "wf-web-a2", ProjectDirectory: "/srv/order"})
	if err != nil || res.StartPrompt != "请接入「订单」，编号 wf-web-a2，目录 /srv/order" {
		t.Fatalf("按覆盖渲染：err=%v %q", err, res.StartPrompt)
	}
}

func TestValidateWakeTemplates(t *testing.T) {
	out, err := ValidateWakeTemplates(WakeTemplates{Resume: " a \r\nb "})
	if err != nil || out.Resume != "a \nb" || out.New != "" || out.WebStart != "" {
		t.Fatalf("规整：%+v err=%v", out, err)
	}
	_, err = ValidateWakeTemplates(WakeTemplates{New: strings.Repeat("x", sessionprompt.TemplateMaxRunes+1)})
	if err == nil || !strings.Contains(err.Error(), "新开对话") {
		t.Fatalf("超长应被拒绝并指明是哪一条：%v", err)
	}
}

// 「现状」变量：标题 / 目录 / 主机取最近一条会话，其余取各自的现状。
func TestRenderWakeLiveVars(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	now := time.Now()
	wf := "wf-live"

	if err := st.db.Create(&model.FeedbackSession{
		ID: "s-old", WorkflowID: wf, Title: "旧标题", Summary: "x", HostName: "旧主机", ProjectDirectory: "/old",
		CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.db.Create(&model.FeedbackSession{
		ID: "s-new", WorkflowID: wf, Title: "新标题", Summary: "x", HostName: "lab-1", ProjectDirectory: "/srv/app",
		GitCommit: "0123456789abcdef", GitSubject: "修复唤醒词", CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveResidentPrompt(ctx, wf, "做完所有非暂缓任务再收尾", "alice", now); err != nil {
		t.Fatal(err)
	}
	for i, q := range []model.QueuedFeedback{
		{WorkflowID: wf, ResponseText: "第一条", Status: "pending", CreatedAt: now},
		{WorkflowID: wf, ResponseText: "第二条", Status: "pending", CreatedAt: now},
	} {
		q.Kind = "append"
		if err := st.db.Create(&q).Error; err != nil {
			t.Fatalf("写追加 %d 失败: %v", i, err)
		}
	}
	putGateTask(t, st, wf, 7, "todo", "", "", "")
	putGateTask(t, st, wf, 9, "doing", "", "", "")
	cp, _ := json.Marshal(map[string]any{"objective": "目标", "next_actions": []string{"  ", "跑回归", "发版"}})
	if err := st.SaveCheckpoint(ctx, &model.WorkflowCheckpoint{WorkflowID: wf, ContentJSON: string(cp)}); err != nil {
		t.Fatal(err)
	}

	tpl := "编号=%workflow 标题=%title 目录=%project 主机=%host 模式=%mode 阶段=%phase 常驻=%resident 未读=%inbox 任务=%tasks 下一步=%next 交付=%anchor"
	if _, _, err := st.SaveWakeTemplate(ctx, wf, sessionprompt.KindResume, tpl, "a", now); err != nil {
		t.Fatal(err)
	}
	res := st.RenderWake(ctx, WakeInput{Kind: sessionprompt.KindResume, WorkflowID: wf, Now: now})
	if res.Layer != WakeLayerSession || res.Template != tpl {
		t.Fatalf("应用会话模板：layer=%s", res.Layer)
	}
	want := "编号=wf-live 标题=新标题 目录=/srv/app 主机=lab-1 模式=同步 阶段=" // 阶段取决于默认值，下面单独断言非空
	if !strings.HasPrefix(res.Text, want) {
		t.Fatalf("前半段不对：\n%s", res.Text)
	}
	for _, frag := range []string{"常驻=做完所有非暂缓任务再收尾", "未读=2", "任务=2 个（#7 #9）", "下一步=跑回归", "交付=0123456 修复唤醒词"} {
		if !strings.Contains(res.Text, frag) {
			t.Errorf("缺少 %q：\n%s", frag, res.Text)
		}
	}
	if strings.Contains(res.Text, "阶段= ") || strings.Contains(res.Text, "阶段=（暂无）") {
		t.Errorf("阶段应取到默认阶段：\n%s", res.Text)
	}

	// 预设给了目录 / 主机时不被覆盖
	res = st.RenderWake(ctx, WakeInput{Kind: sessionprompt.KindResume, WorkflowID: wf, Now: now, PresetBody: "%project@%host",
		Base: sessionprompt.Vars{Project: "/preset/dir", Host: "preset-host"}})
	if res.Text != "/preset/dir@preset-host" || res.Layer != WakeLayerPreset {
		t.Fatalf("预设给的变量优先：%q (%s)", res.Text, res.Layer)
	}
}

// 没引用「现状」变量的正文不查库：自定义正文只写编号时，行为与升级前一致，也没有额外开销。
func TestRenderWakeOnlyQueriesWhenNeeded(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	res := st.RenderWake(ctx, WakeInput{Kind: sessionprompt.KindResume, WorkflowID: "wf-q", PresetBody: "继续 %workflow 的工作"})
	if res.Text != "继续 wf-q 的工作" {
		t.Fatalf("只用编号的正文：%q", res.Text)
	}
	if res.Vars.Inbox != "" || res.Vars.Tasks != "" || res.Vars.Mode != "" {
		t.Fatalf("没引用现状变量就不该现算：%+v", res.Vars)
	}
}

// 没有数据时现状变量显示「（暂无）」/「（未设置）」，不留空洞。
func TestRenderWakeEmptyState(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	res := st.RenderWake(ctx, WakeInput{Kind: sessionprompt.KindResume, WorkflowID: "wf-empty", PresetBody: "[%next][%anchor][%resident][%tasks][%inbox][%title]"})
	// 标题 / 目录 / 主机没有时留空（与升级前的 %project 一致），状态类变量显示「（暂无）」
	if res.Text != "[（暂无）][（暂无）][（未设置）][0 个][0][]" {
		t.Fatalf("空状态：%q", res.Text)
	}
}

// 没有工作流（预设没配）：不现算现状，默认模板用不点名工作流的写法。
func TestRenderWakeWithoutWorkflow(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	res := st.RenderWake(ctx, WakeInput{Kind: sessionprompt.KindResume, Base: sessionprompt.Vars{Project: "/srv/app"}})
	if res.Layer != WakeLayerBuiltin || res.Template != sessionprompt.Builtin(sessionprompt.KindResume, false) {
		t.Fatalf("应用无工作流的内置默认：layer=%s", res.Layer)
	}
	if strings.Contains(res.Text, "%") || strings.Contains(res.Text, "工作流 （") {
		t.Fatalf("不该留变量或残句：\n%s", res.Text)
	}
}

// 续聊 / 新开对话的提示词是命令行里的一个参数，不能含换行：非预设层渲染后压成一行；预设自己的正文（保存时已校验单行）原样不动。
func TestRenderWakeFlattensCommandLinePrompts(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	now := time.Now()
	if _, _, err := st.SaveWakeTemplate(ctx, "wf-flat", sessionprompt.KindResume, "第一行\n\n  第二行\t%resident", "a", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveResidentPrompt(ctx, "wf-flat", "1. 先问\n2. 不动 main", "a", now); err != nil {
		t.Fatal(err)
	}
	res := st.RenderWake(ctx, WakeInput{Kind: sessionprompt.KindResume, WorkflowID: "wf-flat"})
	if res.Layer != WakeLayerSession || res.Text != "第一行 第二行 1. 先问 2. 不动 main" {
		t.Fatalf("会话模板应压成一行：%q (%s)", res.Text, res.Layer)
	}
	for _, k := range []sessionprompt.Kind{sessionprompt.KindResume, sessionprompt.KindNew} {
		b := st.RenderWake(ctx, WakeInput{Kind: k, WorkflowID: "wf-other"})
		if b.Layer != WakeLayerBuiltin || strings.ContainsAny(b.Text, "\r\n\t") {
			t.Fatalf("%s 的内置默认渲染后应是单行：%q", k, b.Text)
		}
	}
	// 预设正文不动（连多余的空格都保持）
	p := st.RenderWake(ctx, WakeInput{Kind: sessionprompt.KindResume, WorkflowID: "wf-flat", PresetBody: "看看  %workflow  的收件箱"})
	if p.Layer != WakeLayerPreset || p.Text != "看看  wf-flat  的收件箱" {
		t.Fatalf("预设正文应原样（只替换变量）：%q", p.Text)
	}
	// 网页启动提示词是给用户看的，保留分行
	st2 := setupTestStore(t)
	saveGlobalWake(t, st2, map[string]any{"webStart": "第一行 %workflow\n第二行"})
	if w := st2.RenderWake(ctx, WakeInput{Kind: sessionprompt.KindWebStart, WorkflowID: "wf-x"}); !strings.Contains(w.Text, "\n") {
		t.Fatalf("网页启动提示词应保留分行：%q", w.Text)
	}
}
