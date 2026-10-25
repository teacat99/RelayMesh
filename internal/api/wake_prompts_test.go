package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
)

// 唤醒词模板 REST（#232 D3，D-376）：默认值与变量表、预览、会话模板的读写、全局设置的校验。

const wkWF = "wf-wake-rest"

func wkPath(suffix string) string { return "/api/v1/workflows/" + wkWF + "/prompts" + suffix }

// 新增的两条路由同样只给登录的 Web 用户：匿名 401，MCP 令牌 403。
func TestWakePromptsREST_OnlyForWebUsers(t *testing.T) {
	e := newCfEnv(t)
	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/wake-prompts/defaults", nil},
		{"POST", "/api/v1/wake-prompts/preview", map[string]any{"kind": "resume"}},
	}
	tokens := map[string]string{"环境变量 MCP 令牌": wiredMCPToken, "凭据库令牌": e.cred}
	for _, rt := range routes {
		if res := wiredCall(e.srv, rt.method, rt.path, rt.body); res.Code != http.StatusUnauthorized {
			t.Errorf("匿名 %s %s 应 401，实际 %d", rt.method, rt.path, res.Code)
		}
		for name, tok := range tokens {
			res := wiredCall(e.srv, rt.method, rt.path, rt.body, "Authorization", "Bearer "+tok)
			if res.Code != http.StatusForbidden || res.str("code") != "mcp_token_forbidden" {
				t.Errorf("%s 访问 %s %s 应 403 mcp_token_forbidden，实际 %d %s", name, rt.method, rt.path, res.Code, res.Raw)
			}
		}
	}
}

func TestWakePromptsREST_Defaults(t *testing.T) {
	e := newCfEnv(t)
	res := e.call("GET", "/api/v1/wake-prompts/defaults", nil)
	if res.Code != http.StatusOK || res.Body["template_max"] != float64(sessionprompt.TemplateMaxRunes) {
		t.Fatalf("默认值应 200 并带字数上限：%d %s", res.Code, res.Raw)
	}
	kinds, _ := res.Body["kinds"].([]any)
	if len(kinds) != 3 {
		t.Fatalf("应有 3 类唤醒词（键入唤醒随守护下线）：%s", res.Raw)
	}
	byKind := map[string]map[string]any{}
	for _, k := range kinds {
		m := k.(map[string]any)
		byKind[m["kind"].(string)] = m
		if m["template"] == "" || m["label"] == "" || m["desc"] == "" {
			t.Errorf("每类都该有名字、说明与内置默认：%v", m)
		}
	}
	for kind, want := range map[string]struct{ session, needsWf bool }{
		"resume": {true, false}, "new": {true, true}, "web_start": {false, true},
	} {
		m := byKind[kind]
		if m == nil || m["session"] != want.session || m["needs_workflow"] != want.needsWf {
			t.Errorf("%s 的 session / needs_workflow 不对：%v", kind, m)
		}
	}
	if byKind["typed"] != nil {
		t.Errorf("键入唤醒已下线，不该再列出：%v", byKind["typed"])
	}
	if byKind["resume"]["template_no_workflow"] == nil || byKind["new"]["template_no_workflow"] != nil {
		t.Errorf("续聊有无工作流的写法，新开对话没有：%v %v", byKind["resume"]["template_no_workflow"], byKind["new"]["template_no_workflow"])
	}
	vars, _ := res.Body["vars"].([]any)
	if len(vars) != len(sessionprompt.VarList()) {
		t.Errorf("变量表数量不对：%d", len(vars))
	}
	if tpl, _ := byKind["web_start"]["template"].(string); !strings.Contains(tpl, "%workflow") || !strings.Contains(tpl, "%title") {
		t.Errorf("网页启动提示词的模板应带 %%title 与 %%workflow：%q", tpl)
	}
}

func TestWakePromptsREST_Preview(t *testing.T) {
	e := newCfEnv(t)
	prev := func(body map[string]any) wiredResp { return e.call("POST", "/api/v1/wake-prompts/preview", body) }

	// 不带工作流：用示例值渲染内置默认
	res := prev(map[string]any{"kind": "resume"})
	if res.Code != http.StatusOK || res.Body["layer"] != "builtin" || res.Body["sample"] != true {
		t.Fatalf("示例预览：%d %s", res.Code, res.Raw)
	}
	if !strings.Contains(res.str("text"), "wf-示例") || strings.Contains(res.str("text"), "%workflow") {
		t.Fatalf("示例预览应把变量换成示例值：%s", res.str("text"))
	}

	// 草稿：层是 draft，变量被替换
	res = prev(map[string]any{"kind": "resume", "template": "编号 %workflow，未读 %inbox"})
	if res.Code != http.StatusOK || res.Body["layer"] != "draft" || res.str("text") != "编号 wf-示例，未读 1" {
		t.Fatalf("草稿预览：%d %s", res.Code, res.Raw)
	}

	// 带工作流：用真实现状（这个会话什么都没有：未读 0）
	res = prev(map[string]any{"kind": "resume", "workflow_id": wkWF, "template": "编号 %workflow，未读 %inbox"})
	if res.Code != http.StatusOK || res.Body["sample"] != false || res.str("text") != "编号 "+wkWF+"，未读 0" {
		t.Fatalf("真实现状预览：%d %s", res.Code, res.Raw)
	}

	// 键入唤醒随守护下线：预览直接 400
	if res = prev(map[string]any{"kind": "typed", "template": "第二行 %workflow"}); res.Code != http.StatusBadRequest {
		t.Fatalf("键入唤醒已下线，预览应 400：%d %s", res.Code, res.Raw)
	}

	// 网页启动提示词：不带草稿时用内置函数的文字
	res = prev(map[string]any{"kind": "web_start"})
	if res.Code != http.StatusOK || res.Body["layer"] != "builtin" || !strings.Contains(res.str("text"), "wf-示例") || !strings.Contains(res.str("text"), "示例会话") {
		t.Fatalf("网页启动提示词预览：%d %s", res.Code, res.Raw)
	}

	// 续聊 / 新开对话是命令行参数：分行写的模板，预览显示的也是压成一行后的样子（与真正发送一致）
	res = prev(map[string]any{"kind": "resume", "template": "先读收件箱\n再看任务 %inbox"})
	if res.Code != http.StatusOK || res.str("text") != "先读收件箱 再看任务 1" {
		t.Fatalf("示例预览应压成一行：%d %s", res.Code, res.Raw)
	}
}

// 预设自己的正文（preset=true）：层是 preset，只按预设的规则校验（不要求带 %workflow），为空时当作没写。
func TestWakePromptsREST_PreviewPresetBody(t *testing.T) {
	e := newCfEnv(t)
	prev := func(body map[string]any) wiredResp { return e.call("POST", "/api/v1/wake-prompts/preview", body) }

	// 新开对话的预设正文可以不带 %workflow（草稿模式会 400）
	res := prev(map[string]any{"kind": "new", "preset": true, "template": "  自己写的，标题 %title  "})
	if res.Code != http.StatusOK || res.Body["layer"] != "preset" || res.Body["sample"] != true || strings.Contains(res.str("text"), "%title") || !strings.HasPrefix(res.str("text"), "自己写的，标题 ") {
		t.Fatalf("示例预览：%d %s", res.Code, res.Raw)
	}
	if res := prev(map[string]any{"kind": "new", "template": "自己写的，没有编号"}); res.Code != http.StatusBadRequest {
		t.Fatalf("同样的文字当模板草稿应 400：%d %s", res.Code, res.Raw)
	}

	// 带工作流：走真正发送时的路径，用这个会话此刻的现状
	res = prev(map[string]any{"kind": "resume", "preset": true, "workflow_id": wkWF, "template": "接着 %workflow，未读 %inbox"})
	if res.Code != http.StatusOK || res.Body["layer"] != "preset" || res.Body["sample"] != false || res.str("text") != "接着 "+wkWF+"，未读 0" {
		t.Fatalf("真实现状预览：%d %s", res.Code, res.Raw)
	}

	// 正文为空：当作没写，渲染分层选出来的那一份（这里是内置默认）
	res = prev(map[string]any{"kind": "resume", "preset": true, "template": "   "})
	if res.Code != http.StatusOK || res.Body["layer"] != "builtin" {
		t.Fatalf("空正文应回到分层选用：%d %s", res.Code, res.Raw)
	}

	// 超长；网页启动提示词不是预设，preset 对它不起作用（仍按模板草稿校验）
	if res := prev(map[string]any{"kind": "resume", "preset": true, "template": strings.Repeat("长", sessionprompt.TemplateMaxRunes+1)}); res.Code != http.StatusBadRequest {
		t.Errorf("超长预设正文应 400：%d %s", res.Code, res.Raw)
	}
	if res := prev(map[string]any{"kind": "web_start", "preset": true, "template": "没有编号"}); res.Code != http.StatusBadRequest {
		t.Errorf("网页启动提示词不认 preset：%d %s", res.Code, res.Raw)
	}

	// 非法请求
	for name, body := range map[string]map[string]any{
		"未知类型":       {"kind": "bogus"},
		"缺类型":        {},
		"新开对话草稿缺编号":  {"kind": "new", "template": "没有编号"},
		"超长草稿":       {"kind": "resume", "template": strings.Repeat("长", sessionprompt.TemplateMaxRunes+1)},
		"网页启动提示词缺编号": {"kind": "web_start", "template": "没有编号"},
	} {
		if res := prev(body); res.Code != http.StatusBadRequest {
			t.Errorf("%s 应 400，实际 %d %s", name, res.Code, res.Raw)
		}
	}
}

func TestWakePromptsREST_SessionTemplates(t *testing.T) {
	e := newCfEnv(t)

	v := wpViewAt(t, e.call("GET", wkPath(""), nil))
	if v["wake_resume"] != "" || v["wake_new"] != "" || v["wake_max"] != float64(sessionprompt.TemplateMaxRunes) {
		t.Fatalf("初始：没有会话模板、带上限：%v", v)
	}

	res := e.call("PUT", wkPath(""), map[string]any{"wake_resume": "  先读收件箱 %inbox\r\n再看任务  ", "wake_new": "接入 %workflow"})
	v = wpViewAt(t, res)
	if res.Code != http.StatusOK || res.Body["changed"] != true || v["wake_resume"] != "先读收件箱 %inbox\n再看任务" || v["wake_new"] != "接入 %workflow" {
		t.Fatalf("保存会话模板：%d %s", res.Code, res.Raw)
	}
	if v["resident_rev"] != float64(0) {
		t.Fatalf("唤醒词模板不该涨常驻提示词的版本：%v", v["resident_rev"])
	}

	// 内容没变：changed=false
	if res = e.call("PUT", wkPath(""), map[string]any{"wake_resume": "先读收件箱 %inbox\n再看任务"}); res.Body["changed"] != false {
		t.Fatalf("没变应 changed=false：%s", res.Raw)
	}

	// 会话模板进了分层：预览不带草稿时用的就是它；续聊的最终文字是命令行里的一个参数，分行写的模板渲染后压成一行
	res = e.call("POST", "/api/v1/wake-prompts/preview", map[string]any{"kind": "resume", "workflow_id": wkWF})
	if res.Body["layer"] != "session" || res.str("text") != "先读收件箱 0 再看任务" {
		t.Fatalf("预览应用会话模板：%s", res.Raw)
	}

	// 原子：一项不合规，整个请求什么也不保存
	res = e.call("PUT", wkPath(""), map[string]any{"resident": "新的常驻规则", "wake_new": "缺编号"})
	if res.Code != http.StatusBadRequest || !strings.Contains(res.str("error"), "%workflow") {
		t.Fatalf("缺 %%workflow 应 400 并说明原因：%d %s", res.Code, res.Raw)
	}
	v = wpViewAt(t, e.call("GET", wkPath(""), nil))
	if v["resident"] != "" || v["wake_new"] != "接入 %workflow" {
		t.Fatalf("被拒绝的请求什么也不该保存：%v", v)
	}

	// 什么都没带 → 400
	if res = e.call("PUT", wkPath(""), map[string]any{}); res.Code != http.StatusBadRequest {
		t.Fatalf("什么都没带应 400：%d %s", res.Code, res.Raw)
	}

	// 清除
	res = e.call("PUT", wkPath(""), map[string]any{"wake_resume": "", "wake_new": ""})
	v = wpViewAt(t, res)
	if res.Code != http.StatusOK || v["wake_resume"] != "" || v["wake_new"] != "" {
		t.Fatalf("清除：%d %s", res.Code, res.Raw)
	}
}

func wpViewAt(t *testing.T, res wiredResp) map[string]any {
	t.Helper()
	v, ok := res.Body["prompts"].(map[string]any)
	if !ok {
		t.Fatalf("响应里没有 prompts: %d %s", res.Code, res.Raw)
	}
	return v
}

// 全局设置里的 wakeTemplates：保存前校验并规整；格式不对 / 缺 %workflow / 超长一律 400，设置原样不动。
func TestWakePromptsREST_GlobalSettingsValidation(t *testing.T) {
	e := newCfEnv(t)
	put := func(wt any) wiredResp { return e.call("PUT", "/api/v1/settings", map[string]any{"wakeTemplates": wt}) }

	res := put(map[string]any{"resume": "  全局续聊 %inbox  ", "new": "接入 %workflow", "webStart": "请接入 %workflow"})
	if res.Code != http.StatusOK {
		t.Fatalf("合法的全局覆盖应保存：%d %s", res.Code, res.Raw)
	}
	got := e.call("GET", "/api/v1/settings", nil)
	wt, _ := got.Body["settings"].(map[string]any)["wakeTemplates"].(map[string]any)
	if wt["resume"] != "全局续聊 %inbox" || wt["new"] != "接入 %workflow" || wt["webStart"] != "请接入 %workflow" {
		t.Fatalf("应保存规整后的值：%v", wt)
	}
	// 预览不带草稿时用全局覆盖
	if res = e.call("POST", "/api/v1/wake-prompts/preview", map[string]any{"kind": "resume"}); res.Body["layer"] != "global" || res.str("text") != "全局续聊 1" {
		t.Fatalf("预览应用全局覆盖：%s", res.Raw)
	}

	// 已下线的键入唤醒（#245）：旧设置或没刷新的旧页面带着 typed 值也不报错，保存时被丢掉，不影响别的类型
	if res = put(map[string]any{"resume": "全局续聊 %inbox", "typed": "旧的键入唤醒"}); res.Code != http.StatusOK {
		t.Fatalf("带着残留的 typed 值也应能保存：%d %s", res.Code, res.Raw)
	}
	got = e.call("GET", "/api/v1/settings", nil)
	wt, _ = got.Body["settings"].(map[string]any)["wakeTemplates"].(map[string]any)
	if _, has := wt["typed"]; has || wt["resume"] != "全局续聊 %inbox" {
		t.Fatalf("保存时应丢掉 typed、保留续聊：%v", wt)
	}

	for name, wt := range map[string]any{
		"不是对象":       "坏数据",
		"新开对话缺编号":    map[string]any{"new": "没有编号"},
		"网页启动提示词缺编号": map[string]any{"webStart": "没有编号"},
		"续聊超长":       map[string]any{"resume": strings.Repeat("长", sessionprompt.TemplateMaxRunes+1)},
	} {
		if res := put(wt); res.Code != http.StatusBadRequest {
			t.Errorf("%s 应 400，实际 %d %s", name, res.Code, res.Raw)
		}
	}
	got = e.call("GET", "/api/v1/settings", nil)
	wt, _ = got.Body["settings"].(map[string]any)["wakeTemplates"].(map[string]any)
	if wt["resume"] != "全局续聊 %inbox" {
		t.Fatalf("被拒绝的请求不该改动已保存的设置：%v", wt)
	}
}
