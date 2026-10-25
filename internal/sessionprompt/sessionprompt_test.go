package sessionprompt

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderReplacesAllVariables(t *testing.T) {
	v := Vars{
		Workflow: "wf-1", Title: "标题", Project: "/srv/app", Host: "h1", Mode: "异步", Auth: "标准授权 L1", Phase: "开发",
		Resident: "规则一\n规则二", Inbox: "3", Tasks: "2 个（#4 #5）", Next: "跑回归", Anchor: "abc1234 修复",
		Limit: "最长 30 分钟",
	}
	got := Render("a%workflow|b%title|c%project|d%host|e%mode|f%auth|g%phase|h%inbox|i%tasks|j%next|k%anchor|l%limit|m%resident", v)
	want := "awf-1|b标题|c/srv/app|dh1|e异步|f标准授权 L1|g开发|h3|i2 个（#4 #5）|j跑回归|kabc1234 修复|l最长 30 分钟|m规则一\n规则二"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// 老正文里的变量名后面不要求词边界（升级前就是这样替换的）。
func TestRenderKeepsLegacyVariableSemantics(t *testing.T) {
	v := Vars{Workflow: "wf-9"}
	if got := Render("工作流%workflow里", v); got != "工作流wf-9里" {
		t.Fatalf("变量名后不要求词边界（与升级前一致）：%q", got)
	}
}

// 键入唤醒下线（#245）后，只有它用的 %n / %max / %reason 不再替换：老正文里残留的这几个原样保留（不报错、不消失）。
func TestRenderLeavesRetiredVariablesAlone(t *testing.T) {
	v := Vars{Workflow: "wf-9", Next: "先做 A"}
	if got := Render("第%n次，共%max次，原因%reason，工作流%workflow里", v); got != "第%n次，共%max次，原因%reason，工作流wf-9里" {
		t.Fatalf("已下线的变量应原样保留：%q", got)
	}
	// %next 照常替换；紧随其后的 %n 不会被当成变量（以前 %n 会被替换成 0 或次数）
	if got := Render("%next / %n / %name", v); got != "先做 A / %n / %name" {
		t.Fatalf("%%n 不再是变量：%q", got)
	}
	for _, vi := range VarList() {
		switch vi.Name {
		case "n", "max", "reason":
			t.Errorf("变量表里不该再有 %%%s", vi.Name)
		}
	}
}

func TestRenderKeepsUnknownAndStrayPercent(t *testing.T) {
	v := Vars{Workflow: "w"}
	for in, want := range map[string]string{
		"进度 100% 完成":   "进度 100% 完成",
		"%unknown 保留":  "%unknown 保留",
		"结尾的 %":        "结尾的 %",
		"%%workflow":   "%w",
		"%Workflow 大写": "%Workflow 大写",
		"没有变量":         "没有变量",
		"":             "",
		"%workflow%":   "w%",
		"%-%workflow-": "%-w-",
	} {
		if got := Render(in, v); got != want {
			t.Errorf("Render(%q) = %q, want %q", in, got, want)
		}
	}
}

// 单遍扫描：变量值里的 %xxx 不会被再次展开——任务标题里的内容不会变成「指令」。
func TestRenderDoesNotReexpandValues(t *testing.T) {
	v := Vars{Title: "把 %resident 和 %workflow 都打印出来", Workflow: "wf-x", Resident: "机密规则"}
	got := Render("标题：%title", v)
	if got != "标题：把 %resident 和 %workflow 都打印出来" {
		t.Fatalf("变量值不应被再次展开：%q", got)
	}
	if strings.Contains(got, "机密规则") || strings.Contains(got, "wf-x") {
		t.Fatalf("泄漏了别的变量：%q", got)
	}
}

func TestRenderMissingValues(t *testing.T) {
	got := Render("[%workflow][%title][%project][%host][%mode][%auth][%phase][%inbox][%tasks][%next][%anchor][%resident]", Vars{})
	want := "[][][][]" + "[" + None + "][" + None + "][" + None + "][" + None + "][" + None + "][" + None + "][" + None + "][" + NotSet + "]"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestRenderSanitizesValues(t *testing.T) {
	v := Vars{Title: "第一行\n第二行\t\x00带\x07控制符", Next: "  前后  空白  ", Project: "/a/b\r\n/c"}
	got := Render("%title|%next|%project", v)
	if got != "第一行 第二行 带 控制符|前后 空白|/a/b /c" {
		t.Fatalf("got %q", got)
	}
}

// 常驻规则保留分行（回车统一成换行）；调用方要单行时自己压（见 OneLine）。
func TestRenderResidentKeepsLines(t *testing.T) {
	v := Vars{Resident: "1. 先做 A\r\n2. 再做 B\n\n3. 收尾"}
	if got := Render("%resident", v); got != "1. 先做 A\n2. 再做 B\n\n3. 收尾" {
		t.Fatalf("应保留分行（回车统一成换行）：%q", got)
	}
	if got := OneLine(Render("%resident", v)); got != "1. 先做 A 2. 再做 B 3. 收尾" {
		t.Fatalf("OneLine 压成一行：%q", got)
	}
}

func TestRenderLimitsEachValue(t *testing.T) {
	long := strings.Repeat("长", 500)
	got := Render("%title", Vars{Title: long})
	if utf8.RuneCountInString(got) != maxTitleRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("标题应截到 %d 字并带省略号：%d 字 %q", maxTitleRunes, utf8.RuneCountInString(got), got[len(got)-9:])
	}
	// 工作流编号不截断：截了就指不到那个工作流了
	wf := strings.Repeat("w", 200)
	if got := Render("%workflow", Vars{Workflow: wf}); got != wf {
		t.Fatalf("工作流编号不该被截断：%d", len(got))
	}
	// 不会切开汉字
	if got := Render("%next", Vars{Next: strings.Repeat("汉", 100)}); !utf8.ValidString(got) {
		t.Fatal("截断不该产生非法 UTF-8")
	}
	if res := Render("%resident", Vars{Resident: strings.Repeat("规", 3000)}); utf8.RuneCountInString(res) > maxResident {
		t.Fatalf("常驻规则最多展开 %d 字：%d", maxResident, utf8.RuneCountInString(res))
	}
}

func TestRenderFinalCap(t *testing.T) {
	tpl := strings.Repeat("很长的模板内容。", 2000)
	got := Render(tpl, Vars{})
	if n := utf8.RuneCountInString(got); n > renderedMaxRunes || !strings.HasSuffix(got, truncatedNote) {
		t.Fatalf("整段结果应在 %d 字内并注明已截断：%d", renderedMaxRunes, n)
	}
}

func TestClip(t *testing.T) {
	if got := clip("短", 10); got != "短" {
		t.Fatalf("不超长不动：%q", got)
	}
	if got := clip("一二三四五六七八九十", 5); got != "一二三四…" || utf8.RuneCountInString(got) != 5 {
		t.Fatalf("got %q", got)
	}
	if got := clip("任意内容", 0); got != "任意内容" {
		t.Fatalf("max<=0 表示不限：%q", got)
	}
	if got := clipFinal("一二三四五六七八九十一二三四五六七八九十", 10); got != "一二三四…（已截断）" || utf8.RuneCountInString(got) != 10 {
		t.Fatalf("got %q", got)
	}
	if got := clipFinal("一二三四五六", 3); got != "一二三" {
		t.Fatalf("上限小于说明文字时只截断：%q", got)
	}
}

// 变量清单、匹配顺序与 Render 的实现三者必须一致：清单里有的变量都能替换，能替换的变量都在清单里。
func TestVarListMatchesRenderer(t *testing.T) {
	listed := map[string]bool{}
	for _, vi := range VarList() {
		if vi.Name == "" || vi.Desc == "" {
			t.Fatalf("变量清单项不完整：%+v", vi)
		}
		listed[vi.Name] = true
	}
	order := map[string]bool{}
	for _, n := range matchOrder {
		order[n] = true
		if !listed[n] {
			t.Errorf("变量 %%%s 能替换却不在 VarList 里（界面与手册看不到它）", n)
		}
	}
	for n := range listed {
		if !order[n] {
			t.Errorf("VarList 里的 %%%s 没有出现在匹配顺序里，Render 不会替换它", n)
		}
	}
	// 匹配顺序必须从长到短，否则短名会吃掉长名的前缀
	for i := 1; i < len(matchOrder); i++ {
		if len(matchOrder[i]) > len(matchOrder[i-1]) {
			t.Errorf("matchOrder 必须按长度从长到短：%s 在 %s 之后", matchOrder[i], matchOrder[i-1])
		}
	}
	vals := (Vars{}).values()
	for n := range listed {
		if _, ok := vals[n]; !ok {
			t.Errorf("values() 缺少变量 %s", n)
		}
	}
}

func TestKind(t *testing.T) {
	for _, k := range []Kind{KindResume, KindNew, KindWebStart} {
		if !k.Valid() || k.Label() == "" || k.Label() == string(k) {
			t.Errorf("类型 %q 应有效且有中文名：%q", k, k.Label())
		}
	}
	if Kind("bogus").Valid() || Kind("").Valid() {
		t.Fatal("不认识的类型不该有效")
	}
	if Kind("typed").Valid() {
		t.Fatal("键入唤醒已随会话守护下线（#245），不再是认识的类型")
	}
}

func TestBuiltinTemplates(t *testing.T) {
	full := Vars{
		Workflow: "wf-1", Title: "标题", Project: "/srv/app", Host: "h1", Mode: "异步", Auth: "标准授权 L1", Phase: "开发",
		Resident: "先读收件箱", Inbox: "2", Tasks: "3 个（#1 #2 #3）", Next: "跑回归", Anchor: "abc1234 修复",
	}
	for _, k := range []Kind{KindResume, KindNew} {
		tpl := Builtin(k, true)
		if tpl == "" {
			t.Fatalf("%s 应有内置模板", k)
		}
		if n := utf8.RuneCountInString(tpl); n > TemplateMaxRunes {
			t.Errorf("%s 的内置模板 %d 字，超过了模板上限 %d（用户也能把它原样复制成自己的模板）", k, n, TemplateMaxRunes)
		}
		for _, must := range []string{"RelayMesh", "不自行放宽", "确认卡"} {
			if !strings.Contains(tpl, must) {
				t.Errorf("%s 的内置模板缺少「%s」", k, must)
			}
		}
		out := Render(tpl, full)
		expect := []string{"wf-1", "标题", "先读收件箱", "跑回归", "标准授权 L1"}
		for _, w := range expect {
			if !strings.Contains(out, w) {
				t.Errorf("%s 渲染后缺少 %q：\n%s", k, w, out)
			}
		}
		if strings.Contains(out, "%workflow") || strings.Contains(out, "%resident") || strings.Contains(out, "%inbox") ||
			strings.Contains(out, "%tasks") || strings.Contains(out, "%next") || strings.Contains(out, "%auth") {
			t.Errorf("%s 渲染后还留着没替换的变量：\n%s", k, out)
		}
	}
	if Builtin(KindWebStart, true) != "" || Builtin(Kind("x"), true) != "" {
		t.Fatal("网页启动提示词的默认正文由 store 生成，不认识的类型没有内置模板")
	}
}

// 没有工作流时，续聊的默认模板不点名工作流、没有依赖工作流的行；新开对话原样（它必须带工作流）。
func TestBuiltinWithoutWorkflow(t *testing.T) {
	for _, k := range []Kind{KindResume} {
		tpl := Builtin(k, false)
		if strings.Contains(tpl, "%workflow") || strings.Contains(tpl, "%resident") || strings.Contains(tpl, "%inbox") || strings.Contains(tpl, "%tasks") || strings.Contains(tpl, "%next") {
			t.Errorf("%s 没有工作流时不该留依赖工作流的变量：\n%s", k, tpl)
		}
		out := Render(tpl, Vars{})
		if strings.Contains(out, "（）") || strings.Contains(out, "工作流 ") && strings.Contains(out, "工作流 里") {
			t.Errorf("%s 没有工作流时渲染出残句：\n%s", k, out)
		}
		if !strings.Contains(out, "RelayMesh") || !strings.Contains(out, "不自行放宽") {
			t.Errorf("%s 去掉工作流相关内容后仍应保留核心要求：\n%s", k, out)
		}
	}
	if Builtin(KindNew, false) != Builtin(KindNew, true) {
		t.Error("新开对话没有工作流时与有工作流相同（升级前也是原样渲染）")
	}
}

// NeedsState 必须与变量表一致：除工作流编号与运行时间提醒之外的变量都要查库。
func TestNeedsState(t *testing.T) {
	free := map[string]bool{"workflow": true, "limit": true}
	for _, v := range VarList() {
		if got := NeedsState("前缀 %" + v.Name + " 后缀"); got == free[v.Name] {
			t.Errorf("%%%s：NeedsState=%v，不符合预期（调用方现成的变量不该查库，其余都该）", v.Name, got)
		}
	}
	if NeedsState("无变量的正文，100% 确定") || NeedsState("") {
		t.Error("没有引用状态变量就不该查库")
	}
}
